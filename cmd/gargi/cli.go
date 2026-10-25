package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/mcp"
	"github.com/gargi-indic/gargi-flow/internal/sim/calibrate"
	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/report"
	"github.com/gargi-indic/gargi-flow/internal/simbackend"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	sdk_mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is the gargi version, overridable with
// -ldflags "-X main.version=...".
var version = "dev"

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// errServerMode is printed by sim run without --local or --server.
const errServerMode = "pass --local or --server URL"

const usage = `usage:
  gargi model init --template canary|loan [dir]
  gargi model drift <model-id> --server <url> [--policy-version <version>] [--json]
  gargi model validate [--json] <sim.yaml>
  gargi model calibrate [--server <url>] [--out <output.yaml>] [--graph <graph.yaml>] <sim.yaml>
  gargi sim run <experiment.yaml> --local [--workers N] [--json] [--out report.json] [--emit-best DIR] [--export-jsonl path] [--top K]
  gargi sim run <experiment.yaml> --server URL [--json] [--out report.json] [--top K]
  gargi sim verify <experiment.yaml> [--scenarios N] [--json]
  gargi sim report <experiment-id> --server URL [--kind summary]
  gargi sim cancel <experiment-id> --server URL
  gargi policy explain <policy.yaml|policy.json>
  gargi policy explain <model-id>[@<version>]:<policy-name> --server URL
  gargi policy deploy <policy-id>[@<version>] --mode shadow|live [--server URL]
  gargi mcp serve (--stdio | --http ADDR)
  gargi version
`

// run executes the gargi CLI with args (without the program name) and
// returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printf(stderr, "%s", usage)
		return exitUsage
	}
	switch args[0] {
	case "version":
		if len(args) > 1 {
			printf(stderr, "gargi version: unexpected arguments %q\n", args[1:])
			return exitUsage
		}
		printf(stdout, "gargi %s\n", version)
		return exitOK
	case "model":
		if len(args) < 2 {
			printf(stderr, "%s", usage)
			return exitUsage
		}
		switch args[1] {
		case "validate":
			return modelValidate(args[2:], stdout, stderr)
		case "init":
			return modelInit(args[2:], stdout, stderr)
		case "drift":
			return modelDrift(args[2:], stdout, stderr)
		case "calibrate":
			return modelCalibrate(args[2:], stdout, stderr)
		default:
			printf(stderr, "%s", usage)
			return exitUsage
		}
	case "sim":
		if len(args) < 2 {
			printf(stderr, "%s", usage)
			return exitUsage
		}
		switch args[1] {
		case "run":
			return simRun(args[2:], stdout, stderr)
		case "verify":
			return simVerify(args[2:], stdout, stderr)
		case "report":
			return simReport(args[2:], stdout, stderr)
		case "cancel":
			return simCancel(args[2:], stdout, stderr)
		default:
			printf(stderr, "%s", usage)
			return exitUsage
		}
	case "mcp":
		if len(args) < 2 || args[1] != "serve" {
			printf(stderr, "%s", usage)
			return exitUsage
		}
		return mcpServe(args[2:], stdout, stderr)
	case "policy":
		if len(args) < 2 {
			printf(stderr, "%s", usage)
			return exitUsage
		}
		switch args[1] {
		case "explain":
			return policyExplain(args[2:], stdout, stderr)
		case "deploy":
			return policyDeploy(args[2:], stdout, stderr)
		default:
			printf(stderr, "%s", usage)
			return exitUsage
		}
	case "help", "-h", "-help", "--help":
		printf(stdout, "%s", usage)
		return exitOK
	default:
		printf(stderr, "gargi: unknown command %q\n", args[0])
		printf(stderr, "%s", usage)
		return exitUsage
	}
}

// parseInterspersed parses args with fs, allowing flags before and after
// the positional arguments, and returns the positional arguments. Arguments
// after "--" are positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		// fs.Parse consumes a "--" terminator; everything after it is positional.
		consumed := args[:len(args)-len(rest)]
		if len(consumed) > 0 && consumed[len(consumed)-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// parseExit maps a flag parse error to an exit code.
func parseExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	return exitUsage
}

