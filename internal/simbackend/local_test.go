package simbackend

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/require"
)

func TestLocalImplementsBackend(t *testing.T) {
	var b Backend = NewLocal()
	require.NotNil(t, b)
}

func TestValidateModel(t *testing.T) {
	ctx := context.Background()
	b := NewLocal()

	issues, err := b.ValidateModel(ctx, filepath.Join("..", "..", "examples", "simulation", "loan", "sim.yaml"))
	require.NoError(t, err)
	require.Empty(t, issues)

	issues, err = b.ValidateModel(ctx, filepath.Join("testdata", "invalid-sim.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, issues)
	require.Equal(t, "$.behaviors.ghost_node", issues[0].Path)
	for i := 1; i < len(issues); i++ {
		require.LessOrEqual(t, issues[i-1].Path, issues[i].Path, "issues are sorted")
	}

	issues, err = b.ValidateModel(ctx, filepath.Join("testdata", "sim.txt"))
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Equal(t, engine.CodeInvalidFormat, issues[0].Code)

	_, err = b.ValidateModel(ctx, filepath.Join("testdata", "missing.yaml"))
	require.Error(t, err)
}

func TestRunExperiment(t *testing.T) {
	var calls int
	var last sim.Progress
	res, err := NewLocal().RunExperiment(context.Background(), filepath.Join("testdata", "experiment.yaml"), RunOptions{
		Progress: func(p sim.Progress) { calls++; last = p },
	})
	require.NoError(t, err)
	require.Empty(t, res.ExperimentID)
	require.NotEmpty(t, res.Policies)
	require.Len(t, res.Report.Worlds, 2)
	means := map[sim.WorldID]float64{}
	for _, w := range res.Report.Worlds {
		require.NotEmpty(t, w.Policies)
		means[w.World] = w.Policies[0].Objective.Mean
	}
	require.InDelta(t, 460.0, means["base"], 1e-9)
	require.InDelta(t, 202.525, means["recession"], 1e-9)
	require.Positive(t, calls)
	require.Equal(t, last.Total, last.Done)
}

func TestRunExperimentTop(t *testing.T) {
	res, err := NewLocal().RunExperiment(context.Background(), filepath.Join("testdata", "experiment.yaml"), RunOptions{Top: 1, Workers: 1})
	require.NoError(t, err)
	for _, w := range res.Report.Worlds {
		require.Len(t, w.Policies, 1)
	}
}

func TestRunExperimentErrors(t *testing.T) {
	_, err := NewLocal().RunExperiment(context.Background(), filepath.Join("testdata", "missing.yaml"), RunOptions{})
	require.Error(t, err)
	var internal *InternalError
	require.NotErrorAs(t, err, &internal)
	issues := IssuesOf(err, engine.CodeInvalidValue)
	require.Len(t, issues, 1)
	require.Equal(t, "$", issues[0].Path)
}

func TestReportUnsupported(t *testing.T) {
	raw, err := NewLocal().Report(context.Background(), "exp-1", "summary")
	require.ErrorIs(t, err, ErrUnsupported)
	require.Nil(t, raw)
}

func TestExplainPolicy(t *testing.T) {
	dir := filepath.Join("..", "sim", "explain", "testdata")
	want, err := os.ReadFile(filepath.Join(dir, "recession-v1.golden"))
	require.NoError(t, err)
	got, err := NewLocal().ExplainPolicy(context.Background(), filepath.Join(dir, "recession-v1.policy.yaml"))
	require.NoError(t, err)
	require.Equal(t, string(want), got)

	_, err = NewLocal().ExplainPolicy(context.Background(), filepath.Join(dir, "missing.yaml"))
	require.Error(t, err)
}

// testBundle returns the testdata loan bundle with the given sim document.
func testBundle(t *testing.T, simFile string) sim.ModelBundle {
	t.Helper()
	simDoc, err := os.ReadFile(filepath.Join("testdata", simFile))
	require.NoError(t, err)
	graphDoc, err := os.ReadFile(filepath.Join("testdata", "graph.yaml"))
	require.NoError(t, err)
	policy, err := os.ReadFile(filepath.Join("testdata", "recession-v1.yaml"))
	require.NoError(t, err)
	return sim.ModelBundle{
		ID: "loan-underwriting", Sim: simDoc, Graph: graphDoc,
		Policies: map[string][]byte{"recession-v1.yaml": policy}, Format: "yaml",
	}
}

func TestLocal_ValidateBundle(t *testing.T) {
	ctx := context.Background()
	issues, err := NewLocal().ValidateBundle(ctx, testBundle(t, "sim.yaml"))
	require.NoError(t, err)
	require.Empty(t, issues)

	issues, err = NewLocal().ValidateBundle(ctx, testBundle(t, "invalid-sim.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, issues)
	require.Equal(t, "$.behaviors.ghost_node", issues[0].Path)
	for i := 1; i < len(issues); i++ {
		require.LessOrEqual(t, issues[i-1].Path, issues[i].Path, "issues are sorted")
	}

	b := testBundle(t, "sim.yaml")
	b.Policies = map[string][]byte{"bad.txt": []byte("x")}
	issues, err = NewLocal().ValidateBundle(ctx, b)
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Equal(t, engine.CodeInvalidFormat, issues[0].Code)
}

func TestLocal_RunBundle(t *testing.T) {
	ctx := context.Background()
	expData, err := os.ReadFile(filepath.Join("testdata", "experiment.yaml"))
	require.NoError(t, err)
	b := testBundle(t, "sim.yaml")
	res, err := NewLocal().RunBundle(ctx, b, expData, "yaml", RunOptions{})
	require.NoError(t, err)
	require.Empty(t, res.ExperimentID)
	require.Len(t, res.Report.Worlds, 2)
	means := map[sim.WorldID]float64{}
	for _, w := range res.Report.Worlds {
		require.NotEmpty(t, w.Policies)
		means[w.World] = w.Policies[0].Objective.Mean
	}
	require.InDelta(t, 460.0, means["base"], 1e-9)
	require.InDelta(t, 202.525, means["recession"], 1e-9)
}
