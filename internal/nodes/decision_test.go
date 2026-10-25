package nodes_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/graph"
	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

type fakeChooser struct {
	chooseFunc func(ctx context.Context, decision engine.NodeID, obs map[string]any, options []string) (string, error)
}

func (f *fakeChooser) Choose(ctx context.Context, decision engine.NodeID, obs map[string]any, options []string) (string, error) {
	if f.chooseFunc != nil {
		return f.chooseFunc(ctx, decision, obs, options)
	}
	if len(options) > 0 {
		return options[0], nil
	}
	return "", nil
}

func TestDecisionConfigValidation(t *testing.T) {
	reg := engine.NewRegistry()
	err := nodes.RegisterDecision(reg, &fakeChooser{})
	if err != nil {
		t.Fatalf("RegisterDecision failed: %v", err)
	}

	tests := []struct {
		name    string
		config  map[string]any
		wantErr bool
	}{
		{
			name: "valid config",
			config: map[string]any{
				"options":  []any{"approve", "reject"},
				"observes": []any{"upstream.output.score", "upstream.status"},
			},
			wantErr: false,
		},
		{
			name: "empty options",
			config: map[string]any{
				"options":  []any{},
				"observes": []any{"upstream.status"},
			},
			wantErr: true, // at least one option is required
		},
		{
			name: "duplicate options",
			config: map[string]any{
				"options":  []any{"approve", "approve"},
				"observes": []any{"upstream.status"},
			},
			wantErr: true,
		},
		{
			name: "missing option field (nil)",
			config: map[string]any{
				"observes": []any{"upstream.status"},
			},
			wantErr: true,
		},
		{
			name: "invalid observe path - missing dots",
			config: map[string]any{
				"options":  []any{"approve"},
				"observes": []any{"upstream_status"},
			},
			wantErr: true,
		},
		{
			name: "invalid observe path - wrong second part",
			config: map[string]any{
				"options":  []any{"approve"},
				"observes": []any{"upstream.invalid.field"},
			},
			wantErr: true,
		},
		{
			name: "invalid observe path - status too long",
			config: map[string]any{
				"options":  []any{"approve"},
				"observes": []any{"upstream.status.extra"},
			},
			wantErr: true,
		},
		{
			name: "invalid observe path - output too short",
			config: map[string]any{
				"options":  []any{"approve"},
				"observes": []any{"upstream.output"},
			},
			wantErr: true,
		},
		{
			name: "unknown field",
			config: map[string]any{
				"options": []any{"approve"},
				"unknown": "field",
			},
			wantErr: true, // DisallowUnknownFields test
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reg.ValidateConfig("decision", tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDecisionNodeExecution(t *testing.T) {
	reg := engine.NewRegistry()

	var lastObs map[string]any
	var lastOptions []string
	_ = lastOptions

	chooser := &fakeChooser{
		chooseFunc: func(_ context.Context, _ engine.NodeID, obs map[string]any, options []string) (string, error) {
			lastObs = obs
			lastOptions = options
			if obs["fail"] == true {
				return "", errors.New("chooser error")
			}
			if len(options) > 0 {
				return options[0], nil
			}
			return "", nil
		},
	}

	err := nodes.RegisterDecision(reg, chooser)
	if err != nil {
		t.Fatalf("RegisterDecision failed: %v", err)
	}

	// Create a graph and some state to test against
	node1, _ := engine.NewNode("node_succ", "passthrough", nil)
	node2, _ := engine.NewNode("node_skip", "passthrough", nil)
	node3, _ := engine.NewNode("decision_node", "decision", nil)

	g, _ := engine.NewGraph("test-graph", []*engine.Node{node1, node2, node3}, nil)

	state, _ := engine.NewGraphState("exec1", g)

	// Pending -> Ready -> Running -> Succeeded
	readyStatus := engine.StatusReady
	runningStatus := engine.StatusRunning
	validStatus := engine.StatusSucceeded
	skippedStatus := engine.StatusSkipped

	state, err = state.Apply(engine.NewStateMutation("node_succ", state.Version(), &readyStatus, nil, nil, nil))
	if err != nil {
		t.Fatalf("failed to apply mutation node_succ ready: %v", err)
	}
	state, err = state.Apply(engine.NewStateMutation("node_succ", state.Version(), &runningStatus, nil, nil, nil))
	if err != nil {
		t.Fatalf("failed to apply mutation node_succ running: %v", err)
	}
	state, err = state.Apply(engine.NewStateMutation("node_succ", state.Version(), &validStatus, map[string]any{"field1": "value1", "nested.field": "value2"}, nil, nil))
	if err != nil {
		t.Fatalf("failed to apply mutation node_succ succeeded: %v", err)
	}

	// Pending -> Ready -> Skipped
	state, err = state.Apply(engine.NewStateMutation("node_skip", state.Version(), &readyStatus, nil, nil, nil))
	if err != nil {
		t.Fatalf("failed to apply mutation node_skip ready: %v", err)
	}
	state, err = state.Apply(engine.NewStateMutation("node_skip", state.Version(), &skippedStatus, nil, nil, nil))
	if err != nil {
		t.Fatalf("failed to apply mutation node_skip skipped: %v", err)
	}

	tests := []struct {
		name       string
		config     map[string]any
		mockChoice string
		mockErr    error
		wantErr    bool
		checkObs   func(t *testing.T, obs map[string]any)
		checkMut   func(t *testing.T, mut *engine.StateMutation)
	}{
		{
			name: "successful extraction and choice",
			config: map[string]any{
				"options": []any{"opt1", "opt2"},
				"observes": []any{
					"node_succ.output.field1",
					"node_succ.output.nested.field",
					"node_succ.output.missing",
					"node_succ.status",
					"node_skip.output.field1",
					"node_skip.status",
					"node_absent.output.field1",
					"node_absent.status",
				},
			},
			mockChoice: "opt1",
			checkObs: func(t *testing.T, obs map[string]any) {
				if len(obs) != 8 {
					t.Errorf("expected 8 observations, got %d", len(obs))
				}
				if obs["node_succ.output.field1"] != "value1" {
					t.Errorf("expected value1, got %v", obs["node_succ.output.field1"])
				}
				if obs["node_succ.output.nested.field"] != "value2" {
					t.Errorf("expected value2, got %v", obs["node_succ.output.nested.field"])
				}
				if obs["node_succ.output.missing"] != nil {
					t.Errorf("expected nil for missing, got %v", obs["node_succ.output.missing"])
				}
				if obs["node_succ.status"] != "Succeeded" {
					t.Errorf("expected Succeeded, got %v", obs["node_succ.status"])
				}
				if obs["node_skip.output.field1"] != nil {
					t.Errorf("expected nil for skipped node output, got %v", obs["node_skip.output.field1"])
				}
				if obs["node_skip.status"] != "Skipped" {
					t.Errorf("expected Skipped, got %v", obs["node_skip.status"])
				}
				if obs["node_absent.output.field1"] != nil {
					t.Errorf("expected nil for absent node output, got %v", obs["node_absent.output.field1"])
				}
				if obs["node_absent.status"] != nil {
					t.Errorf("expected nil for absent node status, got %v", obs["node_absent.status"])
				}
			},
			checkMut: func(t *testing.T, mut *engine.StateMutation) {
				if mut == nil {
					t.Fatal("expected mutation, got nil")
				}
				if mut.Set["choice"] != "opt1" {
					t.Errorf("expected choice 'opt1', got %v", mut.Set["choice"])
				}
			},
		},
		{
			name: "chooser error propagates as non-retryable",
			config: map[string]any{
				"options":  []any{"opt1"},
				"observes": []any{},
			},
			mockErr: errors.New("some error"),
			wantErr: true,
		},
		{
			name: "invalid choice returns non-retryable error",
			config: map[string]any{
				"options":  []any{"opt1", "opt2"},
				"observes": []any{},
			},
			mockChoice: "invalid_opt",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := reg.Instantiate("decision", tt.config)
			if err != nil {
				t.Fatalf("failed to instantiate: %v", err)
			}

			lastObs = nil
			lastOptions = nil
			chooser.chooseFunc = func(_ context.Context, _ engine.NodeID, obs map[string]any, options []string) (string, error) {
				lastObs = obs
				lastOptions = options
				_ = lastOptions
				return tt.mockChoice, tt.mockErr
			}

			nc := &fakeNodeContext{
				nodeID: "decision_node",
				state:  state,
			}

			mut, err := node.Execute(context.Background(), nc)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if engine.ClassOf(err) != engine.ClassNonRetryable {
					t.Errorf("expected non-retryable error, got %v", engine.ClassOf(err))
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.checkObs != nil {
				tt.checkObs(t, lastObs)
			}

			if tt.checkMut != nil {
				tt.checkMut(t, mut)
			}
		})
	}
}

func TestSemanticValidation(t *testing.T) {
	reg := engine.NewRegistry()
	_ = nodes.RegisterDefaults(reg, nodes.Dependencies{})
	_ = nodes.RegisterDecision(reg, &fakeChooser{})

	def := &engine.GraphDefinition{
		APIVersion: "graph/v1",
		Nodes: []engine.NodeDefinition{
			{
				ID:   "decide",
				Type: "decision",
				Config: map[string]any{
					"options":  []any{"yes", "no"},
					"observes": []any{},
				},
			},
			{
				ID:   "yes_branch",
				Type: "passthrough",
			},
		},
		Edges: []engine.EdgeDefinition{
			{
				From:      "decide",
				To:        "yes_branch",
				Condition: `decide.output.choice == "yes"`,
			},
		},
	}

	errs := graph.ValidateSemantics(def, reg)
	if errs.Err() != nil {
		t.Errorf("expected semantic validation to pass, got: %v", errs.Error())
	}
}

func TestRegisterDefaults_WithPolicyStore(t *testing.T) {
	ps := memory.NewPolicyStore()

	// Without policy store, decision is not registered
	regNoPS := engine.NewRegistry()
	if err := nodes.RegisterDefaults(regNoPS, nodes.Dependencies{}); err != nil {
		t.Fatalf("RegisterDefaults failed: %v", err)
	}
	if _, err := regNoPS.Instantiate("decision", map[string]any{"options": []any{"opt1"}}); err == nil {
		t.Errorf("expected decision node to NOT be registered without PolicyStore")
	}

	// With policy store, decision is registered
	regWithPS := engine.NewRegistry()
	if err := nodes.RegisterDefaults(regWithPS, nodes.Dependencies{PolicyStore: ps}); err != nil {
		t.Fatalf("RegisterDefaults failed: %v", err)
	}
	if _, err := regWithPS.Instantiate("decision", map[string]any{"options": []any{"opt1"}}); err != nil {
		t.Errorf("expected decision node to be registered with PolicyStore, got: %v", err)
	}
}

func TestPolicyDecisionChooser_Rules_Pinned(t *testing.T) {
	ctx := context.Background()
	ps := memory.NewPolicyStore()

	policyYAML := `apiVersion: policy/v1
id: credit-policy
version: v1
decisions:
  check_credit:
    rules:
      - when: "score_node.output.score >= 0.8"
        choose: approve
      - else: reject
`
	_, err := ps.SavePolicy(ctx, &engine.PolicyRecord{
		ID:       "credit-policy",
		GraphID:  "test-graph",
		Document: []byte(policyYAML),
		Format:   "yaml",
	})
	if err != nil {
		t.Fatalf("SavePolicy failed: %v", err)
	}

	reg := engine.NewRegistry()
	if err := nodes.RegisterDefaults(reg, nodes.Dependencies{PolicyStore: ps}); err != nil {
		t.Fatalf("RegisterDefaults failed: %v", err)
	}

	node, err := reg.Instantiate("decision", map[string]any{
		"options":  []any{"approve", "reject"},
		"observes": []any{"score_node.output.score"},
		"policy":   "credit-policy",
	})
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	// Helper to create state with score
	makeState := func(score float64) *engine.GraphState {
		n1, _ := engine.NewNode("score_node", "passthrough", nil)
		nd, _ := engine.NewNode("check_credit", "decision", nil)
		g, _ := engine.NewGraph("test-graph", []*engine.Node{n1, nd}, nil)
		st, _ := engine.NewGraphState("exec-1", g)

		statusReady := engine.StatusReady
		statusRunning := engine.StatusRunning
		statusSucc := engine.StatusSucceeded

		st, _ = st.Apply(engine.NewStateMutation("score_node", st.Version(), &statusReady, nil, nil, nil))
		st, _ = st.Apply(engine.NewStateMutation("score_node", st.Version(), &statusRunning, nil, nil, nil))
		st, _ = st.Apply(engine.NewStateMutation("score_node", st.Version(), &statusSucc, map[string]any{"score": score}, nil, nil))
		return st
	}

	// 1. High score -> approve
	stHigh := makeState(0.85)
	ncHigh := &fakeNodeContext{
		nodeID:        "check_credit",
		state:         stHigh,
		config:        map[string]any{"policy": "credit-policy"},
		policyID:      "credit-policy",
		policyVersion: "1",
	}

	mutHigh, err := node.Execute(ctx, ncHigh)
	if err != nil {
		t.Fatalf("Execute high score failed: %v", err)
	}
	if mutHigh.Set["choice"] != "approve" {
		t.Errorf("expected choice 'approve', got %v", mutHigh.Set["choice"])
	}

	// 2. Low score -> reject
	stLow := makeState(0.5)
	ncLow := &fakeNodeContext{
		nodeID:        "check_credit",
		state:         stLow,
		config:        map[string]any{"policy": "credit-policy"},
		policyID:      "credit-policy",
		policyVersion: "1",
	}

	mutLow, err := node.Execute(ctx, ncLow)
	if err != nil {
		t.Fatalf("Execute low score failed: %v", err)
	}
	if mutLow.Set["choice"] != "reject" {
		t.Errorf("expected choice 'reject', got %v", mutLow.Set["choice"])
	}
}

func TestPolicyDecisionChooser_Table_Pinned(t *testing.T) {
	ctx := context.Background()
	ps := memory.NewPolicyStore()

	policyYAML := `apiVersion: policy/v1
id: routing-policy
version: v1
decisions:
  route_case:
    table:
      - observed:
          region.output.code: "NA"
        choose: fast_track
      - observed:
          region.output.code: "EU"
        choose: compliance_review
    default: standard
`
	_, err := ps.SavePolicy(ctx, &engine.PolicyRecord{
		ID:       "routing-policy",
		GraphID:  "test-graph",
		Document: []byte(policyYAML),
		Format:   "yaml",
	})
	if err != nil {
		t.Fatalf("SavePolicy failed: %v", err)
	}

	reg := engine.NewRegistry()
	if err := nodes.RegisterDefaults(reg, nodes.Dependencies{PolicyStore: ps}); err != nil {
		t.Fatalf("RegisterDefaults failed: %v", err)
	}

	node, err := reg.Instantiate("decision", map[string]any{
		"options":  []any{"fast_track", "compliance_review", "standard"},
		"observes": []any{"region.output.code"},
		"policy":   "routing-policy",
	})
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	makeState := func(code string) *engine.GraphState {
		n1, _ := engine.NewNode("region", "passthrough", nil)
		nd, _ := engine.NewNode("route_case", "decision", nil)
		g, _ := engine.NewGraph("test-graph", []*engine.Node{n1, nd}, nil)
		st, _ := engine.NewGraphState("exec-1", g)

		statusReady := engine.StatusReady
		statusRunning := engine.StatusRunning
		statusSucc := engine.StatusSucceeded

		st, _ = st.Apply(engine.NewStateMutation("region", st.Version(), &statusReady, nil, nil, nil))
		st, _ = st.Apply(engine.NewStateMutation("region", st.Version(), &statusRunning, nil, nil, nil))
		st, _ = st.Apply(engine.NewStateMutation("region", st.Version(), &statusSucc, map[string]any{"code": code}, nil, nil))
		return st
	}

	// Match first row
	ncNA := &fakeNodeContext{
		nodeID:        "route_case",
		state:         makeState("NA"),
		config:        map[string]any{"policy": "routing-policy"},
		policyID:      "routing-policy",
		policyVersion: "1",
	}
	mut, err := node.Execute(ctx, ncNA)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if mut.Set["choice"] != "fast_track" {
		t.Errorf("expected choice 'fast_track', got %v", mut.Set["choice"])
	}

	// Fallback to default
	ncOther := &fakeNodeContext{
		nodeID:        "route_case",
		state:         makeState("APAC"),
		config:        map[string]any{"policy": "routing-policy"},
		policyID:      "routing-policy",
		policyVersion: "1",
	}
	mutOther, err := node.Execute(ctx, ncOther)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if mutOther.Set["choice"] != "standard" {
		t.Errorf("expected choice 'standard', got %v", mutOther.Set["choice"])
	}
}

func TestPolicyDecisionChooser_ActiveDeploymentFallback(t *testing.T) {
	ctx := context.Background()
	ps := memory.NewPolicyStore()

	policyYAML := `apiVersion: policy/v1
id: active-policy
version: v1
decisions:
  decide:
    rules:
      - else: opt_deployed
`
	_, err := ps.SavePolicy(ctx, &engine.PolicyRecord{
		ID:       "active-policy",
		GraphID:  "my-graph",
		Document: []byte(policyYAML),
		Format:   "yaml",
	})
	if err != nil {
		t.Fatalf("SavePolicy failed: %v", err)
	}

	// Set deployment to Live
	if err := ps.SetDeployment(ctx, &engine.PolicyDeployment{
		GraphID:       "my-graph",
		PolicyID:      "active-policy",
		PolicyVersion: "1",
		Mode:          engine.DeploymentLive,
	}); err != nil {
		t.Fatalf("SetDeployment failed: %v", err)
	}

	reg := engine.NewRegistry()
	if err := nodes.RegisterDefaults(reg, nodes.Dependencies{PolicyStore: ps}); err != nil {
		t.Fatalf("RegisterDefaults failed: %v", err)
	}

	node, err := reg.Instantiate("decision", map[string]any{
		"options": []any{"opt_deployed", "opt_other"},
		"policy":  "active-policy",
	})
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	nd, _ := engine.NewNode("decide", "decision", nil)
	g, _ := engine.NewGraph("my-graph", []*engine.Node{nd}, nil)
	st, _ := engine.NewGraphState("exec-1", g)

	// Unpinned context (PolicyVersion is empty)
	nc := &fakeNodeContext{
		nodeID:        "decide",
		state:         st,
		config:        map[string]any{"policy": "active-policy"},
		policyID:      "active-policy",
		policyVersion: "",
	}

	mut, err := node.Execute(ctx, nc)
	if err != nil {
		t.Fatalf("Execute with unpinned live deployment failed: %v", err)
	}
	if mut.Set["choice"] != "opt_deployed" {
		t.Errorf("expected opt_deployed, got %v", mut.Set["choice"])
	}
}

func TestPolicyDecisionChooser_Errors(t *testing.T) {
	ctx := context.Background()
	ps := memory.NewPolicyStore()

	reg := engine.NewRegistry()
	_ = nodes.RegisterDefaults(reg, nodes.Dependencies{PolicyStore: ps})

	node, _ := reg.Instantiate("decision", map[string]any{
		"options": []any{"a", "b"},
		"policy":  "missing-policy",
	})

	nd, _ := engine.NewNode("decide", "decision", nil)
	g, _ := engine.NewGraph("err-graph", []*engine.Node{nd}, nil)
	st, _ := engine.NewGraphState("exec-1", g)

	// 1. Unpinned and no deployment exists
	nc1 := &fakeNodeContext{
		nodeID: "decide",
		state:  st,
		config: map[string]any{"policy": "missing-policy"},
	}
	_, err1 := node.Execute(ctx, nc1)
	if err1 == nil {
		t.Errorf("expected error when policy deployment not found")
	}

	// 2. Pinned version does not exist in store
	nc2 := &fakeNodeContext{
		nodeID:        "decide",
		state:         st,
		config:        map[string]any{"policy": "missing-policy"},
		policyID:      "missing-policy",
		policyVersion: "99",
	}
	_, err2 := node.Execute(ctx, nc2)
	if err2 == nil {
		t.Errorf("expected error when pinned policy not found in store")
	}
}
