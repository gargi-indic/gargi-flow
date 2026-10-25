package calibrate

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

func TestCheckDrift(t *testing.T) {
	ctx := context.Background()

	mod := &model.CompiledModel{
		Def: &model.Sim{
			ID: "test_model",
			Behaviors: map[string]model.Behavior{
				"node_fail": {
					Fail: &model.Fail{
						P: 0.1, // 10% expected failure rate
					},
				},
				"node_dur": {
					Duration: model.ValueSpec{
						Lognormal: &model.Lognormal{
							Mu:    0.0, // e^0 = 1s expected mean
							Sigma: 0.0,
						},
					},
				},
				"node_match": {
					Fail: &model.Fail{
						P: 0.5,
					},
				},
			},
		},
		Graph: &engine.GraphDefinition{
			ID: "test_model",
			Nodes: []engine.NodeDefinition{
				{ID: "node_fail"},
				{ID: "node_dur"},
				{ID: "node_match"},
			},
		},
	}

	reader := NewMemoryReader()

	for i := 0; i < 40; i++ {
		execID := engine.ExecutionID(fmt.Sprintf("execF%d", i))

		status := engine.StatusSucceeded
		if i < 20 { // 50% failure rate -> 0.5 > 0.1 threshold drift
			status = engine.StatusFailed
		}

		stateData := map[string]any{
			"executionId": string(execID),
			"nodes":       []string{"node_fail"},
			"nodeStates": map[string]any{
				"node_fail": map[string]any{
					"nodeId": "node_fail",
					"status": string(status),
					"output": map[string]any{"policy_version": "1"},
				},
			},
		}

		stateBytes, _ := json.Marshal(stateData)
		state := &engine.GraphState{}
		_ = state.UnmarshalJSON(stateBytes)

		summary := &engine.ExecutionSummary{
			ExecutionID: execID,
			GraphID:     "test_model",
			Status:      status,
		}

		reader.AddExecution(summary, state, nil)
	}

	for i := 0; i < 40; i++ {
		execID := engine.ExecutionID(fmt.Sprintf("execM%d", i))

		status := engine.StatusSucceeded
		if i < 20 { // 50% failure rate -> matches predicted 0.5 exactly!
			status = engine.StatusFailed
		}

		stateData := map[string]any{
			"executionId": string(execID),
			"nodes":       []string{"node_match"},
			"nodeStates": map[string]any{
				"node_match": map[string]any{
					"nodeId": "node_match",
					"status": string(status),
					"output": map[string]any{"policy_version": "1"},
				},
			},
		}

		stateBytes, _ := json.Marshal(stateData)
		state := &engine.GraphState{}
		_ = state.UnmarshalJSON(stateBytes)

		summary := &engine.ExecutionSummary{
			ExecutionID: execID,
			GraphID:     "test_model",
			Status:      status,
		}

		reader.AddExecution(summary, state, nil)
	}

	// And matching duration data
	for i := 0; i < 40; i++ {
		execID := engine.ExecutionID(fmt.Sprintf("execD%d", i))

		stateData := map[string]any{
			"executionId": string(execID),
			"nodes":       []string{"node_dur"},
			"nodeStates": map[string]any{
				"node_dur": map[string]any{
					"nodeId": "node_dur",
					"status": string(engine.StatusSucceeded),
					"output": map[string]any{"policy_version": "1"},
				},
			},
		}
		stateBytes, _ := json.Marshal(stateData)
		state := &engine.GraphState{}
		_ = state.UnmarshalJSON(stateBytes)

		// 1 second duration
		logs := []*engine.ExecutionLogEntry{
			{
				ExecutionID: execID,
				Sequence:    1,
				Timestamp:   time.Unix(100, 0),
				Details:     map[string]any{"nodeId": "node_dur", "status": "RUNNING"},
			},
			{
				ExecutionID: execID,
				Sequence:    2,
				Timestamp:   time.Unix(101, 0),
				Details:     map[string]any{"nodeId": "node_dur", "status": "SUCCEEDED"},
			},
		}

		summary := &engine.ExecutionSummary{
			ExecutionID: execID,
			GraphID:     "test_model",
			Status:      engine.StatusSucceeded,
		}

		reader.AddExecution(summary, state, logs)
	}

	report, err := CheckDrift(ctx, mod, reader, nil, "1", DriftOptions{MinSamples: 10})
	require.NoError(t, err)
	require.NotNil(t, report)
	require.True(t, report.DriftDetected)

	failNode := report.Nodes["node_fail"]
	require.NotNil(t, failNode, "node_fail should be present in drift report")
	require.True(t, failNode.DriftDetected)
	require.NotNil(t, failNode.FailureRateDrift)
	require.Equal(t, 0.5, failNode.FailureRateDrift.Observed)
	require.Equal(t, 0.1, failNode.FailureRateDrift.Predicted)
	require.True(t, failNode.FailureRateDrift.IsDrift)

	durNode := report.Nodes["node_dur"]
	require.NotNil(t, durNode)
	require.False(t, durNode.DriftDetected)
	require.NotNil(t, durNode.DurationDrift)
	require.Equal(t, 1.0, durNode.DurationDrift.Observed)
	require.Equal(t, 1.0, durNode.DurationDrift.Predicted)
	require.False(t, durNode.DurationDrift.IsDrift)

	matchNode := report.Nodes["node_match"]
	require.NotNil(t, matchNode)
	require.False(t, matchNode.DriftDetected)
	require.NotNil(t, matchNode.FailureRateDrift)
	require.Equal(t, 0.5, matchNode.FailureRateDrift.Observed)
	require.Equal(t, 0.5, matchNode.FailureRateDrift.Predicted)
	require.False(t, matchNode.FailureRateDrift.IsDrift)
}

