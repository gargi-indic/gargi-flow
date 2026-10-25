package reactive_test

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/reactive"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

func serializeGraphState(state *engine.GraphState) string {
	states := state.NodeStates()
	sort.Slice(states, func(i, j int) bool {
		return states[i].NodeID() < states[j].NodeID()
	})
	var sb strings.Builder
	fmt.Fprintf(&sb, "graph=%s status=%s version=%d;", state.GraphID(), state.Status(), state.Version())
	for _, ns := range states {
		outBytes, _ := json.Marshal(ns.Output())
		fmt.Fprintf(&sb, "node=%s status=%s version=%d err=%s out=%s;",
			ns.NodeID(), ns.Status(), ns.Version(), ns.ErrorMessage(), string(outBytes))
	}
	return sb.String()
}

type deterministicTestSink struct {
	events []*engine.Event
}

func (s *deterministicTestSink) Emit(_ context.Context, e *engine.Event) error {
	s.events = append(s.events, e)
	return nil
}

type deterministicMockNode struct {
	out map[string]any
}

func (m *deterministicMockNode) OnInit(_ context.Context, _ engine.NodeContext) error { return nil }
func (m *deterministicMockNode) EvaluateReactivity(_ context.Context, _ engine.NodeContext) (bool, error) {
	return true, nil
}

func (m *deterministicMockNode) Execute(_ context.Context, ctx engine.NodeContext) (*engine.StateMutation, error) {
	out := m.out
	if out == nil {
		out = make(map[string]any)
	}

	// Default route setting for testing branch routing
	if _, ok := out["route"]; !ok {
		if routeCfg, ok := ctx.Config()["route"].(string); ok {
			out["route"] = routeCfg
		}
	}

	mut := engine.NewStateMutation(ctx.NodeID(), ctx.State().Version(), nil, out, nil, nil)
	return &mut, nil
}

func (m *deterministicMockNode) OnSuccess(_ context.Context, _ engine.NodeContext, _ *engine.StateMutation) error {
	return nil
}

func (m *deterministicMockNode) OnFailure(_ context.Context, _ engine.NodeContext, _ error) error {
	return nil
}

func buildDeterminismTestGraph(t *testing.T, shuffleSeed int64) (*engine.Graph, *engine.Registry) {
	reg := engine.NewRegistry()
	_ = reg.Register("mock_det", func(_ any) (engine.NodeInterface, error) {
		return &deterministicMockNode{}, nil
	}, nil)

	nodes := []*engine.Node{
		mustNode(t, "root", "mock_det", map[string]any{"route": "left"}),
		mustNode(t, "left_branch", "mock_det", nil),
		mustNode(t, "right_branch", "mock_det", nil),
		mustNodeJoin(t, "join_all", "mock_det", nil, engine.JoinModeAllOf),
		mustNodeJoin(t, "join_any", "mock_det", nil, engine.JoinModeAnyOf),
		mustNode(t, "leaf", "mock_det", nil),
	}

	edges := []*engine.Edge{
		mustEdge(t, "root", "left_branch", "upstream.output.route == 'left'", map[string]string{"route": "route"}),
		mustEdge(t, "root", "right_branch", "upstream.output.route == 'right'", map[string]string{"route": "route"}),
		mustEdge(t, "left_branch", "join_all", "", nil),
		mustEdge(t, "right_branch", "join_all", "", nil),
		mustEdge(t, "left_branch", "join_any", "", nil),
		mustEdge(t, "right_branch", "join_any", "", nil),
		mustEdge(t, "join_all", "leaf", "", nil),
		mustEdge(t, "join_any", "leaf", "", nil),
	}

	if shuffleSeed != 0 {
		r := rand.New(rand.NewPCG(uint64(shuffleSeed), uint64(shuffleSeed+1)))
		r.Shuffle(len(nodes), func(i, j int) { nodes[i], nodes[j] = nodes[j], nodes[i] })
		r.Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })
	}

	g, err := engine.NewGraph("det_graph", nodes, edges)
	if err != nil {
		t.Fatalf("failed to build test graph: %v", err)
	}

	return g, reg
}

func mustNode(t *testing.T, id string, typ string, cfg map[string]any) *engine.Node {
	n, err := engine.NewNode(engine.NodeID(id), typ, cfg)
	if err != nil {
		t.Fatalf("mustNode: %v", err)
	}
	return n
}

func mustNodeJoin(t *testing.T, id string, typ string, cfg map[string]any, jm engine.JoinMode) *engine.Node {
	n, err := engine.NewNodeWithJoinMode(engine.NodeID(id), typ, cfg, jm)
	if err != nil {
		t.Fatalf("mustNodeJoin: %v", err)
	}
	return n
}

func mustEdge(t *testing.T, from, to string, cond string, mapping map[string]string) *engine.Edge {
	e, err := engine.NewEdge(engine.NodeID(from), engine.NodeID(to), cond, mapping)
	if err != nil {
		t.Fatalf("mustEdge: %v", err)
	}
	return e
}

