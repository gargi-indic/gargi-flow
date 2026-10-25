package reactive

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

type mockSink struct {
	events []*engine.Event
	err    error
	mu     sync.Mutex
}

func (m *mockSink) Emit(_ context.Context, event *engine.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, event)
	return nil
}

func TestSequenceGenerator(t *testing.T) {
	sg := NewSequenceGenerator(10)
	if got := sg.Current(); got != 10 {
		t.Errorf("expected 10, got %d", got)
	}
	if got := sg.Next(); got != 11 {
		t.Errorf("expected 11, got %d", got)
	}
	if got := sg.Current(); got != 11 {
		t.Errorf("expected 11, got %d", got)
	}
}

func TestSequenceGenerator_Concurrency(t *testing.T) {
	sg := NewSequenceGenerator(0)
	var wg sync.WaitGroup
	count := 1000

	wg.Add(count)
	for i := 0; i < count; i++ {
		go func() {
			defer wg.Done()
			sg.Next()
		}()
	}
	wg.Wait()

	if got := sg.Current(); got != uint64(count) {
		t.Errorf("expected %d, got %d", count, got)
	}
}

func TestEmitTransition_Valid(t *testing.T) {
	ctx := context.Background()
	clockTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return clockTime }

	node1, _ := engine.NewNode("n1", "passthrough", nil)
	graph, _ := engine.NewGraph("g1", []*engine.Node{node1}, nil)

	execID := engine.ExecutionID("exec-1")

	newStatusPtr := func(s engine.Status) *engine.Status { return &s }
	errMsgPtr := func(s string) *string { return &s }

	errRetry := engine.NewRetryableError(errors.New("retry me"))
	errCritical := engine.NewCriticalError(errors.New("critical err"))

	tests := []struct {
		name        string
		oldStatus   engine.Status
		newStatus   engine.Status
		errMsg      *string
		cause       error
		wantType    engine.EventType
		wantStruct  reflect.Type
		wantPayload any
	}{
		{
			name:       "Pending -> Ready",
			oldStatus:  engine.StatusPending,
			newStatus:  engine.StatusReady,
			wantType:   engine.EventTypeNodeStateChanged,
			wantStruct: reflect.TypeOf(engine.NodeStateChangedPayload{}),
			wantPayload: engine.NodeStateChangedPayload{
				OldStatus: engine.StatusPending,
				NewStatus: engine.StatusReady,
				Version:   0,
			},
		},
		{
			name:       "Pending -> Skipped",
			oldStatus:  engine.StatusPending,
			newStatus:  engine.StatusSkipped,
			wantType:   engine.EventTypeNodeStateChanged,
			wantStruct: reflect.TypeOf(engine.NodeStateChangedPayload{}),
			wantPayload: engine.NodeStateChangedPayload{
				OldStatus: engine.StatusPending,
				NewStatus: engine.StatusSkipped,
				Version:   0,
			},
		},
		{
			name:       "Ready -> Running",
			oldStatus:  engine.StatusReady,
			newStatus:  engine.StatusRunning,
			wantType:   engine.EventTypeNodeStateChanged,
			wantStruct: reflect.TypeOf(engine.NodeStateChangedPayload{}),
			wantPayload: engine.NodeStateChangedPayload{
				OldStatus: engine.StatusReady,
				NewStatus: engine.StatusRunning,
				Version:   0,
			},
		},
		{
			name:       "Ready -> Skipped",
			oldStatus:  engine.StatusReady,
			newStatus:  engine.StatusSkipped,
			wantType:   engine.EventTypeNodeStateChanged,
			wantStruct: reflect.TypeOf(engine.NodeStateChangedPayload{}),
			wantPayload: engine.NodeStateChangedPayload{
				OldStatus: engine.StatusReady,
				NewStatus: engine.StatusSkipped,
				Version:   0,
			},
		},
		{
			name:       "Running -> Succeeded",
			oldStatus:  engine.StatusRunning,
			newStatus:  engine.StatusSucceeded,
			wantType:   engine.EventTypeNodeCompleted,
			wantStruct: reflect.TypeOf(engine.NodeCompletedPayload{}),
			wantPayload: engine.NodeCompletedPayload{
				Status:  engine.StatusSucceeded,
				Output:  map[string]any{},
				Version: 0,
			},
		},
		{
			name:       "Running -> Failed (Retryable)",
			oldStatus:  engine.StatusRunning,
			newStatus:  engine.StatusFailed,
			errMsg:     errMsgPtr("retry me"),
			cause:      errRetry,
			wantType:   engine.EventTypeNodeFailed,
			wantStruct: reflect.TypeOf(engine.NodeFailedPayload{}),
			wantPayload: engine.NodeFailedPayload{
				Status:       engine.StatusFailed,
				ErrorClass:   engine.ClassRetryable,
				ErrorMessage: "retry me",
				Version:      0,
			},
		},
		{
			name:       "Running -> Failed (Critical)",
			oldStatus:  engine.StatusRunning,
			newStatus:  engine.StatusFailed,
			errMsg:     errMsgPtr("critical err"),
			cause:      errCritical,
			wantType:   engine.EventTypeNodeFailed,
			wantStruct: reflect.TypeOf(engine.NodeFailedPayload{}),
			wantPayload: engine.NodeFailedPayload{
				Status:       engine.StatusFailed,
				ErrorClass:   engine.ClassCritical,
				ErrorMessage: "critical err",
				Version:      0,
			},
		},
		{
			name:       "Running -> Failed (Default)",
			oldStatus:  engine.StatusRunning,
			newStatus:  engine.StatusFailed,
			errMsg:     errMsgPtr("std err"),
			cause:      errors.New("std err"),
			wantType:   engine.EventTypeNodeFailed,
			wantStruct: reflect.TypeOf(engine.NodeFailedPayload{}),
			wantPayload: engine.NodeFailedPayload{
				Status:       engine.StatusFailed,
				ErrorClass:   engine.ClassNonRetryable,
				ErrorMessage: "std err",
				Version:      0,
			},
		},
		{
			name:       "Succeeded -> Compensating",
			oldStatus:  engine.StatusSucceeded,
			newStatus:  engine.StatusCompensating,
			wantType:   engine.EventTypeNodeStateChanged,
			wantStruct: reflect.TypeOf(engine.NodeStateChangedPayload{}),
			wantPayload: engine.NodeStateChangedPayload{
				OldStatus: engine.StatusSucceeded,
				NewStatus: engine.StatusCompensating,
				Version:   0,
			},
		},
		{
			name:       "Failed -> Compensating",
			oldStatus:  engine.StatusFailed,
			newStatus:  engine.StatusCompensating,
			wantType:   engine.EventTypeNodeStateChanged,
			wantStruct: reflect.TypeOf(engine.NodeStateChangedPayload{}),
			wantPayload: engine.NodeStateChangedPayload{
				OldStatus: engine.StatusFailed,
				NewStatus: engine.StatusCompensating,
				Version:   0,
			},
		},
		{
			name:       "Compensating -> Compensated",
			oldStatus:  engine.StatusCompensating,
			newStatus:  engine.StatusCompensated,
			wantType:   engine.EventTypeNodeStateChanged,
			wantStruct: reflect.TypeOf(engine.NodeStateChangedPayload{}),
			wantPayload: engine.NodeStateChangedPayload{
				OldStatus: engine.StatusCompensating,
				NewStatus: engine.StatusCompensated,
				Version:   0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &mockSink{}
			seqGen := NewSequenceGenerator(0)
			emitter := NewTransitionEmitter(execID, seqGen, clock, sink)

			gs, _ := engine.NewGraphState(execID, graph)

			currGS := gs
			var err error

			if tt.oldStatus != engine.StatusPending {
				path := []engine.Status{}
				switch tt.oldStatus {
				case engine.StatusReady:
					path = []engine.Status{engine.StatusReady}
				case engine.StatusSkipped:
					path = []engine.Status{engine.StatusSkipped}
				case engine.StatusRunning:
					path = []engine.Status{engine.StatusReady, engine.StatusRunning}
				case engine.StatusSucceeded:
					path = []engine.Status{engine.StatusReady, engine.StatusRunning, engine.StatusSucceeded}
				case engine.StatusFailed:
					path = []engine.Status{engine.StatusReady, engine.StatusRunning, engine.StatusFailed}
				case engine.StatusCompensating:
					path = []engine.Status{engine.StatusReady, engine.StatusRunning, engine.StatusSucceeded, engine.StatusCompensating}
				case engine.StatusCompensated:
					path = []engine.Status{engine.StatusReady, engine.StatusRunning, engine.StatusSucceeded, engine.StatusCompensating, engine.StatusCompensated}
				}

				for _, st := range path {
					mut := engine.NewStateMutation("n1", currGS.Version(), newStatusPtr(st), nil, nil, nil)
					currGS, err = currGS.Apply(mut)
					if err != nil {
						t.Fatalf("failed to setup old state: %v", err)
					}
				}
			}
			oldState := currGS.NodeState("n1")

			mut := engine.NewStateMutation("n1", currGS.Version(), newStatusPtr(tt.newStatus), nil, nil, tt.errMsg)
			newGS, err := currGS.Apply(mut)
			if err != nil {
				t.Fatalf("failed to apply mutation: %v", err)
			}
			newState := newGS.NodeState("n1")

			ev, err := emitter.EmitTransition(ctx, oldState, newState, mut, tt.cause)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if ev == nil {
				t.Fatal("expected an event, got nil")
			}

			if ev.Type() != tt.wantType {
				t.Errorf("expected EventType %v, got %v", tt.wantType, ev.Type())
			}

			if ev.Sequence() != 1 {
				t.Errorf("expected sequence 1, got %v", ev.Sequence())
			}

			if !ev.Timestamp().Equal(clockTime) {
				t.Errorf("expected time %v, got %v", clockTime, ev.Timestamp())
			}

			if reflect.TypeOf(ev.Payload()) != tt.wantStruct {
				t.Errorf("expected payload type %v, got %v", tt.wantStruct, reflect.TypeOf(ev.Payload()))
			}

			// Dynamically set expected version since version depends on setup path
			switch p := tt.wantPayload.(type) {
			case engine.NodeStateChangedPayload:
				p.Version = newState.Version()
				tt.wantPayload = p
			case engine.NodeCompletedPayload:
				p.Version = newState.Version()
				tt.wantPayload = p
			case engine.NodeFailedPayload:
				p.Version = newState.Version()
				tt.wantPayload = p
			}

			if !reflect.DeepEqual(ev.Payload(), tt.wantPayload) {
				t.Errorf("expected payload %v, got %v", tt.wantPayload, ev.Payload())
			}

			if len(sink.events) != 1 {
				t.Errorf("expected 1 event in sink, got %d", len(sink.events))
			}

			if seqGen.Current() != 1 {
				t.Errorf("expected seqGen.Current to be 1, got %d", seqGen.Current())
			}
		})
	}
}

