package kernel

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/reactive"
	"github.com/gargi-indic/gargi-flow/internal/sim/behavior"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// epoch is virtual time zero. Every event of an episode is stamped with
// epoch plus the virtual time elapsed since the episode started.
var epoch = time.Unix(0, 0).UTC()

// errAbandoned is the failure recorded on a node whose behavior abandoned.
var errAbandoned = errors.New("abandoned")

// Options configures a single simulated episode.
type Options struct {
	// Events is an optional sink that receives the same engine.Event stream
	// a production execution would emit. Event timestamps are on the virtual
	// clock: epoch (time.Unix(0, 0).UTC()) plus the elapsed virtual time. A
	// nil sink discards events. An error returned by the sink for a node
	// transition aborts the episode with that error.
	Events engine.EventSink

	// ExecutionID is the execution ID stamped on the graph state and every
	// event. It defaults to DefaultExecutionID(p.ID(), w.ID) so that the
	// event stream of an episode is reproducible.
	ExecutionID engine.ExecutionID

	// Trace records the nodes that start and the edges they start through
	// in RunRecord.Started and RunRecord.Edges (indexes into the model
	// graph's nodes and edges lists). When a node starts, each of its
	// incoming edges whose source has succeeded and whose condition holds,
	// evaluated on the graph state the scheduler made the node ready in, is
	// traversed; for an all_of join that is every incoming edge, for an
	// any_of join the satisfied ones. Edges are recorded per started node in
	// graph edge order. Tracing re-evaluates edge conditions, so it is off
	// by default.
	Trace bool

	// Replication is the index of this simulation run within an experiment.
	Replication uint64
}

// DefaultExecutionID returns the deterministic execution ID used for an
// episode of policyID in world when Options.ExecutionID is empty.
func DefaultExecutionID(policyID string, world sim.WorldID) engine.ExecutionID {
	return engine.ExecutionID("sim-" + policyID + "-" + string(world))
}

// recordingChooser adapts a sim.Policy to nodes.DecisionChooser and records
// every valid choice.
type recordingChooser struct {
	policy      sim.Policy
	decisions   map[engine.NodeID]string
	replication uint64
}

func (r *recordingChooser) Choose(ctx context.Context, decision engine.NodeID, obs map[string]any, options []string) (string, error) {
	if r.replication > 0 && sim.ReplicationFromContext(ctx) == 0 {
		ctx = sim.WithReplication(ctx, r.replication)
	}
	choice, err := r.policy.Choose(ctx, decision, obs, options)
	if err != nil {
		return "", err
	}
	for _, opt := range options {
		if opt == choice {
			r.decisions[decision] = choice
			return choice, nil
		}
	}
	return "", engine.NewNonRetryableError(fmt.Errorf("policy returned invalid option %q", choice))
}

// errNoEffects is returned if an effectful node ever reaches its real
// implementation; model.Compile requires a behavior for every such node.
var errNoEffects = errors.New("simulation never performs side effects: effectful nodes must have a behavior")

// noEffects is the HTTP client and signal source of the simulation registry.
// The scheduler instantiates every node to evaluate reactivity, so the
// effectful node types need dependencies, but they must never be used.
type noEffects struct{}

func (noEffects) Do(*http.Request) (*http.Response, error) { return nil, errNoEffects }

func (noEffects) Wait(context.Context, engine.ExecutionID, engine.NodeID, string) (map[string]any, error) {
	return nil, errNoEffects
}

type runNodeContext struct {
	execID engine.ExecutionID
	nodeID engine.NodeID
	config map[string]any
	inputs map[string]any
	state  *engine.GraphState
}

func (c *runNodeContext) ExecutionID() engine.ExecutionID { return c.execID }
func (c *runNodeContext) NodeID() engine.NodeID           { return c.nodeID }
func (c *runNodeContext) Config() map[string]any          { return c.config }
func (c *runNodeContext) Inputs() map[string]any          { return c.inputs }
func (c *runNodeContext) State() *engine.GraphState       { return c.state }
func (c *runNodeContext) Attempt() int                    { return 1 }
func (c *runNodeContext) PolicyID() string                { return "" }
func (c *runNodeContext) PolicyVersion() string           { return "" }

