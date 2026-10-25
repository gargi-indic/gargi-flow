package nodes_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

type fakeNodeContext struct {
	executionID         engine.ExecutionID
	nodeID              engine.NodeID
	config              map[string]any
	inputs              map[string]any
	state               *engine.GraphState
	attempt             int
	policyID            string
	policyVersion       string
	shadowPolicyID      string
	shadowPolicyVersion string
	shadowChoice        string
}

func (f *fakeNodeContext) ExecutionID() engine.ExecutionID { return f.executionID }
func (f *fakeNodeContext) NodeID() engine.NodeID           { return f.nodeID }
func (f *fakeNodeContext) Config() map[string]any          { return f.config }
func (f *fakeNodeContext) Inputs() map[string]any          { return f.inputs }
func (f *fakeNodeContext) State() *engine.GraphState       { return f.state }
func (f *fakeNodeContext) Attempt() int                    { return f.attempt }
func (f *fakeNodeContext) PolicyID() string                { return f.policyID }
func (f *fakeNodeContext) PolicyVersion() string           { return f.policyVersion }
func (f *fakeNodeContext) SetResolvedPolicy(policyID, version string) {
	f.policyID = policyID
	f.policyVersion = version
}

func (f *fakeNodeContext) SetShadowPolicy(policyID, version, choice string) {
	f.shadowPolicyID = policyID
	f.shadowPolicyVersion = version
	f.shadowChoice = choice
}

func TestPassthroughNode(t *testing.T) {
	reg := engine.NewRegistry()
	err := nodes.RegisterDefaults(reg, nodes.Dependencies{})
	if err != nil {
		t.Fatalf("failed to register defaults: %v", err)
	}

	t.Run("valid execution", func(t *testing.T) {
		node, err := reg.Instantiate("passthrough", nil)
		if err != nil {
			t.Fatalf("failed to instantiate: %v", err)
		}

		n1, _ := engine.NewNode("node1", "passthrough", nil)
		g, _ := engine.NewGraph("test-graph", []*engine.Node{n1}, nil)

		state, _ := engine.NewGraphState("exec1", g)

		nc := &fakeNodeContext{
			nodeID: "node1",
			inputs: map[string]any{
				"key1": "value1",
				"key2": 42,
			},
			state: state,
		}

		mut, err := node.Execute(context.Background(), nc)
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}

		if mut == nil {
			t.Fatal("expected mutation, got nil")
		}

		if mut.Status != nil {
			t.Errorf("expected nil status, got %v", mut.Status)
		}

		if !reflect.DeepEqual(mut.Set, nc.inputs) {
			t.Errorf("expected %v, got %v", nc.inputs, mut.Set)
		}

		// Ensure deep copy
		nc.inputs["key1"] = "mutated"
		if mut.Set["key1"] == "mutated" {
			t.Error("expected deep copy, but mutation was modified")
		}
	})

	t.Run("empty inputs returns nil mutation", func(t *testing.T) {
		node, err := reg.Instantiate("passthrough", nil)
		if err != nil {
			t.Fatalf("failed to instantiate: %v", err)
		}

		nc := &fakeNodeContext{
			nodeID: "node1",
			inputs: map[string]any{},
			state:  nil, // Verify it does not panic when State() is nil
		}

		mut, err := node.Execute(context.Background(), nc)
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		if mut != nil {
			t.Errorf("expected nil mutation, got %v", mut)
		}
	})

	t.Run("rejects unknown config", func(t *testing.T) {
		_, err := reg.Instantiate("passthrough", map[string]any{"unknown": "key"})
		if err == nil {
			t.Fatal("expected error on unknown config key, got nil")
		}
	})
}