// printf writes to w. Write errors on the terminal streams are not
// actionable, so they are ignored; buffered writers report them on Flush.
func printf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

// printIssues writes one "path: code: message" line per issue.
func printIssues(w io.Writer, issues []engine.ValidationIssue) {
	for _, is := range issues {
		printf(w, "%s: %s: %s\n", is.Path, is.Code, is.Message)
	}
}

// writeJSON writes v as indented JSON followed by a newline.
func writeJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// policyExplain implements gargi policy explain: it prints a plain-language
// rendering of a policy/v1 document. A .json file parses as JSON; anything
// else parses as YAML.
func policyExplain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gargi policy explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "Explain a policy stored in the model of the gargi server at this URL")
	refs, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if len(refs) != 1 {
		printf(stderr, "%s\n", "usage: gargi policy explain <policy.yaml|policy.json>")
		printf(stderr, "%s\n", "       gargi policy explain <model-id>[@<version>]:<policy-name> --server URL")
		return exitUsage
	}
	var backend simbackend.Backend = simbackend.NewLocal()
	if *server != "" {
		remote, err := simbackend.NewRemote(*server, nil)
		if err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitUsage
		}
		backend = remote
	}
	out, err := backend.ExplainPolicy(context.Background(), refs[0])
	if err != nil {
		printf(stderr, "gargi policy explain: %v\n", err)
		return exitError
	}
	printf(stdout, "%s", out)
	return exitOK
}

// policyDeploy implements gargi policy deploy: it deploys a policy version
// in shadow or live mode.
func policyDeploy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gargi policy deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "URL of the gargi server")
	mode := fs.String("mode", "", "Deployment mode: shadow or live")
	refs, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if len(refs) != 1 || *mode == "" {
		printf(stderr, "%s\n", "usage: gargi policy deploy <policy-id>[@<version>] --mode shadow|live [--server URL]")
		return exitUsage
	}
	if *mode != "shadow" && *mode != "live" {
		printf(stderr, "gargi policy deploy: invalid mode %q, want 'shadow' or 'live'\n", *mode)
		return exitUsage
	}
	var backend simbackend.Backend = simbackend.NewLocal()
	if *server != "" {
		remote, err := simbackend.NewRemote(*server, nil)
		if err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitUsage
		}
		backend = remote
	}
	res, err := backend.DeployPolicy(context.Background(), refs[0], *mode)
	if err != nil {
		printf(stderr, "gargi policy deploy: %v\n", err)
		return exitError
	}
	policyID, _, _ := strings.Cut(refs[0], "@")
	printf(stdout, "Deployed policy %s version %s in %s mode\n", policyID, res.Version, res.Mode)
	return exitOK
}

func modelInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gargi model init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	template := fs.String("template", "", "Template to initialize: canary or loan")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if *template == "" || len(pos) > 1 {
		printf(stderr, "%s\n", "usage: gargi model init --template canary|loan|claims [dir]")
		return exitUsage
	}
	dest := "."
	if len(pos) == 1 {
		dest = pos[0]
	}
	if err := mcp.CopyTemplate(*template, dest); err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}
	printf(stdout, "Initialized %s model in %s\n", *template, dest)
	return exitOK
}

func modelValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gargi model validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Print the validation report as JSON")
	files, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if len(files) != 1 {
		printf(stderr, "%s\n", "usage: gargi model validate [--json] <sim.yaml>")
		return exitUsage
	}

	issues, err := simbackend.NewLocal().ValidateModel(context.Background(), files[0])
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}
	if *asJSON {
		report := struct {
			Errors []engine.ValidationIssue `json:"errors"`
		}{Errors: issues}
		if report.Errors == nil {
			report.Errors = []engine.ValidationIssue{}
		}
		if err := writeJSON(stdout, report); err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
	} else if len(issues) == 0 {
		printf(stdout, "%s\n", "OK")
	} else {
		printIssues(stdout, issues)
	}
	if len(issues) > 0 {
		return exitError
	}
	return exitOK
}