func TestCheckDrift_ZeroFalseAlarms(t *testing.T) {
	ctx := context.Background()

	// Model with predicted failure rate 20%, duration 2s, branch low 70% / high 30%, and latent bad 10%
	mod := &model.CompiledModel{
		Def: &model.Sim{
			ID: "matching_model",
			Behaviors: map[string]model.Behavior{
				"task_node": {
					Fail: &model.Fail{
						P: 0.20,
					},
					Duration: "2s",
					Output: map[string]model.ValueSpec{
						"risk": {
							Choice: map[string]any{
								"low":  0.70,
								"high": 0.30,
							},
						},
					},
				},
			},
			Latents: map[string]model.ValueSpec{
				"bad_borrower": {
					Bernoulli: &model.Bernoulli{
						P: 0.10,
					},
				},
			},
		},
		Graph: &engine.GraphDefinition{
			ID: "matching_model",
			Nodes: []engine.NodeDefinition{
				{ID: "task_node"},
			},
		},
	}

	reader := NewMemoryReader()

	// 50 executions matching predictions exactly:
	// 10 failed (20%), 40 succeeded (80%)
	// All durations exactly 2s
	// 35 low (70%), 15 high (30%)
	for i := 0; i < 50; i++ {
		execID := engine.ExecutionID(fmt.Sprintf("match_exec_%d", i))
		status := engine.StatusSucceeded
		if i < 10 {
			status = engine.StatusFailed
		}

		riskVal := "high"
		if i >= 15 {
			riskVal = "low"
		}

		stateData := map[string]any{
			"executionId": string(execID),
			"nodes":       []string{"task_node"},
			"nodeStates": map[string]any{
				"task_node": map[string]any{
					"nodeId": string("task_node"),
					"status": string(status),
					"output": map[string]any{
						"policy_version": "v1",
						"risk":           riskVal,
					},
				},
			},
		}
		stateBytes, _ := json.Marshal(stateData)
		state := &engine.GraphState{}
		_ = state.UnmarshalJSON(stateBytes)

		logs := []*engine.ExecutionLogEntry{
			{
				ExecutionID: execID,
				Sequence:    1,
				Timestamp:   time.Unix(100, 0),
				Details:     map[string]any{"nodeId": "task_node", "status": "RUNNING"},
			},
			{
				ExecutionID: execID,
				Sequence:    2,
				Timestamp:   time.Unix(102, 0), // 2 seconds
				Details:     map[string]any{"nodeId": "task_node", "status": string(status)},
			},
		}

		summary := &engine.ExecutionSummary{
			ExecutionID: execID,
			GraphID:     "matching_model",
			Status:      status,
		}

		reader.AddExecution(summary, state, logs)
	}

	// 50 matching outcomes: 5 bad_borrower = true (10%)
	var outcomes []sim.OutcomeRecord
	for i := 0; i < 50; i++ {
		isBad := (i < 5)
		outcomes = append(outcomes, sim.OutcomeRecord{
			ExecutionID:   fmt.Sprintf("match_exec_%d", i),
			GraphID:       "matching_model",
			PolicyVersion: "v1",
			Latents: map[string]any{
				"bad_borrower": isBad,
			},
		})
	}

	report, err := CheckDrift(ctx, mod, reader, outcomes, "v1", DriftOptions{
		ProbabilityThreshold: 0.1,
		DurationThreshold:    0.2,
		MinSamples:           30,
	})
	require.NoError(t, err)
	require.NotNil(t, report)

	// Zero false alarms
	require.False(t, report.DriftDetected, "expected no drift detected across the entire report")

	node := report.Nodes["task_node"]
	require.NotNil(t, node)
	require.False(t, node.DriftDetected, "expected no drift detected on task_node")

	require.NotNil(t, node.FailureRateDrift)
	require.InDelta(t, 0.20, node.FailureRateDrift.Predicted, 1e-6)
	require.InDelta(t, 0.20, node.FailureRateDrift.Observed, 1e-6)
	require.False(t, node.FailureRateDrift.IsDrift)

	require.NotNil(t, node.DurationDrift)
	require.InDelta(t, 2.0, node.DurationDrift.Predicted, 1e-6)
	require.InDelta(t, 2.0, node.DurationDrift.Observed, 1e-6)
	require.False(t, node.DurationDrift.IsDrift)

	require.NotNil(t, node.OutputDrift)
	require.False(t, node.OutputDrift["risk.low"].IsDrift)
	require.InDelta(t, 0.70, node.OutputDrift["risk.low"].Observed, 1e-6)
	require.False(t, node.OutputDrift["risk.high"].IsDrift)
	require.InDelta(t, 0.30, node.OutputDrift["risk.high"].Observed, 1e-6)

	require.NotNil(t, report.Latents)
	badLat := report.Latents["bad_borrower"]
	require.NotNil(t, badLat)
	require.InDelta(t, 0.10, badLat.Predicted, 1e-6)
	require.InDelta(t, 0.10, badLat.Observed, 1e-6)
	require.False(t, badLat.IsDrift)
}

