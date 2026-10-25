package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/sim/experiment"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/require"
)

// longRuns makes a Monte Carlo experiment run far longer than any test.
const longRuns = 50_000_000

// waitTimeout bounds every wait for a runner state change.
const waitTimeout = 20 * time.Second

var expSeq atomic.Int64

func nextID() string { return fmt.Sprintf("exp-%d", expSeq.Add(1)) }

type harness struct {
	store  *fakeStore
	runner *jobs.Runner
	model  sim.ModelRef
}

func newHarness(t *testing.T, opts jobs.Options) *harness {
	t.Helper()
	reg, err := jobs.NewRegistry()
	require.NoError(t, err)
	st := newFakeStore()
	saved, err := st.SaveModel(context.Background(), loanBundle(t))
	require.NoError(t, err)
	if opts.RunWorkers == 0 {
		opts.RunWorkers = 2
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	r, err := jobs.New(st, reg, opts)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_ = r.Close(ctx)
	})
	return &harness{store: st, runner: r, model: sim.ModelRef{ID: saved.ID}}
}

func (h *harness) submit(t *testing.T, definition []byte) (sim.ExperimentRecord, error) {
	t.Helper()
	return h.runner.Submit(context.Background(), sim.ExperimentRecord{
		ID:         nextID(),
		Model:      h.model,
		Definition: definition,
		Format:     "yaml",
	})
}

func (h *harness) mustSubmit(t *testing.T, definition []byte) sim.ExperimentRecord {
	t.Helper()
	rec, err := h.submit(t, definition)
	require.NoError(t, err)
	return rec
}

// waitFor polls the store until experiment id satisfies cond.
func (h *harness) waitFor(t *testing.T, id string, what string, cond func(sim.ExperimentRecord) bool) sim.ExperimentRecord {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		rec, err := h.store.GetExperiment(context.Background(), id)
		require.NoError(t, err)
		if cond(rec) {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("experiment %s: timed out waiting for %s; last record: status %s error %q progress %+v", id, what, rec.Status, rec.Error, rec.Progress)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) waitStatus(t *testing.T, id string, want sim.ExperimentStatus) sim.ExperimentRecord {
	t.Helper()
	return h.waitFor(t, id, "status "+string(want), func(r sim.ExperimentRecord) bool { return r.Status == want })
}

func (h *harness) waitTerminal(t *testing.T, id string) sim.ExperimentRecord {
	t.Helper()
	return h.waitFor(t, id, "a terminal status", func(r sim.ExperimentRecord) bool { return r.Status.Terminal() })
}

// waitProgress waits until experiment id is running and reported progress.
func (h *harness) waitProgress(t *testing.T, id string) sim.ExperimentRecord {
	t.Helper()
	return h.waitFor(t, id, "running with progress", func(r sim.ExperimentRecord) bool {
		return r.Status == sim.ExperimentRunning && r.Progress.Done > 0
	})
}

func TestRunner_SucceedsWithReportAndFinalProgress(t *testing.T) {
	h := newHarness(t, jobs.Options{})
	rec := h.mustSubmit(t, loanExperiment("exact", 1, exhaustive))
	require.Equal(t, sim.ExperimentQueued, rec.Status)
	require.Equal(t, sim.ModelRef{ID: "loan", Version: "1"}, rec.Model, "Submit resolves the latest version")

	done := h.waitTerminal(t, rec.ID)
	require.Equal(t, sim.ExperimentSucceeded, done.Status, done.Error)
	require.Empty(t, done.Error)
	require.False(t, done.StartedAt.IsZero())
	require.False(t, done.FinishedAt.IsZero())

	var report experiment.Report
	require.NoError(t, json.Unmarshal(done.Report, &report))
	require.Len(t, report.Worlds, 2)
	best := report.Worlds[0].Policies[0]
	require.InDelta(t, 460.0, best.Objective.Mean, 1e-9)
	require.InDelta(t, 202.525, report.Worlds[1].Policies[0].Objective.Mean, 1e-9)

	// The final progress carries the best policy of the first world.
	require.Equal(t, done.Progress.Total, done.Progress.Done)
	require.Positive(t, done.Progress.Total)
	require.Equal(t, best.PolicyID, done.Progress.Best)
	require.InDelta(t, 460.0, done.Progress.BestMean, 1e-9)
	require.Zero(t, done.Progress.CIWidth, "exact estimates have no CI width")
}

func TestNew_RequiresDependencies(t *testing.T) {
	reg := newRegistry(t)
	now := func() time.Time { return time.Time{} }
	_, err := jobs.New(nil, reg, jobs.Options{Now: now})
	require.Error(t, err)
	_, err = jobs.New(newFakeStore(), nil, jobs.Options{Now: now})
	require.Error(t, err)
	_, err = jobs.New(newFakeStore(), reg, jobs.Options{})
	require.ErrorContains(t, err, "Now")
}

func TestRunner_SubmitValidation(t *testing.T) {
	h := newHarness(t, jobs.Options{})

	_, err := h.runner.Submit(context.Background(), sim.ExperimentRecord{Model: h.model, Definition: loanExperiment("exact", 1, exhaustive)})
	require.Error(t, err, "an empty id is rejected")

	_, err = h.runner.Submit(context.Background(), sim.ExperimentRecord{ID: nextID(), Model: sim.ModelRef{ID: "missing"}, Definition: loanExperiment("exact", 1, exhaustive)})
	require.True(t, engine.IsStoreNotFound(err), "unknown model: %v", err)

	id := nextID()
	_, err = h.runner.Submit(context.Background(), sim.ExperimentRecord{ID: id, Model: h.model, Definition: loanExperiment("exact", 0, exhaustive), Format: "yaml"})
	var ve *engine.ValidationErrors
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "$.runs", ve.Issues()[0].Path)
	_, err = h.store.GetExperiment(context.Background(), id)
	require.True(t, engine.IsStoreNotFound(err), "an invalid experiment is not stored")
}

