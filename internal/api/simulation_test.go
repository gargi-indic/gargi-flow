package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/api"
	"github.com/gargi-indic/gargi-flow/internal/config"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/require"
)

// loanDir is the loan underwriting example, the acceptance model.
var loanDir = filepath.Join("..", "..", "examples", "simulation", "loan")

func readLoanFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(loanDir, name))
	require.NoError(t, err)
	return string(data)
}

// loanModelBody is the SaveModel/ValidateModel request body of the loan
// example, read from examples/simulation/loan.
func loanModelBody(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"sim":      readLoanFile(t, "sim.yaml"),
		"graph":    readLoanFile(t, "graph.yaml"),
		"policies": map[string]string{"recession-v1.yaml": readLoanFile(t, filepath.Join("policies", "recession-v1.yaml"))},
		"format":   "yaml",
	}
}

// loanExactExperiment is the loan example experiment with the exact
// evaluator instead of 200k Monte Carlo runs.
func loanExactExperiment(t *testing.T) string {
	t.Helper()
	doc := readLoanFile(t, "experiment.yaml")
	require.Contains(t, doc, "evaluator: both")
	return strings.Replace(doc, "evaluator: both", "evaluator: exact", 1)
}

// longExperiment runs Monte Carlo far longer than any test (cancel it).
const longExperiment = `apiVersion: experiment/v1
model: ./sim.yaml
worlds: [base, recession]
policies: {files: [./policies/recession-v1.yaml]}
evaluator: monte_carlo
runs: 50000000
seed: 1
top: 1
`

type simEnv struct {
	t          *testing.T
	ts         *httptest.Server
	store      *memory.ExperimentStore
	stateStore *memory.Store
	runner     *jobs.Runner
	svc        *api.SimulationServer
}

func newSimEnv(t *testing.T, opts jobs.Options) *simEnv {
	t.Helper()
	reg, err := jobs.NewRegistry()
	require.NoError(t, err)
	st := memory.NewExperimentStore()
	if opts.RunWorkers == 0 {
		opts.RunWorkers = 2
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	runner, err := jobs.New(st, reg, opts)
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
	return &simEnv{t: t, ts: ts, store: st, stateStore: memStore, runner: runner, svc: svc}
}

// do sends a JSON request and returns the status code and decoded body.
func (e *simEnv) do(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	status, raw := e.doRaw(method, path, body)
	var out map[string]any
	if len(raw) > 0 {
		require.NoError(e.t, json.Unmarshal(raw, &out), "body: %s", raw)
	}
	return status, out
}

func (e *simEnv) doRaw(method, path string, body any) (int, []byte) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(e.t, err)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, reader)
	require.NoError(e.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(e.t, err)
	return resp.StatusCode, raw
}

func (e *simEnv) saveLoan() map[string]any {
	e.t.Helper()
	status, body := e.do(http.MethodPost, "/v1/models", loanModelBody(e.t))
	require.Equal(e.t, http.StatusOK, status, "%v", body)
	return body["model"].(map[string]any)
}

func (e *simEnv) createExperiment(modelID, doc string) (int, map[string]any) {
	e.t.Helper()
	return e.do(http.MethodPost, "/v1/experiments", map[string]any{
		"model":      map[string]any{"id": modelID},
		"experiment": doc,
		"format":     "yaml",
	})
}

func (e *simEnv) mustCreate(modelID, doc string) string {
	e.t.Helper()
	status, body := e.createExperiment(modelID, doc)
	require.Equal(e.t, http.StatusOK, status, "%v", body)
	return body["id"].(string)
}

