package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// ExperimentStoreFactory creates a fresh, empty sim.ExperimentStore for one
// test case.
type ExperimentStoreFactory func(t *testing.T) sim.ExperimentStore

// RunExperimentStore runs the sim.ExperimentStore conformance suite against
// stores created by factory. Every subtest gets a fresh store. Timestamps in
// the suite have microsecond precision, so SQL adapters may store them as
// timestamptz.
func RunExperimentStore(t *testing.T, factory ExperimentStoreFactory) {
	t.Helper()
	if factory == nil {
		t.Fatal("storetest: ExperimentStoreFactory cannot be nil")
	}
	tests := []struct {
		name string
		fn   func(t *testing.T, s sim.ExperimentStore)
	}{
		{"SaveModelVersions", testSaveModelVersions},
		{"SaveModelConcurrent", testSaveModelConcurrent},
		{"SaveModelRequiresID", testSaveModelRequiresID},
		{"GetModelNotFound", testGetModelNotFound},
		{"ListModelsLatestSorted", testListModelsLatestSorted},
		{"ModelDeepCopy", testModelDeepCopy},
		{"CreateGetExperiment", testCreateGetExperiment},
		{"CreateExperimentErrors", testCreateExperimentErrors},
		{"GetExperimentNotFound", testGetExperimentNotFound},
		{"UpdateExperiment", testUpdateExperiment},
		{"UpdateExperimentErrors", testUpdateExperimentErrors},
		{"UpdateExperimentAtomic", testUpdateExperimentAtomic},
		{"ExperimentDeepCopy", testExperimentDeepCopy},
		{"ListExperimentsPaging", testListExperimentsPaging},
		{"ListExperimentsTies", testListExperimentsTies},
		{"ListExperimentsInvalidToken", testListExperimentsInvalidToken},
		{"IngestAndListOutcomes", testIngestAndListOutcomes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := factory(t)
			if s == nil {
				t.Fatal("storetest: factory returned a nil ExperimentStore")
			}
			tc.fn(t, s)
		})
	}
}

// baseTime is a fixed, microsecond-precision UTC timestamp.
var baseTime = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func testBundle(id, simDoc string) sim.ModelBundle {
	return sim.ModelBundle{
		ID:     id,
		Sim:    []byte(simDoc),
		Graph:  []byte("graph of " + simDoc),
		Format: "yaml",
		Policies: map[string][]byte{
			"a.yaml": []byte("policy a of " + simDoc),
		},
	}
}

func testRecord(id string, created time.Time) sim.ExperimentRecord {
	return sim.ExperimentRecord{
		ID:         id,
		Model:      sim.ModelRef{ID: "loan", Version: "1"},
		Definition: []byte("apiVersion: experiment/v1 # " + id),
		Format:     "yaml",
		Status:     sim.ExperimentQueued,
		CreatedAt:  created,
	}
}

func mustSaveModel(t *testing.T, s sim.ExperimentStore, b sim.ModelBundle) sim.ModelBundle {
	t.Helper()
	saved, err := s.SaveModel(context.Background(), b)
	if err != nil {
		t.Fatalf("SaveModel(%q): %v", b.ID, err)
	}
	return saved
}

func mustCreate(t *testing.T, s sim.ExperimentStore, rec sim.ExperimentRecord) {
	t.Helper()
	if err := s.CreateExperiment(context.Background(), rec); err != nil {
		t.Fatalf("CreateExperiment(%q): %v", rec.ID, err)
	}
}

func assertBundle(t *testing.T, got sim.ModelBundle, want sim.ModelBundle, version string) {
	t.Helper()
	if got.ID != want.ID || got.Version != version || got.Format != want.Format {
		t.Fatalf("bundle = {ID:%q Version:%q Format:%q}, want {ID:%q Version:%q Format:%q}", got.ID, got.Version, got.Format, want.ID, version, want.Format)
	}
	if !bytes.Equal(got.Sim, want.Sim) || !bytes.Equal(got.Graph, want.Graph) {
		t.Fatalf("bundle documents = (%q, %q), want (%q, %q)", got.Sim, got.Graph, want.Sim, want.Graph)
	}
	if len(got.Policies) != len(want.Policies) {
		t.Fatalf("bundle has %d policies, want %d", len(got.Policies), len(want.Policies))
	}
	for k, v := range want.Policies {
		if !bytes.Equal(got.Policies[k], v) {
			t.Fatalf("policy %q = %q, want %q", k, got.Policies[k], v)
		}
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("bundle CreatedAt is zero")
	}
}

