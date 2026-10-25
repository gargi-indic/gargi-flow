package kernel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/sim/agent"
	"github.com/gargi-indic/gargi-flow/internal/sim/kernel"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/sampler"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/require"
)

func TestAgentBehavior_CachedRerunAndHeaders(t *testing.T) {
	agent.GlobalCache.Clear()
	agent.ResetClients()

	t.Setenv("AGENT_API_TOKEN", "token-secret-xyz")

	var callCount int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&callCount, 1)

		require.Equal(t, "Bearer token-secret-xyz", r.Header.Get("Authorization"))
		require.Equal(t, "custom-val", r.Header.Get("X-Custom"))

		var req agent.BehaviorRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "agent_step", req.Node)
		require.Equal(t, "gpt-4o", req.Model)

		resp := agent.BehaviorResponse{
			Output:   map[string]any{"result": 42.0},
			Cost:     0.025,
			Duration: "150ms",
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	graphYAML := `apiVersion: graph/v1
id: agent-graph
nodes:
  - id: agent_step
    type: passthrough
  - id: done
    type: passthrough
edges:
  - from: agent_step
    to: done
`

	simYAML := `apiVersion: sim/v1
id: agent-sim
graph: ./graph.yaml
worlds:
  base:
    market: "US"
behaviors:
  agent_step:
    agent:
      endpoint: ` + server.URL + `
      model: "gpt-4o"
      headers:
        Authorization: "Bearer ${AGENT_API_TOKEN}"
        X-Custom: "custom-val"
objective:
  name: obj
  maximize: mean
  value: "-run.cost"
`

	dir := t.TempDir()
	require.NoError(t, osWrite(dir, "graph.yaml", graphYAML))
	def, err := model.ParseSimModel([]byte(simYAML), engine.FormatYAML)
	require.NoError(t, err)

	m, errs := model.Compile(def, dir, compileRegistry(t))
	require.Nil(t, errs)

	w := sim.World{ID: "base", Params: m.Def.Worlds["base"]}
	s := sampler.NewMonteCarlo(42)

	// First execution (Cache miss)
	rec1, err := kernel.RunEpisode(context.Background(), m, w, fixedPolicy{}, s, kernel.Options{Replication: 0})
	require.NoError(t, err)
	require.Equal(t, engine.StatusSucceeded, rec1.FinalStatus)
	require.InDelta(t, 0.025, rec1.Cost, 1e-9)
	require.Equal(t, 150*time.Millisecond, rec1.Duration)
	require.Equal(t, int64(1), atomic.LoadInt64(&callCount))

	// Second execution with identical replication (Cache hit: zero external HTTP calls and identical results)
	rec2, err := kernel.RunEpisode(context.Background(), m, w, fixedPolicy{}, sampler.NewMonteCarlo(42), kernel.Options{Replication: 0})
	require.NoError(t, err)
	require.Equal(t, rec1.FinalStatus, rec2.FinalStatus)
	require.Equal(t, rec1.Cost, rec2.Cost)
	require.Equal(t, rec1.Duration, rec2.Duration)
	require.Equal(t, rec1.Objective, rec2.Objective)
	require.Equal(t, int64(1), atomic.LoadInt64(&callCount), "cached rerun must make 0 external HTTP calls")

	// Third execution with different replication index (Cache miss)
	rec3, err := kernel.RunEpisode(context.Background(), m, w, fixedPolicy{}, sampler.NewMonteCarlo(43), kernel.Options{Replication: 1})
	require.NoError(t, err)
	require.Equal(t, int64(2), atomic.LoadInt64(&callCount), "different replication must make a new call")
	require.Equal(t, rec1.Cost, rec3.Cost)
}