func TestRunner_QueueFullAndCancelQueued(t *testing.T) {
	h := newHarness(t, jobs.Options{Workers: 1, QueueSize: 1})
	running := h.mustSubmit(t, loanExperiment("monte_carlo", longRuns, onePolicy))
	h.waitProgress(t, running.ID)

	queued := h.mustSubmit(t, loanExperiment("exact", 1, exhaustive))
	_, err := h.submit(t, loanExperiment("exact", 1, exhaustive))
	require.ErrorIs(t, err, jobs.ErrQueueFull)

	// Cancelling the queued experiment records it before Cancel returns.
	require.NoError(t, h.runner.Cancel(context.Background(), queued.ID))
	rec, err := h.store.GetExperiment(context.Background(), queued.ID)
	require.NoError(t, err)
	require.Equal(t, sim.ExperimentCancelled, rec.Status)
	require.Equal(t, jobs.MsgCancelled, rec.Error)
	require.True(t, rec.StartedAt.IsZero(), "a cancelled queued experiment never started")
	require.False(t, rec.FinishedAt.IsZero())
	require.ErrorIs(t, h.runner.Cancel(context.Background(), queued.ID), jobs.ErrNotActive)

	// The queue has room again.
	again := h.mustSubmit(t, loanExperiment("exact", 1, exhaustive))

	require.NoError(t, h.runner.Cancel(context.Background(), running.ID))
	rec = h.waitTerminal(t, running.ID)
	require.Equal(t, sim.ExperimentCancelled, rec.Status)
	// The worker skips the cancelled queued experiment and runs the next.
	rec = h.waitTerminal(t, again.ID)
	require.Equal(t, sim.ExperimentSucceeded, rec.Status, rec.Error)
	rec, err = h.store.GetExperiment(context.Background(), queued.ID)
	require.NoError(t, err)
	require.Equal(t, sim.ExperimentCancelled, rec.Status, "a cancelled queued experiment stays cancelled")
}