func testSaveModelVersions(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	v1 := testBundle("loan", "v1")
	v1.Version = "ignored"
	saved := mustSaveModel(t, s, v1)
	assertBundle(t, saved, v1, "1")
	v2 := testBundle("loan", "v2")
	assertBundle(t, mustSaveModel(t, s, v2), v2, "2")

	latest, err := s.GetModel(ctx, "loan", "")
	if err != nil {
		t.Fatalf("GetModel latest: %v", err)
	}
	assertBundle(t, latest, v2, "2")
	first, err := s.GetModel(ctx, "loan", "1")
	if err != nil {
		t.Fatalf("GetModel v1: %v", err)
	}
	assertBundle(t, first, v1, "1")
}

// testSaveModelConcurrent saves one model ID from many goroutines at once:
// every save gets a distinct version and together they are 1..n, with no
// gaps or duplicates.
func testSaveModelConcurrent(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	const n = 20
	var wg sync.WaitGroup
	versions := make(chan string, n)
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			saved, err := s.SaveModel(ctx, testBundle("loan", fmt.Sprintf("v%d", i)))
			if err != nil {
				errs <- err
				return
			}
			versions <- saved.Version
		})
	}
	wg.Wait()
	close(versions)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent SaveModel: %v", err)
	}
	seen := make(map[string]bool, n)
	for v := range versions {
		if seen[v] {
			t.Fatalf("concurrent SaveModel assigned version %q twice", v)
		}
		seen[v] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[fmt.Sprint(i)] {
			t.Fatalf("concurrent SaveModel versions %v miss %d, want 1..%d", seen, i, n)
		}
	}
	latest, err := s.GetModel(ctx, "loan", "")
	if err != nil {
		t.Fatalf("GetModel latest: %v", err)
	}
	if latest.Version != fmt.Sprint(n) {
		t.Fatalf("latest version = %q, want %d", latest.Version, n)
	}
}

func testSaveModelRequiresID(t *testing.T, s sim.ExperimentStore) {
	if _, err := s.SaveModel(context.Background(), testBundle("", "x")); err == nil {
		t.Fatal("SaveModel with an empty ID succeeded, want an error")
	}
}

func assertNotFound(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: got nil error, want not found", what)
	}
	if !errors.Is(err, engine.ErrStoreNotFound) || !engine.IsStoreNotFound(err) {
		t.Fatalf("%s: error %v does not wrap engine.ErrStoreNotFound", what, err)
	}
}

func testGetModelNotFound(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	_, err := s.GetModel(ctx, "missing", "")
	assertNotFound(t, err, "GetModel(missing)")
	mustSaveModel(t, s, testBundle("loan", "v1"))
	for _, v := range []string{"2", "0", "x"} {
		_, err = s.GetModel(ctx, "loan", v)
		assertNotFound(t, err, fmt.Sprintf("GetModel(loan, %q)", v))
	}
}

func testListModelsLatestSorted(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	models, err := s.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels on empty store: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("ListModels on empty store = %d models, want 0", len(models))
	}
	mustSaveModel(t, s, testBundle("zeta", "z1"))
	mustSaveModel(t, s, testBundle("alpha", "a1"))
	mustSaveModel(t, s, testBundle("alpha", "a2"))
	mustSaveModel(t, s, testBundle("mid", "m1"))
	models, err = s.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []struct{ id, version, sim string }{{"alpha", "2", "a2"}, {"mid", "1", "m1"}, {"zeta", "1", "z1"}}
	if len(models) != len(want) {
		t.Fatalf("ListModels = %d models, want %d", len(models), len(want))
	}
	for i, w := range want {
		if models[i].ID != w.id || models[i].Version != w.version || string(models[i].Sim) != w.sim {
			t.Fatalf("ListModels[%d] = {%q %q %q}, want {%q %q %q}", i, models[i].ID, models[i].Version, models[i].Sim, w.id, w.version, w.sim)
		}
	}
}

func testModelDeepCopy(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	in := testBundle("loan", "v1")
	want := testBundle("loan", "v1")
	mustSaveModel(t, s, in)
	in.Sim[0] = 'X'
	in.Policies["a.yaml"][0] = 'X'
	in.Policies["b.yaml"] = []byte("added")

	got, err := s.GetModel(ctx, "loan", "")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	assertBundle(t, got, want, "1")
	got.Graph[0] = 'X'
	got.Policies["a.yaml"][0] = 'X'
	delete(got.Policies, "a.yaml")

	again, err := s.GetModel(ctx, "loan", "1")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	assertBundle(t, again, want, "1")
}

