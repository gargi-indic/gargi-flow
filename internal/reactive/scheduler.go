package reactive

import (
	"context"
	"fmt"
	"sort"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

// SchedulePlan represents the deterministic actions resulting from a scheduler evaluation step.
type SchedulePlan struct {
	ReadyNodes     []engine.NodeID
	SkippedNodes   []engine.NodeID
	ResolvedInputs map[engine.NodeID]map[string]any
}

// DispatchItem represents a single ready node and its isolated resolved inputs for parallel dispatch.
type DispatchItem struct {
	NodeID engine.NodeID
	Inputs map[string]any
}

// BatchDispatcher dispatches a batch of independent ready nodes in parallel.
type BatchDispatcher interface {
	DispatchBatch(ctx context.Context, batch []DispatchItem) error
}

// BatchDispatchFunc is an adapter to allow the use of ordinary functions as a BatchDispatcher.
type BatchDispatchFunc func(ctx context.Context, batch []DispatchItem) error

// DispatchBatch calls f(ctx, batch).
func (f BatchDispatchFunc) DispatchBatch(ctx context.Context, batch []DispatchItem) error {
	return f(ctx, batch)
}

// Scheduler drives reactive graph traversal and state progression.
type Scheduler struct {
	graph    *engine.Graph
	registry *engine.Registry
	resolver *PreconditionResolver
	emitter  *TransitionEmitter
}

// NewScheduler creates a new Scheduler.
// If resolver is nil, it initializes NewPreconditionResolver(nil).
func NewScheduler(
	g *engine.Graph,
	registry *engine.Registry,
	resolver *PreconditionResolver,
	emitter *TransitionEmitter,
) *Scheduler {
	if resolver == nil {
		resolver = NewPreconditionResolver(nil)
	}
	return &Scheduler{
		graph:    g,
		registry: registry,
		resolver: resolver,
		emitter:  emitter,
	}
}

type schedulerNodeContext struct {
	executionID engine.ExecutionID
	nodeID      engine.NodeID
	config      map[string]any
	inputs      map[string]any
	state       *engine.GraphState
	attempt     int
}

func (c *schedulerNodeContext) ExecutionID() engine.ExecutionID { return c.executionID }
func (c *schedulerNodeContext) NodeID() engine.NodeID           { return c.nodeID }
func (c *schedulerNodeContext) Config() map[string]any          { return c.config } // Graph.Node().Config() already returns deep copy
func (c *schedulerNodeContext) Inputs() map[string]any          { return deepCopyAny(c.inputs).(map[string]any) }
func (c *schedulerNodeContext) State() *engine.GraphState       { return c.state }
func (c *schedulerNodeContext) Attempt() int                    { return c.attempt }
func (c *schedulerNodeContext) PolicyID() string                { return "" }
func (c *schedulerNodeContext) PolicyVersion() string           { return "" }

// EvaluateStep evaluates all pending nodes, resolves preconditions, calls EvaluateReactivity,
// performs cascading skip propagation, and returns the deterministic SchedulePlan along with the updated GraphState.
func (s *Scheduler) EvaluateStep(
	ctx context.Context,
	currentState *engine.GraphState,
) (*SchedulePlan, *engine.GraphState, error) {
	if s == nil {
		return nil, nil, fmt.Errorf("scheduler is nil")
	}
	if s.graph == nil {
		return nil, currentState, fmt.Errorf("graph is nil")
	}
	if s.registry == nil {
		return nil, currentState, fmt.Errorf("registry is nil")
	}
	if currentState == nil {
		return nil, nil, fmt.Errorf("current state is nil")
	}

	if s.resolver == nil {
		s.resolver = NewPreconditionResolver(nil)
	}

	workingState := currentState

	readyNodes := make([]engine.NodeID, 0)
	skippedNodes := make([]engine.NodeID, 0)
	resolvedInputs := make(map[engine.NodeID]map[string]any)
	declinedReactivity := make(map[engine.NodeID]bool)

	for {
		// Gather all nodes in workingState with StatusPending not in declinedReactivity
		var candidates []engine.NodeID
		for _, ns := range workingState.NodeStates() {
			id := ns.NodeID()
			if ns.Status() == engine.StatusPending && !declinedReactivity[id] {
				candidates = append(candidates, id)
			}
		}

		sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })

		skippedInPass := false

		for _, nodeID := range candidates {
			ns := workingState.NodeState(nodeID)
			if ns == nil || ns.Status() != engine.StatusPending {
				continue
			}

			node := s.graph.Node(nodeID)
			if node == nil {
				return nil, currentState, fmt.Errorf("node %q not found in graph", nodeID)
			}

			incoming := s.graph.IncomingEdges(nodeID)
			var inputs map[string]any

			isRoot := len(incoming) == 0
			isReady := false
			isSkipped := false

			if isRoot {
				isReady = true
				inputs = make(map[string]any)
			} else {
				res, err := s.resolver.ResolveNodePreconditions(ctx, nodeID, s.graph, workingState)
				if err != nil {
					return nil, currentState, err
				}

				isSkipped = res.Skipped
				isReady = res.Ready
				inputs = res.Inputs
			}

			if isSkipped {
				oldNS := workingState.NodeState(nodeID)
				statusSkipped := engine.StatusSkipped
				mut := engine.StateMutation{
					NodeID:          nodeID,
					ExpectedVersion: workingState.Version(),
					Status:          &statusSkipped,
				}
				nextState, err := workingState.Apply(mut)
				if err != nil {
					return nil, currentState, err
				}

				if s.emitter != nil {
					if _, err := s.emitter.EmitTransition(ctx, oldNS, nextState.NodeState(nodeID), mut); err != nil {
						return nil, currentState, err
					}
				}

				workingState = nextState
				skippedNodes = append(skippedNodes, nodeID)
				skippedInPass = true
				continue
			}

			if !isReady {
				continue
			}

			if inputs == nil {
				inputs = make(map[string]any)
			}

			instance, err := s.registry.Instantiate(node.Type(), node.Config())
			if err != nil {
				return nil, currentState, err
			}

			nodeCtx := &schedulerNodeContext{
				executionID: workingState.ExecutionID(),
				nodeID:      nodeID,
				config:      node.Config(),
				inputs:      deepCopyAny(inputs).(map[string]any),
				state:       workingState,
				attempt:     1,
			}

			shouldRun, err := instance.EvaluateReactivity(ctx, nodeCtx)
			if err != nil {
				return nil, currentState, err
			}

			if !shouldRun {
				declinedReactivity[nodeID] = true
				continue
			}

			oldNS := workingState.NodeState(nodeID)
			statusReady := engine.StatusReady
			mut := engine.StateMutation{
				NodeID:          nodeID,
				ExpectedVersion: workingState.Version(),
				Status:          &statusReady,
			}
			nextState, err := workingState.Apply(mut)
			if err != nil {
				return nil, currentState, err
			}

			if s.emitter != nil {
				if _, err := s.emitter.EmitTransition(ctx, oldNS, nextState.NodeState(nodeID), mut); err != nil {
					return nil, currentState, err
				}
			}

			workingState = nextState
			readyNodes = append(readyNodes, nodeID)
			resolvedInputs[nodeID] = deepCopyMap(inputs)
		}

		if !skippedInPass {
			break
		}
	}

	sort.Slice(readyNodes, func(i, j int) bool { return readyNodes[i] < readyNodes[j] })
	sort.Slice(skippedNodes, func(i, j int) bool { return skippedNodes[i] < skippedNodes[j] })

	return &SchedulePlan{
		ReadyNodes:     readyNodes,
		SkippedNodes:   skippedNodes,
		ResolvedInputs: resolvedInputs,
	}, workingState, nil
}