func TestRunner_CancelRunning(t *testing.T) {
	h := newHarness(t, jobs.Options{})
	rec := h.mustSubmit(t, loanExperiment("monte_carlo", longRuns, onePolicy))
	updates, unsubscribe := h.runner.Subscribe(rec.ID)
	defer unsubscribe()
	h.waitProgress(t, rec.ID)

	start := time.Now()
	require.NoError(t, h.runner.Cancel(context.Background(), rec.ID))
	require.NoError(t, h.runner.Cancel(context.Background(), rec.ID), "cancelling twice is fine while it stops")
	done := h.waitTerminal(t, rec.ID)
	require.Equal(t, sim.ExperimentCancelled, done.Status)
	require.Equal(t, jobs.MsgCancelled, done.Error)
	require.Empty(t, done.Report)
	require.False(t, done.FinishedAt.IsZero())
	require.Less(t, done.Progress.Done, done.Progress.Total, "the run stopped early")
	require.Less(t, time.Since(start), 10*time.Second, "cancellation is prompt")

	// The subscription is closed once the experiment is terminal.
	deadline := time.After(waitTimeout)
	for open := true; open; {
		select {
		case _, open = <-updates:
		case <-deadline:
			t.Fatal("subscription not closed after cancellation")
		}
	}
	require.ErrorIs(t, h.runner.Cancel(context.Background(), rec.ID), jobs.ErrNotActive)
	require.ErrorIs(t, h.runner.Cancel(context.Background(), "unknown"), jobs.ErrNotActive)
}

// progressWrites returns the stored progress updates of a running
// experiment (the writes after it was marked running and before it ended).
func progressWrites(writes []sim.ExperimentRecord) []sim.Progress {
	var out []sim.Progress
	for _, w := range writes {
		if w.Status == sim.ExperimentRunning && w.Progress.Done > 0 {
			out = append(out, w.Progress)
		}
	}
	return out
}

func TestRunner_ProgressThrottled(t *testing.T) {
	// A frozen clock: only the first update and the forced final one pass
	// the throttle, however many chunks complete.
	frozen := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, jobs.Options{Now: func() time.Time { return frozen }})
	const runs = 3 * experiment.ChunkSize
	rec := h.mustSubmit(t, loanExperiment("monte_carlo", runs, onePolicy))
	done := h.waitTerminal(t, rec.ID)
	require.Equal(t, sim.ExperimentSucceeded, done.Status, done.Error)

	writes := progressWrites(h.store.writes(rec.ID))
	require.Len(t, writes, 2, "throttled progress writes: %+v", writes)
	require.Less(t, writes[0].Done, writes[0].Total)
	require.Equal(t, writes[1].Total, writes[1].Done, "the final update is never dropped")
	require.Equal(t, done.Progress.Total, writes[1].Total)
	require.NotEmpty(t, done.Progress.Best)
	require.Positive(t, done.Progress.CIWidth, "Monte Carlo estimates have a CI")
}

func TestRunner_ProgressRateBounded(t *testing.T) {
	// Real clock, default interval: at most four updates per second, plus
	// the final one.
	h := newHarness(t, jobs.Options{})
	rec := h.mustSubmit(t, loanExperiment("monte_carlo", longRuns, onePolicy))
	updates, unsubscribe := h.runner.Subscribe(rec.ID)
	defer unsubscribe()
	h.waitProgress(t, rec.ID)
	const window = 1200 * time.Millisecond
	time.Sleep(window)
	require.NoError(t, h.runner.Cancel(context.Background(), rec.ID))
	h.waitTerminal(t, rec.ID)

	writes := progressWrites(h.store.writes(rec.ID))
	require.NotEmpty(t, writes)
	// The run lasted at most a few windows; 4/s bounds the writes.
	require.LessOrEqual(t, len(writes), 4*int(window/time.Second+3), "progress writes %d", len(writes))
	received := 0
	for range updates {
		received++
	}
	require.LessOrEqual(t, received, len(writes), "subscribers see at most the stored updates")
}

func TestRunner_FailedRecordsError(t *testing.T) {
	h := newHarness(t, jobs.Options{})
	// Policy files are read when the experiment runs: a file the bundle does
	// not hold fails the run.
	rec := h.mustSubmit(t, loanExperiment("exact", 1, "{files: [./policies/missing.yaml]}"))
	done := h.waitTerminal(t, rec.ID)
	require.Equal(t, sim.ExperimentFailed, done.Status)
	require.Contains(t, done.Error, "$.policies.files[0]")
	require.Contains(t, done.Error, "missing.yaml")
	require.Empty(t, done.Report)
	require.False(t, done.FinishedAt.IsZero())
}

