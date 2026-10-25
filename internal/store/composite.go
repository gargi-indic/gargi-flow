// Package store provides adapters, factories, and composite stores for state persistence across backends.
package store

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// CompositeStore represents a composed storage engine combining multiple capability adapters.
// It always implements engine.StateStore and io.Closer, and optionally implements
// engine.LockManager, engine.EventPublisher, and engine.ExecutionLog depending on configuration.
type CompositeStore interface {
	engine.StateStore
	engine.PolicyStore
	io.Closer

	// State returns the underlying state persistence adapter.
	State() engine.StateStore

	// Lock returns the underlying distributed lock manager, or nil if not configured.
	Lock() engine.LockManager

	// Events returns the underlying event publisher, or nil if not configured.
	Events() engine.EventPublisher

	// Log returns the underlying execution audit log, or nil if not configured.
	Log() engine.ExecutionLog

	// Experiments returns the simulation experiment store; it is never nil
	// (an in-memory store when none is configured).
	Experiments() sim.ExperimentStore

	// Policies returns the policy store; it is never nil
	// (an in-memory store when none is configured).
	Policies() engine.PolicyStore
}

// compositeBase holds common references and implements the getter methods and Closer.
type compositeBase struct {
	state   engine.StateStore
	lock    engine.LockManager
	events  engine.EventPublisher
	log     engine.ExecutionLog
	exps    sim.ExperimentStore
	policy  engine.PolicyStore
	closers []func() error
	closed  atomic.Bool
}

func (b *compositeBase) State() engine.StateStore      { return b.state }
func (b *compositeBase) Lock() engine.LockManager      { return b.lock }
func (b *compositeBase) Events() engine.EventPublisher { return b.events }
func (b *compositeBase) Log() engine.ExecutionLog      { return b.log }

func (b *compositeBase) Experiments() sim.ExperimentStore { return b.exps }
func (b *compositeBase) Policies() engine.PolicyStore     { return b.policy }

func (b *compositeBase) SavePolicy(ctx context.Context, record *engine.PolicyRecord) (*engine.PolicyRecord, error) {
	return b.policy.SavePolicy(ctx, record)
}

func (b *compositeBase) GetPolicy(ctx context.Context, graphID string, policyID string, version string) (*engine.PolicyRecord, error) {
	return b.policy.GetPolicy(ctx, graphID, policyID, version)
}

func (b *compositeBase) ListPolicies(ctx context.Context, graphID string) ([]*engine.PolicyRecord, error) {
	return b.policy.ListPolicies(ctx, graphID)
}

func (b *compositeBase) SetDeployment(ctx context.Context, deployment *engine.PolicyDeployment) error {
	return b.policy.SetDeployment(ctx, deployment)
}

func (b *compositeBase) GetDeployment(ctx context.Context, graphID string) (*engine.PolicyDeployment, error) {
	return b.policy.GetDeployment(ctx, graphID)
}

func (b *compositeBase) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	var errs []error
	for _, c := range b.closers {
		if c != nil {
			if err := c(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Concrete composite types to selectively expose only configured capability interfaces.

type compNone struct {
	*compositeBase
	engine.StateStore
}

type compLock struct {
	*compositeBase
	engine.StateStore
	engine.LockManager
}

type compEvents struct {
	*compositeBase
	engine.StateStore
	engine.EventPublisher
}

type compLog struct {
	*compositeBase
	engine.StateStore
	engine.ExecutionLog
}

type compLockEvents struct {
	*compositeBase
	engine.StateStore
	engine.LockManager
	engine.EventPublisher
}

type compLockLog struct {
	*compositeBase
	engine.StateStore
	engine.LockManager
	engine.ExecutionLog
}

type compEventsLog struct {
	*compositeBase
	engine.StateStore
	engine.EventPublisher
	engine.ExecutionLog
}

type compAll struct {
	*compositeBase
	engine.StateStore
	engine.LockManager
	engine.EventPublisher
	engine.ExecutionLog
}

// NewComposite creates a new CompositeStore instance wiring together the provided capability implementations.
// If state is nil, NewComposite returns an error.
// It selectively exposes only the interfaces corresponding to non-nil capability adapters,
// ensuring type assertions (e.g. store.(engine.LockManager)) accurately reflect availability.
// The experiment store is a fresh in-memory one; use Builder.WithExperimentStore to inject another.
func NewComposite(
	state engine.StateStore,
	lock engine.LockManager,
	events engine.EventPublisher,
	log engine.ExecutionLog,
	closers ...func() error,
) (CompositeStore, error) {
	return newComposite(state, lock, events, log, nil, nil, closers...)
}

// newComposite is NewComposite with experiment and policy stores; nil selects
// fresh in-memory ones.
func newComposite(
	state engine.StateStore,
	lock engine.LockManager,
	events engine.EventPublisher,
	log engine.ExecutionLog,
	exps sim.ExperimentStore,
	policy engine.PolicyStore,
	closers ...func() error,
) (CompositeStore, error) {
	if state == nil {
		return nil, errors.New("composite store: state store is required")
	}
	if exps == nil {
		exps = memory.NewExperimentStore()
	}
	if policy == nil {
		policy = memory.NewPolicyStore()
	}

	base := &compositeBase{
		state:   state,
		lock:    lock,
		events:  events,
		log:     log,
		exps:    exps,
		policy:  policy,
		closers: closers,
	}

	hasLock := lock != nil
	hasEvents := events != nil
	hasLog := log != nil

	switch {
	case !hasLock && !hasEvents && !hasLog:
		return &compNone{compositeBase: base, StateStore: state}, nil
	case hasLock && !hasEvents && !hasLog:
		return &compLock{compositeBase: base, StateStore: state, LockManager: lock}, nil
	case !hasLock && hasEvents && !hasLog:
		return &compEvents{compositeBase: base, StateStore: state, EventPublisher: events}, nil
	case !hasLock && !hasEvents && hasLog:
		return &compLog{compositeBase: base, StateStore: state, ExecutionLog: log}, nil
	case hasLock && hasEvents && !hasLog:
		return &compLockEvents{compositeBase: base, StateStore: state, LockManager: lock, EventPublisher: events}, nil
	case hasLock && !hasEvents && hasLog:
		return &compLockLog{compositeBase: base, StateStore: state, LockManager: lock, ExecutionLog: log}, nil
	case !hasLock && hasEvents && hasLog:
		return &compEventsLog{compositeBase: base, StateStore: state, EventPublisher: events, ExecutionLog: log}, nil
	default:
		return &compAll{compositeBase: base, StateStore: state, LockManager: lock, EventPublisher: events, ExecutionLog: log}, nil
	}
}

// Helper to wrap a closer returning error into a func() error.
func wrapCloser(c io.Closer) func() error {
	if c == nil {
		return nil
	}
	return c.Close
}

// Helper to wrap a void Closer (like Postgres) into func() error.
type voidCloser interface {
	Close()
}

func wrapVoidCloser(c voidCloser) func() error {
	if c == nil {
		return nil
	}
	return func() error {
		c.Close()
		return nil
	}
}

// Helper to wrap a context Closer (like Neo4j) into func() error.
type ctxCloser interface {
	Close(ctx context.Context) error
}

func wrapCtxCloser(c ctxCloser) func() error {
	if c == nil {
		return nil
	}
	return func() error {
		return c.Close(context.Background())
	}
}