func assertRecord(t *testing.T, got, want sim.ExperimentRecord) {
	t.Helper()
	if got.ID != want.ID || got.Model != want.Model || got.Format != want.Format || got.Status != want.Status || got.Error != want.Error || got.Progress != want.Progress {
		t.Fatalf("record = %+v, want %+v", got, want)
	}
	if !bytes.Equal(got.Definition, want.Definition) || !bytes.Equal(got.Report, want.Report) {
		t.Fatalf("record documents = (%q, %q), want (%q, %q)", got.Definition, got.Report, want.Definition, want.Report)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.StartedAt.Equal(want.StartedAt) || !got.FinishedAt.Equal(want.FinishedAt) {
		t.Fatalf("record times = (%v, %v, %v), want (%v, %v, %v)", got.CreatedAt, got.StartedAt, got.FinishedAt, want.CreatedAt, want.StartedAt, want.FinishedAt)
	}
}

func testCreateGetExperiment(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	rec := testRecord("exp-1", baseTime)
	rec.Progress = sim.Progress{Done: 3, Total: 10, Best: "p1", BestMean: 1.5, CIWidth: 0.25}
	rec.StartedAt = baseTime.Add(time.Second)
	mustCreate(t, s, rec)
	got, err := s.GetExperiment(ctx, "exp-1")
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	assertRecord(t, got, rec)

	noTime := testRecord("exp-2", time.Time{})
	mustCreate(t, s, noTime)
	got, err = s.GetExperiment(ctx, "exp-2")
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreateExperiment kept a zero CreatedAt, want it set")
	}
}

func testCreateExperimentErrors(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	if err := s.CreateExperiment(ctx, testRecord("", baseTime)); err == nil {
		t.Fatal("CreateExperiment with an empty ID succeeded, want an error")
	}
	mustCreate(t, s, testRecord("dup", baseTime))
	err := s.CreateExperiment(ctx, testRecord("dup", baseTime.Add(time.Second)))
	if !errors.Is(err, engine.ErrStoreConflict) {
		t.Fatalf("duplicate CreateExperiment error = %v, want engine.ErrStoreConflict", err)
	}
	got, err := s.GetExperiment(ctx, "dup")
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	assertRecord(t, got, testRecord("dup", baseTime))
}

func testGetExperimentNotFound(t *testing.T, s sim.ExperimentStore) {
	_, err := s.GetExperiment(context.Background(), "missing")
	assertNotFound(t, err, "GetExperiment(missing)")
}

