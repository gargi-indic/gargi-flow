package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/mcp"
	"github.com/gargi-indic/gargi-flow/internal/simbackend"
	sdk_mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// newWorkspaceSession connects an SDK client over in-memory transports to a
// server backed by simbackend.NewLocal().
func newWorkspaceSession(t *testing.T) *sdk_mcp.ClientSession {
	t.Helper()
	s := mcp.NewServer(mcp.Options{Backend: simbackend.NewLocal(), Version: "test"})
	clientTransport, serverTransport := sdk_mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		if err := s.Run(ctx, serverTransport); err != nil && !errors.Is(err, context.Canceled) {
			panic(err)
		}
	}()
	c := sdk_mcp.NewClient(&sdk_mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := c.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type issue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// call calls tool name and decodes its JSON text into out. It returns
// whether the result is a tool error.
func call(t *testing.T, s *sdk_mcp.ClientSession, name string, args map[string]any, out any) bool {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk_mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(*sdk_mcp.TextContent)
	require.True(t, ok)
	if out != nil {
		require.NoError(t, json.Unmarshal([]byte(text.Text), out), text.Text)
	}
	return res.IsError
}

// mustOK calls tool name, requires a successful result and returns it.
func mustOK(t *testing.T, s *sdk_mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	require.False(t, call(t, s, name, args, &out), "%s: %v", name, out)
	return out
}

type patchResult struct {
	Applied int     `json:"applied"`
	Errors  []issue `json:"errors"`
}

func patch(t *testing.T, s *sdk_mcp.ClientSession, id string, ops ...map[string]any) patchResult {
	t.Helper()
	var out patchResult
	isErr := call(t, s, "patch_model", map[string]any{"id": id, "ops": ops}, &out)
	require.False(t, isErr, "patch_model: %+v", out)
	require.Equal(t, len(ops), out.Applied)
	require.NotNil(t, out.Errors, "errors is a list, never null")
	return out
}

func validate(t *testing.T, s *sdk_mcp.ClientSession, id string) []issue {
	t.Helper()
	var out struct {
		Errors []issue `json:"errors"`
	}
	require.False(t, call(t, s, "validate_model", map[string]any{"id": id}, &out))
	require.NotNil(t, out.Errors)
	return out.Errors
}

// readModel reads gargi://models/{id}.
func readModel(t *testing.T, s *sdk_mcp.ClientSession, id string) (string, map[string]any) {
	t.Helper()
	res, err := s.ReadResource(context.Background(), &sdk_mcp.ReadResourceParams{URI: "gargi://models/" + id})
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	require.Equal(t, "application/json", res.Contents[0].MIMEType)
	var docs map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Contents[0].Text), &docs))
	return res.Contents[0].Text, docs
}

func parseYAML(t *testing.T, data string) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(data), &doc))
	return doc
}

func readExample(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "simulation", "loan", name))
	require.NoError(t, err)
	return parseYAML(t, string(data))
}

type m = map[string]any