func modelCalibrate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gargi model calibrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "URL of gargi server to fetch executions and outcomes")
	out := fs.String("out", "", "Output file path for calibrated sim.yaml")
	graph := fs.String("graph", "", "Path to graph definition YAML (overrides sim.yaml graph)")
	executionsPath := fs.String("executions", "", "Optional path to executions file (offline mode)")
	outcomesPath := fs.String("outcomes", "", "Optional path to outcomes file (offline mode)")
	files, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if len(files) != 1 {
		printf(stderr, "%s\n", "usage: gargi model calibrate [--server <url>] [--out <output.yaml>] [--graph <graph.yaml>] <sim.yaml>")
		return exitUsage
	}

	simPath := files[0]
	simData, err := os.ReadFile(simPath)
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}
	format, err := formatFor(simPath)
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}
	simDef, err := model.ParseSimModel(simData, format)
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}

	if *graph != "" {
		if !filepath.IsAbs(*graph) {
			if abs, err := filepath.Abs(*graph); err == nil {
				simDef.Graph = abs
			} else {
				simDef.Graph = *graph
			}
		} else {
			simDef.Graph = *graph
		}
	}

	reg, err := jobs.NewRegistry()
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}
	compMod, compileErrs := model.Compile(simDef, filepath.Dir(simPath), reg)
	if compileErrs != nil && compileErrs.Len() > 0 {
		printIssues(stderr, compileErrs.Issues())
		return exitError
	}

	var reader calibrate.ExecutionReader
	var outcomes []sim.OutcomeRecord

	if *server != "" {
		remReader, err := calibrate.NewRemoteReader(*server, nil)
		if err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
		reader = remReader
	} else if *executionsPath != "" {
		memReader, err := calibrate.LoadExecutionsFile(*executionsPath)
		if err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
		reader = memReader
	} else {
		reader = calibrate.NewEmptyReader()
	}

	if *outcomesPath != "" {
		loadedOutcomes, err := calibrate.LoadOutcomesFile(*outcomesPath)
		if err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
		outcomes = loadedOutcomes
	}

	res, err := calibrate.Calibrate(context.Background(), compMod, reader, outcomes)
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}

	yamlBytes, err := res.OverlayYAML(simDef)
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}

	if *out != "" {
		if err := os.WriteFile(*out, yamlBytes, 0o600); err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
	} else {
		if _, err := stdout.Write(yamlBytes); err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
	}

	return exitOK
}

func formatFor(path string) (engine.Format, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return engine.FormatYAML, nil
	case ".json":
		return engine.FormatJSON, nil
	default:
		return "", fmt.Errorf("unsupported file extension %q (want .yaml, .yml or .json)", filepath.Ext(path))
	}
}

func simRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gargi sim run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	local := fs.Bool("local", false, "Run the experiment in-process")
	server := fs.String("server", "", "Run the experiment on the gargi server at this URL")
	workers := fs.Int("workers", 0, "Number of workers (default: the experiment's workers, else GOMAXPROCS)")
	top := fs.Int("top", 0, "Number of top policies to report per world (default: the experiment's top, else 5)")
	asJSON := fs.Bool("json", false, "Print the report as JSON")
	out := fs.String("out", "", "Write the JSON report to this file")
	emitBest := fs.String("emit-best", "", "Write each world's best policy as policy/v1 YAML into this directory")
	exportJSONL := fs.String("export-jsonl", "", "Write each run record as JSONL to this file (only in --local mode)")
	files, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if *local && *server != "" {
		printf(stderr, "%s\n", "gargi: --local and --server are mutually exclusive")
		return exitUsage
	}
	if !*local && *server == "" {
		printf(stderr, "%s\n", errServerMode)
		return exitUsage
	}
	if len(files) != 1 {
		printf(stderr, "%s\n", "usage: gargi sim run <experiment.yaml> (--local [--workers N] [--emit-best DIR] [--export-jsonl path] | --server URL) [--json] [--out report.json] [--top K]")
		return exitUsage
	}
	if *workers < 0 || *top < 0 {
		printf(stderr, "%s\n", "gargi: --workers and --top must not be negative")
		return exitUsage
	}
	var backend simbackend.Backend = simbackend.NewLocal()
	if *server != "" {
		if *emitBest != "" || *workers != 0 || *exportJSONL != "" {
			printf(stderr, "%s\n", "gargi: --emit-best, --export-jsonl, and --workers require --local")
			return exitUsage
		}
		remote, err := simbackend.NewRemote(*server, nil)
		if err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitUsage
		}
		backend = remote
	}

	res, err := backend.RunExperiment(context.Background(), files[0], simbackend.RunOptions{Workers: *workers, Top: *top, ExportJSONL: *exportJSONL})
	if err != nil {
		var internal *simbackend.InternalError
		if errors.As(err, &internal) {
			printf(stderr, "gargi: %v\n", err)
		} else {
			printIssues(stderr, simbackend.IssuesOf(err, engine.CodeInvalidValue))
		}
		return exitError
	}
	report, policies := res.Report, res.Policies
	if res.ExperimentID != "" {
		printf(stderr, "experiment %s\n", res.ExperimentID)
	}

	if *out != "" {
		if err := writeReportFile(*out, report); err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
	}
	if *emitBest != "" {
		if err := emitBestPolicies(*emitBest, report, policies); err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
	}
	if *asJSON {
		err = writeJSON(stdout, report)
	} else {
		err = printReport(stdout, report)
	}
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}
	return exitOK
}

// simReport implements gargi sim report: it prints one report of a stored
// experiment as indented JSON.
func simReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gargi sim report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "URL of the gargi server holding the experiment")
	kind := fs.String("kind", "summary", "Report kind: summary, regret, robust, pareto, voi, flipPoints, pathHeatmap or policyTree")
	ids, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if len(ids) != 1 || *server == "" {
		printf(stderr, "%s\n", "usage: gargi sim report <experiment-id> --server URL [--kind summary]")
		return exitUsage
	}
	remote, err := simbackend.NewRemote(*server, nil)
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitUsage
	}
	raw, err := remote.Report(context.Background(), ids[0], *kind)
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}
	buf.WriteByte('\n')
	if _, err := stdout.Write(buf.Bytes()); err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}
	return exitOK
}

func writeReportFile(path string, report *experiment.Report) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if err := writeJSON(f, report); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// emitBestPolicies writes <world>.policy.yaml into dir for the best policy
// of every world of report: the solved optimum when the world has a
// solution, otherwise the top-ranked policy.
func emitBestPolicies(dir string, report *experiment.Report, policies []sim.Policy) error {
	byID := make(map[string]sim.Policy, len(policies))
	for _, p := range policies {
		byID[p.ID()] = p
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, w := range report.Worlds {
		if len(w.Policies) == 0 {
			continue
		}
		name := string(w.World)
		if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
			return fmt.Errorf("world id %q is not a valid file name", name)
		}
		bestID := w.Policies[0].PolicyID
		if w.Solution != nil {
			bestID = w.Solution.PolicyID
		}
		p, ok := byID[bestID]
		if !ok {
			return fmt.Errorf("world %q: best policy %q not found", name, bestID)
		}
		if _, ok := p.(*experiment.TablePolicy); !ok {
			return fmt.Errorf("world %q: --emit-best supports policies from policies.search: exhaustive or exact only; policy %q is not a table policy", name, bestID)
		}
		if err := writePolicyFile(filepath.Join(dir, name+".policy.yaml"), p); err != nil {
			return fmt.Errorf("world %q: %w", name, err)
		}
	}
	return nil
}