func TestEmitTransition_Invalid(t *testing.T) {
	ctx := context.Background()
	clock := func() time.Time { return time.Now() }
	execID := engine.ExecutionID("exec-1")

	node1, _ := engine.NewNode("n1", "passthrough", nil)
	graph, _ := engine.NewGraph("g1", []*engine.Node{node1}, nil)
	gs, _ := engine.NewGraphState(execID, graph)

	sink := &mockSink{}
	seqGen := NewSequenceGenerator(0)
	emitter := NewTransitionEmitter(execID, seqGen, clock, sink)

	oldState := gs.NodeState("n1")

	gsSucceeded, _ := engine.NewGraphState(execID, graph)
	mutReady := engine.NewStateMutation("n1", gsSucceeded.Version(), newStatusPtr(engine.StatusReady), nil, nil, nil)
	gsSucceeded, _ = gsSucceeded.Apply(mutReady)
	mutRunning := engine.NewStateMutation("n1", gsSucceeded.Version(), newStatusPtr(engine.StatusRunning), nil, nil, nil)
	gsSucceeded, _ = gsSucceeded.Apply(mutRunning)
	mutSucceeded := engine.NewStateMutation("n1", gsSucceeded.Version(), newStatusPtr(engine.StatusSucceeded), nil, nil, nil)
	gsSucceeded, _ = gsSucceeded.Apply(mutSucceeded)

	invalidNewState := gsSucceeded.NodeState("n1")
	invalidMut := engine.NewStateMutation("n1", 0, newStatusPtr(engine.StatusSucceeded), nil, nil, nil)

	ev, err := emitter.EmitTransition(ctx, oldState, invalidNewState, invalidMut)
	if err == nil {
		t.Fatal("expected error for invalid transition")
	}
	if !errors.Is(err, engine.ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}

	if ev != nil {
		t.Errorf("expected nil event")
	}

	if seqGen.Current() != 0 {
		t.Errorf("expected seqGen not to advance, got %d", seqGen.Current())
	}
}