// episode holds the mutable state of one RunEpisode call.
type episode struct {
	m        *model.CompiledModel
	world    sim.World
	sampler  sim.Sampler
	latents  map[string]any
	execID   engine.ExecutionID
	reg      *engine.Registry
	nodeDefs map[engine.NodeID]*engine.Node
	sink     engine.EventSink
	seqGen   *reactive.SequenceGenerator
	emitter  *reactive.TransitionEmitter
	detector *reactive.TerminalDetector

	now     time.Time
	state   *engine.GraphState
	pending virtualTimeHeap
	record  *sim.RunRecord

	replication uint64

	// trace is set when Options.Trace is: it maps a node id to its index in
	// the model graph's nodes and lists its incoming edge indexes.
	trace map[engine.NodeID]traceNode
}

// traceNode is a node's index in the model graph and its incoming edges.
type traceNode struct {
	index    int
	incoming []int
}

// newTrace indexes the nodes and incoming edges of g.
func newTrace(g *engine.GraphDefinition) map[engine.NodeID]traceNode {
	t := make(map[engine.NodeID]traceNode, len(g.Nodes))
	for i, n := range g.Nodes {
		t[n.ID] = traceNode{index: i}
	}
	for i, ed := range g.Edges {
		tn := t[ed.To]
		tn.incoming = append(tn.incoming, i)
		t[ed.To] = tn
	}
	return t
}

// traverse records that nodeID starts and the incoming edges it starts
// through. It must run before the node leaves Ready, on the state the
// scheduler made it ready in.
func (e *episode) traverse(ctx context.Context, nodeID engine.NodeID) error {
	tn := e.trace[nodeID]
	e.record.Started = append(e.record.Started, tn.index)
	for _, i := range tn.incoming {
		ed := e.m.Graph.Edges[i]
		up := e.state.NodeState(ed.From)
		if up == nil || up.Status() != engine.StatusSucceeded {
			continue
		}
		ok, err := cachedConditionEvaluator{}.Evaluate(ctx, ed.Condition, up.Output(), e.state)
		if err != nil {
			return fmt.Errorf("edge %s -> %s: %w", ed.From, ed.To, err)
		}
		if ok {
			e.record.Edges = append(e.record.Edges, i)
		}
	}
	return nil
}

