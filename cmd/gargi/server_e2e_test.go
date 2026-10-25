package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/api"
	"github.com/gargi-indic/gargi-flow/internal/config"
	"github.com/gargi-indic/gargi-flow/internal/sim/jobs"
	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	"github.com/stretchr/testify/require"
)

var simbackendTestdata = filepath.Join("..", "..", "internal", "simbackend", "testdata")

// startServer starts an in-process gargi server (REST + SSE, memory store,
// jobs runner) and returns its URL. One server per test.
func startServer(t *testing.T) string {
	t.Helper()
	reg, err := jobs.NewRegistry()
	require.NoError(t, err)
	st := memory.NewExperimentStore()
	runner, err := jobs.New(st, reg, jobs.Options{RunWorkers: 2, Now: time.Now})
	require.NoError(t, err)
	svc := api.NewSimulationServer(st, runner, reg, nil)

	execService := api.NewServer(nil, config.TemporalConfig{TaskQueue: "test-queue"}, memory.NewStore(), nil)
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
	return ts.URL
}

func TestServerE2E_LoanExact(t *testing.T) {
	url := startServer(t)
	exp := filepath.Join(simbackendTestdata, "experiment.yaml")

	// 1. stdout of --server is byte-identical to --local.
	code, localOut, errOut := gargi(t, "sim", "run", exp, "--local")
	require.Equal(t, 0, code, errOut)
	code, serverOut, errOut := gargi(t, "sim", "run", exp, "--server", url)
	require.Equal(t, 0, code, errOut)
	require.Equal(t, localOut, serverOut)
	require.Contains(t, serverOut, "Exact EV: 460.0\n")
	require.Contains(t, serverOut, "Exact EV: 202.5\n")

	// 2. The experiment id is on stderr; sim report prints the same EVs.
	m := regexp.MustCompile(`(?m)^experiment (\S+)$`).FindStringSubmatch(errOut)
	require.NotNil(t, m, errOut)
	code, out, errOut := gargi(t, "sim", "report", m[1], "--kind", "summary", "--server", url)
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "\n  \"model\": \"loan-underwriting\"")
	var rep struct {
		Worlds []struct {
			Policies []struct {
				Objective struct {
					Mean float64 `json:"mean"`
				} `json:"objective"`
			} `json:"policies"`
		} `json:"worlds"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.Len(t, rep.Worlds, 2)
	require.InDelta(t, 460.0, rep.Worlds[0].Policies[0].Objective.Mean, 1e-9)
	require.InDelta(t, 202.525, rep.Worlds[1].Policies[0].Objective.Mean, 1e-9)
	require.Equal(t, byte('\n'), out[len(out)-1])

	// Default kind is summary.
	code, out2, _ := gargi(t, "sim", "report", m[1], "--server", url)
	require.Equal(t, 0, code)
	require.Equal(t, out, out2)

	// Unknown experiment: exit 1 with "gargi: <err>".
	code, _, errOut = gargi(t, "sim", "report", "missing", "--server", url)
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "gargi: ")
	require.Contains(t, errOut, "NOT_FOUND")

	// 3. policy explain over the server equals the golden.
	code, _, errOut = gargi(t, "sim", "run", filepath.Join(simbackendTestdata, "experiment-policy.yaml"), "--server", url)
	require.Equal(t, 0, code, errOut)
	golden, err := os.ReadFile(filepath.Join("..", "..", "internal", "sim", "explain", "testdata", "recession-v1.golden"))
	require.NoError(t, err)
	code, out, errOut = gargi(t, "policy", "explain", "loan-underwriting:recession-v1.yaml", "--server", url)
	require.Equal(t, 0, code, errOut)
	require.Equal(t, string(golden), out)
}

func TestServerE2E_UsageErrors(t *testing.T) {
	exp := filepath.Join(simbackendTestdata, "experiment.yaml")
	for _, args := range [][]string{
		{"sim", "run", exp, "--local", "--server", "http://127.0.0.1:1"},
		{"sim", "run", exp, "--server", "http://127.0.0.1:1", "--emit-best", t.TempDir()},
		{"sim", "run", exp, "--server", "http://127.0.0.1:1", "--workers", "2"},
		{"sim", "run", exp, "--server", "not-a-url"},
		{"sim", "report", "some-id"},
		{"sim", "report", "--server", "http://127.0.0.1:1"},
	} {
		code, out, errOut := gargi(t, args...)
		require.Equal(t, 2, code, "%v: %s", args, errOut)
		require.Empty(t, out, "%v", args)
		require.NotEmpty(t, errOut, "%v", args)
	}
}

func TestServerE2E_Cancel(t *testing.T) {
	url := startServer(t)

	// Missing experiment: exit 1 with error.
	code, _, errOut := gargi(t, "sim", "cancel", "missing-id", "--server", url)
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "gargi: ")

	// Usage error: missing --server.
	code, _, errOut = gargi(t, "sim", "cancel", "missing-id")
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "usage: gargi sim cancel")
}

func TestServerE2E_DeployPolicy(t *testing.T) {
	url := startServer(t)

	// Save model with policy by running experiment-policy.yaml on server
	exp := filepath.Join(simbackendTestdata, "experiment-policy.yaml")
	code, _, errOut := gargi(t, "sim", "run", exp, "--server", url)
	require.Equal(t, 0, code, errOut)

	// 1. Deploy in shadow mode
	code, out, errOut := gargi(t, "policy", "deploy", "loan-underwriting:recession-v1.yaml", "--mode", "shadow", "--server", url)
	require.Equal(t, 0, code, errOut)
	require.Equal(t, "Deployed policy loan-underwriting:recession-v1.yaml version 1 in shadow mode\n", out)

	// 2. Deploy in live mode with @version
	code, out, errOut = gargi(t, "policy", "deploy", "loan-underwriting:recession-v1.yaml@1", "--mode", "live", "--server", url)
	require.Equal(t, 0, code, errOut)
	require.Equal(t, "Deployed policy loan-underwriting:recession-v1.yaml version 1 in live mode\n", out)

	// 3. Error on missing policy
	code, _, errOut = gargi(t, "policy", "deploy", "loan-underwriting:nonexistent", "--mode", "shadow", "--server", url)
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "gargi policy deploy: ")
}

func TestServerE2E_ModelCalibrate(t *testing.T) {
	url := startServer(t)

	code, out, errOut := gargi(t, "model", "calibrate", "--server", url, filepath.Join(loanDir, "sim.yaml"))
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "# Calibrated sim/v1 model overlay")
	require.Contains(t, out, "apiVersion: sim/v1")
	require.Contains(t, out, "loan-underwriting")
}