func runDeterminismGraph(t *testing.T, g *engine.Graph, reg *engine.Registry) (string, string) {
	ctx := context.Background()
	execID := engine.ExecutionID("exec-det")
	state, _ := engine.NewGraphState(execID, g)
	sink := &deterministicTestSink{}

	clock := func() time.Time { return time.Unix(1000, 0).UTC() }
	seqGen := reactive.NewSequenceGenerator(0)
	emitter := reactive.NewTransitionEmitter(execID, seqGen, clock, sink)
	resolver := reactive.NewPreconditionResolver(nil)
	sched := reactive.NewScheduler(g, reg, resolver, emitter)
	terminalDetector := reactive.NewTerminalDetector(g, nil, emitter)

	for {
		plan, nextState, err := sched.EvaluateStep(ctx, state)
		if err != nil {
			t.Fatalf("EvaluateStep error: %v", err)
		}

		state = nextState

		// Simulate execution
		for _, nodeID := range plan.ReadyNodes {
			// Ready -> Running
			mutRun := engine.StateMutation{NodeID: nodeID, ExpectedVersion: state.Version(), Status: schedulerStatusPtr(engine.StatusRunning)}
			state, _ = state.Apply(mutRun)
			_, _ = emitter.EmitTransition(ctx, nextState.NodeState(nodeID), state.NodeState(nodeID), mutRun)

			// Running -> Succeeded
			n := g.Node(nodeID)
			inst, _ := reg.Instantiate(n.Type(), n.Config())

			// Hack: inject mock inputs handling to create output payload
			outMut, _ := inst.Execute(ctx, &mockNodeCtx{nodeID: nodeID, state: state, config: n.Config()})

			status := engine.StatusSucceeded
			outMut.Status = &status
			outMut.ExpectedVersion = state.Version()

			oldState := state
			state, err = state.Apply(*outMut)
			if err != nil {
				t.Fatalf("apply outMut error: %v", err)
			}
			_, _ = emitter.EmitTransition(ctx, oldState.NodeState(nodeID), state.NodeState(nodeID), *outMut)
		}

		assessment, err := terminalDetector.AssessState(ctx, state)
		if err != nil {
			t.Fatalf("AssessState error: %v", err)
		}

		if assessment.Phase != reactive.PhaseRunning {
			state = assessment.FinalGraphState
			break
		}

		if len(plan.ReadyNodes) == 0 && len(plan.SkippedNodes) == 0 {
			break
		}
	}

	stateJSON := serializeGraphState(state)

	// Normalize events to just (seq, type, nodeid, payload)
	type evLite struct {
		Seq     uint64
		Type    engine.EventType
		NodeID  engine.NodeID
		Payload any
	}

	var evList []evLite
	for _, e := range sink.events {
		evList = append(evList, evLite{
			Seq:     e.Sequence(),
			Type:    e.Type(),
			NodeID:  e.NodeID(),
			Payload: e.Payload(),
		})
	}

	eventsJSON, _ := json.Marshal(evList)

	return stateJSON, string(eventsJSON)
}

type mockNodeCtx struct {
	nodeID engine.NodeID
	state  *engine.GraphState
	config map[string]any
}

func (c *mockNodeCtx) ExecutionID() engine.ExecutionID { return "exec-det" }
func (c *mockNodeCtx) NodeID() engine.NodeID           { return c.nodeID }
func (c *mockNodeCtx) Config() map[string]any          { return c.config }
func (c *mockNodeCtx) Inputs() map[string]any          { return nil }
func (c *mockNodeCtx) State() *engine.GraphState       { return c.state }
func (c *mockNodeCtx) Attempt() int                    { return 1 }
func (c *mockNodeCtx) PolicyID() string                { return "" }
func (c *mockNodeCtx) PolicyVersion() string           { return "" }

func TestReactivityDeterminism(t *testing.T) {
	var canonicalState, canonicalEvents string

	for i := 0; i < 100; i++ {
		seed := int64(i)
		if i == 0 {
			seed = 0 // canonical un-shuffled
		}

		g, reg := buildDeterminismTestGraph(t, seed)
		finalStateJSON, finalEventsJSON := runDeterminismGraph(t, g, reg)

		if i == 0 {
			canonicalState = finalStateJSON
			canonicalEvents = finalEventsJSON
		} else {
			if finalStateJSON != canonicalState {
				t.Fatalf("Run %d: final state mismatch", i)
			}
			if finalEventsJSON != canonicalEvents {
				t.Fatalf("Run %d: final events mismatch", i)
			}
		}
	}
}

func TestScheduler_NoWallClockAudit(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}

	fset := token.NewFileSet()

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		node, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", name, err)
		}

		for _, imp := range node.Imports {
			path := imp.Path.Value
			if path == `"math/rand"` || path == `"math/rand/v2"` {
				t.Errorf("File %s imports math/rand", name)
			}
		}

		fullNode, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("failed to parse %s fully: %v", name, err)
		}

		ast.Inspect(fullNode, func(n ast.Node) bool {
			if goStmt, ok := n.(*ast.GoStmt); ok {
				t.Errorf("File %s contains naked goroutine at line %d", name, fset.Position(goStmt.Pos()).Line)
			}

			if callExpr, ok := n.(*ast.CallExpr); ok {
				if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
					if ident, ok := selExpr.X.(*ast.Ident); ok {
						if ident.Name == "time" && selExpr.Sel.Name == "Now" {
							t.Errorf("File %s calls time.Now() at line %d", name, fset.Position(callExpr.Pos()).Line)
						}
					}
				}
			}
			return true
		})
	}
}