// RunEpisode runs one mode A episode of m in world w, with decisions made by
// p and randomness drawn from s.
//
// The episode drives the production reactive.Scheduler with a virtual clock,
// mirroring the orchestrator step loop: EvaluateStep, move every ready node to
// Running, execute it, apply its completion or failure, then let the
// TerminalDetector assess the graph. Nodes with a behavior in the model are
// sampled and complete after their sampled duration; every other node runs
// its real registry implementation (decision nodes call p) with zero
// duration. Nodes that become ready at the same virtual instant start
// together; completions are kept in a heap ordered by (virtual time, node
// ID), and all completions at the earliest instant are applied, in node ID
// order, before the graph is assessed and the scheduler runs again. Nothing
// reads the wall clock, so the same seed yields an identical RunRecord and
// event stream.
//
// Failures are never retried. A failed node is classified as critical or
// non-retryable and the graph's failure policy applies: fail_graph ends the
// episode with FinalStatus Failed, continue keeps running independent
// branches, and an onCriticalFailure of compensate ends the episode with
// FinalStatus Compensating (compensation handlers are not simulated). A
// decision whose policy returns an option the node does not offer fails that
// node.
//
// When a behavior abandons, the episode ends immediately: the node's cost
// still counts, the abandoned node and every other in-flight node fail, ready
// nodes that have not started are skipped, no further nodes run, the graph
// fails, Abandoned is set and Duration is the virtual time of abandonment.
//
// After the episode ends (normally or by abandonment) the objective and every
// constraint are evaluated on the final state with world, latent, nodes
// (status, output, errorMessage per node) and run (cost, duration in seconds,
// path, abandoned). Constraints is always a non-nil map. The model's declared
// metrics are evaluated the same way into Metrics (nil when there are none),
// and the condition of every prob() requirement into Events.
func RunEpisode(ctx context.Context, m *model.CompiledModel, w sim.World, p sim.Policy, s sim.Sampler, opts Options) (sim.RunRecord, error) {
	if m == nil || m.Graph == nil {
		return sim.RunRecord{}, errors.New("compiled model is nil")
	}
	if p == nil {
		return sim.RunRecord{}, errors.New("policy is nil")
	}
	if s == nil {
		return sim.RunRecord{}, errors.New("sampler is nil")
	}

	execID := opts.ExecutionID
	if execID == "" {
		execID = DefaultExecutionID(p.ID(), w.ID)
	}

	record := sim.RunRecord{
		World:       w.ID,
		PolicyID:    p.ID(),
		Replication: int(opts.Replication),
		Decisions:   make(map[engine.NodeID]string),
	}

	vars := map[string]any{"world": worldParams(w)}
	latents, err := behavior.SampleLatents(m, s, vars)
	if err != nil {
		return sim.RunRecord{}, err
	}
	vars["latent"] = latents

	reg := engine.NewRegistry()
	if err := nodes.RegisterDefaults(reg, nodes.Dependencies{HTTPClient: noEffects{}, Signals: noEffects{}}); err != nil {
		return sim.RunRecord{}, err
	}
	if err := nodes.RegisterDecision(reg, &recordingChooser{policy: p, decisions: record.Decisions, replication: opts.Replication}); err != nil {
		return sim.RunRecord{}, err
	}

	g, err := m.Graph.ToGraph()
	if err != nil {
		return sim.RunRecord{}, err
	}

	e := &episode{
		m:           m,
		world:       w,
		sampler:     s,
		latents:     latents,
		execID:      execID,
		reg:         reg,
		nodeDefs:    make(map[engine.NodeID]*engine.Node),
		sink:        opts.Events,
		seqGen:      reactive.NewSequenceGenerator(0),
		now:         epoch,
		record:      &record,
		replication: opts.Replication,
	}
	for _, n := range g.Nodes() {
		e.nodeDefs[n.ID()] = n
	}
	if opts.Trace {
		e.trace = newTrace(m.Graph)
	}
	clock := func() time.Time { return e.now }
	e.emitter = reactive.NewTransitionEmitter(execID, e.seqGen, clock, opts.Events)
	e.detector = reactive.NewTerminalDetector(g, m.Graph.FailurePolicy, e.emitter)
	scheduler := reactive.NewScheduler(g, reg, reactive.NewPreconditionResolver(cachedConditionEvaluator{}), e.emitter)

	if e.state, err = engine.NewGraphState(execID, g); err != nil {
		return sim.RunRecord{}, err
	}
	for _, st := range []engine.Status{engine.StatusReady, engine.StatusRunning} {
		if e.state, err = e.state.WithStatus(e.state.Version(), st); err != nil {
			return sim.RunRecord{}, err
		}
	}

	if err := e.run(ctx, scheduler); err != nil {
		return sim.RunRecord{}, err
	}

	record.Duration = e.now.Sub(epoch)
	record.FinalStatus = e.state.Status()
	if err := e.evaluate(vars); err != nil {
		return sim.RunRecord{}, err
	}
	return record, nil
}

