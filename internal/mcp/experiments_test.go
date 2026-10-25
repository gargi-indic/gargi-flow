package mcp_test

import (
	"context"
	"testing"

	sdk_mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestExperiments_Acceptance(t *testing.T) {
	s := newWorkspaceSession(t)
	buildLoanModel(t, s, "loan")

	expYAML := `apiVersion: experiment/v1
worlds: [base, recession]
policies: {search: exact}
evaluator: exact
runs: 1
seed: 42
reports: [regret]
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

	isErr := call(t, s, "run_experiment", m{
		"model_id":   "loan",
		"experiment": expYAML,
	}, &runRes)
	require.False(t, isErr)
	require.NotEmpty(t, runRes.ID)
	require.Len(t, runRes.Worlds, 2)

	means := map[string]float64{}
	bestPolicies := map[string]string{}
	for _, w := range runRes.Worlds {
		means[w.World] = w.BestMean
		bestPolicies[w.World] = w.BestPolicy
	}

	require.InDelta(t, 460.0, means["base"], 1e-9)
	require.InDelta(t, 202.525, means["recession"], 1e-9)
	require.Contains(t, bestPolicies["base"], "exhaustive-b1b8bd20353bca7f")

	// Test get_experiment
	var getExpRes struct {
		ID     string `json:"id"`
		Worlds []struct {
			World      string  `json:"world"`
			BestPolicy string  `json:"best_policy"`
			BestMean   float64 `json:"best_mean"`
			CIWidth    float64 `json:"ci_width"`
		} `json:"worlds"`
	}
	isErr = call(t, s, "get_experiment", m{"id": runRes.ID}, &getExpRes)
	require.False(t, isErr)
	require.Equal(t, runRes.ID, getExpRes.ID)

	// Test get_report for kind "regret"
	var regretData map[string]any
	isErr = call(t, s, "get_report", m{"id": runRes.ID, "kind": "regret"}, &regretData)
	require.False(t, isErr)
	require.NotEmpty(t, regretData)

	// Test compare_policies: ranks the base best policy first
	var compPolicies []struct {
		Policy  string   `json:"policy"`
		Mean    float64  `json:"mean"`
		CIWidth float64  `json:"ci_width"`
		Rank    int      `json:"rank"`
		Regret  *float64 `json:"regret"`
	}
	isErr = call(t, s, "compare_policies", m{"id": runRes.ID, "world": "base"}, &compPolicies)
	require.False(t, isErr)
	require.NotEmpty(t, compPolicies)
	require.Equal(t, 1, compPolicies[0].Rank)
	require.Contains(t, compPolicies[0].Policy, "exhaustive-b1b8bd20353bca7f")
	require.InDelta(t, 460.0, compPolicies[0].Mean, 1e-9)
}

func TestExperiments_SweepParameter(t *testing.T) {
	s := newWorkspaceSession(t)
	buildLoanModel(t, s, "loan-sweep")

	expYAML := `apiVersion: experiment/v1
worlds: [base, recession]
policies: {search: exact}
evaluator: exact
runs: 1
seed: 42
`

	initRaw, _ := readModel(t, s, "loan-sweep")

	var sweepRes []struct {
		Value      float64 `json:"value"`
		BestPolicy string  `json:"best_policy"`
		BestMean   float64 `json:"best_mean"`
	}

	isErr := call(t, s, "sweep_parameter", m{
		"model_id":   "loan-sweep",
		"world":      "base",
		"param":      "p_bad",
		"values":     []float64{0.20, 0.35},
		"experiment": expYAML,
	}, &sweepRes)
	require.False(t, isErr)
	require.Len(t, sweepRes, 2)
	require.InDelta(t, 0.20, sweepRes[0].Value, 1e-9)
	require.InDelta(t, 460.0, sweepRes[0].BestMean, 1e-9)

	// Workspace model should be unchanged after the sweep
	afterRaw, _ := readModel(t, s, "loan-sweep")
	require.Equal(t, initRaw, afterRaw)
}

func TestExperiments_Errors(t *testing.T) {
	s := newWorkspaceSession(t)

	// Create an invalid model
	_ = mustOK(t, s, "create_model", m{"id": "bad-model", "template": "empty"})
	_ = patch(t, s, "bad-model", m{"op": "set_world_param", "world": "base", "param": "p", "value": 1})

	// run_experiment on invalid model is a tool error with issues and hints
	var toolErr struct {
		Errors []issue `json:"errors"`
	}
	isErr := call(t, s, "run_experiment", m{
		"model_id":   "bad-model",
		"experiment": "apiVersion: experiment/v1\nworlds: [base]\n",
	}, &toolErr)
	require.True(t, isErr)
	require.NotEmpty(t, toolErr.Errors)

	// get_report for kind not requested
	buildLoanModel(t, s, "loan-err")
	var runRes struct {
		ID string `json:"id"`
	}
	isErr = call(t, s, "run_experiment", m{
		"model_id": "loan-err",
		"experiment": `apiVersion: experiment/v1
worlds: [base]
policies: {search: exact}
evaluator: exact
runs: 1
seed: 42
`,
	}, &runRes)
	require.False(t, isErr)

	var repErr struct {
		Errors []issue `json:"errors"`
	}
	isErr = call(t, s, "get_report", m{
		"id":   runRes.ID,
		"kind": "voi",
	}, &repErr)
	require.True(t, isErr)
	require.NotEmpty(t, repErr.Errors)
	require.Contains(t, repErr.Errors[0].Hint, "add reports: [voi]")

	// deploy_policy has DestructiveHint annotation
	toolsRes, err := s.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var deployTool *sdk_mcp.Tool
	for _, tool := range toolsRes.Tools {
		if tool.Name == "deploy_policy" {
			deployTool = tool
			break
		}
	}
	require.NotNil(t, deployTool)
	require.NotNil(t, deployTool.Annotations)
	require.NotNil(t, deployTool.Annotations.DestructiveHint)
	require.True(t, *deployTool.Annotations.DestructiveHint)

	var errRes struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	isErr = call(t, s, "deploy_policy", m{
		"model_id": "loan-err!",
		"policy":   "p1",
	}, &errRes)
	require.True(t, isErr)
	require.NotEmpty(t, errRes.Errors)

	// Create loan model with policies and deploy
	mustOK(t, s, "create_model", m{"id": "loan-deploy", "template": "loan-underwriting"})

	var deployRes struct {
		Status  string `json:"status"`
		Policy  string `json:"policy"`
		Version string `json:"version"`
		Mode    string `json:"mode"`
		Message string `json:"message"`
	}
	isErr = call(t, s, "deploy_policy", m{
		"model_id": "loan-deploy",
		"policy":   "recession-v1.yaml",
		"mode":     "shadow",
	}, &deployRes)
	require.False(t, isErr)
	require.Equal(t, "ok", deployRes.Status)
	require.Equal(t, "shadow", deployRes.Mode)
	require.Equal(t, "1", deployRes.Version)

	// Deploy live mode
	isErr = call(t, s, "deploy_policy", m{
		"model_id": "loan-deploy",
		"policy":   "recession-v1.yaml",
		"mode":     "live",
	}, &deployRes)
	require.False(t, isErr)
	require.Equal(t, "ok", deployRes.Status)
	require.Equal(t, "live", deployRes.Mode)
}

func TestExperiments_IngestOutcome(t *testing.T) {
	s := newWorkspaceSession(t)

	// Ingest outcome success
	var ingestRes struct {
		Status      string `json:"status"`
		ExecutionID string `json:"execution_id"`
		Message     string `json:"message"`
	}
	isErr := call(t, s, "ingest_outcome", m{
		"execution_id": "exec-outcome-1",
		"latents": map[string]any{
			"default": 1.0,
			"loss":    5000.0,
		},
		"observed_at": "2026-10-25T15:30:00Z",
	}, &ingestRes)
	require.False(t, isErr)
	require.Equal(t, "ok", ingestRes.Status)
	require.Equal(t, "exec-outcome-1", ingestRes.ExecutionID)

	// Ingest outcome without observed_at (defaults to now)
	isErr = call(t, s, "ingest_outcome", m{
		"execution_id": "exec-outcome-2",
		"latents": map[string]any{
			"default": 0.0,
		},
	}, &ingestRes)
	require.False(t, isErr)
	require.Equal(t, "ok", ingestRes.Status)
	require.Equal(t, "exec-outcome-2", ingestRes.ExecutionID)

	// Ingest outcome with empty execution_id should return tool error
	var errRes struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	isErr = call(t, s, "ingest_outcome", m{
		"execution_id": "",
		"latents":      map[string]any{"default": 1.0},
	}, &errRes)
	require.True(t, isErr)
	require.NotEmpty(t, errRes.Errors)
	require.Contains(t, errRes.Errors[0].Message, "execution_id is required")

	// Ingest outcome with missing execution_id in args fails schema validation
	res, err := s.CallTool(context.Background(), &sdk_mcp.CallToolParams{
		Name:      "ingest_outcome",
		Arguments: m{"latents": map[string]any{"default": 1.0}},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)

	// Ingest outcome with invalid observed_at timestamp
	isErr = call(t, s, "ingest_outcome", m{
		"execution_id": "exec-invalid-time",
		"latents":      map[string]any{},
		"observed_at":  "invalid-date-format",
	}, &errRes)
	require.True(t, isErr)
	require.NotEmpty(t, errRes.Errors)
}

func TestExperiments_GetDriftReport(t *testing.T) {
	s := newWorkspaceSession(t)
	buildLoanModel(t, s, "loan-drift")

	// Get drift report on workspace model
	var rep struct {
		PolicyVersion string         `json:"policyVersion"`
		DriftDetected bool           `json:"driftDetected"`
		Nodes         map[string]any `json:"nodes"`
	}
	isErr := call(t, s, "get_drift_report", m{
		"model_id":       "loan-drift",
		"policy_version": "1",
	}, &rep)
	require.False(t, isErr)
	require.Equal(t, "1", rep.PolicyVersion)

	// Call without policy_version (defaults to "1")
	isErr = call(t, s, "get_drift_report", m{
		"model_id": "loan-drift",
	}, &rep)
	require.False(t, isErr)
	require.Equal(t, "1", rep.PolicyVersion)

	// Invalid model id
	var errRes struct {
		Errors []issue `json:"errors"`
	}
	isErr = call(t, s, "get_drift_report", m{
		"model_id": "INVALID_ID!",
	}, &errRes)
	require.True(t, isErr)
	require.NotEmpty(t, errRes.Errors)

	// Missing model
	isErr = call(t, s, "get_drift_report", m{
		"model_id": "missing-model",
	}, &errRes)
	require.True(t, isErr)
	require.NotEmpty(t, errRes.Errors)
}

func TestPrompts_ModelThisDecision(t *testing.T) {
	s := newWorkspaceSession(t)
	res, err := s.GetPrompt(context.Background(), &sdk_mcp.GetPromptParams{
		Name:      "model_this_decision",
		Arguments: map[string]string{"description": "Underwrite retail personal loans"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Messages)
	txt, ok := res.Messages[0].Content.(*sdk_mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, txt.Text, "patch_model")
	require.Contains(t, txt.Text, "validate_model")
}
