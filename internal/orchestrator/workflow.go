package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/metrics"
	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/reactive"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	// GraphExecutionWorkflowName is the name of the workflow.
	GraphExecutionWorkflowName = "GraphExecutionWorkflow"
	// ExecuteNodeActivityName is the name of the activity.
	ExecuteNodeActivityName = "ExecuteNodeActivity"
	// MaxRecommendedPayloadBytes is the payload size warning threshold.
	MaxRecommendedPayloadBytes = 2 * 1024 * 1024 // 2 MB warning threshold
)

// GraphWorkflowInput is the input payload for GraphExecutionWorkflow.
type GraphWorkflowInput struct {
	ExecutionID    engine.ExecutionID      `json:"executionId"`
	Definition     *engine.GraphDefinition `json:"definition"`
	InitialState   map[string]any          `json:"initialState,omitempty"`
	PinnedPolicies map[string]string       `json:"pinnedPolicies,omitempty"`
}

// GraphWorkflowResult is the output returned when GraphExecutionWorkflow finishes.
type GraphWorkflowResult struct {
	ExecutionID     engine.ExecutionID `json:"executionId"`
	FinalState      *engine.GraphState `json:"finalState"`
	Status          engine.Status      `json:"status"`
	Error           string             `json:"error,omitempty"`
	RejectedSignals []RejectedSignal   `json:"rejectedSignals,omitempty"`
}

// WorkflowRunner binds an optional custom engine.Registry and optional engine.EventSink to GraphExecutionWorkflow.
type WorkflowRunner struct {
	registry  *engine.Registry
	eventSink engine.EventSink
}

// NewWorkflowRunner creates a WorkflowRunner with the given registry (or a default registry if nil).
func NewWorkflowRunner(registry *engine.Registry) *WorkflowRunner {
	return &WorkflowRunner{registry: registry}
}

// WithEventSink configures an engine.EventSink for the workflow runner.
func (r *WorkflowRunner) WithEventSink(sink engine.EventSink) *WorkflowRunner {
	r.eventSink = sink
	return r
}

// GraphExecutionWorkflow is the top-level Temporal workflow function driving the reactive scheduler.
func GraphExecutionWorkflow(
	ctx workflow.Context,
	input GraphWorkflowInput,
) (*GraphWorkflowResult, error) {
	return NewWorkflowRunner(nil).GraphExecutionWorkflow(ctx, input)
}

func checkPayloadSize(ctx workflow.Context, tag string, v any) {
	b, err := json.Marshal(v)
	if err == nil && len(b) > MaxRecommendedPayloadBytes {
		workflow.GetLogger(ctx).Warn("payload size exceeds recommended threshold",
			"tag", tag,
			"size_bytes", len(b),
			"threshold_bytes", MaxRecommendedPayloadBytes,
		)
	}
}

type activityOutcome struct {
	nodeID engine.NodeID
	result *NodeActivityResult
	err    error
}

type waitingSignalEntry struct {
	signalName  string
	cancelTimer workflow.CancelFunc
}

