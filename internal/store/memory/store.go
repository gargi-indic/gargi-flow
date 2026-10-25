package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

var (
	_ engine.StateStore     = (*Store)(nil)
	_ engine.LockManager    = (*Store)(nil)
	_ engine.EventPublisher = (*Store)(nil)
	_ engine.ExecutionLog   = (*Store)(nil)
	_ engine.PolicyStore    = (*Store)(nil)
)

// DefaultSubscriptionBufferSize is the default channel buffer capacity for event subscribers.
const DefaultSubscriptionBufferSize = 64

// lockEntry tracks the state and expiration of a leased distributed lock.
type lockEntry struct {
	token     string
	expiresAt time.Time
}

// subscriber represents an active in-process event subscription.
type subscriber struct {
	id          uint64
	executionID engine.ExecutionID
	ch          chan *engine.Event
	cancel      context.CancelFunc
	dropped     atomic.Uint64
}

// Store is an in-memory, thread-safe implementation of engine.StateStore,
// engine.LockManager, engine.EventPublisher, and engine.ExecutionLog.
type stateRecord struct {
	state     *engine.GraphState
	createdAt time.Time
	updatedAt time.Time
}

// Store is an in-memory, thread-safe implementation of engine.StateStore,
// engine.LockManager, engine.EventPublisher, engine.ExecutionLog, and engine.PolicyStore.
type Store struct {
	*PolicyStore

	stateMu sync.RWMutex
	states  map[engine.ExecutionID]*stateRecord

	lockMu sync.Mutex
	locks  map[string]*lockEntry

	pubsubMu      sync.RWMutex
	subscribers   map[uint64]*subscriber
	nextSubID     uint64
	droppedEvents atomic.Uint64
	subBufferSize int

	logMu sync.RWMutex
	logs  map[engine.ExecutionID][]*engine.ExecutionLogEntry

	closed atomic.Bool
}

// Option configures a Store instance.
type Option func(*Store)

// WithSubscriptionBufferSize sets the buffer size for newly created subscriber channels.
// If size is less than or equal to 0, DefaultSubscriptionBufferSize is used.
func WithSubscriptionBufferSize(size int) Option {
	return func(s *Store) {
		if size > 0 {
			s.subBufferSize = size
		}
	}
}