func TestCheckDrift_InjectedDriftPerMetric(t *testing.T) {
	ctx := context.Background()

	mod := &model.CompiledModel{
		Def: &model.Sim{
			ID: "drift_model",
			Behaviors: map[string]model.Behavior{
				"slow_node": {
					Duration: "1s",
				},
				"failing_node": {
					Fail: &model.Fail{P: 0.05},
				},
				"divergent_branch_node": {
					Output: map[string]model.ValueSpec{
						"path": {
							Choice: map[string]any{"A": 0.90, "B": 0.10},
						},
					},
				},
			},
			Latents: map[string]model.ValueSpec{
				"latent_churn": {
					Bernoulli: &model.Bernoulli{P: 0.05},
				},
			},
		},
		Graph: &engine.GraphDefinition{
			ID: "drift_model",
			Nodes: []engine.NodeDefinition{
				{ID: "slow_node"},
				{ID: "failing_node"},
				{ID: "divergent_branch_node"},
			},
		},
	}

	reader := NewMemoryReader()

	// Injected Duration Drift on slow_node (observed 3s vs predicted 1s -> +200% > 20% threshold)
	// Injected Failure Drift on failing_node (observed 40% vs predicted 5% -> +35% > 10% threshold)
	// Injected Branch Drift on divergent_branch_node (observed A 30% / B 70% vs predicted A 90% / B 10%)
	for i := 0; i < 40; i++ {
		execID := engine.ExecutionID(fmt.Sprintf("drift_exec_%d", i))
		failStatus := engine.StatusSucceeded
		if i < 16 { // 16/40 = 40% fail rate
			failStatus = engine.StatusFailed
		}

		branchVal := "B"
		if i < 12 { // 12/40 = 30% branch A
			branchVal = "A"
		}

		stateData := map[string]any{
			"executionId": string(execID),
			"nodes":       []string{"slow_node", "failing_node", "divergent_branch_node"},
			"nodeStates": map[string]any{
				"slow_node": map[string]any{
					"nodeId": "slow_node",
					"status": string(engine.StatusSucceeded),
					"output": map[string]any{"policy_version": "v2"},
				},
				"failing_node": map[string]any{
					"nodeId": "failing_node",
					"status": string(failStatus),
					"output": map[string]any{"policy_version": "v2"},
				},
				"divergent_branch_node": map[string]any{
					"nodeId": "divergent_branch_node",
					"status": string(engine.StatusSucceeded),
					"output": map[string]any{
						"policy_version": "v2",
						"path":           branchVal,
					},
				},
			},
		}
		stateBytes, _ := json.Marshal(stateData)
		state := &engine.GraphState{}
		_ = state.UnmarshalJSON(stateBytes)

		logs := []*engine.ExecutionLogEntry{
			{
				ExecutionID: execID,
				Sequence:    1,
				Timestamp:   time.Unix(200, 0),
				Details:     map[string]any{"nodeId": "slow_node", "status": "RUNNING"},
			},
			{
				ExecutionID: execID,
				Sequence:    2,
				Timestamp:   time.Unix(203, 0), // 3s duration
				Details:     map[string]any{"nodeId": "slow_node", "status": "SUCCEEDED"},
			},
		}

		summary := &engine.ExecutionSummary{
			ExecutionID: execID,
			GraphID:     "drift_model",
			Status:      engine.StatusSucceeded,
		}

		reader.AddExecution(summary, state, logs)
	}

	// Injected Latent Drift on latent_churn (observed 30/40 = 75% vs predicted 5%)
	var outcomes []sim.OutcomeRecord
	for i := 0; i < 40; i++ {
		isChurn := (i < 30) // 75%
		outcomes = append(outcomes, sim.OutcomeRecord{
			ExecutionID:   fmt.Sprintf("drift_exec_%d", i),
			GraphID:       "drift_model",
			PolicyVersion: "v2",
			Latents: map[string]any{
				"latent_churn": isChurn,
			},
		})
	}

	report, err := CheckDrift(ctx, mod, reader, outcomes, "v2", DriftOptions{MinSamples: 20})
	require.NoError(t, err)
	require.NotNil(t, report)
	require.True(t, report.DriftDetected)

	// Check duration drift
	slowNode := report.Nodes["slow_node"]
	require.NotNil(t, slowNode)
	require.True(t, slowNode.DriftDetected)
	require.NotNil(t, slowNode.DurationDrift)
	require.True(t, slowNode.DurationDrift.IsDrift)
	require.InDelta(t, 1.0, slowNode.DurationDrift.Predicted, 1e-6)
	require.InDelta(t, 3.0, slowNode.DurationDrift.Observed, 1e-6)

	// Check failure rate drift
	failNode := report.Nodes["failing_node"]
	require.NotNil(t, failNode)
	require.True(t, failNode.DriftDetected)
	require.NotNil(t, failNode.FailureRateDrift)
	require.True(t, failNode.FailureRateDrift.IsDrift)
	require.InDelta(t, 0.05, failNode.FailureRateDrift.Predicted, 1e-6)
	require.InDelta(t, 0.40, failNode.FailureRateDrift.Observed, 1e-6)

	// Check branch choice drift
	branchNode := report.Nodes["divergent_branch_node"]
	require.NotNil(t, branchNode)
	require.True(t, branchNode.DriftDetected)
	require.NotNil(t, branchNode.OutputDrift)
	require.True(t, branchNode.OutputDrift["path.A"].IsDrift)
	require.InDelta(t, 0.90, branchNode.OutputDrift["path.A"].Predicted, 1e-6)
	require.InDelta(t, 0.30, branchNode.OutputDrift["path.A"].Observed, 1e-6)

	// Check latent drift
	require.NotNil(t, report.Latents)
	churnLat := report.Latents["latent_churn"]
	require.NotNil(t, churnLat)
	require.True(t, churnLat.IsDrift)
	require.InDelta(t, 0.05, churnLat.Predicted, 1e-6)
	require.InDelta(t, 0.75, churnLat.Observed, 1e-6)
}

