package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/sim/kernel"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/sampler"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/require"
)

const (
	loanDir        = "../../../examples/simulation/loan"
	recessionFile  = "policies/recession-v1.yaml"
	recessionID    = "loan-underwriting@recession-v1"
	rejectAllYAML  = "apiVersion: policy/v1\nid: loan-underwriting\nversion: reject-all\ndecisions:\n  screen: {rules: [{else: skip}]}\n  route: {rules: [{else: reject}]}\n  final: {rules: [{else: reject}]}\n"
	recessionValue = 202.525
)

// stubChooser satisfies the decision node registration used by
// model.Compile; the kernel registers its own policy-backed chooser.
type stubChooser struct{}

func (stubChooser) Choose(_ context.Context, _ engine.NodeID, _ map[string]any, options []string) (string, error) {
	return options[0], nil
}

func registry(t testing.TB) *engine.Registry {
	t.Helper()
	reg := engine.NewRegistry()
	require.NoError(t, nodes.RegisterDefaults(reg, nodes.Dependencies{}))
	require.NoError(t, nodes.RegisterDecision(reg, stubChooser{}))
	return reg
}

// loanExperiment compiles an experiment over the real loan model.
func loanExperiment(t testing.TB, baseDir string, runs int, worlds ...string) *CompiledExperiment {
	t.Helper()
	def := &model.Experiment{
		APIVersion: "experiment/v1",
		Model:      "./sim.yaml",
		Worlds:     worlds,
		Policies:   model.Policies{Files: []string{recessionFile}},
		Evaluator:  "monte_carlo",
		Runs:       runs,
		Seed:       42,
		Top:        5,
	}
	exp, errs := Compile(def, baseDir, registry(t))
	if errs.Len() > 0 {
		t.Fatalf("compile experiment: %v", errs.Error())
	}
	return exp
}

func writeFile(t testing.TB, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func readLoan(t testing.TB, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(loanDir, name))
	require.NoError(t, err)
	return string(data)
}

func marshal(t *testing.T, r *Report) string {
	t.Helper()
	data, err := json.Marshal(r)
	require.NoError(t, err)
	return string(data)
}

func TestCompile_LoanExperimentFile(t *testing.T) {
	exp, err := LoadFile(filepath.Join(loanDir, "experiment.yaml"), registry(t))
	require.NoError(t, err)
	require.Equal(t, "loan-underwriting", exp.Model.Def.ID)
	require.Equal(t, []sim.WorldID{"base", "recession"}, []sim.WorldID{exp.Worlds[0].ID, exp.Worlds[1].ID})
	require.Equal(t, 0.35, exp.Worlds[1].Params["p_bad"])
	require.Equal(t, 200000, exp.Def.Runs)
	require.Equal(t, uint64(42), exp.Def.Seed)

	// Worlds default to every model world, sorted.
	all := loanExperiment(t, loanDir, 1)
	require.Len(t, all.Worlds, 2)
	require.Equal(t, sim.WorldID("base"), all.Worlds[0].ID)
}

func TestCompile_Errors(t *testing.T) {
	reg := registry(t)
	def := &model.Experiment{Model: "./sim.yaml", Worlds: []string{"mars", "base", "base"}, Runs: 1, Evaluator: "monte_carlo"}
	_, errs := Compile(def, loanDir, reg)
	require.Equal(t, []engine.ValidationIssue{
		{Path: "$.worlds[0]", Code: engine.CodeUnresolvedReference, Message: `unknown world "mars"`},
		{Path: "$.worlds[2]", Code: engine.CodeDuplicateID, Message: `duplicate world "base"`},
	}, errs.Issues())

	_, errs = Compile(&model.Experiment{Model: "./nope.yaml"}, loanDir, reg)
	require.Len(t, errs.Issues(), 1)
	require.Equal(t, "$.model", errs.Issues()[0].Path)
	require.Equal(t, engine.CodeUnresolvedReference, errs.Issues()[0].Code)

	dir := t.TempDir()
	writeFile(t, dir, "sim.yaml", strings.Replace(readLoan(t, "sim.yaml"), "graph: ./graph.yaml", "graph: ./missing.yaml", 1))
	_, errs = Compile(&model.Experiment{Model: "sim.yaml"}, dir, reg)
	require.Len(t, errs.Issues(), 1)
	require.Equal(t, "$.model:$.graph", errs.Issues()[0].Path)

	_, errs = Compile(nil, dir, reg)
	require.Equal(t, engine.CodeMissingRequired, errs.Issues()[0].Code)
}