func TestWorkspace_BuildLoanFromEmpty(t *testing.T) {
	s := newWorkspaceSession(t)

	created := mustOK(t, s, "create_model", m{"id": "loan", "template": "empty"})
	require.Equal(t, "loan", created["id"])
	require.Equal(t, []any{"graph.yaml", "sim.yaml"}, created["files"])

	// The sim documents first: behaviors name nodes the graph does not have
	// yet, so validation reports errors to repair.
	res := patch(t, s, "loan",
		m{"op": "set_world_param", "world": "base", "param": "p_bad", "value": 0.20},
		m{"op": "set_world_param", "world": "base", "param": "loss", "value": 4000},
		m{"op": "set_world_param", "world": "recession", "param": "p_bad", "value": 0.35},
		m{"op": "set_world_param", "world": "recession", "param": "loss", "value": 6000},
		m{"op": "set_latent", "name": "bad", "spec": m{"bernoulli": m{"p": "world.p_bad"}}},
		m{"op": "set_behavior", "node": "ai_score", "spec": m{
			"cost":   20,
			"output": m{"band": m{"choice": m{"low": "latent.bad ? 0.3 : 0.9", "high": "latent.bad ? 0.7 : 0.1"}}},
		}},
		m{"op": "set_behavior", "node": "human_review", "spec": m{
			"cost":     150,
			"duration": "48h",
			"abandon":  m{"p": "latent.bad ? 0.0 : 0.3"},
			"output":   m{"result": m{"choice": m{"pass": "latent.bad ? 0.10 : 0.95", "fail": "latent.bad ? 0.90 : 0.05"}}},
		}},
		m{"op": "set_objective", "spec": m{
			"name":     "profit",
			"value":    "-run.cost + (nodes.approve.status == 'Succeeded' ? (latent.bad ? -double(world.loss) : 1000.0) : 0.0)",
			"maximize": "mean",
		}},
	)
	require.NotEmpty(t, res.Errors)

	// Then the graph.
	res = patch(t, s, "loan",
		m{"op": "add_node", "id": "screen", "type": "decision"},
		m{"op": "add_node", "id": "ai_score", "type": "http_call", "config": m{"url": "https://scoring.example.internal/v1/score", "method": "POST"}},
		m{"op": "add_node", "id": "route", "type": "decision", "joinMode": "any_of"},
		m{"op": "add_node", "id": "human_review", "type": "wait_for_signal", "config": m{"signal": "review_done"}},
		m{"op": "add_node", "id": "final", "type": "decision"},
		m{"op": "add_node", "id": "approve", "type": "passthrough", "joinMode": "any_of"},
		m{"op": "add_node", "id": "reject", "type": "passthrough", "joinMode": "any_of"},
		m{"op": "set_decision", "node": "screen", "options": []string{"score", "skip"}, "observes": []string{}},
		m{"op": "set_decision", "node": "route", "options": []string{"approve", "reject", "review"}, "observes": []string{"screen.output.choice", "ai_score.output.band"}},
		m{"op": "set_decision", "node": "final", "options": []string{"approve", "reject"}, "observes": []string{"screen.output.choice", "ai_score.output.band", "human_review.output.result"}},
		m{"op": "add_edge", "from": "screen", "to": "ai_score", "condition": "upstream.output.choice == 'score'"},
		m{"op": "add_edge", "from": "screen", "to": "route", "condition": "upstream.output.choice == 'skip'"},
		m{"op": "add_edge", "from": "ai_score", "to": "route"},
		m{"op": "add_edge", "from": "route", "to": "human_review", "condition": "upstream.output.choice == 'review'"},
		m{"op": "add_edge", "from": "human_review", "to": "final"},
		m{"op": "add_edge", "from": "route", "to": "approve", "condition": "upstream.output.choice == 'approve'"},
		m{"op": "add_edge", "from": "final", "to": "approve", "condition": "upstream.output.choice == 'approve'"},
		m{"op": "add_edge", "from": "route", "to": "reject", "condition": "upstream.output.choice == 'reject'"},
		m{"op": "add_edge", "from": "final", "to": "reject", "condition": "upstream.output.choice == 'reject'"},
	)
	require.Empty(t, res.Errors)
	require.Empty(t, validate(t, s, "loan"))

	_, docs := readModel(t, s, "loan")
	require.Equal(t, map[string]any{}, docs["policies"])

	wantSim := readExample(t, "sim.yaml")
	wantSim["id"] = "loan"
	require.Equal(t, "./graph.yaml", wantSim["graph"])
	require.Equal(t, wantSim, parseYAML(t, docs["sim.yaml"].(string)))

	wantGraph := readExample(t, "graph.yaml")
	wantGraph["id"] = "loan"
	require.Equal(t, wantGraph, parseYAML(t, docs["graph.yaml"].(string)))
}

func TestWorkspace_RepairFlow(t *testing.T) {
	s := newWorkspaceSession(t)
	created := mustOK(t, s, "create_model", m{"id": "loan", "template": "loan-underwriting"})
	require.Equal(t, []any{"graph.yaml", "policies/recession-v1.yaml", "sim.yaml"}, created["files"])
	require.Empty(t, validate(t, s, "loan"))

	res := patch(t, s, "loan", m{
		"op": "set_decision", "node": "route", "options": []string{"approve", "reject", "review"},
		"observes": []string{"screen.output.choice", "ai_score.output.band", "human_review.output.result"},
	})
	require.Len(t, res.Errors, 1, "%+v", res.Errors)
	got := res.Errors[0]
	require.Equal(t, "$.graph:$.nodes[2].config.observes[2]", got.Path)
	require.Equal(t, "observes_non_ancestor", got.Code)
	require.Contains(t, got.Message, `"human_review" is not a strict ancestor of "route"`)
	require.NotEmpty(t, got.Hint)
	require.Equal(t, res.Errors, validate(t, s, "loan"))

	res = patch(t, s, "loan", m{
		"op": "set_decision", "node": "route", "options": []string{"approve", "reject", "review"},
		"observes": []string{"screen.output.choice", "ai_score.output.band"},
	})
	require.Empty(t, res.Errors)
	require.Empty(t, validate(t, s, "loan"))
}

