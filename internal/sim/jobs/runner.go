package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// Default runner options.
const (
	// DefaultWorkers is the number of experiments run concurrently.
	DefaultWorkers = 1
	// DefaultQueueSize is the number of experiments that may wait for a
	// worker.
	DefaultQueueSize = 16
	// DefaultProgressInterval is the minimum interval between two progress
	// updates of one experiment (at most four per second).
	DefaultProgressInterval = 250 * time.Millisecond
	// subscriberBuffer is the channel capacity of a progress subscription.
	subscriberBuffer = 8
)

// Messages recorded in ExperimentRecord.Error for cancelled experiments.
const (
	MsgCancelled = "cancelled by request"
	MsgShutdown  = "cancelled: the runner shut down"
)

// MsgInterrupted is recorded in ExperimentRecord.Error for experiments that
// Recover marks failed.
const MsgInterrupted = "interrupted: server restarted before the experiment finished"

var (
	// ErrQueueFull is returned by Submit when the bounded queue is full.
	ErrQueueFull = errors.New("jobs: experiment queue is full")
	// ErrClosed is returned by Submit after Close.
	ErrClosed = errors.New("jobs: runner is closed")
	// ErrNotActive is returned by Cancel for an experiment that is neither
	// queued nor running in this runner.
	ErrNotActive = errors.New("jobs: experiment is not queued or running")

	// errNotActiveInStore aborts a store update of an experiment whose
	// stored status no longer allows it.
	errNotActiveInStore = errors.New("jobs: stored experiment is not active")
)

// Options configures a Runner.
type Options struct {
	// Workers is the number of experiments run concurrently; <= 0 selects
	// DefaultWorkers.
	Workers int
	// QueueSize is the number of submitted experiments that may wait for a
	// worker; <= 0 selects DefaultQueueSize.
	QueueSize int
	// RunWorkers is experiment.Options.Workers for every run: 0 selects the
	// experiment's workers, else GOMAXPROCS.
	RunWorkers int
	// ProgressInterval is the minimum interval between two progress updates
	// (store writes and subscriber notifications) of one experiment; <= 0
	// selects DefaultProgressInterval. The final update is never dropped.
	ProgressInterval time.Duration
	// Now returns the current time for record timestamps and progress
	// throttling. It is required: internal/sim never reads the wall clock
	// itself (see the kernel's wall-clock audit), so cmd/* inject it
	// (ADR-0004), typically func() time.Time { return time.Now().UTC() }.
	Now func() time.Time
	// Logger receives store errors that cannot be returned to a caller;
	// nil selects slog.Default().
	Logger *slog.Logger
}

// job is the in-memory state of a queued or running experiment.
type job struct {
	// cancel cancels the run; it is set once a worker starts the job.
	cancel context.CancelFunc
	// cancelled records a Cancel request.
	cancelled bool
	subs      map[int]chan sim.Progress
	nextSub   int
}

// Runner runs submitted experiments on a pool of workers fed by a bounded
// queue, persisting their status, progress and report through a
// sim.ExperimentStore. It is safe for concurrent use.
type Runner struct {
	store sim.ExperimentStore
	reg   *engine.Registry
	opts  Options
	log   *slog.Logger

	baseCtx context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	drained chan struct{}

	mu sync.Mutex
	// ready is signalled when pending grows or the runner closes.
	ready  *sync.Cond
	closed bool
	// pending holds the IDs of queued experiments in submission order; a
	// cancelled experiment leaves it at once, freeing its slot.
	pending []string
	// reserved counts queue slots held by Submit calls that are storing
	// their record.
	reserved int
	jobs     map[string]*job
}

