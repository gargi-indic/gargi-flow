package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/orchestrator"
	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

type memoryEventSink struct {
	mu     sync.Mutex
	events []*engine.Event
}

func (s *memoryEventSink) Emit(_ context.Context, ev *engine.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

func (s *memoryEventSink) Events() []*engine.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*engine.Event, len(s.events))
	copy(cp, s.events)
	return cp
}

// TestIssue161_NormalDecisionWithPinnedVersionAndEventRecording tests normal decision dispatch
// with pinned policy versions and verifies PolicyID and PolicyVersion in NodeCompletedPayload.
func TestIssue161_NormalDecisionWithPinnedVersionAndEventRecording(t *testing.T) {
	ctx := context.Background()
	ps := memory.NewPolicyStore()

	policyYAML := `apiVersion: policy/v1
id: route-policy
version: v1
decisions:
  make_decision:
    rules:
      - when: "input_data.output.score >= 0.8"
        choose: fast_path
      - else: slow_path
`
	_, err := ps.SavePolicy(ctx, &engine.PolicyRecord{
		ID:       "route-policy",
		GraphID:  "routing-graph",
		Document: []byte(policyYAML),
		Format:   "yaml",
	})
	require.NoError(t, err)

	reg := engine.NewRegistry()
	require.NoError(t, nodes.RegisterDefaults(reg, nodes.Dependencies{
		Signals:     orchestrator.NewTemporalSignalSource(),
		PolicyStore: ps,
	}))

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	na := orchestrator.NewNodeActivities(reg)
	env.RegisterActivityWithOptions(na.ExecuteNodeActivity, activity.RegisterOptions{
		Name: orchestrator.ExecuteNodeActivityName,
	})

	def := &engine.GraphDefinition{
		ID:         "routing-graph",
		APIVersion: "graph/v1",
		Nodes: []engine.NodeDefinition{
			{
				ID:   "input_data",
				Type: "transform",
				Config: map[string]any{
					"set": map[string]any{"score": 0.95},
				},
			},
			{
				ID:   "make_decision",
				Type: "decision",
				Config: map[string]any{
					"options":  []any{"fast_path", "slow_path"},
					"observes": []any{"input_data.output.score"},
					"policy":   "route-policy",
				},
			},
		},
		Edges: []engine.EdgeDefinition{
			{From: "input_data", To: "make_decision"},
		},
	}

	sink := &memoryEventSink{}
	runner := orchestrator.NewWorkflowRunner(reg).WithEventSink(sink)
	env.RegisterWorkflow(runner.GraphExecutionWorkflow)

	input := orchestrator.GraphWorkflowInput{
		ExecutionID: "exec-decision-pinned-1",
		Definition:  def,
		PinnedPolicies: map[string]string{
			"route-policy": "1",
		},
	}

	env.ExecuteWorkflow(runner.GraphExecutionWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var res orchestrator.GraphWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&res))
	assert.Equal(t, engine.StatusSucceeded, res.Status)

	// Verify decision output
	decideState := res.FinalState.NodeState("make_decision")
	require.NotNil(t, decideState)
	assert.Equal(t, engine.StatusSucceeded, decideState.Status())
	assert.Equal(t, "fast_path", decideState.Output()["choice"])

	// Verify event recording
	var foundCompletedEvent *engine.Event
	for _, ev := range sink.Events() {
		if ev.Type() == engine.EventTypeNodeCompleted && ev.NodeID() == "make_decision" {
			foundCompletedEvent = ev
			break
		}
	}

	require.NotNil(t, foundCompletedEvent, "node_completed event for make_decision must be emitted")
	completedPayload, ok := foundCompletedEvent.Payload().(engine.NodeCompletedPayload)
	require.True(t, ok, "payload must be NodeCompletedPayload")
	assert.Equal(t, "route-policy", completedPayload.PolicyID)
	assert.Equal(t, "1", completedPayload.PolicyVersion)
}

