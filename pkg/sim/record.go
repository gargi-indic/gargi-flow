package sim

import (
	"time"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

// RunRecord stores the outcome of a single simulation run.
type RunRecord struct {
	// World is the identifier of the simulated world.
	World WorldID `json:"world"`
	// PolicyID is the identifier of the policy used for decisions.
	PolicyID string `json:"policyId"`
	// Replication is the index of this simulation run within an experiment.
	Replication int `json:"replication"`
	// EntityID is the entity identifier for Mode B arrivals.
	EntityID string `json:"entityId,omitempty"`
	// ArrivalTime is the virtual timestamp when the entity arrived (Mode B).
	ArrivalTime time.Duration `json:"arrivalTime,omitempty"`
	// QueueWaitTime is the total time spent queued waiting for shared resources (Mode B).
	QueueWaitTime time.Duration `json:"queueWaitTime,omitempty"`
	// Weight is 1 for Monte Carlo, or the branch probability for exact enumeration.
	Weight float64 `json:"weight"`
	// Objective is the overall evaluated objective value of this run.
	Objective float64 `json:"objective"`
	// Cost is the total accumulated cost across all nodes.
	Cost float64 `json:"cost"`
	// Duration is the total simulated time for this run.
	Duration time.Duration `json:"duration"`
	// Abandoned indicates if the run was abandoned early.
	Abandoned bool `json:"abandoned"`
	// Path is the sequence of nodes executed during the run.
	Path []engine.NodeID `json:"path"`
	// Decisions maps decision nodes to the option chosen by the policy.
	Decisions map[engine.NodeID]string `json:"decisions"`
	// Constraints maps constraint names to whether they were satisfied.
	Constraints map[string]bool `json:"constraints"`
	// FinalStatus is the overall execution status at the end of the run.
	FinalStatus engine.Status `json:"finalStatus"`
	// Metrics maps every metric declared by the model (sim/v1 metrics) to
	// its value in this run. It is nil when the model declares no metrics.
	// The built-in metrics (objective, cost, duration) are the fields above.
	Metrics map[string]float64 `json:"metrics,omitempty"`
	// Events maps the name of every prob(...) aggregate requirement of the
	// model (sim/v1 objective.requirements) to whether its per-run condition
	// held in this run. It is derived data for the requirement's probability
	// and is not serialized; it is nil when the model has no such
	// requirement.
	Events map[string]bool `json:"-"`
	// Started lists the indexes, in the model graph's nodes list, of the
	// nodes that started (moved to Running) in this run, in start order.
	// It is derived data for the path heatmap, is not serialized, and is
	// only recorded when the kernel is asked to trace the run.
	Started []int `json:"-"`
	// Edges lists the indexes, in the model graph's edges list, of the
	// edges traversed in this run: when a node started, every incoming edge
	// whose source had succeeded and whose condition held. It is derived
	// data for the path heatmap, is not serialized, and is only recorded
	// when the kernel is asked to trace the run.
	Edges []int `json:"-"`
}

// ReplicationRecord stores the outcome of a Mode B simulation replication across all arrivals.
type ReplicationRecord struct {
	// World is the identifier of the simulated world.
	World WorldID `json:"world"`
	// PolicyID is the identifier of the policy used for decisions.
	PolicyID string `json:"policyId"`
	// Replication is the index of this simulation run within an experiment.
	Replication int `json:"replication"`
	// Horizon is the total virtual duration of this replication timeline.
	Horizon time.Duration `json:"horizon"`
	// Arrivals is the total number of entities that arrived during the horizon.
	Arrivals int `json:"arrivals"`
	// Completed is the number of entities that reached terminal success.
	Completed int `json:"completed"`
	// Abandoned is the number of entities that abandoned due to patience/timeout.
	Abandoned int `json:"abandoned"`
	// MeanQueueWait is the average queue wait time across all arrived entities.
	MeanQueueWait time.Duration `json:"meanQueueWait"`
	// MaxQueueWait is the maximum queue wait time observed for any entity.
	MaxQueueWait time.Duration `json:"maxQueueWait"`
	// ResourceUtil maps each resource name to its time-averaged utilization fraction (0.0 to 1.0).
	ResourceUtil map[string]float64 `json:"resourceUtil"`
	// Entities contains the per-entity RunRecord for each arrived entity.
	Entities []RunRecord `json:"entities,omitempty"`
}