func TestRunner_SubscribeUnknownIsClosed(t *testing.T) {
	h := newHarness(t, jobs.Options{})
	ch, unsubscribe := h.runner.Subscribe("unknown")
	_, open := <-ch
	require.False(t, open)
	unsubscribe()
}

func TestRunner_UnsubscribeClosesChannel(t *testing.T) {
	h := newHarness(t, jobs.Options{})
	rec := h.mustSubmit(t, loanExperiment("monte_carlo", longRuns, onePolicy))
	ch, unsubscribe := h.runner.Subscribe(rec.ID)
	unsubscribe()
	unsubscribe() // idempotent
	for range ch {
	}
	require.NoError(t, h.runner.Cancel(context.Background(), rec.ID))
	h.waitTerminal(t, rec.ID)
}

func TestRunner_CloseDrainsQueue(t *testing.T) {
	h := newHarness(t, jobs.Options{Workers: 1})
	first := h.mustSubmit(t, loanExperiment("exact", 1, exhaustive))
	second := h.mustSubmit(t, loanExperiment("exact", 1, exhaustive))

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	require.NoError(t, h.runner.Close(ctx))
	for _, id := range []string{first.ID, second.ID} {
		rec, err := h.store.GetExperiment(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, sim.ExperimentSucceeded, rec.Status, "Close runs queued experiments: %s", rec.Error)
	}
	_, err := h.submit(t, loanExperiment("exact", 1, exhaustive))
	require.ErrorIs(t, err, jobs.ErrClosed)
	require.NoError(t, h.runner.Close(ctx), "Close is idempotent")
}

func TestRunner_CloseTimeoutCancelsRemaining(t *testing.T) {
	h := newHarness(t, jobs.Options{Workers: 1})
	running := h.mustSubmit(t, loanExperiment("monte_carlo", longRuns, onePolicy))
	h.waitProgress(t, running.ID)
	queued := h.mustSubmit(t, loanExperiment("exact", 1, exhaustive))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := h.runner.Close(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 10*time.Second)
	for _, id := range []string{running.ID, queued.ID} {
		rec, err := h.store.GetExperiment(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, sim.ExperimentCancelled, rec.Status, id)
		require.Equal(t, jobs.MsgShutdown, rec.Error, id)
	}
}

func TestRunner_ConcurrentSubmitters(t *testing.T) {
	h := newHarness(t, jobs.Options{Workers: 2, QueueSize: 4})
	errs := make(chan error, 8)
	ids := make(chan string, 8)
	for range 8 {
		go func() {
			rec, err := h.submit(t, loanExperiment("exact", 1, exhaustive))
			if err == nil {
				ids <- rec.ID
			}
			errs <- err
		}()
	}
	accepted, full := 0, 0
	for range 8 {
		err := <-errs
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, jobs.ErrQueueFull):
			full++
		default:
			t.Fatalf("Submit: %v", err)
		}
	}
	require.Equal(t, 8, accepted+full)
	require.GreaterOrEqual(t, accepted, 4)
	close(ids)
	for id := range ids {
		rec := h.waitTerminal(t, id)
		require.Equal(t, sim.ExperimentSucceeded, rec.Status, rec.Error)
	}
}

