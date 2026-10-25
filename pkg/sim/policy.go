package sim

import (
	"context"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

// Observation represents the observable state available to a policy when making a decision.
// Keys are observed paths (e.g., "ai_score.output.band").
// A node that did not run maps to a nil value.
type Observation map[string]any

// Policy represents a strategy for making decisions within a simulation.
type Policy interface {
	// ID returns the unique identifier for the policy.
	ID() string

	// Describe returns a human-readable description of the policy.
	Describe() string

	// Choose selects an option from the available options based on the observation.
	Choose(ctx context.Context, decision engine.NodeID, obs Observation, options []string) (string, error)
}

type replicationContextKey struct{}

// WithReplication wraps ctx with the given replication index.
func WithReplication(ctx context.Context, rep uint64) context.Context {
	return context.WithValue(ctx, replicationContextKey{}, rep)
}

// ReplicationFromContext returns the replication index stored in ctx, or 0.
func ReplicationFromContext(ctx context.Context) uint64 {
	if v, ok := ctx.Value(replicationContextKey{}).(uint64); ok {
		return v
	}
	return 0
}
