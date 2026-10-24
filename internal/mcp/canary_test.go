package mcp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanaryRolloutTemplateIsByteIdentical(t *testing.T) {
	filesToCompare := []string{
		"sim.yaml",
		"graph.yaml",
		"experiment.yaml",
		"policies/canary-grid.yaml",
	}

	for _, file := range filesToCompare {
		t.Run(file, func(t *testing.T) {
			expectedPath := filepath.Join("../../examples/simulation/canary", file)
			expected, err := os.ReadFile(expectedPath)
			require.NoError(t, err, "failed to read expected file")

			actualPath := filepath.Join("templates/canary-rollout", file)
			actual, err := os.ReadFile(actualPath)
			require.NoError(t, err, "failed to read actual file")

			require.Equal(t, expected, actual, "template file %s drifted from example", file)
		})
	}
}

func TestCanary_AgentWalkthrough(t *testing.T) {
	s := newWorkspaceSession(t)

	// Step 1: list_templates discovers canary-rollout
	var listing struct {
		Templates []struct {
			Name  string   `json:"name"`
			Files []string `json:"files"`
		} `json:"templates"`
	}
	isErr := call(t, s, "list_templates", m{}, &listing)
	require.False(t, isErr)

	var foundCanary bool
	for _, templ := range listing.Templates {
		if templ.Name == "canary-rollout" {
			foundCanary = true
			require.Contains(t, templ.Files, "graph.yaml")
			require.Contains(t, templ.Files, "sim.yaml")
			require.Contains(t, templ.Files, "experiment.yaml")
			require.Contains(t, templ.Files, "policies/canary-grid.yaml")
		}
	}
	require.True(t, foundCanary, "canary-rollout template must be listed")

	// Step 2: create_model with template: "canary"
	var created struct {
		ID    string   `json:"id"`
		Files []string `json:"files"`
	}
	isErr = call(t, s, "create_model", m{
		"id":       "canary-service",
		"template": "canary",
	}, &created)
	require.False(t, isErr)
	require.Equal(t, "canary-service", created.ID)
	require.Contains(t, created.Files, "graph.yaml")
	require.Contains(t, created.Files, "sim.yaml")
	require.Contains(t, created.Files, "policies/canary-grid.yaml")

	// Step 3: validate_model confirms 0 errors
	issues := validate(t, s, "canary-service")
	require.Empty(t, issues, "canary model should have no validation errors")

	// Step 4: run_experiment with Pareto and regret reports
	expYAML := `apiVersion: experiment/v1
worlds: [low_risk_service, payments_service]
policies: {search: grid, policy: canary-grid.yaml}
evaluator: monte_carlo
runs: 50
seed: 42
top: 5
reports: [regret, "pareto:lead_time:min,bad_user_mins:min"]
`

	var runRes struct {
		ID     string `json:"id"`
		Worlds []struct {
			World      string  `json:"world"`
			BestPolicy string  `json:"best_policy"`
			BestMean   float64 `json:"best_mean"`
			CIWidth    float64 `json:"ci_width"`
		} `json:"worlds"`
	}
	isErr = call(t, s, "run_experiment", m{
		"model_id":   "canary-service",
		"experiment": expYAML,
	}, &runRes)
	require.False(t, isErr)
	require.NotEmpty(t, runRes.ID)
	require.Len(t, runRes.Worlds, 2)

	// Step 5: get_report for kind "pareto"
	var paretoRes []struct {
		X struct {
			Metric string `json:"metric"`
			Goal   string `json:"goal"`
		} `json:"x"`
		Y struct {
			Metric string `json:"metric"`
			Goal   string `json:"goal"`
		} `json:"y"`
		Worlds []struct {
			World  string `json:"world"`
			Points []struct {
				PolicyID string  `json:"policyId"`
				X        float64 `json:"x"`
				Y        float64 `json:"y"`
			} `json:"points"`
		} `json:"worlds"`
	}
	isErr = call(t, s, "get_report", m{
		"id":   runRes.ID,
		"kind": "pareto",
	}, &paretoRes)
	require.False(t, isErr)
	require.NotEmpty(t, paretoRes)
	p0 := paretoRes[0]
	require.Equal(t, "lead_time", p0.X.Metric)
	require.Equal(t, "bad_user_mins", p0.Y.Metric)
	require.Len(t, p0.Worlds, 2)

	for _, w := range p0.Worlds {
		require.NotEmpty(t, w.Points, "world %s should have Pareto points", w.World)
		for _, pt := range w.Points {
			require.Greater(t, pt.X, 0.0)
			require.GreaterOrEqual(t, pt.Y, 0.0)
			require.NotEmpty(t, pt.PolicyID)
		}
	}

	// Step 6: get_report for kind "regret"
	var regretData map[string]any
	isErr = call(t, s, "get_report", m{
		"id":   runRes.ID,
		"kind": "regret",
	}, &regretData)
	require.False(t, isErr)
	require.NotEmpty(t, regretData)
}