func TestRunner_SkipsExperimentFinishedInStore(t *testing.T) {
	h := newHarness(t, jobs.Options{Workers: 1})
	running := h.mustSubmit(t, loanExperiment("monte_carlo", longRuns, onePolicy))
	h.waitProgress(t, running.ID)
	queued := h.mustSubmit(t, loanExperiment("exact", 1, exhaustive))
	updates, unsubscribe := h.runner.Subscribe(queued.ID)
	defer unsubscribe()

	// Another party (an API fallback, another server) cancels the queued
	// record directly in the store.
	_, err := h.store.UpdateExperiment(context.Background(), queued.ID, func(r *sim.ExperimentRecord) error {
		r.Status = sim.ExperimentCancelled
		r.Error = "cancelled elsewhere"
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, h.runner.Cancel(context.Background(), running.ID))
	h.waitTerminal(t, running.ID)

	// The worker dequeues it, sees it is no longer queued and drops it.
	for range updates {
	}
	rec, err := h.store.GetExperiment(context.Background(), queued.ID)
	require.NoError(t, err)
	require.Equal(t, sim.ExperimentCancelled, rec.Status)
	require.Equal(t, "cancelled elsewhere", rec.Error)
	require.True(t, rec.StartedAt.IsZero(), "it never ran")
	require.ErrorIs(t, h.runner.Cancel(context.Background(), queued.ID), jobs.ErrNotActive)
}

func TestRunner_KillAndRestart(t *testing.T) {
	// Runs 2 chunks (2000 runs) with 1 policy.
	const runs = 2 * experiment.ChunkSize
	expDef := loanExperiment("monte_carlo", runs, onePolicy)

	// 1. Run uninterrupted in harness 1 to obtain reference report.
	h1 := newHarness(t, jobs.Options{RunWorkers: 2, ProgressInterval: 10 * time.Millisecond})
	rec1 := h1.mustSubmit(t, expDef)
	done1 := h1.waitTerminal(t, rec1.ID)
	require.Equal(t, sim.ExperimentSucceeded, done1.Status, done1.Error)
	var rep1 experiment.Report
	require.NoError(t, json.Unmarshal(done1.Report, &rep1))

	// 2. Start experiment in a fresh store and interrupt midway.
	st := newFakeStore()
	_, err := st.SaveModel(context.Background(), loanBundle(t))
	require.NoError(t, err)
	reg, err := jobs.NewRegistry()
	require.NoError(t, err)

	r1, err := jobs.New(st, reg, jobs.Options{
		RunWorkers:       1,
		ProgressInterval: 10 * time.Millisecond,
		Now:              time.Now,
	})
	require.NoError(t, err)

	rec2, err := r1.Submit(context.Background(), sim.ExperimentRecord{
		ID:         "kill-restart-exp",
		Model:      sim.ModelRef{ID: "loan"},
		Definition: expDef,
		Format:     "yaml",
	})
	require.NoError(t, err)

	waitRec := func(id string, cond func(sim.ExperimentRecord) bool) sim.ExperimentRecord {
		t.Helper()
		deadline := time.Now().Add(waitTimeout)
		for {
			rec, err := st.GetExperiment(context.Background(), id)
			require.NoError(t, err)
			if cond(rec) {
				return rec
			}
			if time.Now().After(deadline) {
				t.Fatalf("experiment %s: timed out; last status %s error %q checkpoint %+v", id, rec.Status, rec.Error, rec.Checkpoint)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Wait until progress shows at least 1 completed chunk, then cancel.
	waitRec(rec2.ID, func(r sim.ExperimentRecord) bool {
		return r.Checkpoint != nil && r.Checkpoint.CompletedChunks > 0 && r.Checkpoint.CompletedChunks < r.Checkpoint.TotalChunks
	})

	require.NoError(t, r1.Cancel(context.Background(), rec2.ID))
	cancelledRec := waitRec(rec2.ID, func(r sim.ExperimentRecord) bool {
		return r.Status.Terminal()
	})
	require.Equal(t, sim.ExperimentCancelled, cancelledRec.Status)
	require.NotNil(t, cancelledRec.Checkpoint)
	require.Greater(t, cancelledRec.Checkpoint.CompletedChunks, 0)
	require.Less(t, cancelledRec.Checkpoint.CompletedChunks, cancelledRec.Checkpoint.TotalChunks)

	// Close runner 1 ("killed").
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_ = r1.Close(ctx)
	cancel()

	// 3. "Restart" with a new runner against the same store.
	r2, err := jobs.New(st, reg, jobs.Options{
		RunWorkers:       2,
		ProgressInterval: 10 * time.Millisecond,
		Now:              time.Now,
	})
	require.NoError(t, err)
	defer func() {
		c, can := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_ = r2.Close(c)
		can()
	}()

	require.NoError(t, r2.Resume(context.Background(), rec2.ID))

	doneRec := waitRec(rec2.ID, func(r sim.ExperimentRecord) bool {
		return r.Status.Terminal()
	})
	require.Equal(t, sim.ExperimentSucceeded, doneRec.Status, doneRec.Error)
	require.Empty(t, doneRec.Error)

	var rep2 experiment.Report
	require.NoError(t, json.Unmarshal(doneRec.Report, &rep2))

	// 4. Report and metrics must match the uninterrupted run exactly!
	require.Equal(t, rep1, rep2)
}