// New returns a Runner over store that compiles models with reg, and starts
// its workers. Call Close to stop them. store, reg and opts.Now are
// required.
func New(store sim.ExperimentStore, reg *engine.Registry, opts Options) (*Runner, error) {
	switch {
	case store == nil:
		return nil, errors.New("jobs: experiment store is nil")
	case reg == nil:
		return nil, errors.New("jobs: node registry is nil")
	case opts.Now == nil:
		return nil, errors.New("jobs: Options.Now is required")
	}
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = DefaultQueueSize
	}
	if opts.ProgressInterval <= 0 {
		opts.ProgressInterval = DefaultProgressInterval
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	ctx, stop := context.WithCancel(context.Background())
	r := &Runner{
		store:   store,
		reg:     reg,
		opts:    opts,
		log:     log,
		baseCtx: ctx,
		stop:    stop,
		drained: make(chan struct{}),
		jobs:    make(map[string]*job),
	}
	r.ready = sync.NewCond(&r.mu)
	for range opts.Workers {
		r.wg.Go(r.work)
	}
	go func() {
		r.wg.Wait()
		close(r.drained)
	}()
	return r, nil
}

// Submit validates rec and queues it. rec.ID must be set. rec.Model names
// the model bundle (an empty version selects the latest, and the stored
// record carries the resolved version); rec.Definition and rec.Format hold
// the experiment/v1 document. The experiment is compiled against the bundle
// first (see CompileExperiment): a missing model returns the store's
// not-found error and an invalid experiment *engine.ValidationErrors. The
// record is then created in the store with status queued and returned.
// Submit returns ErrQueueFull, without storing anything, when the queue is
// full, and ErrClosed after Close.
func (r *Runner) Submit(ctx context.Context, rec sim.ExperimentRecord) (sim.ExperimentRecord, error) {
	if strings.TrimSpace(rec.ID) == "" {
		return sim.ExperimentRecord{}, errors.New("jobs: experiment id is required")
	}
	bundle, err := r.store.GetModel(ctx, rec.Model.ID, rec.Model.Version)
	if err != nil {
		return sim.ExperimentRecord{}, err
	}
	if _, err := CompileExperiment(bundle, rec.Definition, rec.Format, r.reg); err != nil {
		return sim.ExperimentRecord{}, err
	}
	rec.Model = sim.ModelRef{ID: bundle.ID, Version: bundle.Version}
	rec.Status = sim.ExperimentQueued
	rec.Error = ""
	rec.Progress = sim.Progress{}
	rec.Report = nil
	rec.StartedAt, rec.FinishedAt = time.Time{}, time.Time{}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = r.opts.Now()
	}

	// Reserve a queue slot, then store the record without holding r.mu
	// (the store may be remote).
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return sim.ExperimentRecord{}, ErrClosed
	}
	if len(r.pending)+r.reserved >= r.opts.QueueSize {
		r.mu.Unlock()
		return sim.ExperimentRecord{}, ErrQueueFull
	}
	r.reserved++
	r.mu.Unlock()

	err = r.store.CreateExperiment(ctx, rec)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserved--
	if err != nil {
		// Workers of a closed runner may be waiting for this reservation.
		r.ready.Broadcast()
		return sim.ExperimentRecord{}, err
	}
	r.jobs[rec.ID] = &job{subs: make(map[int]chan sim.Progress)}
	r.pending = append(r.pending, rec.ID)
	r.ready.Signal()
	return rec, nil
}