// waitStatus polls GET /v1/experiments/{id} until it has status want.
func (e *simEnv) waitStatus(id, want string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		status, body := e.do(http.MethodGet, "/v1/experiments/"+id, nil)
		require.Equal(e.t, http.StatusOK, status, "%v", body)
		exp := body["experiment"].(map[string]any)
		if exp["status"] == want {
			return exp
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("experiment %s: timed out waiting for %s: %v", id, want, exp)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitRunning waits until the experiment is running with progress.
func (e *simEnv) waitRunning(id string) {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		rec, err := e.store.GetExperiment(context.Background(), id)
		require.NoError(e.t, err)
		if rec.Status == sim.ExperimentRunning && rec.Progress.Done > 0 {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("experiment %s did not start: %+v", id, rec)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type sseEvent struct {
	id    string
	event string
	data  map[string]any
}

// followEvents reads GET /v1/experiments/{id}/events until the stream
// ends and returns its events.
func (e *simEnv) followEvents(id string) []sseEvent {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.ts.URL+"/v1/experiments/"+id+"/events", nil)
	require.NoError(e.t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(e.t, http.StatusOK, resp.StatusCode)
	require.Equal(e.t, "text/event-stream", resp.Header.Get("Content-Type"))

	var events []sseEvent
	var cur sseEvent
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if cur.event != "" {
				events = append(events, cur)
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, ":"):
			// comment (heartbeat)
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			require.NoError(e.t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.data))
		}
	}
	require.NoError(e.t, scanner.Err())
	return events
}

// requireIssueList asserts body["errors"] is a non-empty, sorted list of
// {path, code, message} objects and returns it.
func requireIssueList(t *testing.T, raw any) []map[string]any {
	t.Helper()
	list, ok := raw.([]any)
	require.True(t, ok, "errors is not a list: %v", raw)
	require.NotEmpty(t, list)
	out := make([]map[string]any, len(list))
	for i, item := range list {
		issue, ok := item.(map[string]any)
		require.True(t, ok, "issue %d is not an object: %v", i, item)
		keys := make([]string, 0, len(issue))
		for k := range issue {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		require.Equal(t, []string{"code", "message", "path"}, keys, "issue %d", i)
		for _, k := range keys {
			require.NotEmpty(t, issue[k], "issue %d field %s", i, k)
		}
		out[i] = issue
	}
	require.True(t, slices.IsSortedFunc(out, func(a, b map[string]any) int {
		for _, k := range []string{"path", "code", "message"} {
			if c := strings.Compare(a[k].(string), b[k].(string)); c != 0 {
				return c
			}
		}
		return 0
	}), "issues are not sorted: %v", out)
	return out
}

// TestSimulationHTTP_LoanAcceptance is the #141 acceptance test: create the
// loan experiment over REST, follow SSE to completion, fetch the report and
// replay a run.
func TestSimulationHTTP_LoanAcceptance(t *testing.T) {
	env := newSimEnv(t, jobs.Options{})

	// 1. Save the loan model.
	model := env.saveLoan()
	require.Equal(t, "loan-underwriting", model["id"])
	require.Equal(t, "1", model["version"])

	// 2. Create the experiment with the exact evaluator.
	status, created := env.createExperiment("loan-underwriting", loanExactExperiment(t))
	require.Equal(t, http.StatusOK, status, "%v", created)
	id := created["id"].(string)
	require.NotEmpty(t, id)
	require.Equal(t, "EXPERIMENT_STATUS_QUEUED", created["status"])

	// 3. Follow the SSE stream to the done event.
	events := env.followEvents(id)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, "done", last.event)
	require.Equal(t, "EXPERIMENT_STATUS_SUCCEEDED", last.data["status"], "%v", last.data)
	progress := last.data["progress"].(map[string]any)
	require.NotEmpty(t, progress["best"])
	require.InDelta(t, 460.0, progress["best_mean"], 1e-9)
	for i, ev := range events[:len(events)-1] {
		require.Equal(t, "progress", ev.event, "event %d", i)
	}
	for i, ev := range events {
		require.Equal(t, fmt.Sprint(i+1), ev.id, "sequence ids")
		require.Equal(t, id, ev.data["id"])
	}

	// 4. Fetch the summary report: best EV per world.
	status, raw := env.doRaw(http.MethodGet, "/v1/experiments/"+id+"/reports/summary", nil)
	require.Equal(t, http.StatusOK, status, "%s", raw)
	var report struct {
		Model  string `json:"model"`
		Worlds []struct {
			World    string `json:"world"`
			Policies []struct {
				PolicyID  string `json:"policyId"`
				Objective struct {
					Mean float64 `json:"mean"`
				} `json:"objective"`
			} `json:"policies"`
		} `json:"worlds"`
	}
	require.NoError(t, json.Unmarshal(raw, &report), "the REST body is the report itself")
	require.Equal(t, "loan-underwriting", report.Model)
	require.Len(t, report.Worlds, 2)
	require.Equal(t, "base", report.Worlds[0].World)
	require.InDelta(t, 460.0, report.Worlds[0].Policies[0].Objective.Mean, 1e-9)
	require.Equal(t, "recession", report.Worlds[1].World)
	require.InDelta(t, 202.525, report.Worlds[1].Policies[0].Objective.Mean, 1e-9)

	// GetExperiment summarizes the result.
	exp := env.waitStatus(id, "EXPERIMENT_STATUS_SUCCEEDED")
	require.Equal(t, []any{"summary"}, exp["reportKinds"])
	best := exp["best"].([]any)
	require.Len(t, best, 2)
	require.InDelta(t, 460.0, best[0].(map[string]any)["mean"], 1e-9)
	require.InDelta(t, 202.525, best[1].(map[string]any)["mean"], 1e-9)
	require.Equal(t, map[string]any{"id": "loan-underwriting", "version": "1"}, exp["model"])

	// 5. Replay run 0 as an engine event stream.
	status, run := env.do(http.MethodGet, "/v1/experiments/"+id+"/runs/0", nil)
	require.Equal(t, http.StatusOK, status, "%v", run)
	evs := run["events"].([]any)
	require.NotEmpty(t, evs)
	first := evs[0].(map[string]any)
	for _, k := range []string{"executionId", "sequence", "timestamp", "eventType"} {
		require.Contains(t, first, k, "execution event shape")
	}
	require.Equal(t, "base", run["world"])
	require.Equal(t, report.Worlds[0].Policies[0].PolicyID, run["policyId"])
	record := run["record"].(map[string]any)
	require.NotEmpty(t, record["path"])
	require.NotEmpty(t, record["decisions"])
	require.Contains(t, record, "objective")
	require.Contains(t, record, "duration")
	sawNodeChange := false
	for _, ev := range evs {
		if ev.(map[string]any)["eventType"] == "node_state_changed" {
			sawNodeChange = true
		}
	}
	require.True(t, sawNodeChange, "replayed events include node transitions")

	// Following a finished experiment yields only the done event.
	again := env.followEvents(id)
	require.Len(t, again, 1)
	require.Equal(t, "done", again[0].event)
}

func TestSimulationHTTP_ValidateModel(t *testing.T) {
	env := newSimEnv(t, jobs.Options{})

	status, body := env.do(http.MethodPost, "/v1/models:validate", loanModelBody(t))
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, true, body["valid"])
	require.Empty(t, body["errors"])

	invalid := loanModelBody(t)
	invalid["sim"] = strings.Replace(invalid["sim"].(string), "cost: 20", "cost: \"nope +\"", 1)
	invalid["policies"] = map[string]string{"x.txt": "y", "broken.json": "{"}
	status, body = env.do(http.MethodPost, "/v1/models:validate", invalid)
	require.Equal(t, http.StatusOK, status, "validation problems are not an RPC error: %v", body)
	require.Equal(t, false, body["valid"])
	issues := requireIssueList(t, body["errors"])
	var gotPaths []string
	for _, is := range issues {
		gotPaths = append(gotPaths, is["path"].(string))
	}
	require.Contains(t, gotPaths, "$.policies['x.txt']", "every problem is reported")

	// Nothing was stored.
	require.Empty(t, env.listModels())

	status, body = env.do(http.MethodPost, "/v1/models:validate", map[string]any{"sim": ""})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = env.do(http.MethodPost, "/v1/models:validate", map[string]any{"sim": "x", "format": "toml"})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
}

