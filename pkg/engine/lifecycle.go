package engine

import (
	"context"
)

// NodeContext provides the runtime context for a NodeInterface during execution.
// It exposes the immutable inputs, config, state, and execution metadata for the current execution step.
type NodeContext interface {
	// ExecutionID returns the unique identifier for this graph execution workflow.
	ExecutionID() ExecutionID

	// NodeID returns the identifier of the node currently executing.
	NodeID() NodeID

	// Config returns a deep copy of the static configuration for this node.
	// Since it is a deep copy, modifications will not affect the engine or other hooks.
	Config() map[string]any

	// Inputs returns a deep copy of the values resolved from upstream outputs
	// via edge DataMapping. It may be empty if the node has no inputs.
	Inputs() map[string]any

	// State returns an immutable read-only snapshot of the entire graph state
	// at the time this execution step started. Nodes cannot write state except
	// by returning a StateMutation from their Execute hook.
	State() *GraphState

	// Attempt returns the 1-based execution attempt number for this node.
	Attempt() int

	// PolicyID returns the policy identifier pinned for this node execution, or empty if none.
	PolicyID() string

	// PolicyVersion returns the policy version pinned for this node execution, or empty if none.
	PolicyVersion() string
}

// NodeInterface defines the lifecycle hooks for a reactive graph node.
// Implementations define the behaviour of different node types within the graph.
//
// Lifecycle and Invocation Order:
// The engine drives the node lifecycle.
// 1. EvaluateReactivity is called whenever upstream state changes to decide if a Pending node should run.
// 2. When the node runs (moves from Ready to Running), per execution attempt, the engine calls:
//   - OnInit
//   - Execute
//
// 3. Depending on the outcome of Execute (and OnInit):
//   - If successful (nil error), the engine applies any returned StateMutation, then calls OnSuccess.
//   - If an error is returned and no further retries will happen (as determined by the orchestrator per ADR-003),
//     the engine calls OnFailure.
//
// Mutation Rules:
//   - ONLY the Execute method may change the state, and ONLY by returning a *StateMutation.
//   - The mutation may only contain Set, Unset, and ErrorMessage instructions.
//   - The engine is solely responsible for status transitions. It stamps the mutation with the NodeID
//     and ExpectedVersion. Any mutation returned by the node that attempts to set the node Status is invalid
//     and will be rejected by the engine.
//   - All other hooks (OnInit, EvaluateReactivity, OnSuccess, OnFailure) must NOT return mutations.
//   - Errors returned from OnSuccess or OnFailure are recorded for observability but do not alter the
//     final status of the node (the node has already succeeded or failed at that point).
//
// Execution Constraints:
// - All hooks MUST honour context cancellation (ctx.Done()).
// - Hooks must be safe for retries (e.g., Execute must be idempotent or handle retries gracefully).
// - Implementations MUST NOT retain the NodeContext past the duration of the method call.
type NodeInterface interface {
	// OnInit runs before execution begins.
	// Maps to spec hook: on_init
	OnInit(ctx context.Context, nc NodeContext) error

	// EvaluateReactivity is called when upstream state changes.
	// It returns true if the node should transition to Ready given the current state snapshot.
	// Maps to spec hook: evaluate_reactivity
	EvaluateReactivity(ctx context.Context, nc NodeContext) (bool, error)

	// Execute performs the main logic for this node.
	// It returns a StateMutation containing the output patch (variables to set/unset).
	// Returning nil means no output changes.
	// Maps to spec hook: execute
	Execute(ctx context.Context, nc NodeContext) (*StateMutation, error)

	// OnSuccess is called after Execute returns successfully and its mutation has been applied.
	// Maps to spec hook: on_success
	OnSuccess(ctx context.Context, nc NodeContext, result *StateMutation) error

	// OnFailure is called if OnInit or Execute fails and no further retries will occur.
	// Maps to spec hook: on_failure
	OnFailure(ctx context.Context, nc NodeContext, cause error) error
}
