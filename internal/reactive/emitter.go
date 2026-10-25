package reactive

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

// Clock is a deterministic function returning the current logical timestamp.
type Clock func() time.Time

// SequenceGenerator manages monotonic per-execution event sequence numbers.
type SequenceGenerator struct {
	mu  sync.Mutex
	seq uint64
}

// NewSequenceGenerator creates a SequenceGenerator starting from initialSeq.
func NewSequenceGenerator(initialSeq uint64) *SequenceGenerator {
	return &SequenceGenerator{
		seq: initialSeq,
	}
}

// Next increments and returns the next sequence number.
func (s *SequenceGenerator) Next() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// Current returns the current sequence number without incrementing.
func (s *SequenceGenerator) Current() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// TransitionEmitter converts engine state transitions into ordered events.
type TransitionEmitter struct {
	execID         engine.ExecutionID
	clock          Clock
	seqGen         *SequenceGenerator
	sink           engine.EventSink
	policyResolver func(nodeID engine.NodeID) (string, string)
}

// SetPolicyResolver registers a resolver function that provides policy metadata for completed nodes.
func (e *TransitionEmitter) SetPolicyResolver(resolver func(nodeID engine.NodeID) (string, string)) {
	e.policyResolver = resolver
}

// NewTransitionEmitter creates a new TransitionEmitter.
// If seqGen is nil, it initializes NewSequenceGenerator(0).
func NewTransitionEmitter(
	execID engine.ExecutionID,
	seqGen *SequenceGenerator,
	clock Clock,
	sink engine.EventSink,
) *TransitionEmitter {
	if seqGen == nil {
		seqGen = NewSequenceGenerator(0)
	}
	return &TransitionEmitter{
		execID: execID,
		clock:  clock,
		seqGen: seqGen,
		sink:   sink,
	}
}

// EmitTransition evaluates a node state change and dispatches the corresponding event.
// Every valid status transition produces exactly one event.
func (e *TransitionEmitter) EmitTransition(
	ctx context.Context,
	oldState *engine.NodeState,
	newState *engine.NodeState,
	mutation engine.StateMutation,
	cause ...error,
) (*engine.Event, error) {
	if e == nil {
		return nil, fmt.Errorf("emitter is nil")
	}
	if e.execID == "" {
		return nil, fmt.Errorf("execution ID is empty")
	}
	if e.clock == nil {
		return nil, fmt.Errorf("clock is nil")
	}
	if oldState == nil {
		return nil, fmt.Errorf("oldState is nil")
	}
	if newState == nil {
		return nil, fmt.Errorf("newState is nil")
	}
	if oldState.NodeID() == "" {
		return nil, fmt.Errorf("oldState has empty NodeID")
	}
	if oldState.NodeID() != newState.NodeID() {
		return nil, fmt.Errorf("node ID mismatch: %s != %s", oldState.NodeID(), newState.NodeID())
	}

	if mutation.Status == nil {
		return nil, nil // No-op on non-status mutations
	}

	if *mutation.Status != newState.Status() {
		return nil, fmt.Errorf("mutation status %s does not match newState status %s", *mutation.Status, newState.Status())
	}

	if !engine.CanTransition(oldState.Status(), newState.Status()) {
		return nil, fmt.Errorf("%w: cannot transition from %s to %s", engine.ErrInvalidTransition, oldState.Status(), newState.Status())
	}

	var eventType engine.EventType
	var payload any

	oldStatus := oldState.Status()
	newStatus := newState.Status()

	if oldStatus == engine.StatusRunning && newStatus == engine.StatusSucceeded {
		eventType = engine.EventTypeNodeCompleted
		var polID, polVer string
		if e.policyResolver != nil {
			polID, polVer = e.policyResolver(newState.NodeID())
		}
		payload = engine.NodeCompletedPayload{
			Status:        engine.StatusSucceeded,
			Output:        newState.Output(),
			Version:       newState.Version(),
			PolicyID:      polID,
			PolicyVersion: polVer,
		}
	} else if oldStatus == engine.StatusRunning && newStatus == engine.StatusFailed {
		eventType = engine.EventTypeNodeFailed
		errClass := engine.ClassNonRetryable
		var errMsg string
		if len(cause) > 0 && cause[0] != nil {
			errClass = engine.ClassOf(cause[0])
		}
		errMsg = newState.ErrorMessage()
		payload = engine.NodeFailedPayload{
			Status:       engine.StatusFailed,
			ErrorClass:   errClass,
			ErrorMessage: errMsg,
			Version:      newState.Version(),
		}
	} else {
		eventType = engine.EventTypeNodeStateChanged
		payload = engine.NodeStateChangedPayload{
			OldStatus:    oldStatus,
			NewStatus:    newStatus,
			OutputPatch:  mutation.Set,
			ErrorMessage: newState.ErrorMessage(),
			Version:      newState.Version(),
		}
	}

	seq := e.seqGen.Next()
	ev, err := engine.NewEvent(e.execID, seq, e.clock(), eventType, newState.NodeID(), payload)
	if err != nil {
		return nil, fmt.Errorf("failed to create event: %w", err)
	}

	if e.sink != nil {
		if err := e.sink.Emit(ctx, ev); err != nil {
			return nil, fmt.Errorf("failed to emit event: %w", err)
		}
	}

	return ev, nil
}
