package mcp

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/stretchr/testify/require"
)

func loanDocs(t *testing.T) *modelDocs {
	t.Helper()
	d, err := loadTemplate("loan-underwriting")
	require.NoError(t, err)
	return d
}

// run applies the JSON ops to d.
func run(t *testing.T, d *modelDocs, ops ...string) (int, *opError) {
	t.Helper()
	raw := make([]json.RawMessage, len(ops))
	for i, op := range ops {
		raw[i] = json.RawMessage(op)
	}
	return applyOps(d, raw)
}

func nodeIDs(d *modelDocs) []string {
	var ids []string
	for _, n := range list(d.graph, "nodes") {
		ids = append(ids, n.(map[string]any)["id"].(string))
	}
	return ids
}

func TestOps_Apply(t *testing.T) {
	d := loanDocs(t)
	i, err := run(t, d,
		`{"op":"set_world_param","world":"boom","param":"loss","value":2500}`,
		`{"op":"set_world_param","world":"boom","param":"p_bad","value":0.1}`,
		`{"op":"remove_world","world":"recession"}`,
		`{"op":"set_latent","name":"fraud","spec":{"bernoulli":{"p":0.01}}}`,
		`{"op":"remove_latent","name":"fraud"}`,
		`{"op":"remove_behavior","node":"human_review"}`,
		`{"op":"remove_node","id":"human_review"}`,
		`{"op":"remove_edge","from":"route","to":"reject"}`,
		`{"op":"set_policy","name":"mine.yaml","document":{"apiVersion":"policy/v1"}}`,
		`{"op":"remove_policy","name":"recession-v1"}`,
	)
	require.Nil(t, err, "op %d: %v", i, err)

	worlds := d.sim["worlds"].(map[string]any)
	require.Equal(t, map[string]any{"loss": 2500, "p_bad": 0.1}, worlds["boom"])
	require.NotContains(t, worlds, "recession")
	require.NotContains(t, d.sim["latents"], "fraud")
	require.NotContains(t, d.sim["behaviors"], "human_review")
	require.NotContains(t, nodeIDs(d), "human_review")
	for _, e := range list(d.graph, "edges") {
		em := e.(map[string]any)
		require.NotEqual(t, "human_review", em["from"])
		require.NotEqual(t, "human_review", em["to"])
		require.False(t, em["from"] == "route" && em["to"] == "reject")
	}
	require.Len(t, list(d.graph, "edges"), 6) // 9 - 2 (human_review) - 1
	require.Equal(t, map[string]map[string]any{"mine": {"apiVersion": "policy/v1"}}, d.policies)
	require.Equal(t, []string{"graph.yaml", "policies/mine.yaml", "sim.yaml"}, d.files())
}

func TestOps_Errors(t *testing.T) {
	for _, tc := range []struct {
		op   string
		code engine.ErrorCode
	}{
		{`[]`, engine.CodeInvalidValue},
		{`{"op":"nope"}`, engine.CodeInvalidValue},
		{`{"world":"base"}`, engine.CodeInvalidValue},
		{`{"op":"remove_world","world":"base","x":1}`, engine.CodeInvalidValue},
		{`{"op":"set_world_param","world":"base","param":"loss"}`, engine.CodeInvalidValue},
		{`{"op":"set_world_param","world":"","param":"loss","value":1}`, engine.CodeInvalidValue},
		{`{"op":"set_world_param","world":"a b","param":"loss","value":1}`, engine.CodeInvalidValue},
		{`{"op":"remove_world","world":"ghost"}`, engine.CodeUnresolvedReference},
		{`{"op":"set_latent","name":"x","spec":3}`, engine.CodeInvalidValue},
		{`{"op":"remove_latent","name":"ghost"}`, engine.CodeUnresolvedReference},
		{`{"op":"set_behavior","node":"x"}`, engine.CodeInvalidValue},
		{`{"op":"remove_behavior","node":"ghost"}`, engine.CodeUnresolvedReference},
		{`{"op":"set_objective","spec":"profit"}`, engine.CodeInvalidValue},
		{`{"op":"add_node","id":"screen","type":"decision"}`, engine.CodeInvalidValue},
		{`{"op":"add_node","id":"x"}`, engine.CodeInvalidValue},
		{`{"op":"add_node","id":"x","type":"passthrough","config":[1]}`, engine.CodeInvalidValue},
		{`{"op":"remove_node","id":"ghost"}`, engine.CodeUnresolvedReference},
		{`{"op":"add_edge","from":"screen","to":"ghost"}`, engine.CodeUnresolvedReference},
		{`{"op":"add_edge","from":"screen","to":"route"}`, engine.CodeInvalidValue},
		{`{"op":"remove_edge","from":"route","to":"screen"}`, engine.CodeUnresolvedReference},
		{`{"op":"set_decision","node":"ghost","options":["a"]}`, engine.CodeUnresolvedReference},
		{`{"op":"set_decision","node":"approve","options":["a"]}`, engine.CodeInvalidValue},
		{`{"op":"set_decision","node":"route","options":[]}`, engine.CodeInvalidValue},
		{`{"op":"set_policy","name":"../x","document":{}}`, engine.CodeInvalidValue},
		{`{"op":"set_policy","name":"x"}`, engine.CodeInvalidValue},
		{`{"op":"remove_policy","name":"ghost"}`, engine.CodeUnresolvedReference},
	} {
		t.Run(tc.op, func(t *testing.T) {
			d := loanDocs(t)
			i, err := run(t, d, `{"op":"set_world_param","world":"base","param":"loss","value":1}`, tc.op)
			require.NotNil(t, err)
			require.Equal(t, 1, i)
			require.Equal(t, tc.code, err.code, err.message)
			require.NotEmpty(t, err.message)
		})
	}
}

