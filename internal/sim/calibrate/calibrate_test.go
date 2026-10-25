package calibrate

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

type mockReader struct {
	execsSucceeded []*engine.ExecutionSummary
	execsFailed    []*engine.ExecutionSummary
	state          *engine.GraphState
	logs           []*engine.ExecutionLogEntry
}

func (m *mockReader) ListExecutions(_ context.Context, filter engine.ExecutionFilter) ([]*engine.ExecutionSummary, error) {
	if filter.Status == engine.StatusSucceeded {
		return m.execsSucceeded, nil
	}
	if filter.Status == engine.StatusFailed {
		return m.execsFailed, nil
	}
	return append(m.execsSucceeded, m.execsFailed...), nil
}

func (m *mockReader) GetState(_ context.Context, _ engine.ExecutionID) (*engine.GraphState, error) {
	return m.state, nil
}

func (m *mockReader) GetLogs(_ context.Context, _ engine.ExecutionID, _ uint64, _ int) ([]*engine.ExecutionLogEntry, error) {
	return m.logs, nil
}

func TestCalibrate(t *testing.T) {
	stateJSON := `{"executionId": "exec1", "nodes": ["node1", "node2"], "nodeStates": {"node1": {"nodeId": "node1", "status": "Failed", "errorMessage": "err"}, "node2": {"nodeId": "node2", "status": "Succeeded"}}}`
	state := &engine.GraphState{}
	if err := state.UnmarshalJSON([]byte(stateJSON)); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reader := &mockReader{
		execsFailed: []*engine.ExecutionSummary{
			{ExecutionID: "exec1", CreatedAt: now},
		},
		execsSucceeded: []*engine.ExecutionSummary{},
		state:          state,
		logs: []*engine.ExecutionLogEntry{
			{Timestamp: now, Details: map[string]any{"nodeId": "node1", "oldStatus": "Pending", "status": "Running"}},
			{Timestamp: now.Add(time.Second), Details: map[string]any{"nodeId": "node1", "oldStatus": "Running", "status": "Failed", "errorClass": "gargi.retryable"}},
			{Timestamp: now, Details: map[string]any{"nodeId": "node2", "oldStatus": "Pending", "status": "Running"}},
			{Timestamp: now.Add(2 * time.Second), Details: map[string]any{"nodeId": "node2", "oldStatus": "Running", "status": "Succeeded"}},
		},
	}

	mod := &model.CompiledModel{
		Def: &model.Sim{
			ID: "test-sim",
			Behaviors: map[string]model.Behavior{
				"node1": {},
				"node2": {},
			},
			Latents: map[string]model.ValueSpec{
				"latent1": {},
			},
		},
		Graph: &engine.GraphDefinition{
			Nodes: []engine.NodeDefinition{
				{ID: "node1"},
				{ID: "node2"},
			},
		},
	}

	outcomes := []sim.OutcomeRecord{
		{Latents: map[string]any{"latent1": true}},
		{Latents: map[string]any{"latent1": false}},
	}

	res, err := Calibrate(context.Background(), mod, reader, outcomes)
	if err != nil {
		t.Fatalf("Calibrate failed: %v", err)
	}

	if res.Samples != 1 {
		t.Errorf("expected 1 sample, got %d", res.Samples)
	}
	if res.Outcomes != 2 {
		t.Errorf("expected 2 outcomes, got %d", res.Outcomes)
	}

	b1 := res.Behaviors["node1"]
	if b1.Fail == nil || b1.Fail.Class != "gargi.retryable" || b1.Fail.P != 1.0 {
		t.Errorf("unexpected failure rate for node1: %+v", b1.Fail)
	}
	if len(b1.Duration.(model.ValueSpec).Empirical) != 1 || b1.Duration.(model.ValueSpec).Empirical[0] != 1.0 {
		t.Errorf("unexpected empirical duration for node1: %+v", b1.Duration.(model.ValueSpec).Empirical)
	}

	b2 := res.Behaviors["node2"]
	if b2.Fail != nil {
		t.Errorf("unexpected failure rate for node2: %+v", b2.Fail)
	}
	if len(b2.Duration.(model.ValueSpec).Empirical) != 1 || b2.Duration.(model.ValueSpec).Empirical[0] != 2.0 {
		t.Errorf("unexpected empirical duration for node2: %+v", b2.Duration.(model.ValueSpec).Empirical)
	}

	l := res.Latents["latent1"]
	if l.Bernoulli == nil || l.Bernoulli.P != 0.5 {
		t.Errorf("unexpected latent rate: %+v", l.Bernoulli)
	}
}