// run is the discrete-event loop. It returns when the graph reaches a
// terminal phase, the episode is abandoned, or nothing can make progress.
func (e *episode) run(ctx context.Context, scheduler *reactive.Scheduler) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		plan, next, err := scheduler.EvaluateStep(ctx, e.state)
		if err != nil {
			return fmt.Errorf("scheduler evaluation failed: %w", err)
		}
		e.state = next

		for i, nodeID := range plan.ReadyNodes {
			if e.trace != nil {
				if err := e.traverse(ctx, nodeID); err != nil {
					return err
				}
			}
			abandoned, err := e.start(ctx, nodeID, plan.ResolvedInputs[nodeID])
			if err != nil {
				return err
			}
			if abandoned {
				return e.abandon(ctx, nodeID, plan.ReadyNodes[i+1:])
			}
		}

		if e.pending.Len() == 0 {
			done, err := e.assess(ctx)
			if err != nil || done {
				return err
			}
			if len(plan.ReadyNodes) == 0 && len(plan.SkippedNodes) == 0 {
				// Nothing in flight and nothing new: the graph cannot progress.
				return nil
			}
			continue
		}

		batch := e.pending.popInstant()
		e.now = batch[0].time
		for _, ev := range batch {
			if err := e.complete(ctx, ev); err != nil {
				return err
			}
		}
		done, err := e.assess(ctx)
		if err != nil || done {
			return err
		}
	}
}

// assess runs the terminal detector and reports whether the episode is over.
func (e *episode) assess(ctx context.Context) (bool, error) {
	assessment, err := e.detector.AssessState(ctx, e.state)
	if err != nil {
		return false, err
	}
	if assessment.Phase == reactive.PhaseRunning {
		return false, nil
	}
	e.state = assessment.FinalGraphState
	return true, nil
}

// start moves nodeID to Running and executes it, scheduling its completion.
// It reports true when the node's behavior abandoned.
func (e *episode) start(ctx context.Context, nodeID engine.NodeID, inputs map[string]any) (bool, error) {
	if err := e.transition(ctx, engine.StateMutation{NodeID: nodeID, Status: statusPtr(engine.StatusRunning)}); err != nil {
		return false, err
	}
	if inputs == nil {
		inputs = make(map[string]any)
	}

	strID := string(nodeID)
	cb, simulated := e.m.CompiledBehaviors[strID]
	if !simulated {
		mut, err := e.execute(ctx, nodeID, inputs)
		e.pending.push(scheduledEvent{time: e.now, nodeID: nodeID, mutation: mut, err: err})
		return false, nil
	}

	failClass := ""
	if b, ok := e.m.Def.Behaviors[strID]; ok && b.Fail != nil {
		failClass = b.Fail.Class
	}
	out, err := behavior.New(cb, failClass).Sample(ctx, e.sampler, sim.BehaviorInput{
		NodeID:      nodeID,
		Inputs:      inputs,
		Latents:     e.latents,
		World:       e.world,
		Replication: e.replication,
	})
	if err != nil {
		return false, fmt.Errorf("node %s: %w", nodeID, err)
	}
	if out.Abandoned {
		// The behavior samples nothing else on abandonment, but the work was
		// started, so its cost still counts.
		c, err := e.abandonCost(cb, inputs)
		if err != nil {
			return false, fmt.Errorf("node %s: abandon cost: %w", nodeID, err)
		}
		e.record.Cost += c
		return true, nil
	}
	e.record.Cost += out.Cost
	ev := scheduledEvent{time: e.now.Add(out.Duration), nodeID: nodeID, err: out.Err}
	if out.Err == nil {
		ev.mutation = &engine.StateMutation{NodeID: nodeID, Set: out.Output}
	}
	e.pending.push(ev)
	return false, nil
}