func TestFilesSource_LoadsRecession(t *testing.T) {
	exp := loanExperiment(t, loanDir, 1)
	policies, err := exp.FilesSource().Policies(exp.Model)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Equal(t, recessionID, policies[0].ID())
	require.Equal(t, "policy loan-underwriting version recession-v1", policies[0].Describe())
}

func TestFilesSource_Errors(t *testing.T) {
	exp := loanExperiment(t, loanDir, 1)
	dir := t.TempDir()
	bad := strings.Replace(readLoan(t, recessionFile), "choose: approve", "choose: maybe", 1)
	writeFile(t, dir, "bad.yaml", bad)
	writeFile(t, dir, "garbage.yaml", "apiVersion: policy/v1\nid: [\n")
	writeFile(t, dir, "policy.txt", "x")
	dup := filepath.Join(dir, "dup.yaml")
	require.NoError(t, os.WriteFile(dup, []byte(readLoan(t, recessionFile)), 0o600))

	recAbs, err := filepath.Abs(filepath.Join(loanDir, recessionFile))
	require.NoError(t, err)
	src := FilesSource{BaseDir: dir, Files: []string{
		recAbs,         // 0 ok
		"missing.yaml", // 1
		"bad.yaml",     // 2
		"policy.txt",   // 3
		"dup.yaml",     // 4
		"garbage.yaml", // 5
	}}
	_, err = src.Policies(exp.Model)
	var ve *engine.ValidationErrors
	require.ErrorAs(t, err, &ve)
	issues := ve.Issues()
	require.Len(t, issues, 5, "issues: %v", issues)

	paths := make([]string, len(issues))
	for i, is := range issues {
		paths[i] = is.Path
	}
	require.IsNonDecreasing(t, paths, "issues must be sorted by path")
	byPath := map[string]engine.ErrorCode{}
	for _, is := range issues {
		byPath[is.Path] = is.Code
	}
	require.Equal(t, engine.CodeUnresolvedReference, byPath["$.policies.files[1]"])
	require.Equal(t, sim.CodePolicyUnknownOption, byPath["$.policies.files[2]:$.decisions.final.rules[0].choose"], "issues: %v", issues)
	require.Equal(t, engine.CodeInvalidFormat, byPath["$.policies.files[3]"])
	require.Equal(t, engine.CodeDuplicateID, byPath["$.policies.files[4]"], "issues: %v", issues)
	found := false
	for p := range byPath {
		if strings.HasPrefix(p, "$.policies.files[5]") {
			found = true
		}
	}
	require.True(t, found, "garbage file must be reported: %v", issues)

	_, err = FilesSource{}.Policies(exp.Model)
	require.ErrorAs(t, err, &ve)
	require.Equal(t, engine.CodeMissingRequired, ve.Issues()[0].Code)
}

func TestRun_WorkerCountIndependent(t *testing.T) {
	runs := 2500 // two full chunks and a partial one
	if raceEnabled {
		runs = 1200
	}
	exp := loanExperiment(t, loanDir, runs)
	dir := t.TempDir()
	src := FilesSource{Files: []string{filepath.Join(loanDir, recessionFile), writeFile(t, dir, "reject.yaml", rejectAllYAML)}}

	r1, err := Run(context.Background(), exp, src, Options{Workers: 1, All: true})
	require.NoError(t, err)
	r8, err := Run(context.Background(), exp, src, Options{Workers: 8, All: true})
	require.NoError(t, err)
	require.Equal(t, marshal(t, r1), marshal(t, r8))
	require.Len(t, r1.Worlds, 2)
	require.Equal(t, runs, r1.Worlds[0].Policies[0].Objective.N)
	t.Logf("report: %s", marshal(t, r1))
}

