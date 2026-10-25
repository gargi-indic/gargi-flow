// Package agent provides HTTP client adapters, guardrails, and deterministic response caching for AI agents.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
)

var (
	clientsMu sync.Mutex
	clients   = make(map[*model.AgentConfig]*Client)
)

// ClientFor returns a cached or newly created Client for config.
func ClientFor(config *model.AgentConfig) *Client {
	if config == nil {
		return nil
	}
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[config]; ok {
		return c
	}
	c := NewClient(config)
	clients[config] = c
	return c
}

// ResetClients clears all cached clients.
func ResetClients() {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	clients = make(map[*model.AgentConfig]*Client)
}

// Client is a generic HTTP client adapter for AI agents.
type Client struct {
	config    *model.AgentConfig
	client    *http.Client
	guardrail *Guardrail
}

// NewClient creates a new agent client.
func NewClient(config *model.AgentConfig) *Client {
	c := &http.Client{}
	if config.Timeout != "" {
		if d, err := time.ParseDuration(config.Timeout); err == nil {
			c.Timeout = d
		}
	}
	var g *Guardrail
	if config.MaxRequests > 0 || config.MaxCost > 0 {
		g = NewGuardrail(config.MaxRequests, config.MaxCost)
	}
	return &Client{
		config:    config,
		client:    c,
		guardrail: g,
	}
}

// Guardrail returns the guardrail associated with the client.
func (c *Client) Guardrail() *Guardrail {
	return c.guardrail
}

// ExpandHeaders returns the configured headers with environment variables expanded.
func (c *Client) ExpandHeaders() map[string]string {
	headers := make(map[string]string)
	for k, v := range c.config.Headers {
		headers[k] = os.ExpandEnv(v)
	}
	return headers
}

// BehaviorRequest represents the payload sent for a behavior node.
type BehaviorRequest struct {
	Node         string         `json:"node"`
	Inputs       map[string]any `json:"inputs"`
	World        map[string]any `json:"world"`
	Model        string         `json:"model,omitempty"`
	SystemPrompt string         `json:"system_prompt,omitempty"`
}

// BehaviorResponse represents the payload received for a behavior node.
type BehaviorResponse struct {
	Output   map[string]any `json:"output"`
	Cost     float64        `json:"cost"`
	Duration string         `json:"duration"`
}

// DecisionRequest represents the payload sent for a decision node.
type DecisionRequest struct {
	Decision     string         `json:"decision"`
	Options      []string       `json:"options"`
	Observed     map[string]any `json:"observed"`
	Model        string         `json:"model,omitempty"`
	SystemPrompt string         `json:"system_prompt,omitempty"`
}

// DecisionResponse represents the payload received for a decision node.
type DecisionResponse struct {
	Choose string  `json:"choose"`
	Cost   float64 `json:"cost"`
}

// CallBehavior sends an HTTP request to an agent endpoint for a behavior node.
func (c *Client) CallBehavior(ctx context.Context, req *BehaviorRequest) (*BehaviorResponse, error) {
	if c.config.Model != "" && req.Model == "" {
		req.Model = c.config.Model
	}
	if c.config.SystemPrompt != "" && req.SystemPrompt == "" {
		req.SystemPrompt = c.config.SystemPrompt
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal behavior request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create behavior request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.ExpandHeaders() {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do behavior request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("agent returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var bResp BehaviorResponse
	if err := json.NewDecoder(resp.Body).Decode(&bResp); err != nil {
		return nil, fmt.Errorf("decode behavior response: %w", err)
	}
	return &bResp, nil
}

// CallDecision sends an HTTP request to an agent endpoint for a decision node.
func (c *Client) CallDecision(ctx context.Context, req *DecisionRequest) (*DecisionResponse, error) {
	if c.config.Model != "" && req.Model == "" {
		req.Model = c.config.Model
	}
	if c.config.SystemPrompt != "" && req.SystemPrompt == "" {
		req.SystemPrompt = c.config.SystemPrompt
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal decision request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create decision request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.ExpandHeaders() {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do decision request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("agent returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var dResp DecisionResponse
	if err := json.NewDecoder(resp.Body).Decode(&dResp); err != nil {
		return nil, fmt.Errorf("decode decision response: %w", err)
	}
	return &dResp, nil
}