// execute runs the node's real implementation through its lifecycle hooks,
// as the orchestrator's node activity does, without retries.
func (e *episode) execute(ctx context.Context, nodeID engine.NodeID, inputs map[string]any) (*engine.StateMutation, error) {
	def := e.nodeDefs[nodeID]
	instance, err := e.reg.Instantiate(def.Type(), def.Config())
	if err != nil {
		return nil, engine.NewNonRetryableError(fmt.Errorf("instantiating node: %w", err))
	}
	nc := &runNodeContext{execID: e.execID, nodeID: nodeID, config: def.Config(), inputs: inputs, state: e.state}

	if err := instance.OnInit(ctx, nc); err != nil {
		_ = instance.OnFailure(ctx, nc, err)
		return nil, err
	}
	mut, err := instance.Execute(ctx, nc)
	if err != nil {
		_ = instance.OnFailure(ctx, nc, err)
		return nil, err
	}
	if mut != nil && mut.Status != nil {
		mutErr := engine.NewNonRetryableError(fmt.Errorf("%w: node %q may not set Status directly", engine.ErrInvalidMutation, nodeID))
		_ = instance.OnFailure(ctx, nc, mutErr)
		return nil, mutErr
	}
	_ = instance.OnSuccess(ctx, nc, mut)
	return mut, nil
}

// complete applies a scheduled completion or failure to the graph state.
func (e *episode) complete(ctx context.Context, ev scheduledEvent) error {
	if ev.err != nil {
		return e.fail(ctx, ev.nodeID, ev.err)
	}
	mut := engine.StateMutation{NodeID: ev.nodeID, Status: statusPtr(engine.StatusSucceeded)}
	if ev.mutation != nil {
		mut.Set = ev.mutation.Set
		mut.Unset = ev.mutation.Unset
		mut.ErrorMessage = ev.mutation.ErrorMessage
	}
	if err := e.transition(ctx, mut); err != nil {
		return err
	}
	e.record.Path = append(e.record.Path, ev.nodeID)
	return nil
}

// fail records the failure with the detector and moves the node to Failed.
// Failures are never retried: anything that is not critical becomes
// non-retryable.
func (e *episode) fail(ctx context.Context, nodeID engine.NodeID, err error) error {
	cause := err
	var ne *engine.NodeError
	if !engine.IsCritical(err) && (!errors.As(err, &ne) || ne.Class != engine.ClassNonRetryable) {
		cause = engine.NewNonRetryableError(err)
	}
	e.detector.RecordNodeFailure(nodeID, cause)
	msg := cause.Error()
	return e.transition(ctx, engine.StateMutation{NodeID: nodeID, Status: statusPtr(engine.StatusFailed), ErrorMessage: &msg}, cause)
}

// abandon ends the episode at the current virtual instant: the abandoned node
// and every in-flight node fail, ready nodes that have not started are
// skipped, and the graph fails.
func (e *episode) abandon(ctx context.Context, nodeID engine.NodeID, notStarted []engine.NodeID) error {
	e.record.Abandoned = true
	if err := e.fail(ctx, nodeID, errAbandoned); err != nil {
		return err
	}
	for e.pending.Len() > 0 {
		ev := e.pending.pop()
		if err := e.fail(ctx, ev.nodeID, fmt.Errorf("episode abandoned at node %s", nodeID)); err != nil {
			return err
		}
	}
	for _, id := range notStarted {
		if err := e.transition(ctx, engine.StateMutation{NodeID: id, Status: statusPtr(engine.StatusSkipped)}); err != nil {
			return err
		}
	}

	next, err := e.state.WithStatus(e.state.Version(), engine.StatusFailed)
	if err != nil {
		return err
	}
	e.state = next
	ev, err := engine.NewEvent(e.execID, e.seqGen.Next(), e.now, engine.EventTypeGraphFailed, "",
		engine.GraphFailedPayload{ErrorMessage: fmt.Sprintf("episode abandoned at node %s", nodeID), Version: next.Version()})
	if err != nil {
		return err
	}
	if e.sink != nil {
		return e.sink.Emit(ctx, ev)
	}
	return nil
}

// transition applies a status mutation and emits the matching event.
func (e *episode) transition(ctx context.Context, mut engine.StateMutation, cause ...error) error {
	old := e.state.NodeState(mut.NodeID)
	mut.ExpectedVersion = e.state.Version()
	next, err := e.state.Apply(mut)
	if err != nil {
		return err
	}
	e.state = next
	if _, err := e.emitter.EmitTransition(ctx, old, next.NodeState(mut.NodeID), mut, cause...); err != nil {
		return fmt.Errorf("emit transition for node %s: %w", mut.NodeID, err)
	}
	return nil
}