func TestRun_CommonRandomNumbers(t *testing.T) {
	exp := loanExperiment(t, loanDir, 300, "recession")
	dir := t.TempDir()
	clone := strings.Replace(readLoan(t, recessionFile), "version: recession-v1", "version: recession-clone", 1)
	src := FilesSource{Files: []string{filepath.Join(loanDir, recessionFile), writeFile(t, dir, "clone.yaml", clone)}}

	rep, err := Run(context.Background(), exp, src, Options{Workers: 4})
	require.NoError(t, err)
	ps := rep.Worlds[0].Policies
	require.Len(t, ps, 2)
	require.Equal(t, "loan-underwriting@recession-clone", ps[0].PolicyID, "equal means sort by id")
	require.Equal(t, recessionID, ps[1].PolicyID)
	require.Equal(t, ps[0].Objective, ps[1].Objective)
	require.Equal(t, ps[0].Duration, ps[1].Duration)
	require.NotZero(t, ps[0].Objective.StdDev)
}

func TestRun_SortTopAndAll(t *testing.T) {
	exp := loanExperiment(t, loanDir, 200, "recession")
	dir := t.TempDir()
	src := FilesSource{Files: []string{
		writeFile(t, dir, "reject.yaml", rejectAllYAML),
		filepath.Join(loanDir, recessionFile),
		writeFile(t, dir, "clone.yaml", strings.Replace(readLoan(t, recessionFile), "version: recession-v1", "version: a-clone", 1)),
	}}

	rep, err := Run(context.Background(), exp, src, Options{Top: 2})
	require.NoError(t, err)
	w := rep.Worlds[0]
	require.Equal(t, 2, rep.Top)
	require.Nil(t, w.All)
	require.Equal(t, []string{"loan-underwriting@a-clone", recessionID}, []string{w.Policies[0].PolicyID, w.Policies[1].PolicyID})
	require.NotContains(t, marshal(t, rep), `"all"`)

	full, err := Run(context.Background(), exp, src, Options{Top: 2, All: true})
	require.NoError(t, err)
	fw := full.Worlds[0]
	require.Len(t, fw.Policies, 2)
	require.Len(t, fw.All, 3)
	require.Equal(t, fw.Policies, fw.All[:2])
	require.Equal(t, "loan-underwriting@reject-all", fw.All[2].PolicyID)
	for i := 1; i < len(fw.All); i++ {
		require.GreaterOrEqual(t, fw.All[i-1].Objective.Mean, fw.All[i].Objective.Mean)
	}

	// Without an override the experiment's top (5) applies.
	def, err := Run(context.Background(), exp, src, Options{})
	require.NoError(t, err)
	require.Equal(t, 5, def.Top)
	require.Len(t, def.Worlds[0].Policies, 3)
}

