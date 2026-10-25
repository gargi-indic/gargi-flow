package mcp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaimsTriageTemplateIsByteIdentical(t *testing.T) {
	filesToCompare := []string{
		"sim.yaml",
		"graph.yaml",
		"experiment.yaml",
		"policies/rules.yaml",
		"policies/matrix.yaml",
		"policies/agent.yaml",
	}

	for _, file := range filesToCompare {
		t.Run(file, func(t *testing.T) {
			expectedPath := filepath.Join("../../examples/simulation/claims", file)
			expected, err := os.ReadFile(expectedPath)
			require.NoError(t, err, "failed to read expected file")

			actualPath := filepath.Join("templates/claims-triage", file)
			actual, err := os.ReadFile(actualPath)
			require.NoError(t, err, "failed to read actual file")

			require.Equal(t, expected, actual, "template file %s drifted from example", file)
		})
	}
}

func TestClaims_AgentWalkthrough(t *testing.T) {
	s := newWorkspaceSession(t)

	// Step 1: list_templates discovers claims-triage
	var listing struct {
		Templates []struct {
			Name  string   `json:"name"`
			Files []string `json:"files"`
		} `json:"templates"`
	}
	isErr := call(t, s, "list_templates", m{}, &listing)
	require.False(t, isErr)

	var foundClaims bool
	for _, templ := range listing.Templates {
		if templ.Name == "claims-triage" {
			foundClaims = true
			require.Contains(t, templ.Files, "graph.yaml")
			require.Contains(t, templ.Files, "sim.yaml")
			require.Contains(t, templ.Files, "experiment.yaml")
			require.Contains(t, templ.Files, "policies/rules.yaml")
			require.Contains(t, templ.Files, "policies/matrix.yaml")
			require.Contains(t, templ.Files, "policies/agent.yaml")
		}
	}
	require.True(t, foundClaims, "claims-triage template must be listed")

	// Step 2: create_model with template: "claims"
	var created struct {
		ID    string   `json:"id"`
		Files []string `json:"files"`
	}
	isErr = call(t, s, "create_model", m{
		"id":       "claims-service",
		"template": "claims",
	}, &created)
	require.False(t, isErr)
	require.Equal(t, "claims-service", created.ID)
	require.Contains(t, created.Files, "graph.yaml")
	require.Contains(t, created.Files, "sim.yaml")
	require.Contains(t, created.Files, "policies/rules.yaml")

	// Step 3: validate_model confirms 0 errors
	issues := validate(t, s, "claims-service")
	require.Empty(t, issues, "claims model should have no validation errors")

	// Step 4: run_experiment with Pareto and tournament reports
	expYAML := `apiVersion: experiment/v1
worlds: [normal, cat_surge]
policies:
  files:
    - rules.yaml
    - matrix.yaml
    - agent.yaml
evaluator: monte_carlo
runs: 50
seed: 42
top: 5
reports:
  - "pareto:total_cost:min,p95_duration:min"
  - "tournament"
world_prior:
  normal: 0.8
  cat_surge: 0.2
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
		"model_id":   "claims-service",
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
	require.Equal(t, "total_cost", p0.X.Metric)
	require.Equal(t, "p95_duration", p0.Y.Metric)
	require.Len(t, p0.Worlds, 2)

	for _, w := range p0.Worlds {
		require.NotEmpty(t, w.Points, "world %s should have Pareto points", w.World)
		for _, pt := range w.Points {
			require.Greater(t, pt.X, 0.0)
			require.Greater(t, pt.Y, 0.0)
			require.NotEmpty(t, pt.PolicyID)
		}
	}

	// Step 6: get_report for kind "tournament"
	var tournData struct {
		Rankings []struct {
			PolicyID string  `json:"policyId"`
			Rank     int     `json:"rank"`
			Mean     float64 `json:"mean"`
		} `json:"rankings"`
	}
	isErr = call(t, s, "get_report", m{
		"id":   runRes.ID,
		"kind": "tournament",
	}, &tournData)
	require.False(t, isErr)
	require.Len(t, tournData.Rankings, 3)
}