func (e *simEnv) listModels() []any {
	e.t.Helper()
	status, body := e.do(http.MethodGet, "/v1/models", nil)
	require.Equal(e.t, http.StatusOK, status, "%v", body)
	models, _ := body["models"].([]any)
	return models
}

func TestSimulationHTTP_SaveModelRejectsInvalid(t *testing.T) {
	env := newSimEnv(t, jobs.Options{})
	invalid := loanModelBody(t)
	invalid["graph"] = "apiVersion: graph/v1\nnodes: ["
	status, body := env.do(http.MethodPost, "/v1/models", invalid)
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	require.Equal(t, "InvalidArgument", body["status"])
	issues := requireIssueList(t, body["errors"])
	require.True(t, strings.HasPrefix(issues[0]["path"].(string), "$.graph"), "%v", issues)
	require.Empty(t, env.listModels())
}

func TestSimulationHTTP_Models(t *testing.T) {
	env := newSimEnv(t, jobs.Options{})
	require.Empty(t, env.listModels())

	v1 := env.saveLoan()
	require.Equal(t, "1", v1["version"])
	require.NotEmpty(t, v1["createdAt"])
	v2 := env.saveLoan()
	require.Equal(t, "2", v2["version"])

	other := loanModelBody(t)
	other["id"] = "another"
	status, body := env.do(http.MethodPost, "/v1/models", other)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "another", body["model"].(map[string]any)["id"])

	status, body = env.do(http.MethodGet, "/v1/models/loan-underwriting", nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	got := body["model"].(map[string]any)
	require.Equal(t, "2", got["version"])
	require.Equal(t, readLoanFile(t, "sim.yaml"), got["sim"])
	require.Equal(t, readLoanFile(t, "graph.yaml"), got["graph"])
	require.Contains(t, got["policies"], "recession-v1.yaml")
	require.Equal(t, "yaml", got["format"])

	status, body = env.do(http.MethodGet, "/v1/models/loan-underwriting?version=1", nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "1", body["model"].(map[string]any)["version"])

	models := env.listModels()
	require.Len(t, models, 2)
	require.Equal(t, "another", models[0].(map[string]any)["id"])
	require.Equal(t, "loan-underwriting", models[1].(map[string]any)["id"])
	require.Equal(t, "2", models[1].(map[string]any)["version"])

	status, body = env.do(http.MethodGet, "/v1/models/missing", nil)
	require.Equal(t, http.StatusNotFound, status, "%v", body)
	status, body = env.do(http.MethodGet, "/v1/models/loan-underwriting?version=9", nil)
	require.Equal(t, http.StatusNotFound, status, "%v", body)

	bad := loanModelBody(t)
	bad["id"] = "a/b"
	status, body = env.do(http.MethodPost, "/v1/models", bad)
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	status, body = env.do(http.MethodPost, "/v1/models", map[string]any{"graph": "x"})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
}

