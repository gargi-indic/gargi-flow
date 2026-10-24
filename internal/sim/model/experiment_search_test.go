package model

import (
	"testing"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/stretchr/testify/require"
)

// TestParseExperiment_Search checks the policies.search values: exhaustive,
// exact, grid and random are accepted, anything else is rejected.
func TestParseExperiment_Search(t *testing.T) {
	doc := func(search string) []byte {
		return []byte("apiVersion: experiment/v1\nmodel: ./sim.yaml\npolicies: {search: " + search + "}\nevaluator: exact\nruns: 1\nseed: 1\n")
	}
	for _, search := range []string{"exhaustive", "exact", "racing"} {
		exp, err := ParseExperiment(doc(search), engine.FormatYAML)
		require.NoError(t, err, search)
		require.Equal(t, search, exp.Policies.Search)
	}

	// max_policies is accepted with search: exact (and ignored by it).
	exp, err := ParseExperiment(append(doc("exact"), "max_policies: 3\n"...), engine.FormatYAML)
	require.NoError(t, err)
	require.Equal(t, 3, exp.MaxPolicies)

	for _, search := range []string{"bayesian", "Exact", "backward_induction"} {
		_, err := ParseExperiment(doc(search), engine.FormatYAML)
		var ve *engine.ValidationErrors
		require.ErrorAs(t, err, &ve, search)
		require.Equal(t, []engine.ValidationIssue{
			{Path: "$.policies.search", Code: engine.CodeInvalidValue, Message: "search must be exhaustive, exact, grid, random or racing"},
		}, ve.Issues(), search)
	}
}

// TestParseExperiment_GridRandom checks the policy, samples and seed fields
// of grid and random search.
func TestParseExperiment_GridRandom(t *testing.T) {
	doc := func(policies string) []byte {
		return []byte("apiVersion: experiment/v1\nmodel: ./sim.yaml\npolicies: " + policies + "\nruns: 1\nseed: 9\n")
	}

	exp, err := ParseExperiment(doc("{search: grid, policy: ./theta.yaml}"), engine.FormatYAML)
	require.NoError(t, err)
	require.Equal(t, Policies{Search: "grid", Policy: "./theta.yaml"}, exp.Policies)

	exp, err = ParseExperiment(doc("{search: random, policy: ./theta.yaml, samples: 50, seed: 7}"), engine.FormatYAML)
	require.NoError(t, err)
	seven := uint64(7)
	require.Equal(t, Policies{Search: "random", Policy: "./theta.yaml", Samples: 50, Seed: &seven}, exp.Policies)

	exp, err = ParseExperiment(doc("{search: random, policy: ./theta.yaml, samples: 1}"), engine.FormatYAML)
	require.NoError(t, err)
	require.Nil(t, exp.Policies.Seed)

	tests := []struct {
		policies string
		want     []engine.ValidationIssue
	}{
		{"{search: grid}", []engine.ValidationIssue{
			{Path: "$.policies.policy", Code: engine.CodeMissingRequired, Message: "search: grid requires policy"},
		}},
		{"{search: random, samples: 3}", []engine.ValidationIssue{
			{Path: "$.policies.policy", Code: engine.CodeMissingRequired, Message: "search: random requires policy"},
		}},
		{"{search: random, policy: p.yaml}", []engine.ValidationIssue{
			{Path: "$.policies.samples", Code: engine.CodeInvalidValue, Message: "search: random requires samples >= 1"},
		}},
		{"{search: random, policy: p.yaml, samples: -2}", []engine.ValidationIssue{
			{Path: "$.policies.samples", Code: engine.CodeInvalidValue, Message: "search: random requires samples >= 1"},
		}},
		{"{search: exhaustive, policy: p.yaml, samples: 3, seed: 1}", []engine.ValidationIssue{
			{Path: "$.policies.policy", Code: engine.CodeInvalidValue, Message: "policy is only allowed with search: grid or random"},
			{Path: "$.policies.samples", Code: engine.CodeInvalidValue, Message: "samples is only allowed with search: random"},
			{Path: "$.policies.seed", Code: engine.CodeInvalidValue, Message: "seed is only allowed with search: random"},
		}},
		{"{search: grid, policy: p.yaml, samples: 3, seed: 1}", []engine.ValidationIssue{
			{Path: "$.policies.samples", Code: engine.CodeInvalidValue, Message: "samples is only allowed with search: random"},
			{Path: "$.policies.seed", Code: engine.CodeInvalidValue, Message: "seed is only allowed with search: random"},
		}},
		{"{files: [a.yaml], policy: p.yaml}", []engine.ValidationIssue{
			{Path: "$.policies.policy", Code: engine.CodeInvalidValue, Message: "policy is only allowed with search: grid or random"},
		}},
		{"{search: grid, files: [a.yaml], policy: p.yaml}", []engine.ValidationIssue{
			{Path: "$.policies", Code: engine.CodeInvalidValue, Message: "policies must have exactly one of search or files"},
		}},
		{"{search: random, policy: 3, samples: x, seed: -1, extra: 1}", []engine.ValidationIssue{
			{Path: "$.policies.extra", Code: engine.CodeUnknownField, Message: "unknown field 'extra'"},
			{Path: "$.policies.policy", Code: engine.CodeInvalidType, Message: "expected string"},
			{Path: "$.policies.policy", Code: engine.CodeMissingRequired, Message: "search: random requires policy"},
			{Path: "$.policies.samples", Code: engine.CodeInvalidType, Message: "expected integer"},
			{Path: "$.policies.samples", Code: engine.CodeInvalidValue, Message: "search: random requires samples >= 1"},
			{Path: "$.policies.seed", Code: engine.CodeInvalidValue, Message: "seed must be >= 0"},
		}},
	}
	for _, tt := range tests {
		_, err := ParseExperiment(doc(tt.policies), engine.FormatYAML)
		var ve *engine.ValidationErrors
		require.ErrorAs(t, err, &ve, tt.policies)
		require.Equal(t, tt.want, ve.Issues(), tt.policies)
	}
}