func testUpdateExperiment(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	mustCreate(t, s, testRecord("exp", baseTime))
	want := testRecord("exp", baseTime)
	want.Status = sim.ExperimentSucceeded
	want.Progress = sim.Progress{Done: 10, Total: 10, Best: "best", BestMean: 460, CIWidth: 0}
	want.Report = []byte(`{"model":"loan"}`)
	want.StartedAt = baseTime.Add(time.Second)
	want.FinishedAt = baseTime.Add(2 * time.Second)
	got, err := s.UpdateExperiment(ctx, "exp", func(r *sim.ExperimentRecord) error {
		if r.Status != sim.ExperimentQueued {
			return fmt.Errorf("fn saw status %q, want queued", r.Status)
		}
		r.ID = "renamed"
		r.Status = want.Status
		r.Progress = want.Progress
		r.Report = want.Report
		r.StartedAt = want.StartedAt
		r.FinishedAt = want.FinishedAt
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateExperiment: %v", err)
	}
	assertRecord(t, got, want)
	stored, err := s.GetExperiment(ctx, "exp")
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	assertRecord(t, stored, want)
	if _, err := s.GetExperiment(ctx, "renamed"); err == nil {
		t.Fatal("UpdateExperiment renamed the record")
	}
}

func testUpdateExperimentErrors(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	_, err := s.UpdateExperiment(ctx, "missing", func(*sim.ExperimentRecord) error { return nil })
	assertNotFound(t, err, "UpdateExperiment(missing)")

	mustCreate(t, s, testRecord("exp", baseTime))
	errBoom := errors.New("boom")
	_, err = s.UpdateExperiment(ctx, "exp", func(r *sim.ExperimentRecord) error {
		r.Status = sim.ExperimentFailed
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("UpdateExperiment error = %v, want the fn error", err)
	}
	got, err := s.GetExperiment(ctx, "exp")
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	assertRecord(t, got, testRecord("exp", baseTime))
}

func testUpdateExperimentAtomic(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	mustCreate(t, s, testRecord("exp", baseTime))
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			_, err := s.UpdateExperiment(ctx, "exp", func(r *sim.ExperimentRecord) error {
				r.Progress.Done++
				return nil
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent UpdateExperiment: %v", err)
		}
	}
	got, err := s.GetExperiment(ctx, "exp")
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	if got.Progress.Done != n {
		t.Fatalf("after %d concurrent increments Done = %d", n, got.Progress.Done)
	}
}

func testExperimentDeepCopy(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	in := testRecord("exp", baseTime)
	in.Report = []byte(`{"a":1}`)
	want := testRecord("exp", baseTime)
	want.Report = []byte(`{"a":1}`)
	mustCreate(t, s, in)
	in.Definition[0] = 'X'
	in.Report[0] = 'X'

	got, err := s.GetExperiment(ctx, "exp")
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	assertRecord(t, got, want)
	got.Definition[0] = 'X'

	var kept []byte
	updated, err := s.UpdateExperiment(ctx, "exp", func(r *sim.ExperimentRecord) error {
		kept = r.Report
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateExperiment: %v", err)
	}
	kept[0] = 'X'
	updated.Report[0] = 'Y'
	again, err := s.GetExperiment(ctx, "exp")
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	assertRecord(t, again, want)
}

func listIDs(t *testing.T, s sim.ExperimentStore, limit int) ([][]string, []string) {
	t.Helper()
	var pages [][]string
	var tokens []string
	token := ""
	for range 100 {
		recs, next, err := s.ListExperiments(context.Background(), sim.ListExperimentsOptions{Limit: limit, PageToken: token})
		if err != nil {
			t.Fatalf("ListExperiments(limit %d, token %q): %v", limit, token, err)
		}
		ids := make([]string, len(recs))
		for i, r := range recs {
			ids[i] = r.ID
		}
		pages = append(pages, ids)
		tokens = append(tokens, next)
		if next == "" {
			return pages, tokens
		}
		token = next
	}
	t.Fatal("ListExperiments did not terminate")
	return nil, nil
}

func testListExperimentsPaging(t *testing.T, s sim.ExperimentStore) {
	pages, _ := listIDs(t, s, 2)
	if len(pages) != 1 || len(pages[0]) != 0 {
		t.Fatalf("empty store pages = %v, want one empty page", pages)
	}
	// Created out of order; listing is newest first.
	for _, i := range []int{3, 1, 5, 2, 4} {
		mustCreate(t, s, testRecord(fmt.Sprintf("exp-%d", i), baseTime.Add(time.Duration(i)*time.Minute)))
	}
	pages, tokens := listIDs(t, s, 2)
	want := [][]string{{"exp-5", "exp-4"}, {"exp-3", "exp-2"}, {"exp-1"}}
	if fmt.Sprint(pages) != fmt.Sprint(want) {
		t.Fatalf("pages = %v, want %v", pages, want)
	}
	if tokens[len(tokens)-1] != "" {
		t.Fatalf("last page token = %q, want empty", tokens[len(tokens)-1])
	}
	all, _ := listIDs(t, s, 0)
	if fmt.Sprint(all) != fmt.Sprint([][]string{{"exp-5", "exp-4", "exp-3", "exp-2", "exp-1"}}) {
		t.Fatalf("default page = %v, want every record", all)
	}
	exact, _ := listIDs(t, s, 5)
	if len(exact) != 1 || len(exact[0]) != 5 {
		t.Fatalf("limit 5 over 5 records = %v, want one full page and no next token", exact)
	}
}

func testListExperimentsTies(t *testing.T, s sim.ExperimentStore) {
	for _, id := range []string{"b", "a", "c"} {
		mustCreate(t, s, testRecord(id, baseTime))
	}
	mustCreate(t, s, testRecord("z-old", baseTime.Add(-time.Hour)))
	pages, _ := listIDs(t, s, 1)
	want := [][]string{{"c"}, {"b"}, {"a"}, {"z-old"}}
	if fmt.Sprint(pages) != fmt.Sprint(want) {
		t.Fatalf("pages = %v, want %v (CreatedAt desc, then ID desc)", pages, want)
	}
}

func testListExperimentsInvalidToken(t *testing.T, s sim.ExperimentStore) {
	mustCreate(t, s, testRecord("exp", baseTime))
	_, _, err := s.ListExperiments(context.Background(), sim.ListExperimentsOptions{PageToken: "no-such-token"})
	if !errors.Is(err, sim.ErrInvalidPageToken) {
		t.Fatalf("ListExperiments with an unknown token error = %v, want sim.ErrInvalidPageToken", err)
	}
}

func testIngestAndListOutcomes(t *testing.T, s sim.ExperimentStore) {
	ctx := context.Background()
	t1 := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 25, 11, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	t4 := time.Date(2026, 10, 25, 13, 0, 0, 0, time.UTC)

	o1 := sim.OutcomeRecord{
		ExecutionID:   "exec-1",
		GraphID:       "graph-A",
		PolicyVersion: "v1",
		Latents:       map[string]any{"default": 1.0, "reason": "loss"},
		ObservedAt:    t1,
	}
	o2 := sim.OutcomeRecord{
		ExecutionID:   "exec-2",
		GraphID:       "graph-A",
		PolicyVersion: "v1",
		Latents:       map[string]any{"default": 0.0},
		ObservedAt:    t2,
	}
	o3 := sim.OutcomeRecord{
		ExecutionID:   "exec-3",
		GraphID:       "graph-A",
		PolicyVersion: "v2",
		Latents:       map[string]any{"default": 0.5},
		ObservedAt:    t3,
	}
	o4 := sim.OutcomeRecord{
		ExecutionID:   "exec-4",
		GraphID:       "graph-B",
		PolicyVersion: "v1",
		Latents:       map[string]any{"score": 95.0},
		ObservedAt:    t4,
	}

	for _, o := range []sim.OutcomeRecord{o1, o2, o3, o4} {
		if err := s.IngestOutcome(ctx, o); err != nil {
			t.Fatalf("IngestOutcome(%s): %v", o.ExecutionID, err)
		}
	}

	// Idempotent re-ingest
	if err := s.IngestOutcome(ctx, o1); err != nil {
		t.Fatalf("re-ingest o1: %v", err)
	}

	// Empty execution ID should fail
	if err := s.IngestOutcome(ctx, sim.OutcomeRecord{}); err == nil {
		t.Fatal("IngestOutcome with empty ExecutionID should error")
	}

	// Query graph-A and v1
	resA1, err := s.ListOutcomes(ctx, sim.OutcomeQueryOptions{GraphID: "graph-A", PolicyVersion: "v1"})
	if err != nil {
		t.Fatalf("ListOutcomes(graph-A, v1): %v", err)
	}
	if len(resA1) != 2 {
		t.Fatalf("expected 2 outcomes for graph-A v1, got %d", len(resA1))
	}
	if resA1[0].ExecutionID != "exec-1" || resA1[1].ExecutionID != "exec-2" {
		t.Errorf("unexpected exec IDs: %s, %s", resA1[0].ExecutionID, resA1[1].ExecutionID)
	}
	if resA1[0].Latents["reason"] != "loss" {
		t.Errorf("latents round-trip failed: got %v", resA1[0].Latents)
	}

	// Query graph-A and v2
	resA2, err := s.ListOutcomes(ctx, sim.OutcomeQueryOptions{GraphID: "graph-A", PolicyVersion: "v2"})
	if err != nil {
		t.Fatalf("ListOutcomes(graph-A, v2): %v", err)
	}
	if len(resA2) != 1 || resA2[0].ExecutionID != "exec-3" {
		t.Fatalf("expected [exec-3], got %v", resA2)
	}

	// Query graph-B and v1
	resB1, err := s.ListOutcomes(ctx, sim.OutcomeQueryOptions{GraphID: "graph-B", PolicyVersion: "v1"})
	if err != nil {
		t.Fatalf("ListOutcomes(graph-B, v1): %v", err)
	}
	if len(resB1) != 1 || resB1[0].ExecutionID != "exec-4" {
		t.Fatalf("expected [exec-4], got %v", resB1)
	}

	// Query graph-A (all versions)
	resAAll, err := s.ListOutcomes(ctx, sim.OutcomeQueryOptions{GraphID: "graph-A"})
	if err != nil {
		t.Fatalf("ListOutcomes(graph-A): %v", err)
	}
	if len(resAAll) != 3 {
		t.Fatalf("expected 3 outcomes for graph-A, got %d", len(resAAll))
	}
}