func TestSimulationHTTP_CreateExperimentErrors(t *testing.T) {
	env := newSimEnv(t, jobs.Options{})

	status, body := env.createExperiment("missing", loanExactExperiment(t))
	require.Equal(t, http.StatusNotFound, status, "%v", body)

	env.saveLoan()
	status, body = env.createExperiment("loan-underwriting", "apiVersion: experiment/v1\nworlds: [mars]\npolicies: {search: exhaustive}\nevaluator: nope\nruns: 0\ntop: 1\n")
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	issues := requireIssueList(t, body["errors"])
	var gotPaths []string
	for _, is := range issues {
		gotPaths = append(gotPaths, is["path"].(string))
	}
	require.Equal(t, []string{"$.evaluator", "$.runs"}, gotPaths)

	status, body = env.do(http.MethodPost, "/v1/experiments", map[string]any{"experiment": "x"})
	require.Equal(t, http.StatusBadRequest, status, "model.id is required: %v", body)
	status, body = env.do(http.MethodPost, "/v1/experiments", map[string]any{"model": map[string]any{"id": "loan-underwriting"}})
	require.Equal(t, http.StatusBadRequest, status, "experiment is required: %v", body)
	status, body = env.do(http.MethodPost, "/v1/experiments", map[string]any{"model": map[string]any{"id": "loan-underwriting", "version": "7"}, "experiment": loanExactExperiment(t)})
	require.Equal(t, http.StatusNotFound, status, "unknown version: %v", body)

	for _, path := range []string{"/v1/experiments/missing", "/v1/experiments/missing/reports/summary", "/v1/experiments/missing/runs/0"} {
		status, body = env.do(http.MethodGet, path, nil)
		require.Equal(t, http.StatusNotFound, status, "%s: %v", path, body)
	}
	status, body = env.do(http.MethodPost, "/v1/experiments/missing:cancel", map[string]any{})
	require.Equal(t, http.StatusNotFound, status, "%v", body)

	// The SSE endpoint reports an unknown experiment as a JSON error.
	status, raw := env.doRaw(http.MethodGet, "/v1/experiments/missing/events", nil)
	require.Equal(t, http.StatusNotFound, status, "%s", raw)
	var apiErr api.APIError
	require.NoError(t, json.Unmarshal(raw, &apiErr))
	require.Equal(t, "NotFound", apiErr.Status)
}

func TestSimulationHTTP_QueueFullIs429(t *testing.T) {
	env := newSimEnv(t, jobs.Options{Workers: 1, QueueSize: 1})
	env.saveLoan()
	running := env.mustCreate("loan-underwriting", longExperiment)
	env.waitRunning(running)
	env.mustCreate("loan-underwriting", longExperiment)

	status, body := env.createExperiment("loan-underwriting", longExperiment)
	require.Equal(t, http.StatusTooManyRequests, status, "%v", body)
	require.Equal(t, "ResourceExhausted", body["status"])
}

