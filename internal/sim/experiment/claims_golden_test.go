package experiment_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	"github.com/gargi-indic/gargi-flow/internal/sim/report"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/require"
)

func TestClaimsGolden(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	claimsDir := filepath.Join(root, "examples", "simulation", "claims")

	simYAML, err := os.ReadFile(filepath.Join(claimsDir, "sim.yaml"))
	require.NoError(t, err)
	graphYAML, err := os.ReadFile(filepath.Join(claimsDir, "graph.yaml"))
	require.NoError(t, err)
	expYAML, err := os.ReadFile(filepath.Join(claimsDir, "experiment.yaml"))
	require.NoError(t, err)
	rulesYAML, err := os.ReadFile(filepath.Join(claimsDir, "policies", "rules.yaml"))
	require.NoError(t, err)
	matrixYAML, err := os.ReadFile(filepath.Join(claimsDir, "policies", "matrix.yaml"))
	require.NoError(t, err)
	agentYAML, err := os.ReadFile(filepath.Join(claimsDir, "policies", "agent.yaml"))
	require.NoError(t, err)

	bundle := sim.ModelBundle{
		ID:      "claims-triage",
		Version: "1",
		Sim:     simYAML,
		Graph:   graphYAML,
		Policies: map[string][]byte{
			"rules.yaml":  rulesYAML,
			"matrix.yaml": matrixYAML,
			"agent.yaml":  agentYAML,
		},
		Format: "yaml",
	}

	registry, err := jobs.NewRegistry()
	require.NoError(t, err)

	compiledExp, err := jobs.CompileExperiment(bundle, expYAML, "yaml", registry)
	require.NoError(t, err)

	// Run 1
	rep1, err := experiment.Run(context.Background(), compiledExp, compiledExp.PolicySource(), experiment.Options{
		Workers: 4,
		All:     true,
	})
	require.NoError(t, err)
	require.NotNil(t, rep1)
	require.Len(t, rep1.Worlds, 2)

	// 1. Deterministic outputs: Run 2 with same seed yields zero variance
	rep2, err := experiment.Run(context.Background(), compiledExp, compiledExp.PolicySource(), experiment.Options{
		Workers: 4,
		All:     true,
	})
	require.NoError(t, err)
	require.NotNil(t, rep2)

	for w := range rep1.Worlds {
		require.Equal(t, rep1.Worlds[w].World, rep2.Worlds[w].World)
		require.Len(t, rep2.Worlds[w].Policies, len(rep1.Worlds[w].Policies))
		for p := range rep1.Worlds[w].Policies {
			p1 := rep1.Worlds[w].Policies[p]
			p2 := rep2.Worlds[w].Policies[p]
			require.Equal(t, p1.PolicyID, p2.PolicyID)
			require.Equal(t, p1.Objective.Mean, p2.Objective.Mean, "mean objective must match exactly across runs with same seed")
			require.Equal(t, p1.Objective.StdDev, p2.Objective.StdDev, "stddev must match exactly")
		}
	}

	// 2. Pareto report validation
	require.NotNil(t, rep1.Reports)
	require.NotEmpty(t, rep1.Reports.Pareto)

	pareto := rep1.Reports.Pareto[0]
	require.Equal(t, "total_cost", pareto.X.Metric)
	require.Equal(t, "p95_duration", pareto.Y.Metric)
	require.Equal(t, report.GoalMin, pareto.X.Goal)
	require.Equal(t, report.GoalMin, pareto.Y.Goal)

	var normalPareto, catSurgePareto *report.ParetoWorld
	for i := range pareto.Worlds {
		switch pareto.Worlds[i].World {
		case "normal":
			normalPareto = &pareto.Worlds[i]
		case "cat_surge":
			catSurgePareto = &pareto.Worlds[i]
		}
	}
	require.NotNil(t, normalPareto)
	require.NotEmpty(t, normalPareto.Points)
	require.NotNil(t, catSurgePareto)
	require.NotEmpty(t, catSurgePareto.Points)

	for _, pt := range normalPareto.Points {
		require.Greater(t, pt.X, 0.0, "cost must be positive")
		require.Greater(t, pt.Y, 0.0, "duration must be positive")
		require.NotEmpty(t, pt.PolicyID)
	}
	for _, pt := range catSurgePareto.Points {
		require.Greater(t, pt.X, 0.0, "cost must be positive")
		require.Greater(t, pt.Y, 0.0, "duration must be positive")
		require.NotEmpty(t, pt.PolicyID)
	}

	// Catastrophe surge should incur higher volume and cost than normal world
	require.Greater(t, catSurgePareto.Points[0].X, 0.0)

	// 3. Agent tournament validation
	require.NotNil(t, rep1.Tournament)
	require.NotNil(t, rep1.Reports.Tournament)
	tourn := rep1.Tournament

	require.Equal(t, 0.05, tourn.Alpha)
	require.Len(t, tourn.Rankings, 3)
	require.Len(t, tourn.Pairwise, 3)

	for i, rk := range tourn.Rankings {
		require.Equal(t, i+1, rk.Rank)
		require.NotEmpty(t, rk.PolicyID)
		if i > 0 {
			prev := tourn.Rankings[i-1]
			require.GreaterOrEqual(t, prev.Mean, rk.Mean)
		}
	}

	// Verify tournament determinism across runs
	tourn2 := rep2.Tournament
	require.Len(t, tourn2.Rankings, 3)
	for i := range tourn.Rankings {
		require.Equal(t, tourn.Rankings[i].PolicyID, tourn2.Rankings[i].PolicyID)
		require.Equal(t, tourn.Rankings[i].Mean, tourn2.Rankings[i].Mean)
		require.Equal(t, tourn.Rankings[i].Rank, tourn2.Rankings[i].Rank)
	}

	// Pairwise symmetry and CI properties
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			mAB := tourn.Pairwise[i][j]
			mBA := tourn.Pairwise[j][i]

			require.Equal(t, tourn.Rankings[i].PolicyID, mAB.PolicyA)
			require.Equal(t, tourn.Rankings[j].PolicyID, mAB.PolicyB)

			require.InDelta(t, mAB.MeanDiff, -mBA.MeanDiff, 1e-9)
			require.InDelta(t, mAB.CI[0], -mBA.CI[1], 1e-9)
			require.InDelta(t, mAB.CI[1], -mBA.CI[0], 1e-9)
			require.InDelta(t, 1.0, mAB.WinRate+mBA.WinRate, 1e-9)

			if i == j {
				require.Equal(t, 0.0, mAB.MeanDiff)
				require.Equal(t, 0.5, mAB.WinRate)
			}
		}
	}
}
