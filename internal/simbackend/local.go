package simbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/internal/sim/explain"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// Local is a Backend that runs everything in-process. It keeps no state and
// no experiment store.
type Local struct {
	store engine.PolicyStore
}

var _ Backend = (*Local)(nil)

// NewLocal returns an in-process Backend.
func NewLocal() *Local { return &Local{store: memory.NewPolicyStore()} }

// NewLocalWithStore returns an in-process Backend with the given policy store.
func NewLocalWithStore(store engine.PolicyStore) *Local { return &Local{store: store} }

// PolicyStore returns the underlying policy store.
func (l *Local) PolicyStore() engine.PolicyStore { return l.store }

// registry returns a node registry able to compile simulation models: the
// default reference nodes plus a decision node whose chooser the kernel
// replaces with the simulated policy.
func registry() (*engine.Registry, error) {
	reg := engine.NewRegistry()
	if err := nodes.RegisterDefaults(reg, nodes.Dependencies{}); err != nil {
		return nil, err
	}
	if err := nodes.RegisterDecision(reg, firstOption{}); err != nil {
		return nil, err
	}
	return reg, nil
}

// firstOption is a placeholder decision chooser; simulations never call it.
type firstOption struct{}

func (firstOption) Choose(_ context.Context, _ engine.NodeID, _ map[string]any, options []string) (string, error) {
	if len(options) == 0 {
		return "", errors.New("decision has no options")
	}
	return options[0], nil
}

// formatFor returns the document format of path from its extension.
func formatFor(path string) (engine.Format, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return engine.FormatYAML, nil
	case ".json":
		return engine.FormatJSON, nil
	default:
		return "", fmt.Errorf("unsupported file extension %q", filepath.Ext(path))
	}
}