func TestWorkspace_PatchIsAtomic(t *testing.T) {
	s := newWorkspaceSession(t)
	mustOK(t, s, "create_model", m{"id": "loan", "template": "loan-underwriting"})
	before, _ := readModel(t, s, "loan")

	var out struct {
		Errors []issue `json:"errors"`
	}
	isErr := call(t, s, "patch_model", m{"id": "loan", "ops": []m{
		{"op": "set_world_param", "world": "base", "param": "loss", "value": 1},
		{"op": "add_edge", "from": "ghost", "to": "final"},
		{"op": "remove_node", "id": "final"},
	}}, &out)
	require.True(t, isErr)
	require.Len(t, out.Errors, 1)
	require.Equal(t, "$.ops[1]", out.Errors[0].Path)
	require.Equal(t, "unresolved_reference", out.Errors[0].Code)
	require.NotEmpty(t, out.Errors[0].Hint)

	after, _ := readModel(t, s, "loan")
	require.Equal(t, before, after)

	// Unknown fields and unknown ops are rejected at their index.
	for _, op := range []m{
		{"op": "remove_world", "world": "base", "extra": true},
		{"op": "nope"},
		{"op": "set_decision", "node": "ai_score", "options": []string{"a"}},
		{"op": "add_node", "id": "screen", "type": "decision"},
	} {
		isErr := call(t, s, "patch_model", m{"id": "loan", "ops": []m{op}}, &out)
		require.True(t, isErr, "%v", op)
		require.Equal(t, "$.ops[0]", out.Errors[0].Path, "%v", op)
		require.Equal(t, "invalid_value", out.Errors[0].Code, "%v", op)
		require.NotEmpty(t, out.Errors[0].Hint, "%v", op)
	}
	after, _ = readModel(t, s, "loan")
	require.Equal(t, before, after)

	isErr = call(t, s, "patch_model", m{"id": "missing", "ops": []m{{"op": "remove_world", "world": "base"}}}, &out)
	require.True(t, isErr)
	require.Equal(t, "$", out.Errors[0].Path)
	require.Equal(t, "unresolved_reference", out.Errors[0].Code)
}

func TestWorkspace_ToolErrors(t *testing.T) {
	s := newWorkspaceSession(t)
	mustOK(t, s, "create_model", m{"id": "loan", "template": "empty"})

	for _, args := range []m{
		{"id": "loan", "template": "empty"},                       // duplicate id
		{"id": "Loan", "template": "empty"},                       // bad id
		{"id": "9loan", "template": "empty"},                      // bad id
		{"id": "", "template": "empty"},                           // bad id
		{"id": "other", "template": "nope"},                       // unknown template
		{"id": "other", "template": "../templates/empty"},         // unknown template
		{"id": "other", "template": "loan-underwriting/policies"}, // unknown template
	} {
		var out struct {
			Errors []issue `json:"errors"`
		}
		require.True(t, call(t, s, "create_model", args, &out), "%v", args)
		require.Len(t, out.Errors, 1)
		require.Equal(t, "$", out.Errors[0].Path, "%v", args)
	}

	var out struct {
		Errors []issue `json:"errors"`
	}
	require.True(t, call(t, s, "validate_model", m{"id": "missing"}, &out))
	require.Equal(t, "$", out.Errors[0].Path)
	require.Equal(t, "unresolved_reference", out.Errors[0].Code)

	// toolErrorf errors of the #193 tools carry the root path too.
	require.True(t, call(t, s, "get_schema", m{"kind": "unknown"}, &out))
	require.Equal(t, "$", out.Errors[0].Path)

	_, err := s.ReadResource(context.Background(), &sdk_mcp.ReadResourceParams{URI: "gargi://models/missing"})
	require.Error(t, err)
}

func TestWorkspace_PerServer(t *testing.T) {
	a, b := newWorkspaceSession(t), newWorkspaceSession(t)
	mustOK(t, a, "create_model", m{"id": "loan", "template": "empty"})
	// Another server has its own workspace.
	mustOK(t, b, "create_model", m{"id": "loan", "template": "empty"})
}