// TestIssue161_InFlightDeploymentUpdateProtection (ADR-0006) tests that an in-flight workflow
// execution pinned to V1 continues using V1 even when PolicyStore.SetDeployment updates active deployment to V2.
func TestIssue161_InFlightDeploymentUpdateProtection(t *testing.T) {
	ctx := context.Background()
	ps := memory.NewPolicyStore()

	// Policy V1: returns "approve"
	v1YAML := `apiVersion: policy/v1
id: approval-policy
version: v1
decisions:
  approval_decision:
    rules:
      - else: approve
`
	_, err := ps.SavePolicy(ctx, &engine.PolicyRecord{
		ID:       "approval-policy",
		GraphID:  "loan-graph",
		Document: []byte(v1YAML),
		Format:   "yaml",
	})
	require.NoError(t, err)

	// Policy V2: returns "reject"
	v2YAML := `apiVersion: policy/v1
id: approval-policy
version: v2
decisions:
  approval_decision:
    rules:
      - else: reject
`
	_, err = ps.SavePolicy(ctx, &engine.PolicyRecord{
		ID:       "approval-policy",
		GraphID:  "loan-graph",
		Document: []byte(v2YAML),
		Format:   "yaml",
	})
	require.NoError(t, err)

	// Initially active deployment is V1
	require.NoError(t, ps.SetDeployment(ctx, &engine.PolicyDeployment{
		GraphID:       "loan-graph",
		PolicyID:      "approval-policy",
		PolicyVersion: "1",
		Mode:          engine.DeploymentLive,
	}))

	reg := engine.NewRegistry()
	require.NoError(t, nodes.RegisterDefaults(reg, nodes.Dependencies{
		Signals:     orchestrator.NewTemporalSignalSource(),
		PolicyStore: ps,
	}))

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	na := orchestrator.NewNodeActivities(reg)
	env.RegisterActivityWithOptions(na.ExecuteNodeActivity, activity.RegisterOptions{
		Name: orchestrator.ExecuteNodeActivityName,
	})

	def := &engine.GraphDefinition{
		ID:         "loan-graph",
		APIVersion: "graph/v1",
		Nodes: []engine.NodeDefinition{
			{
				ID:   "wait_for_review",
				Type: "wait_for_signal",
				Config: map[string]any{
					"signal": "proceed",
				},
			},
			{
				ID:   "approval_decision",
				Type: "decision",
				Config: map[string]any{
					"options": []any{"approve", "reject"},
					"policy":  "approval-policy",
				},
			},
		},
		Edges: []engine.EdgeDefinition{
			{From: "wait_for_review", To: "approval_decision"},
		},
	}

	sink := &memoryEventSink{}
	runner := orchestrator.NewWorkflowRunner(reg).WithEventSink(sink)
	env.RegisterWorkflow(runner.GraphExecutionWorkflow)

	// Workflow starts pinned to V1
	input := orchestrator.GraphWorkflowInput{
		ExecutionID: "exec-inflight-protection",
		Definition:  def,
		PinnedPolicies: map[string]string{
			"approval-policy": "1",
		},
	}

	// Mid-flight: while workflow is waiting for signal, update PolicyStore deployment to V2!
	env.RegisterDelayedCallback(func() {
		// Update deployment to V2
		setErr := ps.SetDeployment(context.Background(), &engine.PolicyDeployment{
			GraphID:       "loan-graph",
			PolicyID:      "approval-policy",
			PolicyVersion: "2",
			Mode:          engine.DeploymentLive,
		})
		require.NoError(t, setErr)

		// Send signal "proceed" to resume workflow
		env.SignalWorkflow(orchestrator.SignalChannelName, orchestrator.ExternalSignalPayload{
			TargetNodeID: "wait_for_review",
			SignalName:   "proceed",
			Payload:      map[string]any{"ok": true},
		})
	}, time.Millisecond)

	env.ExecuteWorkflow(runner.GraphExecutionWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var res orchestrator.GraphWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&res))
	assert.Equal(t, engine.StatusSucceeded, res.Status)

	// MUST be "approve" from V1, not "reject" from V2!
	decideState := res.FinalState.NodeState("approval_decision")
	require.NotNil(t, decideState)
	assert.Equal(t, "approve", decideState.Output()["choice"])

	// Event payload must record pinned V1
	var foundCompletedEvent *engine.Event
	for _, ev := range sink.Events() {
		if ev.Type() == engine.EventTypeNodeCompleted && ev.NodeID() == "approval_decision" {
			foundCompletedEvent = ev
			break
		}
	}
	require.NotNil(t, foundCompletedEvent)
	completedPayload := foundCompletedEvent.Payload().(engine.NodeCompletedPayload)
	assert.Equal(t, "approval-policy", completedPayload.PolicyID)
	assert.Equal(t, "1", completedPayload.PolicyVersion)
}