func TestAgentDecision_OptionSelectionAndCache(t *testing.T) {
	agent.GlobalCache.Clear()
	agent.ResetClients()

	var callCount int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&callCount, 1)

		var req agent.DecisionRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "decide", req.Decision)
		require.Equal(t, []string{"approve", "reject"}, req.Options)
		require.Equal(t, "agent-decision-model", req.Model)

		resp := agent.DecisionResponse{
			Choose: "reject",
			Cost:   0.005,
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	graphYAML := `apiVersion: graph/v1
id: decision-graph
nodes:
  - id: start
    type: passthrough
  - id: decide
    type: decision
    config:
      options: ["approve", "reject"]
      observes: []
  - id: approve
    type: passthrough
  - id: reject
    type: passthrough
edges:
  - from: start
    to: decide
  - from: decide
    to: approve
    condition: "nodes.decide.output.choice == 'approve'"
  - from: decide
    to: reject
    condition: "nodes.decide.output.choice == 'reject'"
`

	simYAML := `apiVersion: sim/v1
id: decision-sim
graph: ./graph.yaml
worlds:
  w: {}
objective:
  name: obj
  maximize: mean
  value: "-run.cost"
`

	policyYAML := `apiVersion: policy/v1
id: agent-policy
version: v1
decisions:
  decide:
    agent:
      endpoint: ` + server.URL + `
      model: "agent-decision-model"
`

	dir := t.TempDir()
	require.NoError(t, osWrite(dir, "graph.yaml", graphYAML))
	simDef, err := model.ParseSimModel([]byte(simYAML), engine.FormatYAML)
	require.NoError(t, err)

	m, errs := model.Compile(simDef, dir, compileRegistry(t))
	require.Nil(t, errs)

	polDef, err := model.ParsePolicy([]byte(policyYAML), engine.FormatYAML)
	require.NoError(t, err)

	p, perrs := model.CompilePolicy(polDef, m)
	require.Nil(t, perrs)

	w := sim.World{ID: "w", Params: m.Def.Worlds["w"]}

	// First execution (Cache miss)
	rec1, err := kernel.RunEpisode(context.Background(), m, w, p, sampler.NewMonteCarlo(100), kernel.Options{Replication: 0})
	require.NoError(t, err)
	require.Equal(t, engine.StatusSucceeded, rec1.FinalStatus)
	require.Equal(t, "reject", rec1.Decisions["decide"])
	require.Contains(t, rec1.Path, engine.NodeID("reject"))
	require.NotContains(t, rec1.Path, engine.NodeID("approve"))
	require.Equal(t, int64(1), atomic.LoadInt64(&callCount))

	// Second execution with same replication (Cache hit: zero external HTTP calls)
	rec2, err := kernel.RunEpisode(context.Background(), m, w, p, sampler.NewMonteCarlo(100), kernel.Options{Replication: 0})
	require.NoError(t, err)
	require.Equal(t, rec1.Decisions, rec2.Decisions)
	require.Equal(t, rec1.Path, rec2.Path)
	require.Equal(t, int64(1), atomic.LoadInt64(&callCount), "cached rerun must make 0 external HTTP calls")
}

func TestAgentGuardrails_MaxRequestsExceeded(t *testing.T) {
	agent.GlobalCache.Clear()
	agent.ResetClients()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := agent.BehaviorResponse{
			Output: map[string]any{"v": 1},
			Cost:   0.01,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	graphYAML := `apiVersion: graph/v1
id: guardrail-graph
nodes:
  - id: step
    type: passthrough
`

	simYAML := `apiVersion: sim/v1
id: guardrail-sim
graph: ./graph.yaml
worlds:
  w: {}
behaviors:
  step:
    agent:
      endpoint: ` + server.URL + `
      max_requests: 1
objective:
  name: obj
  maximize: mean
  value: "1.0"
`

	dir := t.TempDir()
	require.NoError(t, osWrite(dir, "graph.yaml", graphYAML))
	simDef, err := model.ParseSimModel([]byte(simYAML), engine.FormatYAML)
	require.NoError(t, err)

	m, errs := model.Compile(simDef, dir, compileRegistry(t))
	require.Nil(t, errs)

	w := sim.World{ID: "w", Params: m.Def.Worlds["w"]}

	// Call 1: succeeds
	rec1, err := kernel.RunEpisode(context.Background(), m, w, fixedPolicy{}, sampler.NewMonteCarlo(1), kernel.Options{Replication: 0})
	require.NoError(t, err)
	require.Equal(t, engine.StatusSucceeded, rec1.FinalStatus)

	// Call 2 with different replication: exceeds max_requests: 1 guardrail
	_, err = kernel.RunEpisode(context.Background(), m, w, fixedPolicy{}, sampler.NewMonteCarlo(2), kernel.Options{Replication: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max requests guardrail exceeded")
}

func TestAgentGuardrails_MaxCostExceeded(t *testing.T) {
	agent.GlobalCache.Clear()
	agent.ResetClients()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := agent.BehaviorResponse{
			Output: map[string]any{"v": 1},
			Cost:   0.05,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	graphYAML := `apiVersion: graph/v1
id: guardrail-cost-graph
nodes:
  - id: step
    type: passthrough
`

	simYAML := `apiVersion: sim/v1
id: guardrail-cost-sim
graph: ./graph.yaml
worlds:
  w: {}
behaviors:
  step:
    agent:
      endpoint: ` + server.URL + `
      max_cost: 0.03
objective:
  name: obj
  maximize: mean
  value: "1.0"
`

	dir := t.TempDir()
	require.NoError(t, osWrite(dir, "graph.yaml", graphYAML))
	simDef, err := model.ParseSimModel([]byte(simYAML), engine.FormatYAML)
	require.NoError(t, err)

	m, errs := model.Compile(simDef, dir, compileRegistry(t))
	require.Nil(t, errs)

	w := sim.World{ID: "w", Params: m.Def.Worlds["w"]}

	// Call 1 returns cost 0.05 which exceeds max_cost 0.03
	_, err = kernel.RunEpisode(context.Background(), m, w, fixedPolicy{}, sampler.NewMonteCarlo(1), kernel.Options{Replication: 0})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max cost guardrail exceeded")
}

func osWrite(dir, file, content string) error {
	return os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600)
}
