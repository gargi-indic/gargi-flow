package sim

import (
	"context"
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

// BehaviorInput provides the inputs for a simulated node behavior.
type BehaviorInput struct {
	// NodeID is the identifier of the node being simulated.
	NodeID engine.NodeID
	// Inputs holds the mapped input data for the node.
	Inputs map[string]any
	// Latents holds any hidden variables or state injected by the environment.
	Latents map[string]any
	// World is the simulation world in which this behavior occurs.
	World World
	// Replication is the index of this simulation run within an experiment.
	Replication uint64
}

// BehaviorOutcome represents the simulated result of a node execution.
type BehaviorOutcome struct {
	// Output contains the resulting output data.
	Output map[string]any
	// Cost is the estimated monetary or resource cost of the execution.
	Cost float64
	// Duration is the estimated time taken for the execution.
	Duration time.Duration
	// Abandoned indicates if the execution was abandoned or cancelled.
	Abandoned bool
	// Err is any error that occurred during the execution.
	Err error
}

// Behavior represents the logic to simulate a node's execution within a given world.
type Behavior interface {
	// Sample executes the behavior using the given sampler and input.
	Sample(ctx context.Context, s Sampler, in BehaviorInput) (BehaviorOutcome, error)
}