// Resume restarts a failed or interrupted experiment from its last checkpoint.
func (r *Runner) Resume(ctx context.Context, id string) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	if len(r.pending)+r.reserved >= r.opts.QueueSize {
		r.mu.Unlock()
		return ErrQueueFull
	}

	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.reserved++
	r.mu.Unlock()

	_, err := r.store.UpdateExperiment(ctx, id, func(rec *sim.ExperimentRecord) error {
		if rec.Status == sim.ExperimentQueued || rec.Status == sim.ExperimentRunning {
			return errors.New("jobs: experiment is already running")
		}
		if rec.Status == sim.ExperimentSucceeded {
			return errors.New("jobs: experiment already succeeded")
		}
		rec.Status = sim.ExperimentQueued
		rec.Error = ""
		rec.FinishedAt = time.Time{}
		return nil
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserved--
	if err != nil {
		r.ready.Broadcast()
		return err
	}
	r.jobs[id] = &job{subs: make(map[int]chan sim.Progress)}
	r.pending = append(r.pending, id)
	r.ready.Signal()
	return nil
}

// Cancel cancels a queued or running experiment; its status ends
// cancelled. A queued experiment is marked cancelled before Cancel returns;
// a running one stops promptly and its worker records the cancellation.
// Cancel returns ErrNotActive when id is not queued or running in this
// runner (it is unknown or already finished).
func (r *Runner) Cancel(ctx context.Context, id string) error {
	r.mu.Lock()
	j, ok := r.jobs[id]
	if !ok || j.cancelled {
		r.mu.Unlock()
		if ok {
			return nil
		}
		return ErrNotActive
	}
	j.cancelled = true
	if j.cancel != nil {
		j.cancel()
		r.mu.Unlock()
		return nil
	}
	// Queued: take it off the queue, freeing its slot.
	r.pending = slices.DeleteFunc(r.pending, func(p string) bool { return p == id })
	r.mu.Unlock()
	r.finish(context.WithoutCancel(ctx), id, func(rec *sim.ExperimentRecord) {
		rec.Status = sim.ExperimentCancelled
		rec.Error = MsgCancelled
	})
	return nil
}

// Subscribe returns a channel of the progress updates of experiment id and
// a function that ends the subscription. The channel is closed when the
// experiment reaches a terminal status (its final state is then in the
// store) or when unsubscribe is called; it is closed immediately when id is
// not queued or running in this runner. Updates are snapshots: a
// subscriber that falls behind loses the oldest ones.
func (r *Runner) Subscribe(id string) (<-chan sim.Progress, func()) {
	ch := make(chan sim.Progress, subscriberBuffer)
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		close(ch)
		return ch, func() {}
	}
	n := j.nextSub
	j.nextSub++
	j.subs[n] = ch
	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if c, ok := j.subs[n]; ok {
			delete(j.subs, n)
			close(c)
		}
	}
}

// Close stops accepting submissions and drains the queue: queued
// experiments still run and running ones finish. When ctx is done first,
// every running and remaining queued experiment is cancelled (Error
// MsgShutdown), Close waits for the workers to record that, and returns
// ctx.Err().
func (r *Runner) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		r.ready.Broadcast()
	}
	r.mu.Unlock()
	select {
	case <-r.drained:
		r.stop()
		return nil
	case <-ctx.Done():
		r.stop()
		<-r.drained
		return ctx.Err()
	}
}

// Recover marks the experiments a previous process left unfinished as
// failed and returns how many it marked. The in-process queue does not
// survive a restart, so a stored record that is still queued or running
// would otherwise never finish. Recover pages through the store's
// experiments; every record that is not terminal (and not queued or running
// in this runner) becomes failed with Error MsgInterrupted and FinishedAt
// Options.Now(). Nothing is re-queued.
//
// Call it once at startup, before the runner accepts submissions. It
// assumes a single server per experiment store: another live process's
// queued and running experiments would be marked failed too.
func (r *Runner) Recover(ctx context.Context) (int, error) {
	var ids []string
	token := ""
	for {
		recs, next, err := r.store.ListExperiments(ctx, sim.ListExperimentsOptions{PageToken: token})
		if err != nil {
			return 0, fmt.Errorf("jobs: listing experiments to recover: %w", err)
		}
		for _, rec := range recs {
			if !rec.Status.Terminal() {
				ids = append(ids, rec.ID)
			}
		}
		if next == "" {
			break
		}
		token = next
	}
	n := 0
	for _, id := range ids {
		r.mu.Lock()
		_, active := r.jobs[id]
		r.mu.Unlock()
		if active {
			continue
		}
		finishedAt := r.opts.Now()
		_, err := r.store.UpdateExperiment(ctx, id, func(rec *sim.ExperimentRecord) error {
			if rec.Status.Terminal() {
				return errNotActiveInStore
			}
			rec.Status = sim.ExperimentFailed
			rec.Error = MsgInterrupted
			rec.FinishedAt = finishedAt
			return nil
		})
		switch {
		case errors.Is(err, errNotActiveInStore), engine.IsStoreNotFound(err):
			// Finished or removed since it was listed.
		case err != nil:
			return n, fmt.Errorf("jobs: recovering experiment %q: %w", id, err)
		default:
			n++
		}
	}
	return n, nil
}

