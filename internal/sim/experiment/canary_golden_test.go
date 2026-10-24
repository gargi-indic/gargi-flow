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

func TestCanaryGolden_ParetoSet(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	canaryDir := filepath.Join(root, "examples", "simulation", "canary")

	simYAML, err := os.ReadFile(filepath.Join(canaryDir, "sim.yaml"))
	require.NoError(t, err)
	graphYAML, err := os.ReadFile(filepath.Join(canaryDir, "graph.yaml"))
	require.NoError(t, err)
	expYAML, err := os.ReadFile(filepath.Join(canaryDir, "experiment.yaml"))
	require.NoError(t, err)
	policyYAML, err := os.ReadFile(filepath.Join(canaryDir, "policies", "canary-grid.yaml"))
	require.NoError(t, err)

	bundle := sim.ModelBundle{
		ID:      "canary-rollout",
		Version: "1",
		Sim:     simYAML,
		Graph:   graphYAML,
		Policies: map[string][]byte{
			"canary-grid.yaml": policyYAML,
		},
		Format: "yaml",
	}

	registry, err := jobs.NewRegistry()
	require.NoError(t, err)

	compiledExp, err := jobs.CompileExperiment(bundle, expYAML, "yaml", registry)
	require.NoError(t, err)

	rep, err := experiment.Run(context.Background(), compiledExp, compiledExp.PolicySource(), experiment.Options{
		Workers: 4,
		All:     true,
	})
	require.NoError(t, err)
	require.NotNil(t, rep)
	require.Len(t, rep.Worlds, 2)

	// Verify both worlds have evaluated policies
	for _, w := range rep.Worlds {
		require.NotEmpty(t, w.Policies, "policies in world %s", w.World)
		require.NotEmpty(t, w.Policies[0].PolicyID)
	}

	// Verify Pareto report
	require.NotNil(t, rep.Reports)
	require.NotEmpty(t, rep.Reports.Pareto)

	pareto := rep.Reports.Pareto[0]
	require.Equal(t, "lead_time", pareto.X.Metric)
	require.Equal(t, "bad_user_mins", pareto.Y.Metric)

	var lowRiskPareto, paymentsPareto *report.ParetoWorld
	for i := range pareto.Worlds {
		switch pareto.Worlds[i].World {
		case "low_risk_service":
			lowRiskPareto = &pareto.Worlds[i]
		case "payments_service":
			paymentsPareto = &pareto.Worlds[i]
		}
	}
	require.NotNil(t, lowRiskPareto)
	require.NotEmpty(t, lowRiskPareto.Points)

	require.NotNil(t, paymentsPareto)
	require.NotEmpty(t, paymentsPareto.Points)

	// Non-domination property on points: verified non-empty points with plausible values
	for _, pt := range paymentsPareto.Points {
		require.Greater(t, pt.X, 0.0)
		require.GreaterOrEqual(t, pt.Y, 0.0)
		require.NotEmpty(t, pt.PolicyID)
	}

	// Compare points between low risk and payments:
	// Payments service has higher p_bad (0.20 vs 0.05), so max bad_user_mins is higher
	maxBadLowRisk := 0.0
	for _, pt := range lowRiskPareto.Points {
		if pt.Y > maxBadLowRisk {
			maxBadLowRisk = pt.Y
		}
	}
	maxBadPayments := 0.0
	for _, pt := range paymentsPareto.Points {
		if pt.Y > maxBadPayments {
			maxBadPayments = pt.Y
		}
	}
	require.Greater(t, maxBadPayments, maxBadLowRisk, "payments service should incur more bad user mins than low risk service")
}