// abandonCost evaluates the node's cost parameter for an abandoned node.
func (e *episode) abandonCost(cb model.CompiledBehavior, inputs map[string]any) (float64, error) {
	if cb.Cost == nil {
		return 0, nil
	}
	if cb.Cost.IsLiteral() {
		return cb.Cost.Literal, nil
	}
	val, _, err := cb.Cost.Program.Eval(map[string]any{
		"world":  worldParams(e.world),
		"latent": e.latents,
		"inputs": inputs,
	})
	if err != nil {
		return 0, err
	}
	return toFloat(val.Value())
}

// evaluate fills Objective, Constraints, Metrics and Events from the final
// state.
func (e *episode) evaluate(vars map[string]any) error {
	nodeVars := make(map[string]any)
	for _, ns := range e.state.NodeStates() {
		output := ns.Output()
		if output == nil {
			output = map[string]any{}
		}
		nodeVars[string(ns.NodeID())] = map[string]any{
			"status":       string(ns.Status()),
			"output":       output,
			"errorMessage": ns.ErrorMessage(),
		}
	}
	path := make([]string, 0, len(e.record.Path))
	for _, id := range e.record.Path {
		path = append(path, string(id))
	}
	vars["nodes"] = nodeVars
	vars["run"] = map[string]any{
		"cost":      e.record.Cost,
		"duration":  e.record.Duration.Seconds(),
		"path":      path,
		"abandoned": e.record.Abandoned,
	}

	if e.m.CompiledObjective != nil {
		val, _, err := e.m.CompiledObjective.Eval(vars)
		if err != nil {
			return fmt.Errorf("objective: %w", err)
		}
		obj, err := toFloat(val.Value())
		if err != nil {
			return fmt.Errorf("objective: %w", err)
		}
		e.record.Objective = obj
	}

	e.record.Constraints = make(map[string]bool, len(e.m.CompiledConstraints))
	for name, prog := range e.m.CompiledConstraints {
		val, _, err := prog.Eval(vars)
		if err != nil {
			return fmt.Errorf("constraint %s: %w", name, err)
		}
		b, ok := val.Value().(bool)
		if !ok {
			return fmt.Errorf("constraint %s must evaluate to bool, got %T", name, val.Value())
		}
		e.record.Constraints[name] = b
	}

	if len(e.m.MetricOrder) > 0 {
		e.record.Metrics = make(map[string]float64, len(e.m.MetricOrder))
		for _, name := range e.m.MetricOrder {
			val, _, err := e.m.CompiledMetrics[name].Eval(vars)
			if err != nil {
				return fmt.Errorf("metric %s: %w", name, err)
			}
			v, err := toFloat(val.Value())
			if err != nil {
				return fmt.Errorf("metric %s: %w", name, err)
			}
			e.record.Metrics[name] = v
		}
	}

	for _, req := range e.m.Requirements {
		if req.Event == nil {
			continue
		}
		val, _, err := req.Event.Eval(vars)
		if err != nil {
			return fmt.Errorf("requirement %s: %w", req.Name, err)
		}
		b, ok := val.Value().(bool)
		if !ok {
			return fmt.Errorf("requirement %s: prob() condition must evaluate to bool, got %T", req.Name, val.Value())
		}
		if e.record.Events == nil {
			e.record.Events = make(map[string]bool)
		}
		e.record.Events[req.Name] = b
	}
	return nil
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) {
			return 0, errors.New("value is NaN")
		}
		return n, nil
	case int64:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	case int:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("expected a number, got %T", v)
	}
}

func worldParams(w sim.World) map[string]any {
	if w.Params == nil {
		return map[string]any{}
	}
	return w.Params
}

func statusPtr(s engine.Status) *engine.Status { return &s }