// ValidateModel parses and compiles the simulation model at path and returns
// its sorted validation issues (none when the model is valid).
func (*Local) ValidateModel(_ context.Context, path string) ([]engine.ValidationIssue, error) {
	format, err := formatFor(path)
	if err != nil {
		return IssuesOf(err, engine.CodeInvalidFormat), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	def, err := model.ParseSimModel(data, format)
	if err != nil {
		return IssuesOf(err, engine.CodeInvalidFormat), nil
	}
	reg, err := registry()
	if err != nil {
		return nil, err
	}
	if _, errs := model.Compile(def, filepath.Dir(path), reg); errs.Len() > 0 {
		return IssuesOf(errs, engine.CodeInvalidValue), nil
	}
	return nil, nil
}

// ValidateBundle validates the in-memory model bundle b the way the gargi
// server does (jobs.ValidateModel) and returns its sorted validation issues
// (none when the bundle is valid).
func (*Local) ValidateBundle(_ context.Context, b sim.ModelBundle) ([]engine.ValidationIssue, error) {
	reg, err := jobs.NewRegistry()
	if err != nil {
		return nil, &InternalError{Err: err}
	}
	if _, errs := jobs.ValidateModel(b, reg); errs.Len() > 0 {
		return IssuesOf(errs, engine.CodeInvalidValue), nil
	}
	return nil, nil
}

// policyList is a PolicySource over an already enumerated policy list, so
// the policies are enumerated once and returned alongside the report.
type policyList []sim.Policy

func (l policyList) Policies(*model.CompiledModel) ([]sim.Policy, error) { return l, nil }

// RunBundle runs the given experiment document against an in-memory model bundle.
func (*Local) RunBundle(ctx context.Context, b sim.ModelBundle, definition []byte, format string, opts RunOptions) (*RunResult, error) {
	reg, err := jobs.NewRegistry()
	if err != nil {
		return nil, &InternalError{Err: err}
	}
	exp, err := jobs.CompileExperiment(b, definition, format, reg)
	if err != nil {
		return nil, err
	}
	return runCompiledExperiment(ctx, exp, opts)
}

// RunExperiment loads and runs the experiment/v1 document at path. Load,
// solve, enumeration and run failures are returned unwrapped so that
// IssuesOf can turn them into validation issues; a registry failure is an
// *InternalError.
func (*Local) RunExperiment(ctx context.Context, path string, opts RunOptions) (*RunResult, error) {
	reg, err := registry()
	if err != nil {
		return nil, &InternalError{Err: err}
	}
	exp, err := experiment.LoadFile(path, reg)
	if err != nil {
		return nil, err
	}
	return runCompiledExperiment(ctx, exp, opts)
}

func runCompiledExperiment(ctx context.Context, exp *experiment.CompiledExperiment, opts RunOptions) (*RunResult, error) {
	// Enumerate or solve once; the policies are kept for --emit-best. A
	// solution (policies: {search: exact}) is passed to Run as is, so the
	// report carries each world's solution.
	var src experiment.PolicySource
	if solver, ok := exp.PolicySource().(experiment.Solver); ok {
		sol, err := solver.Solve(ctx, exp.Model, exp.Worlds)
		if err != nil {
			return nil, err
		}
		src = sol
	}
	var policies []sim.Policy
	var err error
	if src != nil {
		policies, err = src.Policies(exp.Model)
	} else {
		policies, err = exp.PolicySource().Policies(exp.Model)
		src = policyList(policies)
	}
	if err != nil {
		return nil, err
	}
	runOpts := experiment.Options{Workers: opts.Workers, Top: opts.Top}
	if opts.Progress != nil {
		runOpts.Progress = func(done, total int) {
			opts.Progress(sim.Progress{Done: done, Total: total})
		}
	}
	if opts.RecordSink != nil {
		runOpts.RecordSink = opts.RecordSink
	} else if opts.ExportJSONL != "" {
		f, err := os.Create(opts.ExportJSONL)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		runOpts.RecordSink = f
	}
	report, err := experiment.Run(ctx, exp, src, runOpts)
	if err != nil {
		return nil, err
	}
	return &RunResult{Report: report, Policies: policies}, nil
}

// Report is not supported by Local, which keeps no experiment store.
func (*Local) Report(_ context.Context, _, kind string) (json.RawMessage, error) {
	return nil, fmt.Errorf("report %q: local backend keeps no experiment store: %w", kind, ErrUnsupported)
}

// ExplainPolicy renders the policy/v1 file at ref in plain language. A .json
// file parses as JSON; anything else parses as YAML.
func (*Local) ExplainPolicy(_ context.Context, ref string) (string, error) {
	format := engine.FormatYAML
	if strings.EqualFold(filepath.Ext(ref), ".json") {
		format = engine.FormatJSON
	}
	data, err := os.ReadFile(ref)
	if err != nil {
		return "", err
	}
	p, err := model.ParsePolicy(data, format)
	if err != nil {
		return "", err
	}
	return explain.Explain(p), nil
}

// Cancel is not supported by Local, which keeps no experiment store.
func (*Local) Cancel(_ context.Context, experimentID string) error {
	return fmt.Errorf("cancel experiment %q: local backend keeps no experiment store: %w", experimentID, ErrUnsupported)
}

// DeployPolicy sets the deployment mode of a policy version in the local policy store.
func (l *Local) DeployPolicy(ctx context.Context, ref string, mode string) (*DeployResult, error) {
	if l.store == nil {
		return nil, fmt.Errorf("deploy_policy: local backend has no policy store: %w", ErrUnsupported)
	}
	if mode != "live" && mode != "shadow" {
		return nil, fmt.Errorf("invalid mode %q: want 'live' or 'shadow'", mode)
	}
	if ref == "" {
		return nil, errors.New("id is required")
	}

	var graphID, policyID, version string
	if strings.Contains(ref, ":") {
		graphID, policyID, _ = strings.Cut(ref, ":")
	} else {
		graphID = "default"
		policyID = ref
	}

	if gID, gVer, ok := strings.Cut(graphID, "@"); ok {
		graphID = gID
		if version == "" {
			version = gVer
		}
	}

	if pID, pVer, ok := strings.Cut(policyID, "@"); ok {
		policyID = pID
		if version == "" {
			version = pVer
		}
	}

	if graphID == "" || policyID == "" {
		return nil, fmt.Errorf("invalid policy id format %q", ref)
	}

	rec, err := l.store.GetPolicy(ctx, graphID, policyID, version)
	if errors.Is(err, engine.ErrPolicyNotFound) {
		base := strings.TrimSuffix(strings.TrimSuffix(policyID, ".yaml"), ".json")
		if base != policyID {
			rec, err = l.store.GetPolicy(ctx, graphID, base, version)
		} else {
			rec, err = l.store.GetPolicy(ctx, graphID, policyID+".yaml", version)
		}
	}
	if err != nil {
		return nil, err
	}

	dep := &engine.PolicyDeployment{
		GraphID:       graphID,
		PolicyID:      policyID,
		PolicyVersion: rec.Version,
		Mode:          engine.DeploymentMode(mode),
	}
	if err := l.store.SetDeployment(ctx, dep); err != nil {
		return nil, err
	}

	return &DeployResult{
		ID:      ref,
		Version: rec.Version,
		Mode:    mode,
	}, nil
}