func TestSyntheticGroundTruth(t *testing.T) {
	// Ground truth parameters:
	const (
		numExecutions = 2500
		numOutcomes   = 2500

		// Node "service": Lognormal latency with mu=1.0, sigma=0.25
		trueMu    = 1.0
		trueSigma = 0.25

		// Node "service": Failure rate p=0.15 with errorClass="timeout"
		trueFailP     = 0.15
		trueFailClass = "timeout"

		// Node "decision": Branch choice "approved"=0.70, "rejected"=0.30
		trueChoiceApproved = 0.70

		// Latent "defect": Bernoulli rate p=0.30
		trueLatentP = 0.30
	)

	rng := rand.New(rand.NewSource(42))
	baseTime := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	memReader := NewMemoryReader()

	for i := 0; i < numExecutions; i++ {
		execID := engine.ExecutionID(fmt.Sprintf("exec-%d", i))
		createdAt := baseTime.Add(time.Duration(i) * time.Minute)

		failed := rng.Float64() < trueFailP
		durSec := math.Exp(trueMu + trueSigma*rng.NormFloat64())
		if durSec <= 0.001 {
			durSec = 0.001
		}

		choice := "rejected"
		if rng.Float64() < trueChoiceApproved {
			choice = "approved"
		}

		serviceStatus := engine.StatusSucceeded
		if failed {
			serviceStatus = engine.StatusFailed
		}

		stateJSON := fmt.Sprintf(`{
			"executionId": "%s",
			"status": "%s",
			"nodes": ["service", "decision"],
			"nodeStates": {
				"service": {
					"nodeId": "service",
					"status": "%s"
				},
				"decision": {
					"nodeId": "decision",
					"status": "Succeeded",
					"output": {"choice": "%s"}
				}
			}
		}`, execID, serviceStatus, serviceStatus, choice)

		state := &engine.GraphState{}
		require.NoError(t, state.UnmarshalJSON([]byte(stateJSON)))

		t0 := createdAt
		t1 := t0.Add(time.Duration(durSec * float64(time.Second)))

		logs := []*engine.ExecutionLogEntry{
			{
				ExecutionID: execID,
				Sequence:    1,
				Timestamp:   t0,
				Details: map[string]any{
					"nodeId":    "service",
					"oldStatus": "Pending",
					"status":    "Running",
				},
			},
		}

		if failed {
			logs = append(logs, &engine.ExecutionLogEntry{
				ExecutionID: execID,
				Sequence:    2,
				Timestamp:   t1,
				Details: map[string]any{
					"nodeId":     "service",
					"oldStatus":  "Running",
					"status":     "Failed",
					"errorClass": trueFailClass,
				},
			})
		} else {
			logs = append(logs, &engine.ExecutionLogEntry{
				ExecutionID: execID,
				Sequence:    2,
				Timestamp:   t1,
				Details: map[string]any{
					"nodeId":    "service",
					"oldStatus": "Running",
					"status":    "Succeeded",
				},
			})
		}

		// Decision logs
		logs = append(logs, &engine.ExecutionLogEntry{
			ExecutionID: execID,
			Sequence:    3,
			Timestamp:   t1,
			Details: map[string]any{
				"nodeId": "decision",
				"choice": choice,
			},
		})

		memReader.AddExecution(&engine.ExecutionSummary{
			ExecutionID: execID,
			GraphID:     "gt-graph",
			Status:      serviceStatus,
			CreatedAt:   createdAt,
		}, state, logs)
	}

	var outcomes []sim.OutcomeRecord
	for i := 0; i < numOutcomes; i++ {
		isDefect := rng.Float64() < trueLatentP
		outcomes = append(outcomes, sim.OutcomeRecord{
			ExecutionID: fmt.Sprintf("exec-%d", i),
			GraphID:     "gt-graph",
			Latents: map[string]any{
				"defect": isDefect,
			},
		})
	}

	mod := &model.CompiledModel{
		Def: &model.Sim{
			ID:    "gt-graph",
			Graph: "./graph.yaml",
			Behaviors: map[string]model.Behavior{
				"service":  {},
				"decision": {},
			},
			Latents: map[string]model.ValueSpec{
				"defect": {},
			},
		},
		Graph: &engine.GraphDefinition{
			Nodes: []engine.NodeDefinition{
				{ID: "service"},
				{ID: "decision"},
			},
		},
	}

	res, err := Calibrate(context.Background(), mod, memReader, outcomes)
	require.NoError(t, err)

	require.Equal(t, numExecutions, res.Samples)
	require.Equal(t, numOutcomes, res.Outcomes)
	require.True(t, res.Window > 0)
	require.Equal(t, "gt-graph", res.ModelID)
	require.Equal(t, numExecutions, res.Provenance.Samples)

	// 1. Recovered failure rate: within ±3% of true 0.15
	bService := res.Behaviors["service"]
	require.NotNil(t, bService.Fail)
	require.Equal(t, trueFailClass, bService.Fail.Class)
	require.InDelta(t, trueFailP, bService.Fail.P.(float64), 0.03, "recovered failure rate should match ground truth")

	// 2. Recovered lognormal latency: mu within ±5%, sigma within ±5%
	durSpec, ok := bService.Duration.(model.ValueSpec)
	require.True(t, ok)
	require.NotNil(t, durSpec.Lognormal)
	require.InDelta(t, trueMu, durSpec.Lognormal.Mu.(float64), 0.05, "recovered mu should match ground truth")
	require.InDelta(t, trueSigma, durSpec.Lognormal.Sigma.(float64), 0.05, "recovered sigma should match ground truth")

	// 3. Recovered decision branch choice: within ±3% of true 0.70
	bDecision := res.Behaviors["decision"]
	require.NotNil(t, bDecision.Output)
	choiceSpec, hasChoice := bDecision.Output["choice"]
	require.True(t, hasChoice)
	require.NotNil(t, choiceSpec.Choice)
	approvedP, ok := choiceSpec.Choice["approved"].(float64)
	require.True(t, ok)
	require.InDelta(t, trueChoiceApproved, approvedP, 0.03, "recovered choice frequency should match ground truth")

	// 4. Recovered latent rate: within ±3% of true 0.30
	latSpec := res.Latents["defect"]
	require.NotNil(t, latSpec.Bernoulli)
	require.InDelta(t, trueLatentP, latSpec.Bernoulli.P.(float64), 0.03, "recovered latent probability should match ground truth")

	// 5. Test OverlayYAML serialization
	yamlBytes, err := res.OverlayYAML(mod.Def)
	require.NoError(t, err)
	yamlStr := string(yamlBytes)
	require.Contains(t, yamlStr, "# Calibrated sim/v1 model overlay")
	require.Contains(t, yamlStr, fmt.Sprintf("# Samples: %d executions", numExecutions))
	require.Contains(t, yamlStr, fmt.Sprintf("# Outcomes: %d records", numOutcomes))
	require.Contains(t, yamlStr, "apiVersion: sim/v1")
	require.Contains(t, yamlStr, "id: gt-graph")
	require.Contains(t, yamlStr, "class: timeout")
	require.Contains(t, yamlStr, "choice:")
	require.Contains(t, yamlStr, "lognormal:")
}

