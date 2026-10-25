package simbackend_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/api"
	"github.com/gargi-indic/gargi-flow/internal/config"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	"github.com/gargi-indic/gargi-flow/internal/simbackend"
	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/require"
)

// newRemoteWithStores starts an in-process gargi server (memory store, jobs runner,
// REST + SSE) and returns a Remote for it alongside the underlying stores.
func newRemoteWithStores(t *testing.T) (*simbackend.Remote, sim.ExperimentStore, engine.StateStore) {
	t.Helper()
	reg, err := jobs.NewRegistry()
	require.NoError(t, err)
	st := memory.NewExperimentStore()
	runner, err := jobs.New(st, reg, jobs.Options{RunWorkers: 2, Now: time.Now})
	require.NoError(t, err)
	memStore := memory.NewStore()
	svc := api.NewSimulationServer(st, runner, reg, nil, api.WithStateStore(memStore))

	execService := api.NewServer(nil, config.TemporalConfig{TaskQueue: "test-queue"}, memStore, nil)
	httpServer, err := api.NewHTTPServer(context.Background(), execService, ":0", nil, nil, api.WithSimulationService(svc))
	require.NoError(t, err)
	ts := httptest.NewServer(httpServer.Handler())
	t.Cleanup(ts.Close)
	// Runs before ts.Close: ends running experiments and their streams.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_ = runner.Close(ctx)
	})
	remote, err := simbackend.NewRemote(ts.URL, ts.Client())
	require.NoError(t, err)
	return remote, st, memStore
}

// newRemote starts an in-process gargi server (memory store, jobs runner,
// REST + SSE) and returns a Remote for it. One server per test.
func newRemote(t *testing.T) *simbackend.Remote {
	r, _, _ := newRemoteWithStores(t)
	return r
}

func testdata(name string) string { return filepath.Join("testdata", name) }