func TestEmitTransition_NoStatusMutation(t *testing.T) {
	ctx := context.Background()
	clock := func() time.Time { return time.Now() }
	execID := engine.ExecutionID("exec-1")

	node1, _ := engine.NewNode("n1", "passthrough", nil)
	graph, _ := engine.NewGraph("g1", []*engine.Node{node1}, nil)
	gs, _ := engine.NewGraphState(execID, graph)

	seqGen := NewSequenceGenerator(0)
	emitter := NewTransitionEmitter(execID, seqGen, clock, nil)

	oldState := gs.NodeState("n1")
	mut := engine.NewStateMutation("n1", gs.Version(), nil, map[string]any{"k": "v"}, nil, nil)
	newGS, _ := gs.Apply(mut)
	newState := newGS.NodeState("n1")

	ev, err := emitter.EmitTransition(ctx, oldState, newState, mut)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev != nil {
		t.Errorf("expected nil event for nil status mutation")
	}

	if seqGen.Current() != 0 {
		t.Errorf("expected seqGen not to advance")
	}
}

func TestEmitTransition_SinkError(t *testing.T) {
	ctx := context.Background()
	clock := func() time.Time { return time.Now() }
	execID := engine.ExecutionID("exec-1")

	node1, _ := engine.NewNode("n1", "passthrough", nil)
	graph, _ := engine.NewGraph("g1", []*engine.Node{node1}, nil)
	gs, _ := engine.NewGraphState(execID, graph)

	sinkErr := errors.New("sink failure")
	sink := &mockSink{err: sinkErr}
	seqGen := NewSequenceGenerator(0)
	emitter := NewTransitionEmitter(execID, seqGen, clock, sink)

	oldState := gs.NodeState("n1")
	mut := engine.NewStateMutation("n1", gs.Version(), newStatusPtr(engine.StatusReady), nil, nil, nil)
	newGS, _ := gs.Apply(mut)
	newState := newGS.NodeState("n1")

	_, err := emitter.EmitTransition(ctx, oldState, newState, mut)
	if err == nil || !errors.Is(err, sinkErr) {
		t.Errorf("expected sink error, got %v", err)
	}
}