func TestCalibrateEdgeCases(t *testing.T) {
	mod := &model.CompiledModel{
		Def: &model.Sim{
			ID: "edge-model",
			Behaviors: map[string]model.Behavior{
				"nodeA": {
					Fail: &model.Fail{P: 0.1, Class: "orig"},
				},
			},
			Latents: map[string]model.ValueSpec{
				"latA": {Bernoulli: &model.Bernoulli{P: 0.5}},
			},
		},
		Graph: &engine.GraphDefinition{
			Nodes: []engine.NodeDefinition{{ID: "nodeA"}},
		},
	}

	// 1. Nil model / nil reader returns error
	_, err := Calibrate(context.Background(), nil, NewEmptyReader(), nil)
	require.Error(t, err)
	_, err = Calibrate(context.Background(), mod, nil, nil)
	require.Error(t, err)

	// 2. Zero samples (EmptyReader), empty outcomes
	resEmpty, err := Calibrate(context.Background(), mod, NewEmptyReader(), nil)
	require.NoError(t, err)
	require.Equal(t, 0, resEmpty.Samples)
	require.Equal(t, 0, resEmpty.Outcomes)
	require.Equal(t, time.Duration(0), resEmpty.Window)
	require.Equal(t, 0.5, resEmpty.Latents["latA"].Bernoulli.P)

	// 3. Missing logs: execution state exists but GetLogs returns empty
	memReader := NewMemoryReader()
	stateJSON := `{"executionId": "e1", "nodes": ["nodeA"], "nodeStates": {"nodeA": {"nodeId": "nodeA", "status": "Failed", "errorMessage": "timeout_err"}}}`
	state := &engine.GraphState{}
	require.NoError(t, state.UnmarshalJSON([]byte(stateJSON)))
	memReader.AddExecution(&engine.ExecutionSummary{
		ExecutionID: "e1",
		GraphID:     "edge-model",
		Status:      engine.StatusFailed,
		CreatedAt:   time.Now(),
	}, state, nil) // nil logs!

	resNoLogs, err := Calibrate(context.Background(), mod, memReader, nil)
	require.NoError(t, err)
	require.Equal(t, 1, resNoLogs.Samples)
	require.NotNil(t, resNoLogs.Behaviors["nodeA"].Fail)
	require.Equal(t, 1.0, resNoLogs.Behaviors["nodeA"].Fail.P)

	// 4. Latent choices (discrete string options)
	outcomes := []sim.OutcomeRecord{
		{Latents: map[string]any{"latA": "choice1"}},
		{Latents: map[string]any{"latA": "choice1"}},
		{Latents: map[string]any{"latA": "choice2"}},
	}
	resChoices, err := Calibrate(context.Background(), mod, NewEmptyReader(), outcomes)
	require.NoError(t, err)
	require.NotNil(t, resChoices.Latents["latA"].Choice)
	require.InDelta(t, 2.0/3.0, resChoices.Latents["latA"].Choice["choice1"].(float64), 1e-6)
	require.InDelta(t, 1.0/3.0, resChoices.Latents["latA"].Choice["choice2"].(float64), 1e-6)
}