func TestOps_SetDecisionDefaultsObserves(t *testing.T) {
	d := loanDocs(t)
	_, err := run(t, d, `{"op":"add_node","id":"gate","type":"decision"}`, `{"op":"set_decision","node":"gate","options":["a","b"]}`)
	require.Nil(t, err)
	nodes := list(d.graph, "nodes")
	gate := nodes[len(nodes)-1].(map[string]any)
	require.Equal(t, map[string]any{"options": []any{"a", "b"}, "observes": []any{}}, gate["config"])
}

func TestNormalizeValue(t *testing.T) {
	op, err := parseOp(json.RawMessage(`{"op":"set_latent","name":"x","spec":{"a":4000,"b":0.25,"c":[1,2.5,"s"],"d":1e3}}`))
	require.Nil(t, err)
	require.Equal(t, map[string]any{"a": 4000, "b": 0.25, "c": []any{1, 2.5, "s"}, "d": 1000.0}, op.(*setLatentOp).Spec)
}

func TestWorkspace_UpdateIsAtomicAndConcurrent(t *testing.T) {
	w := newWorkspace()
	_, err := w.create("loan", "loan-underwriting")
	require.NoError(t, err)
	before, err := w.get("loan")
	require.NoError(t, err)

	// A failing update leaves the model unchanged.
	_, err = w.update("loan", func(d *modelDocs) error {
		d.sim["id"] = "changed"
		return fmt.Errorf("boom")
	})
	require.Error(t, err)
	after, err := w.get("loan")
	require.NoError(t, err)
	require.Equal(t, before, after)

	// get returns a copy.
	after.sim["id"] = "changed"
	again, err := w.get("loan")
	require.NoError(t, err)
	require.Equal(t, "loan", again.sim["id"])

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			op := fmt.Sprintf(`{"op":"set_world_param","world":"w%d","param":"loss","value":%d}`, i, i)
			_, err := w.update("loan", func(d *modelDocs) error {
				if _, err := applyOps(d, []json.RawMessage{json.RawMessage(op)}); err != nil {
					return err
				}
				return nil
			})
			require.NoError(t, err)
			_, _ = w.get("loan")
		}()
	}
	wg.Wait()
	final, err := w.get("loan")
	require.NoError(t, err)
	require.Len(t, final.sim["worlds"], 2+16)
}

func TestRender_Deterministic(t *testing.T) {
	d := loanDocs(t)
	a, err := toBundle("loan", d)
	require.NoError(t, err)
	for range 5 {
		b, err := toBundle("loan", d.clone())
		require.NoError(t, err)
		require.Equal(t, a, b)
	}
	require.Equal(t, "yaml", a.Format)
	require.Contains(t, a.Policies, "recession-v1.yaml")
	require.Empty(t, hintFor("no_such_code"))
	require.NotEmpty(t, hintFor("observes_non_ancestor"))
}
