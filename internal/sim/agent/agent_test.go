// Package agent tests external agent adapters, cache, and guardrails.
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_ExpandHeaders(t *testing.T) {
	t.Setenv("TEST_API_KEY", "secret-key")
	config := &model.AgentConfig{
		Endpoint: "http://example.com",
		Headers: map[string]string{
			"Authorization": "Bearer ${TEST_API_KEY}",
			"X-Static":      "static-value",
		},
	}
	client := NewClient(config)
	headers := client.ExpandHeaders()
	assert.Equal(t, "Bearer secret-key", headers["Authorization"])
	assert.Equal(t, "static-value", headers["X-Static"])
}

func TestGuardrail(t *testing.T) {
	g := NewGuardrail(2, 0.05)
	assert.NoError(t, g.AddRequest(0.02))
	assert.NoError(t, g.AddRequest(0.01))
	err := g.AddRequest(0.01)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "max requests guardrail exceeded")

	g2 := NewGuardrail(10, 0.05)
	assert.NoError(t, g2.AddRequest(0.03))
	err = g2.AddRequest(0.03)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "max cost guardrail exceeded")
}

func TestGenerateInputHash(t *testing.T) {
	req1 := &BehaviorRequest{
		Node:   "nodeA",
		Inputs: map[string]any{"a": 1, "b": 2},
	}
	req2 := &BehaviorRequest{
		Node:   "nodeA",
		Inputs: map[string]any{"b": 2, "a": 1},
	}
	h1, _ := GenerateInputHash(req1)
	h2, _ := GenerateInputHash(req2)
	assert.Equal(t, h1, h2)
}

func TestClientCall_BehaviorCacheAndGuardrail(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		resp := BehaviorResponse{
			Output:   map[string]any{"result": "success"},
			Cost:     0.01,
			Duration: "1s",
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	config := &model.AgentConfig{
		Endpoint:    server.URL,
		MaxRequests: 2,
	}
	client := NewClient(config)
	req := &BehaviorRequest{Node: "test"}

	cacheKey := CacheKey{InputHash: "hash1", Model: "gpt", Replication: 1}
	GlobalCache.Set(cacheKey, BehaviorResponse{
		Output: map[string]any{"result": "cached"},
		Cost:   0.0,
	})

	var cachedResp BehaviorResponse
	ok := GlobalCache.Get(cacheKey, &cachedResp)
	assert.True(t, ok)
	assert.Equal(t, "cached", cachedResp.Output["result"])

	// Real call
	resp, err := client.CallBehavior(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, "success", resp.Output["result"])
	assert.Equal(t, 1, callCount)
}

func TestClientCall_Decision(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req DecisionRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		resp := DecisionResponse{
			Choose: req.Options[1], // select second option
			Cost:   0.02,
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	config := &model.AgentConfig{
		Endpoint: server.URL,
	}
	client := NewClient(config)
	req := &DecisionRequest{
		Decision: "decision_node",
		Options:  []string{"opt1", "opt2", "opt3"},
		Observed: map[string]any{"key": "value"},
	}

	resp, err := client.CallDecision(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, "opt2", resp.Choose)
	assert.Equal(t, 0.02, resp.Cost)
	assert.Equal(t, 1, callCount)
}