func TestSimulationHTTP_NotReadyAndCancel(t *testing.T) {
	env := newSimEnv(t, jobs.Options{Workers: 1})
	env.saveLoan()
	id := env.mustCreate("loan-underwriting", longExperiment)
	queued := env.mustCreate("loan-underwriting", longExperiment)
	env.waitRunning(id)

	// No report before the experiment succeeds: FAILED_PRECONDITION (400).
	status, body := env.do(http.MethodGet, "/v1/experiments/"+id+"/reports/summary", nil)
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	require.Equal(t, "FailedPrecondition", body["status"])
	status, body = env.do(http.MethodGet, "/v1/experiments/"+id+"/runs/0", nil)
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
	require.Equal(t, "FailedPrecondition", body["status"])
	// With an explicit policy a run replays while the experiment runs.
	status, body = env.do(http.MethodGet, "/v1/experiments/"+id+"/runs/5?world=recession&policy=loan-underwriting@recession-v1", nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "recession", body["world"])
	require.NotEmpty(t, body["events"])

	exp := env.waitStatus(id, "EXPERIMENT_STATUS_RUNNING")
	require.Positive(t, exp["progress"].(map[string]any)["total"])

	// Cancel the queued experiment, then the running one.
	status, body = env.do(http.MethodPost, "/v1/experiments/"+queued+":cancel", map[string]any{})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "EXPERIMENT_STATUS_CANCELLED", body["status"])

	done := make(chan []sseEvent, 1)
	go func() { done <- env.followEvents(id) }()
	status, body = env.do(http.MethodPost, "/v1/experiments/"+id+":cancel", map[string]any{})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "EXPERIMENT_STATUS_CANCELLED", body["status"])
	exp = env.waitStatus(id, "EXPERIMENT_STATUS_CANCELLED")
	require.Equal(t, jobs.MsgCancelled, exp["error"])
	require.NotEmpty(t, exp["finishedAt"])

	events := <-done
	require.NotEmpty(t, events)
	require.Equal(t, "done", events[len(events)-1].event)
	require.Equal(t, "EXPERIMENT_STATUS_CANCELLED", events[len(events)-1].data["status"])

	// Cancelling a finished experiment reports its terminal status.
	status, body = env.do(http.MethodPost, "/v1/experiments/"+id+":cancel", map[string]any{})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "EXPERIMENT_STATUS_CANCELLED", body["status"])
	status, body = env.do(http.MethodGet, "/v1/experiments/"+id+"/reports/summary", nil)
	require.Equal(t, http.StatusBadRequest, status, "%v", body)
}

func TestSimulationHTTP_ReportKindsAndRuns(t *testing.T) {
	env := newSimEnv(t, jobs.Options{})
	env.saveLoan()
	doc := loanExactExperiment(t) + "reports: [regret]\n"
	id := env.mustCreate("loan-underwriting", doc)
	exp := env.waitStatus(id, "EXPERIMENT_STATUS_SUCCEEDED")
	require.Equal(t, []any{"summary", "regret"}, exp["reportKinds"])

	status, raw := env.doRaw(http.MethodGet, "/v1/experiments/"+id+"/reports/regret", nil)
	require.Equal(t, http.StatusOK, status, "%s", raw)
	var regret map[string]any
	require.NoError(t, json.Unmarshal(raw, &regret))
	require.NotEmpty(t, regret)

	status, body := env.do(http.MethodGet, "/v1/experiments/"+id+"/reports/pareto", nil)
	require.Equal(t, http.StatusNotFound, status, "a kind the experiment did not request: %v", body)
	status, body = env.do(http.MethodGet, "/v1/experiments/"+id+"/reports/bogus", nil)
	require.Equal(t, http.StatusBadRequest, status, "%v", body)

	status, body = env.do(http.MethodGet, "/v1/experiments/"+id+"/runs/200000", nil)
	require.Equal(t, http.StatusBadRequest, status, "index out of range: %v", body)
	status, body = env.do(http.MethodGet, "/v1/experiments/"+id+"/runs/0?world=mars", nil)
	require.Equal(t, http.StatusBadRequest, status, "unknown world: %v", body)
	status, body = env.do(http.MethodGet, "/v1/experiments/"+id+"/runs/0?policy=nope", nil)
	require.Equal(t, http.StatusNotFound, status, "unknown policy: %v", body)

	status, body = env.do(http.MethodGet, "/v1/experiments/"+id+"/runs/1?world=recession", nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "recession", body["world"])
	require.EqualValues(t, 1, body["index"])
}