func TestRun_ConstraintProb(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "graph.yaml", readLoan(t, "graph.yaml"))
	s := readLoan(t, "sim.yaml")
	require.Contains(t, s, "  maximize: mean\n")
	s = strings.Replace(s, "  maximize: mean\n", "  maximize: mean\n  constraints:\n    cheap: \"run.cost <= 100.0\"\n    approved: \"nodes.approve.status == 'Succeeded'\"\n", 1)
	writeFile(t, dir, "sim.yaml", s)
	exp := loanExperiment(t, dir, 400, "recession")
	src := FilesSource{Files: []string{filepath.Join(loanDir, recessionFile)}}

	rep, err := Run(context.Background(), exp, src, Options{Workers: 3})
	require.NoError(t, err)
	got := rep.Worlds[0].Policies[0].Objective.ConstraintProb

	// Recompute independently by running the same episodes directly.
	policies, err := src.Policies(exp.Model)
	require.NoError(t, err)
	counts := map[string]int{}
	for r := range 400 {
		rec, err := kernel.RunEpisode(context.Background(), exp.Model, exp.Worlds[0], policies[0],
			sampler.NewMonteCarlo(sim.Seed(42, "recession", r)), kernel.Options{})
		require.NoError(t, err)
		for name, ok := range rec.Constraints {
			if ok {
				counts[name]++
			}
		}
	}
	require.Equal(t, map[string]float64{
		"approved": float64(counts["approved"]) / 400,
		"cheap":    float64(counts["cheap"]) / 400,
	}, got)
	require.Greater(t, got["cheap"], 0.0)
	require.Less(t, got["cheap"], 1.0)
}

func TestRun_Progress(t *testing.T) {
	exp := loanExperiment(t, loanDir, 2100)
	src := FilesSource{Files: []string{filepath.Join(loanDir, recessionFile)}}
	var mu sync.Mutex
	var calls [][2]int
	_, err := Run(context.Background(), exp, src, Options{Workers: 4, Progress: func(done, total int) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, [2]int{done, total})
	}})
	require.NoError(t, err)
	require.Len(t, calls, 6) // 2 worlds x 3 chunks
	for i, c := range calls {
		require.Equal(t, 4200, c[1])
		if i > 0 {
			require.Greater(t, c[0], calls[i-1][0])
		}
	}
	require.Equal(t, 4200, calls[len(calls)-1][0])
}