// TestIssue161_UnpinnedAndMissingPolicyHandling tests handling of unpinned policies with live deployment
// and error behavior when policy or deployment is missing.
func TestIssue161_UnpinnedAndMissingPolicyHandling(t *testing.T) {
	ctx := context.Background()
	ps := memory.NewPolicyStore()

	policyYAML := `apiVersion: policy/v1
id: dyn-policy
version: v1
decisions:
  dyn_decision:
    rules:
      - else: opt_from_deployment
`
	_, err := ps.SavePolicy(ctx, &engine.PolicyRecord{
		ID:       "dyn-policy",
		GraphID:  "dyn-graph",
		Document: []byte(policyYAML),
		Format:   "yaml",
	})
	require.NoError(t, err)

	// Live deployment for dyn-graph
	require.NoError(t, ps.SetDeployment(ctx, &engine.PolicyDeployment{
		GraphID:       "dyn-graph",
		PolicyID:      "dyn-policy",
		PolicyVersion: "1",
		Mode:          engine.DeploymentLive,
	}))

	reg := engine.NewRegistry()
	require.NoError(t, nodes.RegisterDefaults(reg, nodes.Dependencies{
		Signals:     orchestrator.NewTemporalSignalSource(),
		PolicyStore: ps,
	}))

	suite := &testsuite.WorkflowTestSuite{}

	t.Run("unpinned with live deployment resolves correctly", func(t *testing.T) {
		env := suite.NewTestWorkflowEnvironment()
		na := orchestrator.NewNodeActivities(reg)
		env.RegisterActivityWithOptions(na.ExecuteNodeActivity, activity.RegisterOptions{
			Name: orchestrator.ExecuteNodeActivityName,
		})

		def := &engine.GraphDefinition{
			ID:         "dyn-graph",
			APIVersion: "graph/v1",
			Nodes: []engine.NodeDefinition{
				{
					ID:   "dyn_decision",
					Type: "decision",
					Config: map[string]any{
						"options": []any{"opt_from_deployment", "opt_other"},
						"policy":  "dyn-policy",
					},
				},
			},
		}

		sink := &memoryEventSink{}
		runner := orchestrator.NewWorkflowRunner(reg).WithEventSink(sink)
		env.RegisterWorkflow(runner.GraphExecutionWorkflow)

		// No PinnedPolicies supplied in input
		input := orchestrator.GraphWorkflowInput{
			ExecutionID: "exec-unpinned-live",
			Definition:  def,
		}

		env.ExecuteWorkflow(runner.GraphExecutionWorkflow, input)

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())

		var res orchestrator.GraphWorkflowResult
		require.NoError(t, env.GetWorkflowResult(&res))
		assert.Equal(t, engine.StatusSucceeded, res.Status)

		decideState := res.FinalState.NodeState("dyn_decision")
		require.NotNil(t, decideState)
		assert.Equal(t, "opt_from_deployment", decideState.Output()["choice"])

		// Verify event has resolved version
		var foundEvent *engine.Event
		for _, ev := range sink.Events() {
			if ev.Type() == engine.EventTypeNodeCompleted && ev.NodeID() == "dyn_decision" {
				foundEvent = ev
				break
			}
		}
		require.NotNil(t, foundEvent)
		payload := foundEvent.Payload().(engine.NodeCompletedPayload)
		assert.Equal(t, "dyn-policy", payload.PolicyID)
		assert.Equal(t, "1", payload.PolicyVersion)
	})

	t.Run("unpinned without deployment fails", func(t *testing.T) {
		env := suite.NewTestWorkflowEnvironment()
		na := orchestrator.NewNodeActivities(reg)
		env.RegisterActivityWithOptions(na.ExecuteNodeActivity, activity.RegisterOptions{
			Name: orchestrator.ExecuteNodeActivityName,
		})

		def := &engine.GraphDefinition{
			ID:         "no-dep-graph",
			APIVersion: "graph/v1",
			Nodes: []engine.NodeDefinition{
				{
					ID:   "dyn_decision",
					Type: "decision",
					Config: map[string]any{
						"options": []any{"opt1", "opt2"},
						"policy":  "dyn-policy",
					},
				},
			},
		}

		runner := orchestrator.NewWorkflowRunner(reg)
		env.RegisterWorkflow(runner.GraphExecutionWorkflow)

		input := orchestrator.GraphWorkflowInput{
			ExecutionID: "exec-missing-dep",
			Definition:  def,
		}

		env.ExecuteWorkflow(runner.GraphExecutionWorkflow, input)

		require.True(t, env.IsWorkflowCompleted())
		var res orchestrator.GraphWorkflowResult
		require.NoError(t, env.GetWorkflowResult(&res))
		// Workflow terminal detector transitions to Failed when node activity fails non-retryably
		assert.Equal(t, engine.StatusFailed, res.Status)
	})
}