func TestNewRemote_RejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "localhost:8080", "ftp://host", "http://", "://x", "/v1"} {
		_, err := simbackend.NewRemote(u, nil)
		require.Error(t, err, u)
	}
	r, err := simbackend.NewRemote("http://localhost:8080/", nil)
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestRemote_RunExperiment(t *testing.T) {
	r := newRemote(t)
	var calls int
	res, err := r.RunExperiment(context.Background(), testdata("experiment.yaml"), simbackend.RunOptions{
		Progress: func(sim.Progress) { calls++ },
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.ExperimentID)
	require.Nil(t, res.Policies)
	require.Positive(t, calls, "progress was reported")

	require.Equal(t, "loan-underwriting", res.Report.Model)
	require.Len(t, res.Report.Worlds, 2)
	require.Equal(t, sim.WorldID("base"), res.Report.Worlds[0].World)
	require.InDelta(t, 460.0, res.Report.Worlds[0].Policies[0].Objective.Mean, 1e-9)
	require.Equal(t, sim.WorldID("recession"), res.Report.Worlds[1].World)
	require.InDelta(t, 202.525, res.Report.Worlds[1].Policies[0].Objective.Mean, 1e-9)

	// The stored experiment's report is served by ID.
	raw, err := r.Report(context.Background(), res.ExperimentID, "summary")
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	require.Equal(t, "loan-underwriting", body["model"])
	require.NotContains(t, body, "regret")
}

func TestRemote_RunExperimentInvalid(t *testing.T) {
	r := newRemote(t)
	dir := t.TempDir()
	for _, f := range []string{"sim.yaml", "graph.yaml"} {
		data, err := os.ReadFile(testdata(f))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), data, 0o600))
	}
	path := filepath.Join(dir, "experiment.yaml")
	doc := "apiVersion: experiment/v1\nmodel: ./sim.yaml\nworlds: [base]\npolicies: {search: exact}\nevaluator: nope\nruns: 0\n"
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	_, err := r.RunExperiment(context.Background(), path, simbackend.RunOptions{})
	require.Error(t, err)
	issues := simbackend.IssuesOf(err, "invalid_value")
	require.NotEmpty(t, issues)
	require.Equal(t, "$.evaluator", issues[0].Path)

	_, err = r.RunExperiment(context.Background(), filepath.Join(dir, "missing.yaml"), simbackend.RunOptions{})
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRemote_RunExperimentCancel(t *testing.T) {
	r := newRemote(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	// RunExperiment follows the SSE stream in this goroutine, so once it
	// returns nothing is left reading the stream.
	_, err := r.RunExperiment(ctx, testdata("experiment-long.yaml"), simbackend.RunOptions{
		Progress: func(sim.Progress) { cancel() },
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 20*time.Second)
}

func TestRemote_ValidateModel(t *testing.T) {
	r := newRemote(t)
	issues, err := r.ValidateModel(context.Background(), testdata("sim.yaml"))
	require.NoError(t, err)
	require.Empty(t, issues)

	issues, err = r.ValidateModel(context.Background(), testdata("invalid-sim.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, issues)
	require.Contains(t, issues[0].Path, "ghost_node")
	for i := 1; i < len(issues); i++ {
		require.LessOrEqual(t, issues[i-1].Path, issues[i].Path, "issues are sorted")
	}
}

func TestRemote_ValidateBundle(t *testing.T) {
	r := newRemote(t)
	bundle := func(simFile string) sim.ModelBundle {
		simDoc, err := os.ReadFile(testdata(simFile))
		require.NoError(t, err)
		graphDoc, err := os.ReadFile(testdata("graph.yaml"))
		require.NoError(t, err)
		policy, err := os.ReadFile(testdata("recession-v1.yaml"))
		require.NoError(t, err)
		return sim.ModelBundle{
			Sim: simDoc, Graph: graphDoc,
			Policies: map[string][]byte{"recession-v1.yaml": policy}, Format: "yaml",
		}
	}
	issues, err := r.ValidateBundle(context.Background(), bundle("sim.yaml"))
	require.NoError(t, err)
	require.Empty(t, issues)

	issues, err = r.ValidateBundle(context.Background(), bundle("invalid-sim.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, issues)
	require.Equal(t, "$.behaviors.ghost_node", issues[0].Path)
	for i := 1; i < len(issues); i++ {
		require.LessOrEqual(t, issues[i-1].Path, issues[i].Path, "issues are sorted")
	}

	// A policy problem is reported too, so the policies reach the server.
	b := bundle("sim.yaml")
	b.Policies = map[string][]byte{"bad.txt": []byte("x")}
	issues, err = r.ValidateBundle(context.Background(), b)
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Equal(t, "$.policies['bad.txt']", issues[0].Path)
}

func TestRemote_ReportNotFound(t *testing.T) {
	r := newRemote(t)
	_, err := r.Report(context.Background(), "missing", "summary")
	require.ErrorIs(t, err, simbackend.ErrNotFound)
	require.Contains(t, err.Error(), "NOT_FOUND")
}

func TestRemote_ExplainPolicy(t *testing.T) {
	r := newRemote(t)
	// Upload a model bundle that carries recession-v1.yaml.
	res, err := r.RunExperiment(context.Background(), testdata("experiment-policy.yaml"), simbackend.RunOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, res.ExperimentID)

	golden, err := os.ReadFile(filepath.Join("..", "sim", "explain", "testdata", "recession-v1.golden"))
	require.NoError(t, err)
	for _, ref := range []string{"loan-underwriting:recession-v1.yaml", "loan-underwriting@1:recession-v1.yaml"} {
		got, err := r.ExplainPolicy(context.Background(), ref)
		require.NoError(t, err, ref)
		require.Equal(t, string(golden), got, ref)
	}

	for _, ref := range []string{"", "loan-underwriting", ":x.yaml", "loan-underwriting:"} {
		_, err = r.ExplainPolicy(context.Background(), ref)
		require.Error(t, err, ref)
		require.NotErrorIs(t, err, simbackend.ErrNotFound, ref)
	}
	_, err = r.ExplainPolicy(context.Background(), "loan-underwriting:nope.yaml")
	require.ErrorIs(t, err, simbackend.ErrNotFound)
	_, err = r.ExplainPolicy(context.Background(), "missing:recession-v1.yaml")
	require.ErrorIs(t, err, simbackend.ErrNotFound)
}

func TestRemote_RunBundle(t *testing.T) {
	r := newRemote(t)
	simDoc, err := os.ReadFile(testdata("sim.yaml"))
	require.NoError(t, err)
	graphDoc, err := os.ReadFile(testdata("graph.yaml"))
	require.NoError(t, err)
	policy, err := os.ReadFile(testdata("recession-v1.yaml"))
	require.NoError(t, err)
	expData, err := os.ReadFile(testdata("experiment.yaml"))
	require.NoError(t, err)

	b := sim.ModelBundle{
		ID:       "loan-underwriting",
		Sim:      simDoc,
		Graph:    graphDoc,
		Policies: map[string][]byte{"recession-v1.yaml": policy},
		Format:   "yaml",
	}

	res, err := r.RunBundle(context.Background(), b, expData, "yaml", simbackend.RunOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, res.ExperimentID)
	require.Nil(t, res.Policies)
	require.Equal(t, "loan-underwriting", res.Report.Model)
	require.Len(t, res.Report.Worlds, 2)
	require.Equal(t, sim.WorldID("base"), res.Report.Worlds[0].World)
	require.InDelta(t, 460.0, res.Report.Worlds[0].Policies[0].Objective.Mean, 1e-9)
	require.Equal(t, sim.WorldID("recession"), res.Report.Worlds[1].World)
	require.InDelta(t, 202.525, res.Report.Worlds[1].Policies[0].Objective.Mean, 1e-9)
}

func TestRemote_IngestOutcome(t *testing.T) {
	r, simStore, stateStore := newRemoteWithStores(t)
	ctx := context.Background()

	// Missing execution ID
	err := r.IngestOutcome(ctx, simbackend.OutcomeRequest{})
	require.Error(t, err)

	// Non-existent execution
	err = r.IngestOutcome(ctx, simbackend.OutcomeRequest{
		ExecutionID: "exec-does-not-exist",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, simbackend.ErrNotFound)

	// Create an execution in stateStore
	g, err := engine.NewGraph("graph-test", nil, nil)
	require.NoError(t, err)
	st, err := engine.NewGraphState(engine.ExecutionID("exec-remote-1"), g)
	require.NoError(t, err)
	require.NoError(t, stateStore.SetState(ctx, "exec-remote-1", st, 0))

	// Ingest outcome for this execution
	obsAt := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	err = r.IngestOutcome(ctx, simbackend.OutcomeRequest{
		ExecutionID: "exec-remote-1",
		Latents:     map[string]any{"default": 1.0, "loss": 250.0},
		ObservedAt:  obsAt,
	})
	require.NoError(t, err)

	// Verify outcome is stored in simStore
	outcomes, err := simStore.ListOutcomes(ctx, sim.OutcomeQueryOptions{GraphID: "graph-test"})
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, "exec-remote-1", outcomes[0].ExecutionID)
	require.Equal(t, "graph-test", outcomes[0].GraphID)
	require.Equal(t, "1", outcomes[0].PolicyVersion)
	require.Equal(t, 1.0, outcomes[0].Latents["default"])
}

func TestRemote_GetDriftReport(t *testing.T) {
	r, simStore, _ := newRemoteWithStores(t)
	ctx := context.Background()

	// Missing model ID
	_, err := r.GetDriftReport(ctx, "", "1")
	require.Error(t, err)

	// Non-existent model
	_, err = r.GetDriftReport(ctx, "nonexistent-model", "1")
	require.Error(t, err)
	require.ErrorIs(t, err, simbackend.ErrNotFound)

	// Save model first
	simDoc, err := os.ReadFile(testdata("sim.yaml"))
	require.NoError(t, err)
	graphDoc, err := os.ReadFile(testdata("graph.yaml"))
	require.NoError(t, err)
	policy, err := os.ReadFile(testdata("recession-v1.yaml"))
	require.NoError(t, err)

	b := sim.ModelBundle{
		ID:       "loan-drift-test",
		Sim:      simDoc,
		Graph:    graphDoc,
		Policies: map[string][]byte{"recession-v1.yaml": policy},
		Format:   "yaml",
	}
	_, err = simStore.SaveModel(ctx, b)
	require.NoError(t, err)

	// Fetch drift report
	reportRaw, err := r.GetDriftReport(ctx, "loan-drift-test", "1")
	require.NoError(t, err)
	require.NotEmpty(t, reportRaw)

	var rep struct {
		PolicyVersion string `json:"policyVersion"`
		DriftDetected bool   `json:"driftDetected"`
	}
	require.NoError(t, json.Unmarshal(reportRaw, &rep))
	require.Equal(t, "1", rep.PolicyVersion)
}