func TestRun_Cancellation(t *testing.T) {
	exp := loanExperiment(t, loanDir, 10_000_000)
	src := FilesSource{Files: []string{filepath.Join(loanDir, recessionFile)}}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	rep, err := Run(ctx, exp, src, Options{Workers: 4})
	elapsed := time.Since(start)
	require.Nil(t, rep)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, elapsed, time.Second)

	_, err = Run(ctx, exp, src, Options{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestRun_Errors(t *testing.T) {
	exp := loanExperiment(t, loanDir, 10)
	ctx := context.Background()
	src := FilesSource{Files: []string{filepath.Join(loanDir, recessionFile)}}

	_, err := Run(ctx, nil, src, Options{})
	require.Error(t, err)
	_, err = Run(ctx, exp, nil, Options{})
	require.Error(t, err)

	_, err = Run(ctx, exp, FilesSource{Files: []string{"missing.yaml"}}, Options{})
	var ve *engine.ValidationErrors
	require.ErrorAs(t, err, &ve)

	dup := FilesSource{Files: []string{filepath.Join(loanDir, recessionFile), filepath.Join(loanDir, recessionFile)}}
	_, err = Run(ctx, exp, dup, Options{})
	require.ErrorAs(t, err, &ve)
	require.Equal(t, engine.CodeDuplicateID, ve.Issues()[0].Code)

	unknown := *exp
	unknownDef := *exp.Def
	unknownDef.Evaluator = "bayesian"
	unknown.Def = &unknownDef
	_, err = Run(ctx, &unknown, src, Options{})
	require.ErrorContains(t, err, `unknown evaluator "bayesian"`)

	// Exact evaluation ignores runs; Monte Carlo requires runs >= 1.
	noRuns := *exp
	noRunsDef := *exp.Def
	noRunsDef.Runs = 0
	noRuns.Def = &noRunsDef
	_, err = Run(ctx, &noRuns, src, Options{})
	require.ErrorContains(t, err, "runs must be >= 1")
	noRunsDef.Evaluator = EvaluatorExact
	rep, err := Run(ctx, &noRuns, src, Options{})
	require.NoError(t, err)
	require.Equal(t, 0, rep.Runs)
	require.True(t, rep.Worlds[0].Policies[0].Objective.Exact)
}

// TestRun_LoanRecessionMean checks the Monte Carlo mean of recession-v1 in
// the recession world against its exact value 202.525 (200k runs, seed 42;
// fewer runs under the race detector).
func TestRun_LoanRecessionMean(t *testing.T) {
	if testing.Short() {
		t.Skip("long Monte Carlo run")
	}
	runs := 200_000
	if raceEnabled {
		runs = 20_000
	}
	exp := loanExperiment(t, loanDir, runs, "recession")
	src := FilesSource{Files: []string{filepath.Join(loanDir, recessionFile)}}
	start := time.Now()
	rep, err := Run(context.Background(), exp, src, Options{})
	require.NoError(t, err)
	elapsed := time.Since(start)

	est := rep.Worlds[0].Policies[0].Objective
	se := est.StdDev / math.Sqrt(float64(est.N))
	t.Logf("runs=%d mean=%.4f stddev=%.4f se=%.4f ci=[%.4f, %.4f] p50=%v p95=%v elapsed=%v",
		est.N, est.Mean, est.StdDev, se, est.CI95Low, est.CI95High, est.P50, est.P95, elapsed)
	require.Equal(t, runs, est.N)
	require.InDelta(t, recessionValue, est.Mean, 3*se)
}

func TestRun_CheckpointResume(t *testing.T) {
	const runs = 200
	const chunkSize = 50
	exp := loanExperiment(t, loanDir, runs, "recession")
	src := FilesSource{Files: []string{filepath.Join(loanDir, recessionFile)}}

	// 1. Uninterrupted run
	repFull, err := Run(context.Background(), exp, src, Options{
		Workers:   2,
		ChunkSize: chunkSize,
	})
	require.NoError(t, err)

	// 2. Interrupted run: cancel after 2 chunks
	ctx, cancel := context.WithCancel(context.Background())
	var savedCP sim.ExperimentCheckpoint
	var cpCount int
	repInterrupted, err := Run(ctx, exp, src, Options{
		Workers:   1,
		ChunkSize: chunkSize,
		Checkpoint: func(cp sim.ExperimentCheckpoint) {
			savedCP = cp
			cpCount++
			if cp.CompletedChunks == 2 {
				cancel()
			}
		},
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, repInterrupted)
	require.Equal(t, 2, savedCP.CompletedChunks)
	require.Equal(t, 4, savedCP.TotalChunks)
	require.NotEmpty(t, savedCP.PartialResults)

	// 3. Resumed run
	repResumed, err := Run(context.Background(), exp, src, Options{
		Workers:    2,
		ChunkSize:  chunkSize,
		ResumeFrom: &savedCP,
	})
	require.NoError(t, err)

	// 4. Assert resumed run exactly matches uninterrupted run
	require.Equal(t, marshal(t, repFull), marshal(t, repResumed))
}

func TestRun_RecordSink(t *testing.T) {
	const runs = 20
	const chunkSize = 10
	exp := loanExperiment(t, loanDir, runs, "recession")
	src := FilesSource{Files: []string{filepath.Join(loanDir, recessionFile)}}

	var buf bytes.Buffer
	rep, err := Run(context.Background(), exp, src, Options{
		Workers:    2,
		ChunkSize:  chunkSize,
		RecordSink: &buf,
	})
	require.NoError(t, err)
	require.NotNil(t, rep)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	// 1 world * 1 policy * 20 runs = 20 episodes
	require.Len(t, lines, runs)
	seen := make(map[int]bool)
	for i, line := range lines {
		var rec sim.RunRecord
		err := json.Unmarshal([]byte(line), &rec)
		require.NoError(t, err, "line %d: invalid JSON: %s", i, line)
		require.Equal(t, sim.WorldID("recession"), rec.World)
		require.Equal(t, recessionID, rec.PolicyID)
		seen[rec.Replication] = true
	}
	require.Len(t, seen, runs)
	for r := 0; r < runs; r++ {
		require.True(t, seen[r], "expected replication %d in export", r)
	}
}
