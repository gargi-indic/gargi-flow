package agent

import (
	"context"
	"fmt"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

func init() {
	model.AgentDecisionChooser = ChooseDecision
}

// ChooseDecision evaluates an agent decision using cache and guardrails.
func ChooseDecision(ctx context.Context, cfg *model.AgentConfig, decision string, options []string, observed map[string]any, replication uint64) (string, error) {
	client := ClientFor(cfg)

	obs := observed
	if obs == nil {
		obs = make(map[string]any)
	}

	req := &DecisionRequest{
		Decision: decision,
		Options:  options,
		Observed: obs,
		Model:    cfg.Model,
	}

	hash, err := GenerateInputHash(req)
	if err != nil {
		return "", fmt.Errorf("generate input hash: %w", err)
	}

	cacheKey := CacheKey{
		InputHash:   hash,
		Model:       cfg.Model,
		Replication: replication,
	}

	var resp DecisionResponse
	if GlobalCache.Get(cacheKey, &resp) {
		// Cache hit: zero external HTTP calls and zero spend
		return validateOption(resp.Choose, options)
	}

	// Cache miss: call agent HTTP endpoint
	r, err := client.CallDecision(ctx, req)
	if err != nil {
		return "", err
	}

	// Check guardrails
	if g := client.Guardrail(); g != nil {
		if err := g.AddRequest(r.Cost); err != nil {
			return "", err
		}
	}

	// Record in cache
	GlobalCache.Set(cacheKey, *r)

	return validateOption(r.Choose, options)
}

func validateOption(choice string, options []string) (string, error) {
	for _, opt := range options {
		if opt == choice {
			return choice, nil
		}
	}
	return "", engine.NewNonRetryableError(fmt.Errorf("policy returned invalid option %q", choice))
}