// New creates a new in-memory Store with optional configuration options.
func New(opts ...Option) *Store {
	s := &Store{
		PolicyStore:   NewPolicyStore(),
		states:        make(map[engine.ExecutionID]*stateRecord),
		locks:         make(map[string]*lockEntry),
		subscribers:   make(map[uint64]*subscriber),
		logs:          make(map[engine.ExecutionID][]*engine.ExecutionLogEntry),
		subBufferSize: DefaultSubscriptionBufferSize,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// NewStore is an alias for New, constructing a new in-memory Store.
func NewStore(opts ...Option) *Store {
	return New(opts...)
}

// GetState retrieves the current GraphState for the given executionID.
// It returns a StoreNotFoundError if no state exists for executionID.
func (s *Store) GetState(ctx context.Context, executionID engine.ExecutionID) (*engine.GraphState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if executionID == "" {
		return nil, errors.New("store: execution ID cannot be empty")
	}
	if s.closed.Load() {
		return nil, &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}

	s.stateMu.RLock()
	st, exists := s.states[executionID]
	s.stateMu.RUnlock()

	if !exists {
		return nil, &engine.StoreNotFoundError{
			ExecutionID: executionID,
			Entity:      "GraphState",
			Err:         engine.ErrStoreNotFound,
		}
	}

	return cloneGraphState(st.state)
}

// SetState stores or updates the entire GraphState for an execution enforcing CAS concurrency.
func (s *Store) SetState(ctx context.Context, executionID engine.ExecutionID, state *engine.GraphState, expectedVersion uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed.Load() {
		return &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}
	if executionID == "" {
		return errors.New("store: execution ID cannot be empty")
	}
	if state == nil {
		return errors.New("store: state cannot be nil")
	}

	cloned, err := cloneGraphState(state)
	if err != nil {
		return err
	}

	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	current, exists := s.states[executionID]
	if !exists {
		if expectedVersion != 0 {
			return &engine.StoreConflictError{
				ExecutionID:     executionID,
				ExpectedVersion: expectedVersion,
				ActualVersion:   0,
				Err:             engine.ErrStoreConflict,
			}
		}
		s.states[executionID] = &stateRecord{
			state:     cloned,
			createdAt: time.Now().UTC(),
			updatedAt: time.Now().UTC(),
		}
	} else {
		if current.state.Version() != expectedVersion {
			return &engine.StoreConflictError{
				ExecutionID:     executionID,
				ExpectedVersion: expectedVersion,
				ActualVersion:   current.state.Version(),
				Err:             engine.ErrStoreConflict,
			}
		}
		s.states[executionID] = &stateRecord{
			state:     cloned,
			createdAt: current.createdAt,
			updatedAt: time.Now().UTC(),
		}
	}

	return nil
}

// PatchState atomically applies a StateMutation to the GraphState for executionID with CAS validation.
func (s *Store) PatchState(ctx context.Context, executionID engine.ExecutionID, mutation *engine.StateMutation, expectedVersion uint64) (*engine.GraphState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed.Load() {
		return nil, &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}
	if executionID == "" {
		return nil, errors.New("store: execution ID cannot be empty")
	}
	if mutation == nil {
		return nil, errors.New("store: mutation cannot be nil")
	}

	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	current, exists := s.states[executionID]
	if !exists {
		return nil, &engine.StoreNotFoundError{
			ExecutionID: executionID,
			Entity:      "GraphState",
			Err:         engine.ErrStoreNotFound,
		}
	}

	if current.state.Version() != expectedVersion {
		return nil, &engine.StoreConflictError{
			ExecutionID:     executionID,
			ExpectedVersion: expectedVersion,
			ActualVersion:   current.state.Version(),
			Err:             engine.ErrStoreConflict,
		}
	}

	newState, err := current.state.Apply(*mutation)
	if err != nil {
		return nil, err
	}

	cloned, err := cloneGraphState(newState)
	if err != nil {
		return nil, err
	}

	s.states[executionID] = &stateRecord{
		state:     cloned,
		createdAt: current.createdAt,
		updatedAt: time.Now().UTC(),
	}
	return cloneGraphState(cloned)
}

// AcquireLock attempts to acquire an in-memory lock on the given resource with the provided TTL.
func (s *Store) AcquireLock(ctx context.Context, resource string, ttl time.Duration) (*engine.LockHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resource == "" {
		return nil, errors.New("store: resource cannot be empty")
	}
	if ttl <= 0 {
		return nil, errors.New("store: ttl must be greater than zero")
	}
	if s.closed.Load() {
		return nil, &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}

	s.lockMu.Lock()
	defer s.lockMu.Unlock()

	now := time.Now()
	existing, exists := s.locks[resource]
	if exists && now.Before(existing.expiresAt) {
		return nil, fmt.Errorf("resource %q is already locked", resource)
	}

	token := generateLockToken()
	handle := &engine.LockHandle{
		Resource:      resource,
		Token:         token,
		LeaseDuration: ttl,
		AcquiredAt:    now,
	}
	s.locks[resource] = &lockEntry{
		token:     token,
		expiresAt: now.Add(ttl),
	}
	return handle, nil
}

// ReleaseLock releases the lock associated with the given handle.
func (s *Store) ReleaseLock(ctx context.Context, handle *engine.LockHandle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if handle == nil {
		return errors.New("store: lock handle cannot be nil")
	}
	if handle.Resource == "" {
		return errors.New("store: resource cannot be empty")
	}
	if s.closed.Load() {
		return &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}

	s.lockMu.Lock()
	defer s.lockMu.Unlock()

	existing, exists := s.locks[handle.Resource]
	if !exists || existing.token != handle.Token {
		return errors.New("store: lock handle is invalid or not owner")
	}

	if time.Now().After(existing.expiresAt) {
		delete(s.locks, handle.Resource)
		return errors.New("store: lock handle has expired")
	}

	delete(s.locks, handle.Resource)
	return nil
}

// ExtendLock extends the lease duration of an existing held lock handle.
func (s *Store) ExtendLock(ctx context.Context, handle *engine.LockHandle, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if handle == nil {
		return errors.New("store: lock handle cannot be nil")
	}
	if ttl <= 0 {
		return errors.New("store: ttl must be greater than zero")
	}
	if s.closed.Load() {
		return &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}

	s.lockMu.Lock()
	defer s.lockMu.Unlock()

	existing, exists := s.locks[handle.Resource]
	if !exists || existing.token != handle.Token {
		return errors.New("store: lock handle is invalid or not owner")
	}

	now := time.Now()
	if now.After(existing.expiresAt) {
		delete(s.locks, handle.Resource)
		return errors.New("store: lock has expired and cannot be extended")
	}

	handle.LeaseDuration += ttl
	existing.expiresAt = existing.expiresAt.Add(ttl)
	return nil
}

// PublishEvent publishes a single reactive engine event to registered subscribers.
func (s *Store) PublishEvent(ctx context.Context, event *engine.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed.Load() {
		return &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}
	if event == nil {
		return errors.New("store: event cannot be nil")
	}

	s.pubsubMu.RLock()
	defer s.pubsubMu.RUnlock()

	s.dispatchLocked(event)
	return nil
}

// PublishBatch publishes multiple reactive engine events in sequential order.
func (s *Store) PublishBatch(ctx context.Context, events []*engine.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed.Load() {
		return &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}
	if len(events) == 0 {
		return nil
	}
	for i, e := range events {
		if e == nil {
			return fmt.Errorf("store: batch event at index %d cannot be nil", i)
		}
	}

	s.pubsubMu.RLock()
	defer s.pubsubMu.RUnlock()

	for _, e := range events {
		s.dispatchLocked(e)
	}
	return nil
}

// dispatchLocked fans out an event to matching subscribers. Caller must hold pubsubMu.RLock.
func (s *Store) dispatchLocked(event *engine.Event) {
	targetExec := event.ExecutionID()
	for _, sub := range s.subscribers {
		if sub.executionID == "" || sub.executionID == targetExec {
			select {
			case sub.ch <- event:
			default:
				s.droppedEvents.Add(1)
				sub.dropped.Add(1)
			}
		}
	}
}

// Subscribe subscribes to events for a specific executionID.
// If executionID is empty, it subscribes to all events across all executions.
// When ctx is canceled, the subscription is unregistered and the channel is closed.
func (s *Store) Subscribe(ctx context.Context, executionID engine.ExecutionID) (<-chan *engine.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed.Load() {
		return nil, &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}

	subCtx, cancel := context.WithCancel(ctx)
	ch := make(chan *engine.Event, s.subBufferSize)

	s.pubsubMu.Lock()
	if s.closed.Load() {
		s.pubsubMu.Unlock()
		cancel()
		close(ch)
		return nil, &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}
	subID := s.nextSubID
	s.nextSubID++
	sub := &subscriber{
		id:          subID,
		executionID: executionID,
		ch:          ch,
		cancel:      cancel,
	}
	s.subscribers[subID] = sub
	s.pubsubMu.Unlock()

	go func() {
		<-subCtx.Done()
		s.removeSubscriber(subID)
	}()

	return ch, nil
}

// SubscribeAll subscribes to events across all execution IDs.
func (s *Store) SubscribeAll(ctx context.Context) (<-chan *engine.Event, error) {
	return s.Subscribe(ctx, "")
}

// removeSubscriber unregisters and closes a subscriber channel safely.
func (s *Store) removeSubscriber(id uint64) {
	s.pubsubMu.Lock()
	defer s.pubsubMu.Unlock()

	sub, exists := s.subscribers[id]
	if !exists {
		return
	}
	delete(s.subscribers, id)
	sub.cancel()
	close(sub.ch)
}

// AppendLog appends an audit log entry for the specified execution.
func (s *Store) AppendLog(ctx context.Context, entry *engine.ExecutionLogEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry == nil {
		return errors.New("store: log entry cannot be nil")
	}
	if entry.ExecutionID == "" {
		return errors.New("store: execution ID cannot be empty")
	}
	if s.closed.Load() {
		return &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}

	cloned := cloneLogEntry(entry)

	s.logMu.Lock()
	defer s.logMu.Unlock()

	s.logs[entry.ExecutionID] = append(s.logs[entry.ExecutionID], cloned)
	return nil
}

// GetLogs queries audit log entries for an execution starting from fromSequence up to limit entries.
func (s *Store) GetLogs(ctx context.Context, executionID engine.ExecutionID, fromSequence uint64, limit int) ([]*engine.ExecutionLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if executionID == "" {
		return nil, errors.New("store: execution ID cannot be empty")
	}
	if s.closed.Load() {
		return nil, &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}

	s.logMu.RLock()
	defer s.logMu.RUnlock()

	entries, exists := s.logs[executionID]
	if !exists || len(entries) == 0 {
		return []*engine.ExecutionLogEntry{}, nil
	}

	res := make([]*engine.ExecutionLogEntry, 0, len(entries))
	for _, e := range entries {
		if e.Sequence >= fromSequence {
			res = append(res, cloneLogEntry(e))
			if limit > 0 && len(res) >= limit {
				break
			}
		}
	}
	return res, nil
}

// Reset clears all stored states, locks, subscriber channels, and logs.
func (s *Store) Reset() {
	s.stateMu.Lock()
	s.states = make(map[engine.ExecutionID]*stateRecord)
	s.stateMu.Unlock()

	s.lockMu.Lock()
	s.locks = make(map[string]*lockEntry)
	s.lockMu.Unlock()

	s.pubsubMu.Lock()
	for id, sub := range s.subscribers {
		delete(s.subscribers, id)
		sub.cancel()
		close(sub.ch)
	}
	s.droppedEvents.Store(0)
	s.pubsubMu.Unlock()

	s.logMu.Lock()
	s.logs = make(map[engine.ExecutionID][]*engine.ExecutionLogEntry)
	s.logMu.Unlock()

	s.PolicyStore = NewPolicyStore()
}

// Stats holds operational statistics of the in-memory store.
type Stats struct {
	StateCount        int    `json:"stateCount"`
	ActiveLockCount   int    `json:"activeLockCount"`
	SubscriberCount   int    `json:"subscriberCount"`
	ExecutionLogCount int    `json:"executionLogCount"`
	DroppedEvents     uint64 `json:"droppedEvents"`
}

// Stats returns a snapshot of current store operational metrics.
func (s *Store) Stats() Stats {
	s.stateMu.RLock()
	stateCount := len(s.states)
	s.stateMu.RUnlock()

	s.lockMu.Lock()
	now := time.Now()
	lockCount := 0
	for _, l := range s.locks {
		if now.Before(l.expiresAt) {
			lockCount++
		}
	}
	s.lockMu.Unlock()

	s.pubsubMu.RLock()
	subCount := len(s.subscribers)
	dropped := s.droppedEvents.Load()
	s.pubsubMu.RUnlock()

	s.logMu.RLock()
	logCount := 0
	for _, l := range s.logs {
		logCount += len(l)
	}
	s.logMu.RUnlock()

	return Stats{
		StateCount:        stateCount,
		ActiveLockCount:   lockCount,
		SubscriberCount:   subCount,
		ExecutionLogCount: logCount,
		DroppedEvents:     dropped,
	}
}

// Close closes the store and cleans up all active subscriptions and resources.
func (s *Store) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.Reset()
	return nil
}

var fallbackTokenSeq uint64

func generateLockToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("token-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&fallbackTokenSeq, 1))
	}
	return hex.EncodeToString(b)
}