func TestSimulationHTTP_ExecutionAPIUnchanged(t *testing.T) {
	// The execution routes keep working next to the simulation ones.
	env := newSimEnv(t, jobs.Options{})
	status, body := env.do(http.MethodGet, "/v1/executions", nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, _ = env.do(http.MethodGet, "/v1/executions/missing", nil)
	require.Equal(t, http.StatusNotFound, status)

	status, raw := env.doRaw(http.MethodGet, "/openapi.json", nil)
	require.Equal(t, http.StatusOK, status)
	var spec struct {
		Paths map[string]any `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &spec))
	for _, p := range []string{"/v1/executions", "/v1/models", "/v1/models:validate", "/v1/experiments", "/v1/experiments/{id}/reports/{kind}", "/v1/experiments/{id}/runs/{index}"} {
		require.Contains(t, spec.Paths, p)
	}
}

func TestSimulationHTTP_DeployPolicy(t *testing.T) {
	env := newSimEnv(t, jobs.Options{})
	env.saveLoan()

	// Deploy shadow mode via REST
	status, body := env.do(http.MethodPost, "/v1/policies/loan-underwriting:recession-v1.yaml:deploy", map[string]any{
		"mode": "shadow",
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "shadow", body["mode"])
	require.Equal(t, "1", body["version"])

	// Deploy live mode via REST
	status, body = env.do(http.MethodPost, "/v1/policies/loan-underwriting:recession-v1.yaml:deploy", map[string]any{
		"mode": "live",
	})
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "live", body["mode"])

	// Error: invalid mode
	status, body = env.do(http.MethodPost, "/v1/policies/loan-underwriting:recession-v1.yaml:deploy", map[string]any{
		"mode": "invalid-mode",
	})
	require.Equal(t, http.StatusBadRequest, status, "%v", body)

	// Error: missing policy
	status, body = env.do(http.MethodPost, "/v1/policies/loan-underwriting:missing-policy:deploy", map[string]any{
		"mode": "shadow",
	})
	require.Equal(t, http.StatusNotFound, status, "%v", body)
}

func TestSimulationREST_IngestOutcome(t *testing.T) {
	env := newSimEnv(t, jobs.Options{})
	ctx := context.Background()

	// 1. Missing execution_id
	status, errBody := env.do(http.MethodPost, "/v1/outcomes", map[string]any{
		"latents": map[string]any{"default": 1.0},
	})
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, errBody["message"], "execution_id is required")

	// 2. Execution not found
	status, _ = env.do(http.MethodPost, "/v1/outcomes", map[string]any{
		"execution_id": "non-existent-exec",
		"latents":      map[string]any{"default": 1.0},
	})
	require.Equal(t, http.StatusNotFound, status)

	// 3. Create execution in stateStore
	g, err := engine.NewGraph("graph-underwrite", nil, nil)
	require.NoError(t, err)
	st, err := engine.NewGraphState(engine.ExecutionID("exec-http-1"), g)
	require.NoError(t, err)
	require.NoError(t, env.stateStore.SetState(ctx, "exec-http-1", st, 0))

	// 4. Ingest outcome
	status, body := env.do(http.MethodPost, "/v1/outcomes", map[string]any{
		"execution_id": "exec-http-1",
		"latents": map[string]any{
			"default": 1.0,
			"loss":    3000.5,
		},
		"observed_at": "2026-10-25T14:00:00Z",
	})
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, body)

	// 5. Verify outcome in experiment store
	outcomes, err := env.store.ListOutcomes(ctx, sim.OutcomeQueryOptions{GraphID: "graph-underwrite", PolicyVersion: "1"})
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, "exec-http-1", outcomes[0].ExecutionID)
	require.Equal(t, "graph-underwrite", outcomes[0].GraphID)
	require.Equal(t, "1", outcomes[0].PolicyVersion)
	require.Equal(t, 1.0, outcomes[0].Latents["default"])
	require.Equal(t, 3000.5, outcomes[0].Latents["loss"])
}