func TestEmitTransition_WithPolicyResolver(t *testing.T) {
	ctx := context.Background()
	execID := engine.ExecutionID("exec-policy-1")
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	node1, _ := engine.NewNode("n1", "passthrough", nil)
	graph, _ := engine.NewGraph("g1", []*engine.Node{node1}, nil)
	gs, _ := engine.NewGraphState(execID, graph)

	emitter := NewTransitionEmitter(execID, NewSequenceGenerator(0), clock, nil)
	emitter.SetPolicyResolver(func(nodeID engine.NodeID) (string, string) {
		if nodeID == "n1" {
			return "policy-xyz", "v2.0"
		}
		return "", ""
	})

	// Transition n1 from Pending -> Ready -> Running -> Succeeded
	mReady := engine.NewStateMutation("n1", gs.Version(), newStatusPtr(engine.StatusReady), nil, nil, nil)
	gs, _ = gs.Apply(mReady)

	mRunning := engine.NewStateMutation("n1", gs.Version(), newStatusPtr(engine.StatusRunning), nil, nil, nil)
	gsRunning, _ := gs.Apply(mRunning)
	_, _ = emitter.EmitTransition(ctx, gs.NodeState("n1"), gsRunning.NodeState("n1"), mRunning)

	mSucc := engine.NewStateMutation("n1", gsRunning.Version(), newStatusPtr(engine.StatusSucceeded), map[string]any{"res": 1}, nil, nil)
	gsSucc, _ := gsRunning.Apply(mSucc)

	ev, err := emitter.EmitTransition(ctx, gsRunning.NodeState("n1"), gsSucc.NodeState("n1"), mSucc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type() != engine.EventTypeNodeCompleted {
		t.Fatalf("expected node_completed, got %v", ev.Type())
	}
	payload, ok := ev.Payload().(engine.NodeCompletedPayload)
	if !ok {
		t.Fatalf("expected NodeCompletedPayload, got %T", ev.Payload())
	}
	if payload.PolicyID != "policy-xyz" {
		t.Errorf("expected PolicyID 'policy-xyz', got %q", payload.PolicyID)
	}
	if payload.PolicyVersion != "v2.0" {
		t.Errorf("expected PolicyVersion 'v2.0', got %q", payload.PolicyVersion)
	}
}

func newStatusPtr(s engine.Status) *engine.Status { return &s }