func writePolicyFile(path string, p sim.Policy) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if err := experiment.EmitPolicy(p, f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// printReport writes the human-readable report: per world a table of the
// top policies followed by the best policy's description.
func printReport(w io.Writer, report *experiment.Report) error {
	for i, wr := range report.Worlds {
		if i > 0 {
			printf(w, "\n")
		}
		header := fmt.Sprintf("World %s (model %s, evaluator %s", wr.World, report.Model, report.Evaluator)
		if report.Runs > 0 {
			header += fmt.Sprintf(", %d runs, seed %d", report.Runs, report.Seed)
		}
		printf(w, "%s\n", header+")")

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		printf(tw, "%s\n", "POLICY\tMEAN\tCI95\tP95 DURATION\tEXACT")
		for _, p := range wr.Policies {
			mean, ci, dur := p.Objective, p.Objective, p.Duration
			if p.MonteCarlo != nil {
				// evaluator both: the primary estimates are exact; show the
				// Monte Carlo statistics next to the exact value.
				mean, ci, dur = p.MonteCarlo.Objective, p.MonteCarlo.Objective, p.MonteCarlo.Duration
			}
			exact := "-"
			if p.Objective.Exact {
				exact = fmt.Sprintf("%.1f", p.Objective.Mean)
			}
			printf(tw, "%s\t%.1f\t[%.1f, %.1f]\t%s\t%s\n",
				p.PolicyID, mean.Mean, ci.CI95Low, ci.CI95High, formatSeconds(dur.P95), exact)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		if len(wr.Policies) > 0 {
			// Policies are ranked feasible first, so the first one is the
			// best feasible policy if there is any.
			best := wr.Policies[0]
			if best.IsFeasible() {
				printf(w, "Best policy: %s\n", best.Description)
				if best.Objective.Exact {
					printf(w, "Exact EV: %.1f\n", best.Objective.Mean)
				}
			} else {
				printf(w, "%s\n", "Best policy: none is feasible (every policy violates a requirement)")
			}
		}
		if wr.Solution != nil {
			if err := printSolution(w, wr.Solution); err != nil {
				return err
			}
		}
	}
	if report.Reports != nil {
		return printReports(w, report.Reports)
	}
	return nil
}

// printReports writes one compact table per requested report.
func printReports(w io.Writer, rs *experiment.ReportSet) error {
	if r := rs.Regret; r != nil {
		printf(w, "\n%s\n", "Regret (rows: policy tuned for a world; columns: value (regret) in each world)")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		header := []string{"TUNED FOR", "POLICY"}
		for _, world := range r.Worlds {
			header = append(header, strings.ToUpper(string(world)))
		}
		printf(tw, "%s\n", strings.Join(append(header, "MAX REGRET"), "\t"))
		for _, row := range r.Rows {
			cells := []string{string(row.TunedFor), row.PolicyID}
			for _, c := range row.Cells {
				cell := fmt.Sprintf("%.1f", c.Value)
				if c.Regret != nil {
					cell += fmt.Sprintf(" (%.1f)", *c.Regret)
				}
				if !c.Feasible {
					cell += " infeasible"
				}
				cells = append(cells, cell)
			}
			printf(tw, "%s\n", strings.Join(append(cells, fmt.Sprintf("%.1f", row.MaxRegret)), "\t"))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		for _, world := range r.NoFeasiblePolicy {
			printf(w, "World %s has no feasible policy.\n", world)
		}
	}
	if r := rs.Robust; r != nil {
		worlds := make([]string, 0, len(r.Prior))
		for world := range r.Prior {
			worlds = append(worlds, string(world))
		}
		sort.Strings(worlds)
		prior := make([]string, len(worlds))
		for i, world := range worlds {
			prior[i] = fmt.Sprintf("%s=%.2f", world, r.Prior[sim.WorldID(world)])
		}
		printf(w, "\nRobust (prior %s; CVaR alpha %g; %d eligible policies)\n", strings.Join(prior, " "), r.CVaRAlpha, r.Eligible)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		printf(tw, "%s\n", "CRITERION\tPOLICY\tSCORE\tEXPECTED VALUE\tWORST CASE\tCVAR")
		for _, c := range []struct {
			name string
			pick *report.RobustPick
		}{{"expected value", r.ExpectedValue}, {"maximin", r.Maximin}, {"cvar", r.CVaR}} {
			if c.pick == nil {
				printf(tw, "%s\t%s\n", c.name, "none (no policy is feasible in every world)")
				continue
			}
			s := c.pick.Scores
			printf(tw, "%s\t%s\t%.1f\t%.1f\t%.1f\t%.1f\n", c.name, c.pick.PolicyID, c.pick.Score, s.ExpectedValue, s.WorstCase, s.CVaR)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	for _, p := range rs.Pareto {
		for _, pw := range p.Worlds {
			printf(w, "\nPareto frontier, world %s (x: %s %s, y: %s %s)\n", pw.World, p.X.Metric, p.X.Goal, p.Y.Metric, p.Y.Goal)
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			printf(tw, "%s\n", "POLICY\tX\tY")
			for _, pt := range pw.Points {
				printf(tw, "%s\t%.4g\t%.4g\n", pt.PolicyID, pt.X, pt.Y)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
		}
	}
	if r := rs.Tournament; r != nil {
		printf(w, "\nTournament (alpha %g; %d policies)\n", r.Alpha, len(r.Rankings))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		printf(tw, "%s\n", "RANK\tPOLICY\tMEAN\tWINS\tLOSSES\tTIES\tWIN RATE")
		for _, rk := range r.Rankings {
			printf(tw, "%d\t%s\t%.4g\t%d\t%d\t%d\t%.2f\n", rk.Rank, rk.PolicyID, rk.Mean, rk.Wins, rk.Losses, rk.Ties, rk.WinRate)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return printExplainReports(w, rs)
}

// heatmapTopEdges is the number of edges listed per world by the text
// rendering of the path heatmap.
const heatmapTopEdges = 5

// printExplainReports writes the voi, flip_point, path_heatmap and
// policy_tree reports.
func printExplainReports(w io.Writer, rs *experiment.ReportSet) error {
	if len(rs.VOI) > 0 {
		printf(w, "\n%s\n", "Value of information (optimal EV with the node observed minus without it, its cost removed)")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		printf(tw, "%s\n", "WORLD\tNODE\tWITH\tWITHOUT\tVOI")
		for _, v := range rs.VOI {
			printf(tw, "%s\t%s\t%.1f\t%.1f\t%.1f\n", v.World, v.Node, v.WithObservation.EV, v.WithoutObservation.EV, v.VOI)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	for _, fp := range rs.FlipPoints {
		printf(w, "\nFlip points of %s (world %s, swept from %g to %g)\n", fp.Param, fp.BaseWorld, fp.Lo, fp.Hi)
		if len(fp.Flips) == 0 && len(fp.Sweep) > 0 {
			printf(w, "No flip: %s is optimal over the whole range.\n", fp.Sweep[0].PolicyID)
		}
		for _, f := range fp.Flips {
			printf(w, "%s in [%.7g, %.7g]:\n", fp.Param, f.Left.Value, f.Right.Value)
			printf(w, "  from %s (EV %.1f): %s\n", f.Left.PolicyID, f.Left.EV, f.From)
			printf(w, "  to   %s (EV %.1f): %s\n", f.Right.PolicyID, f.Right.EV, f.To)
		}
	}
	for _, hm := range rs.PathHeatmap {
		printf(w, "\nPath heatmap, world %s (policy %s; top edges)\n", hm.World, hm.PolicyID)
		edges := slices.Clone(hm.Edges)
		sort.SliceStable(edges, func(i, j int) bool { return edges[i].Mass > edges[j].Mass })
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		printf(tw, "%s\n", "EDGE\tMASS")
		for i, e := range edges {
			if i == heatmapTopEdges || e.Mass <= 0 {
				break
			}
			printf(tw, "%s -> %s\t%.4f\n", e.From, e.To, e.Mass)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	for _, pt := range rs.PolicyTree {
		printf(w, "\nPolicy tree, world %s (policy %s, EV %.1f; optimal path)\n", pt.World, pt.PolicyID, pt.EV)
		for _, n := range pt.Roots {
			printTreeNode(w, n, 0)
		}
	}
	return nil
}

// printTreeNode writes the on-path information sets below n, one indented
// line each: the information set, its reach probability, the choice and its
// value.
func printTreeNode(w io.Writer, n report.TreeNode, depth int) {
	if !n.OnPath {
		return
	}
	for _, a := range n.Actions {
		if a.Option != n.Choice {
			continue
		}
		printf(w, "%s%s (reach %.4f) -> %s (%.1f)\n", strings.Repeat("  ", depth), n.InfoSet, n.ReachProbability, a.Option, a.Value)
		for _, c := range a.Children {
			printTreeNode(w, c, depth+1)
		}
	}
}

// printSolution writes a world's exact solution: the optimal policy, its EV
// and the action values of every information set the optimal policy reaches
// (the JSON report lists every information set).
func printSolution(w io.Writer, s *experiment.WorldSolution) error {
	printf(w, "Optimal policy (exact solver, %d histories): %s\n", s.Histories, s.Description)
	printf(w, "Optimal EV: %.1f\n", s.EV)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	printf(tw, "%s\n", "INFORMATION SET\tREACH\tCHOICE\tACTION VALUES")
	for _, is := range s.InformationSets {
		if is.ReachProbability <= 0 {
			continue
		}
		values := make([]string, len(is.Actions))
		for i, a := range is.Actions {
			values[i] = fmt.Sprintf("%s=%.1f", a.Option, a.Value)
		}
		printf(tw, "%s\t%.4f\t%s\t%s\n", is.InfoSet, is.ReachProbability, is.Choice, strings.Join(values, " "))
	}
	return tw.Flush()
}

// simCancel implements gargi sim cancel.
func simCancel(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gargi sim cancel", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "URL of the gargi server holding the experiment")
	ids, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if len(ids) != 1 || *server == "" {
		printf(stderr, "%s\n", "usage: gargi sim cancel <experiment-id> --server URL")
		return exitUsage
	}
	remote, err := simbackend.NewRemote(*server, nil)
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitUsage
	}
	if err := remote.Cancel(context.Background(), ids[0]); err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}
	printf(stdout, "Cancelled experiment %s\n", ids[0])
	return exitOK
}

// formatSeconds formats a duration in seconds, rounded to the second.
func formatSeconds(s float64) string {
	return time.Duration(s * float64(time.Second)).Round(time.Second).String()
}

func mcpServe(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useStdio := fs.Bool("stdio", false, "run over stdio")
	useHTTP := fs.String("http", "", "run over streamable HTTP at ADDR")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if (*useStdio && *useHTTP != "") || (!*useStdio && *useHTTP == "") {
		printf(stderr, "gargi mcp serve: exactly one of --stdio or --http ADDR is required\n")
		return exitUsage
	}

	backend := simbackend.NewLocal()
	srv := mcp.NewServer(mcp.Options{
		Backend: backend,
		Version: version,
	})

	ctx := context.Background()

	if *useStdio {
		// Use stdin/stdout transport
		// MCP sdk has StdioTransport? Actually we can use NewReaderWriterTransport?
		transport := &sdk_mcp.StdioTransport{}
		if err := srv.Run(ctx, transport); err != nil {
			printf(stderr, "mcp server error: %v\n", err)
			return exitError
		}
		return exitOK
	}

	// Streamable HTTP
	handler := sdk_mcp.NewStreamableHTTPHandler(func(*http.Request) *sdk_mcp.Server { return srv }, nil)

	printf(stderr, "Starting MCP streamable HTTP server on %s...\n", *useHTTP)
	if err := http.ListenAndServe(*useHTTP, handler); err != nil {
		printf(stderr, "mcp server error: %v\n", err)
		return exitError
	}

	return exitOK
}