// work runs queued experiments until the runner is closed and the queue
// is empty.
func (r *Runner) work() {
	for {
		r.mu.Lock()
		for len(r.pending) == 0 && (!r.closed || r.reserved > 0) {
			r.ready.Wait()
		}
		if len(r.pending) == 0 {
			r.mu.Unlock()
			return
		}
		id := r.pending[0]
		r.pending = r.pending[1:]
		r.mu.Unlock()
		r.run(id)
	}
}

// run executes the queued experiment id.
func (r *Runner) run(id string) {
	r.mu.Lock()
	j, ok := r.jobs[id]
	if !ok || j.cancelled {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(r.baseCtx)
	defer cancel()
	j.cancel = cancel
	r.mu.Unlock()

	storeCtx := context.WithoutCancel(ctx)
	if r.baseCtx.Err() != nil {
		r.finish(storeCtx, id, func(rec *sim.ExperimentRecord) {
			rec.Status = sim.ExperimentCancelled
			rec.Error = MsgShutdown
		})
		return
	}
	rec, err := r.store.UpdateExperiment(storeCtx, id, func(rec *sim.ExperimentRecord) error {
		if rec.Status != sim.ExperimentQueued {
			return errNotActiveInStore
		}
		rec.Status = sim.ExperimentRunning
		rec.StartedAt = r.opts.Now()
		return nil
	})
	if errors.Is(err, errNotActiveInStore) {
		// Finished elsewhere (for example cancelled directly in the
		// store) while it waited in the queue.
		r.forget(id)
		return
	}
	if err != nil {
		r.log.Error("jobs: marking experiment running", "experiment_id", id, "error", err)
		r.finish(storeCtx, id, func(rec *sim.ExperimentRecord) {
			rec.Status = sim.ExperimentFailed
			rec.Error = err.Error()
		})
		return
	}

	report, last, lastCP, runErr := r.execute(ctx, storeCtx, rec)
	var data []byte
	if runErr == nil {
		if data, err = json.Marshal(report); err != nil {
			runErr = fmt.Errorf("jobs: encoding report: %w", err)
		}
	}
	r.mu.Lock()
	cancelled := j.cancelled
	r.mu.Unlock()
	r.finish(storeCtx, id, func(rec *sim.ExperimentRecord) {
		rec.Progress = last
		if lastCP != nil {
			rec.Checkpoint = lastCP
		}
		switch {
		case cancelled:
			rec.Status = sim.ExperimentCancelled
			rec.Error = MsgCancelled
		case runErr != nil && r.baseCtx.Err() != nil:
			rec.Status = sim.ExperimentCancelled
			rec.Error = MsgShutdown
		case runErr != nil:
			rec.Status = sim.ExperimentFailed
			rec.Error = runErr.Error()
		default:
			rec.Status = sim.ExperimentSucceeded
			rec.Report = data
			rec.Progress = finalProgress(last, report)
		}
	})
}

// execute compiles and runs rec, publishing throttled progress. It returns
// the report and the last progress snapshot.
func (r *Runner) execute(ctx, storeCtx context.Context, rec sim.ExperimentRecord) (*experiment.Report, sim.Progress, *sim.ExperimentCheckpoint, error) {
	var last sim.Progress
	var lastCP *sim.ExperimentCheckpoint
	bundle, err := r.store.GetModel(storeCtx, rec.Model.ID, rec.Model.Version)
	if err != nil {
		return nil, last, nil, err
	}
	exp, err := CompileExperiment(bundle, rec.Definition, rec.Format, r.reg)
	if err != nil {
		return nil, last, nil, err
	}
	th := throttle{interval: r.opts.ProgressInterval, now: r.opts.Now}
	progress := func(done, total int) {
		last = sim.Progress{Done: done, Total: total}
		if !th.allow(done >= total) {
			return
		}
		r.publishProgress(storeCtx, rec.ID, last, lastCP)
	}
	opts := experiment.Options{
		Workers:    r.opts.RunWorkers,
		Progress:   progress,
		ResumeFrom: rec.Checkpoint,
		Checkpoint: func(cp sim.ExperimentCheckpoint) {
			lastCP = &cp
		},
	}
	report, err := experiment.Run(ctx, exp, exp.PolicySource(), opts)
	return report, last, lastCP, err
}

// publishProgress stores p as the progress of experiment id and notifies
// its subscribers.
func (r *Runner) publishProgress(ctx context.Context, id string, p sim.Progress, cp *sim.ExperimentCheckpoint) {
	if _, err := r.store.UpdateExperiment(ctx, id, func(rec *sim.ExperimentRecord) error {
		rec.Progress = p
		if cp != nil {
			rec.Checkpoint = cp
		}
		return nil
	}); err != nil {
		r.log.Warn("jobs: storing experiment progress", "experiment_id", id, "error", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return
	}
	for _, c := range j.subs {
		select {
		case c <- p:
		default:
			// Drop the oldest snapshot to make room for the newest.
			select {
			case <-c:
			default:
			}
			select {
			case c <- p:
			default:
			}
		}
	}
}

// finish records the terminal state of experiment id (set by mutate, with
// FinishedAt), then forgets the job and closes its subscriptions, so a
// subscriber that sees its channel closed reads the final record. A record
// that is already terminal in the store (finished elsewhere) is kept.
func (r *Runner) finish(ctx context.Context, id string, mutate func(*sim.ExperimentRecord)) {
	finishedAt := r.opts.Now()
	if _, err := r.store.UpdateExperiment(ctx, id, func(rec *sim.ExperimentRecord) error {
		if rec.Status.Terminal() {
			return errNotActiveInStore
		}
		mutate(rec)
		rec.FinishedAt = finishedAt
		return nil
	}); err != nil && !errors.Is(err, errNotActiveInStore) {
		r.log.Error("jobs: storing final experiment state", "experiment_id", id, "error", err)
	}
	r.forget(id)
}

// forget drops the job state of experiment id and closes its
// subscriptions.
func (r *Runner) forget(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return
	}
	delete(r.jobs, id)
	for n, c := range j.subs {
		delete(j.subs, n)
		close(c)
	}
}

// finalProgress completes last with the best policy of the report's first
// world.
func finalProgress(last sim.Progress, report *experiment.Report) sim.Progress {
	if report == nil || len(report.Worlds) == 0 || len(report.Worlds[0].Policies) == 0 {
		return last
	}
	best := report.Worlds[0].Policies[0]
	last.Best = best.PolicyID
	last.BestMean = best.Objective.Mean
	last.CIWidth = best.Objective.CI95High - best.Objective.CI95Low
	if last.Total == 0 {
		last.Done, last.Total = 1, 1
	}
	return last
}

// throttle limits progress updates to one per interval; forced updates
// always pass.
type throttle struct {
	interval time.Duration
	now      func() time.Time
	last     time.Time
	started  bool
}

// allow reports whether an update may be published now.
func (t *throttle) allow(force bool) bool {
	now := t.now()
	if force || !t.started || now.Sub(t.last) >= t.interval {
		t.started = true
		t.last = now
		return true
	}
	return false
}
