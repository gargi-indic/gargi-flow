package simverify_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/simverify"
	"github.com/stretchr/testify/require"
)

func TestVerify_LoanGoldenModel(t *testing.T) {
	loanExpPath := filepath.Join("..", "..", "examples", "simulation", "loan", "experiment.yaml")

	report, err := simverify.VerifyFile(context.Background(), loanExpPath, simverify.VerifyOptions{
		Scenarios: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, report)
	require.Equal(t, 10, report.Scenarios)
	require.Equal(t, 10, report.Matched, "all 10 scenarios must match perfectly with 100% fidelity")
	require.Empty(t, report.Mismatches, "expected 0 mismatches for loan golden model")
}

func TestVerify_MismatchedRetryPolicy_ForbidsRetries(t *testing.T) {
	tmpDir := t.TempDir()

	graphYAML := `apiVersion: graph/v1
id: test-retry-graph
retryPolicies:
  no_retry:
    maxAttempts: 1
nodes:
  - id: worker
    type: http_call
    retryPolicy: no_retry
    config:
      url: "https://example.internal/work"
      method: POST
edges: []
`
	simYAML := `apiVersion: sim/v1
id: test-retry-sim
graph: ./graph.yaml
worlds:
  w1: {}
behaviors:
  worker:
    fail:
      p: 1.0
      class: retryable
objective:
  name: test
  value: "-run.cost"
  maximize: mean
`
	expYAML := `apiVersion: experiment/v1
model: ./sim.yaml
worlds: [w1]
policies: {search: exhaustive}
evaluator: monte_carlo
runs: 10
seed: 42
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "graph.yaml"), []byte(graphYAML), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "sim.yaml"), []byte(simYAML), 0o600))
	expPath := filepath.Join(tmpDir, "experiment.yaml")
	require.NoError(t, os.WriteFile(expPath, []byte(expYAML), 0o600))

	report, err := simverify.VerifyFile(context.Background(), expPath, simverify.VerifyOptions{
		Scenarios: 5,
	})
	require.NoError(t, err)
	require.NotNil(t, report)
	require.Equal(t, 5, report.Scenarios)
	require.Equal(t, 0, report.Matched)
	require.NotEmpty(t, report.Mismatches)

	var foundRetryMismatch bool
	for _, m := range report.Mismatches {
		if m.Type == simverify.MismatchRetryPolicy {
			foundRetryMismatch = true
			require.Contains(t, m.Details, "worker")
			require.Contains(t, m.Details, "retryable")
			require.Contains(t, m.Details, "forbids retries")
		}
	}
	require.True(t, foundRetryMismatch, "expected at least one retry_policy mismatch to be caught and flagged")
}

func TestVerify_MismatchedRetryPolicy_NonRetryableClass(t *testing.T) {
	tmpDir := t.TempDir()

	graphYAML := `apiVersion: graph/v1
id: test-retry-graph-nonretryable
retryPolicies:
  retrying:
    maxAttempts: 3
nodes:
  - id: worker
    type: http_call
    retryPolicy: retrying
    config:
      url: "https://example.internal/work"
      method: POST
edges: []
`
	simYAML := `apiVersion: sim/v1
id: test-retry-sim-nonretryable
graph: ./graph.yaml
worlds:
  w1: {}
behaviors:
  worker:
    fail:
      p: 1.0
      class: non_retryable
objective:
  name: test
  value: "-run.cost"
  maximize: mean
`
	expYAML := `apiVersion: experiment/v1
model: ./sim.yaml
worlds: [w1]
policies: {search: exhaustive}
evaluator: monte_carlo
runs: 10
seed: 42
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "graph.yaml"), []byte(graphYAML), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "sim.yaml"), []byte(simYAML), 0o600))
	expPath := filepath.Join(tmpDir, "experiment.yaml")
	require.NoError(t, os.WriteFile(expPath, []byte(expYAML), 0o600))

	report, err := simverify.VerifyFile(context.Background(), expPath, simverify.VerifyOptions{
		Scenarios: 5,
	})
	require.NoError(t, err)
	require.NotNil(t, report)
	require.Equal(t, 5, report.Scenarios)
	require.Equal(t, 0, report.Matched)
	require.NotEmpty(t, report.Mismatches)

	var foundRetryMismatch bool
	for _, m := range report.Mismatches {
		if m.Type == simverify.MismatchRetryPolicy {
			foundRetryMismatch = true
			require.Contains(t, m.Details, "worker")
			require.Contains(t, m.Details, "non_retryable")
		}
	}
	require.True(t, foundRetryMismatch, "expected non_retryable retry_policy mismatch to be flagged")
}