// TestParseExperiment_Racing checks the racing optimizer configuration
// and candidate sources.
func TestParseExperiment_Racing(t *testing.T) {
	doc := func(policies string) []byte {
		return []byte("apiVersion: experiment/v1\nmodel: ./sim.yaml\npolicies: " + policies + "\nruns: 100\nseed: 42\n")
	}

	// Direct policy
	exp, err := ParseExperiment(doc("{search: racing, policy: ./theta.yaml}"), engine.FormatYAML)
	require.NoError(t, err)
	require.Equal(t, Policies{Search: "racing", Policy: "./theta.yaml"}, exp.Policies)

	// Direct files
	exp, err = ParseExperiment(doc("{search: racing, files: [./p1.yaml, ./p2.yaml]}"), engine.FormatYAML)
	require.NoError(t, err)
	require.Equal(t, Policies{Search: "racing", Files: []string{"./p1.yaml", "./p2.yaml"}}, exp.Policies)

	// Candidates object: grid
	exp, err = ParseExperiment(doc("{search: racing, min_runs: 10, max_runs: 100, factor: 2.5, alpha: 0.01, candidates: {search: grid, policy: ./theta.yaml}}"), engine.FormatYAML)
	require.NoError(t, err)
	require.Equal(t, Policies{
		Search:  "racing",
		MinRuns: 10,
		MaxRuns: 100,
		Factor:  2.5,
		Alpha:   0.01,
		Candidates: &Policies{
			Search: "grid",
			Policy: "./theta.yaml",
		},
	}, exp.Policies)

	// Candidates object: exhaustive
	exp, err = ParseExperiment(doc("{search: racing, candidates: {search: exhaustive}}"), engine.FormatYAML)
	require.NoError(t, err)
	require.Equal(t, Policies{
		Search: "racing",
		Candidates: &Policies{
			Search: "exhaustive",
		},
	}, exp.Policies)

	tests := []struct {
		policies string
		want     []engine.ValidationIssue
	}{
		{"{search: racing, min_runs: -1}", []engine.ValidationIssue{
			{Path: "$.policies.min_runs", Code: engine.CodeInvalidValue, Message: "min_runs must be >= 1"},
		}},
		{"{search: racing, min_runs: 50, max_runs: 10}", []engine.ValidationIssue{
			{Path: "$.policies.min_runs", Code: engine.CodeInvalidValue, Message: "min_runs must be <= max_runs"},
		}},
		{"{search: racing, factor: 0.5}", []engine.ValidationIssue{
			{Path: "$.policies.factor", Code: engine.CodeInvalidValue, Message: "factor must be > 1"},
		}},
		{"{search: racing, alpha: 1.5}", []engine.ValidationIssue{
			{Path: "$.policies.alpha", Code: engine.CodeInvalidValue, Message: "alpha must be in (0, 1)"},
		}},
		{"{search: racing, policy: p.yaml, files: [a.yaml]}", []engine.ValidationIssue{
			{Path: "$.policies", Code: engine.CodeInvalidValue, Message: "racing candidates must be specified via only one of candidates, policy, or files"},
		}},
		{"{search: grid, candidates: {search: exhaustive}}", []engine.ValidationIssue{
			{Path: "$.policies.candidates", Code: engine.CodeInvalidValue, Message: "candidates is only allowed with search: racing"},
			{Path: "$.policies.policy", Code: engine.CodeMissingRequired, Message: "search: grid requires policy"},
		}},
	}
	for _, tt := range tests {
		_, err := ParseExperiment(doc(tt.policies), engine.FormatYAML)
		var ve *engine.ValidationErrors
		require.ErrorAs(t, err, &ve, tt.policies)
		require.Equal(t, tt.want, ve.Issues(), tt.policies)
	}
}