func TestCheckDrift_PolicyVersionSeparation(t *testing.T) {
	ctx := context.Background()

	mod := &model.CompiledModel{
		Def: &model.Sim{
			ID: "pv_model",
			Behaviors: map[string]model.Behavior{
				"test_node": {
					Fail: &model.Fail{P: 0.10},
				},
			},
		},
		Graph: &engine.GraphDefinition{
			ID: "pv_model",
			Nodes: []engine.NodeDefinition{
				{ID: "test_node"},
			},
		},
	}

	reader := NewMemoryReader()

	// Policy v1: 40 executions, 4 failures (10% -> matches predicted 10%)
	for i := 0; i < 40; i++ {
		execID := engine.ExecutionID(fmt.Sprintf("v1_exec_%d", i))
		status := engine.StatusSucceeded
		if i < 4 {
			status = engine.StatusFailed
		}
		stateData := map[string]any{
			"executionId": string(execID),
			"nodes":       []string{"test_node"},
			"nodeStates": map[string]any{
				"test_node": map[string]any{
					"nodeId": "test_node",
					"status": string(status),
					"output": map[string]any{"policy_version": "v1"},
				},
			},
		}
		stateBytes, _ := json.Marshal(stateData)
		state := &engine.GraphState{}
		_ = state.UnmarshalJSON(stateBytes)
		reader.AddExecution(&engine.ExecutionSummary{
			ExecutionID: execID,
			GraphID:     "pv_model",
			Status:      status,
		}, state, nil)
	}

	// Policy v2: 40 executions, 24 failures (60% -> drifts significantly from predicted 10%)
	for i := 0; i < 40; i++ {
		execID := engine.ExecutionID(fmt.Sprintf("v2_exec_%d", i))
		status := engine.StatusSucceeded
		if i < 24 {
			status = engine.StatusFailed
		}
		stateData := map[string]any{
			"executionId": string(execID),
			"nodes":       []string{"test_node"},
			"nodeStates": map[string]any{
				"test_node": map[string]any{
					"nodeId": "test_node",
					"status": string(status),
					"output": map[string]any{"policy_version": "v2"},
				},
			},
		}
		stateBytes, _ := json.Marshal(stateData)
		state := &engine.GraphState{}
		_ = state.UnmarshalJSON(stateBytes)
		reader.AddExecution(&engine.ExecutionSummary{
			ExecutionID: execID,
			GraphID:     "pv_model",
			Status:      status,
		}, state, nil)
	}

	// Report for v1: should detect NO drift
	repV1, err := CheckDrift(ctx, mod, reader, nil, "v1", DriftOptions{MinSamples: 20})
	require.NoError(t, err)
	require.False(t, repV1.DriftDetected)
	require.False(t, repV1.Nodes["test_node"].DriftDetected)
	require.InDelta(t, 0.10, repV1.Nodes["test_node"].FailureRateDrift.Observed, 1e-6)

	// Report for v2: should detect DRIFT
	repV2, err := CheckDrift(ctx, mod, reader, nil, "v2", DriftOptions{MinSamples: 20})
	require.NoError(t, err)
	require.True(t, repV2.DriftDetected)
	require.True(t, repV2.Nodes["test_node"].DriftDetected)
	require.InDelta(t, 0.60, repV2.Nodes["test_node"].FailureRateDrift.Observed, 1e-6)
}
