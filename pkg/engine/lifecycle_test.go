package engine

import (
	"context"
	"testing"
)

// fakeNodeContext implements NodeContext for testing.
type fakeNodeContext struct {
	execID  ExecutionID
	nodeID  NodeID
	config  map[string]any
	inputs  map[string]any
	state   *GraphState
	attempt int
}

func (f *fakeNodeContext) ExecutionID() ExecutionID { return f.execID }
func (f *fakeNodeContext) NodeID() NodeID           { return f.nodeID }
func (f *fakeNodeContext) Config() map[string]any   { return f.config }
func (f *fakeNodeContext) Inputs() map[string]any   { return f.inputs }
func (f *fakeNodeContext) State() *GraphState       { return f.state }
func (f *fakeNodeContext) Attempt() int             { return f.attempt }
func (f *fakeNodeContext) PolicyID() string         { return "" }
func (f *fakeNodeContext) PolicyVersion() string    { return "" }

func TestNodeInterfaceHooksCanBeCalled(t *testing.T) {
	// Just a compile-time and basic sanity test that we can call
	// all hooks on a NodeInterface with our fake context.
	var node NodeInterface = &fakeNode{}

	ctx := context.Background()
	nc := &fakeNodeContext{
		execID:  "exec-1",
		nodeID:  "node-1",
		attempt: 1,
	}

	if err := node.OnInit(ctx, nc); err != nil {
		t.Fatalf("OnInit error: %v", err)
	}

	ready, err := node.EvaluateReactivity(ctx, nc)
	if err != nil {
		t.Fatalf("EvaluateReactivity error: %v", err)
	}
	if !ready {
		t.Errorf("Expected node to be ready")
	}

	mut, err := node.Execute(ctx, nc)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if err := node.OnSuccess(ctx, nc, mut); err != nil {
		t.Fatalf("OnSuccess error: %v", err)
	}

	if err := node.OnFailure(ctx, nc, nil); err != nil {
		t.Fatalf("OnFailure error: %v", err)
	}
}

// fakeNode is a minimal implementation for the test above.
type fakeNode struct{}

func (f *fakeNode) OnInit(context.Context, NodeContext) error { return nil }
func (f *fakeNode) EvaluateReactivity(context.Context, NodeContext) (bool, error) {
	return true, nil
}

func (f *fakeNode) Execute(context.Context, NodeContext) (*StateMutation, error) {
	return nil, nil
}
func (f *fakeNode) OnSuccess(context.Context, NodeContext, *StateMutation) error { return nil }
func (f *fakeNode) OnFailure(context.Context, NodeContext, error) error          { return nil }