// BuildDispatchBatch constructs a deterministic slice of DispatchItem (ordered by plan.ReadyNodes)
// with deep-copied Inputs maps so independent ready nodes can be dispatched in parallel without shared-map races.
func (s *Scheduler) BuildDispatchBatch(plan *SchedulePlan) []DispatchItem {
	if plan == nil || len(plan.ReadyNodes) == 0 {
		return nil
	}

	batch := make([]DispatchItem, 0, len(plan.ReadyNodes))
	for _, nodeID := range plan.ReadyNodes {
		inputs := plan.ResolvedInputs[nodeID]
		batch = append(batch, DispatchItem{
			NodeID: nodeID,
			Inputs: deepCopyMap(inputs),
		})
	}
	return batch
}

// DispatchReady dispatches all independent ready nodes in plan as a parallel batch via dispatcher.
// If plan is nil, len(plan.ReadyNodes) == 0, or dispatcher is nil, it is a no-op returning nil.
func (s *Scheduler) DispatchReady(
	ctx context.Context,
	plan *SchedulePlan,
	dispatcher BatchDispatcher,
) error {
	if plan == nil || len(plan.ReadyNodes) == 0 || dispatcher == nil {
		return nil
	}

	batch := s.BuildDispatchBatch(plan)
	return dispatcher.DispatchBatch(ctx, batch)
}

func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = deepCopyAny(v)
	}
	return cp
}