// GraphExecutionWorkflow executes a reactive graph workflow using r's registry.
func (r *WorkflowRunner) GraphExecutionWorkflow(
	ctx workflow.Context,
	input GraphWorkflowInput,
) (*GraphWorkflowResult, error) {
	if input.Definition == nil {
		return nil, temporal.NewNonRetryableApplicationError("GraphDefinition cannot be nil", "InvalidInput", nil)
	}

	wfVersion := GetWorkflowVersion(ctx, ChangeIDGraphWorkflowVersion, VersionInitial, VersionV1)
	if wfVersion >= VersionV1 {
		checkPayloadSize(ctx, "input_definition", input.Definition)
	}

	execID := input.ExecutionID
	if execID == "" {
		execID = engine.ExecutionID(workflow.GetInfo(ctx).WorkflowExecution.ID)
	}

	startTime := workflow.Now(ctx)
	if !workflow.IsReplaying(ctx) {
		metrics.RecordExecutionStarted(string(input.Definition.ID))
	}

	signalRouter := NewSignalRouter()
	var state *engine.GraphState
	defer func() {
		if !workflow.IsReplaying(ctx) && state != nil {
			metrics.RecordExecutionCompleted(string(state.Status()), workflow.Now(ctx).Sub(startTime))
		}
	}()
	if err := RegisterWorkflowQueries(ctx, func() *engine.GraphState { return state }); err != nil {
		return nil, temporal.NewNonRetryableApplicationError(fmt.Sprintf("failed to register queries: %v", err), "InternalError", err)
	}
	if err := workflow.SetQueryHandler(ctx, QueryTypeRejectedSignals, func() ([]RejectedSignal, error) {
		return signalRouter.RejectedSignals(), nil
	}); err != nil {
		return nil, temporal.NewNonRetryableApplicationError(fmt.Sprintf("failed to register rejected signals query: %v", err), "InternalError", err)
	}

	g, err := input.Definition.ToGraph()
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(fmt.Sprintf("failed to parse graph definition: %v", err), "InvalidDefinition", err)
	}

	state, err = engine.NewGraphState(execID, g)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(fmt.Sprintf("failed to create graph state: %v", err), "InternalError", err)
	}

	state, err = state.WithStatus(state.Version(), engine.StatusReady)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError("failed to transition graph state to Ready", "InternalError", err)
	}
	state, err = state.WithStatus(state.Version(), engine.StatusRunning)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError("failed to transition graph state to Running", "InternalError", err)
	}

	reg := r.registry
	if reg == nil {
		reg = engine.NewRegistry()
		_ = nodes.RegisterDefaults(reg, nodes.Dependencies{})
	}

	// Register fallback for unknown node types so we don't fail during schedule (EvaluateReactivity).
	for _, nodeDef := range input.Definition.Nodes {
		if !reg.Has(nodeDef.Type) {
			_ = reg.Register(nodeDef.Type, func(_ any) (engine.NodeInterface, error) {
				return &engine.BaseNode{}, nil
			}, nil)
		}
	}

	nodeDefs := make(map[engine.NodeID]engine.NodeDefinition, len(input.Definition.Nodes))
	for _, n := range input.Definition.Nodes {
		nodeDefs[n.ID] = n
	}

	clock := func() time.Time { return workflow.Now(ctx) }
	seqGen := reactive.NewSequenceGenerator(0)
	emitter := reactive.NewTransitionEmitter(execID, seqGen, clock, r.eventSink)
	executedPolicies := make(map[engine.NodeID][2]string)
	emitter.SetPolicyResolver(func(nodeID engine.NodeID) (string, string) {
		if p, ok := executedPolicies[nodeID]; ok && p[0] != "" {
			return p[0], p[1]
		}
		for _, nd := range input.Definition.Nodes {
			if nd.ID == nodeID && nd.Type == "decision" {
				polID := ""
				if p, ok := nd.Config["policy"].(string); ok && p != "" {
					polID = p
				} else if len(input.PinnedPolicies) == 1 {
					for k := range input.PinnedPolicies {
						polID = k
					}
				}
				if polID != "" && input.PinnedPolicies != nil {
					return polID, input.PinnedPolicies[polID]
				}
			}
		}
		return "", ""
	})
	resolver := reactive.NewPreconditionResolver(nil)
	scheduler := reactive.NewScheduler(g, reg, resolver, emitter)
	detector := reactive.NewTerminalDetector(g, input.Definition.FailurePolicy, emitter)
	saga := NewSagaCoordinator()

	stdCtx := context.Background()

	selector := workflow.NewSelector(ctx)
	inFlightCount := 0
	var completedOutcomes []activityOutcome

	waitingSignals := make(map[engine.NodeID]*waitingSignalEntry)

	handleIncomingSignal := func(sig ExternalSignalPayload) {
		if g.Node(sig.TargetNodeID) == nil {
			signalRouter.RecordRejected(sig, fmt.Sprintf("unknown target node %q", sig.TargetNodeID))
			workflow.GetLogger(ctx).Warn("rejected signal for unknown node", "nodeId", sig.TargetNodeID, "signalName", sig.SignalName)
			return
		}

		ns := state.NodeState(sig.TargetNodeID)
		if ns != nil && ns.Status().IsTerminal() {
			signalRouter.RecordRejected(sig, fmt.Sprintf("node %q is already in terminal status %s", sig.TargetNodeID, ns.Status()))
			workflow.GetLogger(ctx).Warn("rejected signal for terminal node", "nodeId", sig.TargetNodeID, "status", ns.Status())
			return
		}

		signalRouter.RouteSignal(sig)

		if entry, waiting := waitingSignals[sig.TargetNodeID]; waiting {
			if entry.signalName == "" || entry.signalName == sig.SignalName {
				payload, _ := signalRouter.ReceiveNamedSignal(sig.TargetNodeID, entry.signalName)
				if entry.cancelTimer != nil {
					entry.cancelTimer()
				}
				delete(waitingSignals, sig.TargetNodeID)
				inFlightCount--

				setMap := map[string]any{
					"signal":  entry.signalName,
					"payload": payload,
				}
				for k, v := range payload {
					setMap[k] = v
				}
				mut := engine.NewStateMutation(sig.TargetNodeID, 0, nil, setMap, nil, nil)
				completedOutcomes = append(completedOutcomes, activityOutcome{
					nodeID: sig.TargetNodeID,
					result: &NodeActivityResult{NodeID: sig.TargetNodeID, Mutation: &mut},
				})
			}
		}
	}

	signalCh := workflow.GetSignalChannel(ctx, SignalChannelName)
	selector.AddReceive(signalCh, func(c workflow.ReceiveChannel, _ bool) {
		var sig ExternalSignalPayload
		c.Receive(ctx, &sig)
		handleIncomingSignal(sig)
	})

	for {
		// Drain any buffered signals before evaluation
		for {
			var sig ExternalSignalPayload
			if !signalCh.ReceiveAsync(&sig) {
				break
			}
			handleIncomingSignal(sig)
		}

		plan, nextState, err := scheduler.EvaluateStep(stdCtx, state)
		if err != nil {
			return &GraphWorkflowResult{ExecutionID: execID, FinalState: state, Status: engine.StatusFailed, Error: err.Error()}, temporal.NewNonRetryableApplicationError(fmt.Sprintf("scheduler evaluation failed: %v", err), "InternalError", err)
		}
		state = nextState

		for _, nodeID := range plan.ReadyNodes {

			// Scheduler already transitioned to Ready, we skip this transition because it is done in EvaluateStep.
			// Wait, the planner in scheduler transitions to StatusReady, but we should transition to StatusRunning when starting activity.
			oldNS := state.NodeState(nodeID)
			statusRunning := engine.StatusRunning
			mutRunning := engine.StateMutation{NodeID: nodeID, ExpectedVersion: state.Version(), Status: &statusRunning}

			state, err = state.Apply(mutRunning)
			if err != nil {
				return nil, err
			}
			_, _ = emitter.EmitTransition(stdCtx, oldNS, state.NodeState(nodeID), mutRunning)

			inputs := plan.ResolvedInputs[nodeID]
			if inputs == nil {
				inputs = make(map[string]any)
			}
			if len(g.IncomingEdges(nodeID)) == 0 && len(input.InitialState) > 0 {
				for k, v := range input.InitialState {
					if _, exists := inputs[k]; !exists {
						inputs[k] = v
					}
				}
			}

			nodeDef := nodeDefs[nodeID]

			if nodeDef.Type == SubGraphNodeType {
				cfg, err := DecodeSubGraphConfig(nodeDef.Config)
				if err != nil {
					completedOutcomes = append(completedOutcomes, activityOutcome{
						nodeID: nodeID,
						err:    engine.NewNonRetryableError(fmt.Errorf("invalid sub_graph config: %w", err)),
					})
					continue
				}

				fut, err := StartSubGraphChildWorkflow(ctx, execID, nodeID, cfg, inputs)
				if err != nil {
					completedOutcomes = append(completedOutcomes, activityOutcome{
						nodeID: nodeID,
						err:    engine.NewNonRetryableError(fmt.Errorf("failed to start sub_graph: %w", err)),
					})
					continue
				}

				inFlightCount++
				nID := nodeID
				selector.AddFuture(fut, func(f workflow.Future) {
					inFlightCount--
					var childRes GraphWorkflowResult
					err := f.Get(ctx, &childRes)
					if err != nil {
						completedOutcomes = append(completedOutcomes, activityOutcome{
							nodeID: nID,
							err:    err,
						})
					} else {
						res, mergeErr := MergeChildWorkflowResult(nID, &childRes)
						completedOutcomes = append(completedOutcomes, activityOutcome{
							nodeID: nID,
							result: res,
							err:    mergeErr,
						})
					}

					for {
						var sig ExternalSignalPayload
						if !signalCh.ReceiveAsync(&sig) {
							break
						}
						handleIncomingSignal(sig)
					}
				})
			} else if nodeDef.Type == "wait_for_signal" {
				sigName, _ := nodeDef.Config["signal"].(string)
				var timeoutSeconds float64
				if ts, ok := nodeDef.Config["timeout_seconds"]; ok {
					switch v := ts.(type) {
					case int:
						timeoutSeconds = float64(v)
					case float64:
						timeoutSeconds = v
					}
				}

				if sigName == "" {
					completedOutcomes = append(completedOutcomes, activityOutcome{
						nodeID: nodeID,
						err:    engine.NewNonRetryableError(fmt.Errorf("signal is required")),
					})
				} else if payload, ok := signalRouter.ReceiveNamedSignal(nodeID, sigName); ok {
					setMap := map[string]any{
						"signal":  sigName,
						"payload": payload,
					}
					for k, v := range payload {
						setMap[k] = v
					}
					mut := engine.NewStateMutation(nodeID, 0, nil, setMap, nil, nil)
					completedOutcomes = append(completedOutcomes, activityOutcome{
						nodeID: nodeID,
						result: &NodeActivityResult{NodeID: nodeID, Mutation: &mut},
					})
				} else {
					inFlightCount++
					entry := &waitingSignalEntry{signalName: sigName}
					waitingSignals[nodeID] = entry

					if timeoutSeconds > 0 {
						timerCtx, cancelTimer := workflow.WithCancel(ctx)
						entry.cancelTimer = cancelTimer
						timerFut := workflow.NewTimer(timerCtx, time.Duration(timeoutSeconds)*time.Second)

						nID := nodeID
						selector.AddFuture(timerFut, func(f workflow.Future) {
							if err := f.Get(ctx, nil); err != nil {
								return // timer was cancelled because signal arrived
							}
							if _, stillWaiting := waitingSignals[nID]; stillWaiting {
								delete(waitingSignals, nID)
								inFlightCount--
								completedOutcomes = append(completedOutcomes, activityOutcome{
									nodeID: nID,
									err:    engine.NewNonRetryableError(fmt.Errorf("wait_for_signal timeout exceeded")),
								})
							}
						})
					}
				}
			} else {
				actOpts := BuildNodeActivityOptions(workflow.ActivityOptions{}, &nodeDef)
				actOpts.RetryPolicy = ResolveNodeRetryPolicy(&nodeDef, input.Definition.RetryPolicies)
				actCtx := workflow.WithActivityOptions(ctx, actOpts)

				policyID := ""
				policyVersion := ""
				if pol, ok := nodeDef.Config["policy"].(string); ok && pol != "" {
					policyID = pol
					if input.PinnedPolicies != nil {
						policyVersion = input.PinnedPolicies[pol]
					}
				} else if nodeDef.Type == "decision" && len(input.PinnedPolicies) == 1 {
					for k, v := range input.PinnedPolicies {
						policyID = k
						policyVersion = v
					}
				}

				actInput := NodeActivityInput{
					ExecutionID:   execID,
					NodeID:        nodeID,
					NodeType:      nodeDef.Type,
					Config:        nodeDef.Config,
					Inputs:        inputs,
					GraphState:    state,
					Attempt:       1,
					MaxAttempts:   int(actOpts.RetryPolicy.MaximumAttempts),
					PolicyID:      policyID,
					PolicyVersion: policyVersion,
				}

				inFlightCount++
				fut := workflow.ExecuteActivity(actCtx, ExecuteNodeActivityName, actInput)

				nID := nodeID
				selector.AddFuture(fut, func(f workflow.Future) {
					inFlightCount--
					var res NodeActivityResult
					err := f.Get(ctx, &res)
					completedOutcomes = append(completedOutcomes, activityOutcome{nodeID: nID, result: &res, err: err})
					for {
						var sig ExternalSignalPayload
						if !signalCh.ReceiveAsync(&sig) {
							break
						}
						handleIncomingSignal(sig)
					}
				})
			}
		}

		if inFlightCount == 0 && len(completedOutcomes) == 0 {
			assessment, err := detector.AssessState(stdCtx, state)
			if err != nil {
				return nil, err
			}
			if assessment.Phase != reactive.PhaseRunning {
				state = assessment.FinalGraphState
				if assessment.Phase == reactive.PhaseCompensating {
					compState, compErr := saga.ExecuteCompensation(ctx, input.Definition, state)
					state = compState
					if compErr != nil {
						return &GraphWorkflowResult{
							ExecutionID:     execID,
							FinalState:      state,
							Status:          state.Status(),
							Error:           compErr.Error(),
							RejectedSignals: signalRouter.RejectedSignals(),
						}, nil
					}
				}
				break
			}
			if len(plan.ReadyNodes) == 0 && len(plan.SkippedNodes) == 0 {
				break
			}
		} else if inFlightCount > 0 && len(completedOutcomes) == 0 {
			selector.Select(ctx)
		}

		if len(completedOutcomes) > 0 {
			for _, outcome := range completedOutcomes {
				oldNS := state.NodeState(outcome.nodeID)
				if outcome.err == nil {
					if outcome.result != nil && outcome.result.PolicyID != "" {
						executedPolicies[outcome.nodeID] = [2]string{outcome.result.PolicyID, outcome.result.PolicyVersion}
					}
					saga.RecordCompletion(outcome.nodeID, nodeDefs[outcome.nodeID].Compensation)
					statusSucceeded := engine.StatusSucceeded
					mut := engine.StateMutation{
						NodeID:          outcome.nodeID,
						ExpectedVersion: state.Version(),
						Status:          &statusSucceeded,
					}
					if outcome.result != nil && outcome.result.Mutation != nil {
						mut.Set = outcome.result.Mutation.Set
						mut.Unset = outcome.result.Mutation.Unset
						mut.ErrorMessage = outcome.result.Mutation.ErrorMessage
					}
					state, err = state.Apply(mut)
					if err != nil {
						return nil, err
					}
					_, _ = emitter.EmitTransition(stdCtx, oldNS, state.NodeState(outcome.nodeID), mut)
				} else {
					var cause error
					if IsCriticalError(outcome.err) {
						cause = engine.NewCriticalError(outcome.err)
					} else {
						cause = engine.NewNonRetryableError(outcome.err)
					}
					detector.RecordNodeFailure(outcome.nodeID, cause)

					statusFailed := engine.StatusFailed
					errMsg := cause.Error()
					mut := engine.StateMutation{
						NodeID:          outcome.nodeID,
						ExpectedVersion: state.Version(),
						Status:          &statusFailed,
						ErrorMessage:    &errMsg,
					}
					state, err = state.Apply(mut)
					if err != nil {
						return nil, err
					}
					_, _ = emitter.EmitTransition(stdCtx, oldNS, state.NodeState(outcome.nodeID), mut, cause)
				}
			}
			completedOutcomes = nil

			assessment, err := detector.AssessState(stdCtx, state)
			if err != nil {
				return nil, err
			}
			if assessment.Phase != reactive.PhaseRunning {
				state = assessment.FinalGraphState
				if assessment.Phase == reactive.PhaseCompensating {
					compState, compErr := saga.ExecuteCompensation(ctx, input.Definition, state)
					state = compState
					if compErr != nil {
						return &GraphWorkflowResult{
							ExecutionID:     execID,
							FinalState:      state,
							Status:          state.Status(),
							Error:           compErr.Error(),
							RejectedSignals: signalRouter.RejectedSignals(),
						}, nil
					}
				}
				break
			}
		}
	}

	if wfVersion >= VersionV1 {
		checkPayloadSize(ctx, "final_state", state)
	}

	var errStr string
	if state.Status() == engine.StatusFailed {
		errStr = "graph execution failed"
	}

	return &GraphWorkflowResult{
		ExecutionID:     execID,
		FinalState:      state,
		Status:          state.Status(),
		Error:           errStr,
		RejectedSignals: signalRouter.RejectedSignals(),
	}, nil
}