func TestEmptyReader(t *testing.T) {
	reader := NewEmptyReader()
	execs, err := reader.ListExecutions(context.Background(), engine.ExecutionFilter{})
	require.NoError(t, err)
	require.Empty(t, execs)

	_, err = reader.GetState(context.Background(), "none")
	require.Error(t, err)

	logs, err := reader.GetLogs(context.Background(), "none", 0, 10)
	require.NoError(t, err)
	require.Empty(t, logs)
}

func TestFileReaders(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Executions JSON
	execJSON := `[
		{
			"summary": {"executionId": "e1", "graphId": "g1", "status": "Succeeded"},
			"state": {"executionId": "e1", "nodes": ["n1"], "nodeStates": {"n1": {"nodeId": "n1", "status": "Succeeded"}}},
			"logs": [{"executionId": "e1", "sequence": 1, "details": {"nodeId": "n1", "status": "Running"}}]
		}
	]`
	execFile := filepath.Join(tmpDir, "execs.json")
	require.NoError(t, os.WriteFile(execFile, []byte(execJSON), 0o600))

	reader, err := LoadExecutionsFile(execFile)
	require.NoError(t, err)
	execs, err := reader.ListExecutions(context.Background(), engine.ExecutionFilter{GraphID: "g1"})
	require.NoError(t, err)
	require.Len(t, execs, 1)

	st, err := reader.GetState(context.Background(), "e1")
	require.NoError(t, err)
	require.NotNil(t, st)

	logs, err := reader.GetLogs(context.Background(), "e1", 0, 10)
	require.NoError(t, err)
	require.Len(t, logs, 1)

	// 2. Outcomes JSON
	outcomeJSON := `[
		{"executionId": "e1", "graphId": "g1", "latents": {"defect": true}}
	]`
	outcomeFile := filepath.Join(tmpDir, "outcomes.json")
	require.NoError(t, os.WriteFile(outcomeFile, []byte(outcomeJSON), 0o600))

	outcomes, err := LoadOutcomesFile(outcomeFile)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, true, outcomes[0].Latents["defect"])
}

func TestRemoteReader(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/executions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"executions": [{"executionId": "rem1", "graphId": "rem-graph", "status": "Succeeded", "version": 1, "createdAt": "2026-10-01T00:00:00Z"}]}`))
		case r.URL.Path == "/v1/executions/rem1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"executionId": "rem1", "status": "Succeeded", "version": 1, "nodeStates": {"n1": {"nodeId": "n1", "status": "Succeeded"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/executions/rem1/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "id: 1\nevent: message\ndata: {\"sequence\": 1, \"timestamp\": \"2026-10-01T00:00:01Z\", \"nodeId\": \"n1\", \"details\": {\"status\": \"Running\"}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	reader, err := NewRemoteReader(ts.URL, ts.Client())
	require.NoError(t, err)

	execs, err := reader.ListExecutions(context.Background(), engine.ExecutionFilter{GraphID: "rem-graph"})
	require.NoError(t, err)
	require.Len(t, execs, 1)
	require.Equal(t, engine.ExecutionID("rem1"), execs[0].ExecutionID)

	st, err := reader.GetState(context.Background(), "rem1")
	require.NoError(t, err)
	require.NotNil(t, st)

	logs, err := reader.GetLogs(context.Background(), "rem1", 0, 10)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, "Running", logs[0].Details["status"])
}