func cloneGraphState(s *engine.GraphState) (*engine.GraphState, error) {
	if s == nil {
		return nil, nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("store: failed to marshal state for cloning: %w", err)
	}
	var clone engine.GraphState
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, fmt.Errorf("store: failed to unmarshal state for cloning: %w", err)
	}
	return &clone, nil
}

func cloneLogEntry(e *engine.ExecutionLogEntry) *engine.ExecutionLogEntry {
	if e == nil {
		return nil
	}
	return &engine.ExecutionLogEntry{
		ExecutionID: e.ExecutionID,
		Sequence:    e.Sequence,
		Timestamp:   e.Timestamp,
		Message:     e.Message,
		Details:     deepCopyMap(e.Details),
	}
}

func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = deepCopyAny(v)
	}
	return cp
}

func deepCopySlice(s []any) []any {
	if s == nil {
		return nil
	}
	cp := make([]any, len(s))
	for i, v := range s {
		cp[i] = deepCopyAny(v)
	}
	return cp
}

func deepCopyAny(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return deepCopyMap(val)
	case []any:
		return deepCopySlice(val)
	default:
		return val
	}
}

// ListExecutions filters and queries executions by status, time range, and graph ID.
func (s *Store) ListExecutions(ctx context.Context, filter engine.ExecutionFilter) ([]*engine.ExecutionSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed.Load() {
		return nil, &engine.StoreUnavailableError{
			Backend: "memory",
			Err:     errors.New("store is closed"),
		}
	}

	s.stateMu.RLock()
	defer s.stateMu.RUnlock()

	var summaries []*engine.ExecutionSummary
	for execID, record := range s.states {
		if filter.GraphID != "" && record.state.GraphID() != filter.GraphID {
			continue
		}
		if filter.Status != "" && record.state.Status() != filter.Status {
			continue
		}
		if filter.StartTime != nil && record.createdAt.Before(*filter.StartTime) {
			continue
		}
		if filter.EndTime != nil && record.createdAt.After(*filter.EndTime) {
			continue
		}

		summaries = append(summaries, &engine.ExecutionSummary{
			ExecutionID: execID,
			GraphID:     record.state.GraphID(),
			Status:      record.state.Status(),
			Version:     record.state.Version(),
			CreatedAt:   record.createdAt,
			UpdatedAt:   record.updatedAt,
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].CreatedAt.Equal(summaries[j].CreatedAt) {
			return summaries[i].ExecutionID > summaries[j].ExecutionID
		}
		return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
	})

	if filter.Offset > 0 {
		if filter.Offset >= len(summaries) {
			return []*engine.ExecutionSummary{}, nil
		}
		summaries = summaries[filter.Offset:]
	}
	if filter.Limit > 0 && len(summaries) > filter.Limit {
		summaries = summaries[:filter.Limit]
	}

	return summaries, nil
}
