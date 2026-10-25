package kernel

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/reactive"
	"github.com/gargi-indic/gargi-flow/internal/sim/behavior"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// ModeBOptions configures a Mode B simulation replication.
type ModeBOptions struct {
	// Events is an optional sink that receives engine.Events from every entity.
	Events engine.EventSink
	// Replication is the index of this simulation run within an experiment.
	Replication int
	// MaxArrivals optionally caps the total arrivals in the replication (0 = uncapped).
	MaxArrivals int
}

type modeBEventKind int

const (
	eventKindArrival modeBEventKind = iota
	eventKindNodeComplete
	eventKindPatienceExpired
)

type modeBEvent struct {
	time       time.Time
	seq        uint64
	kind       modeBEventKind
	entityID   string
	nodeID     engine.NodeID
	patienceID uint64
	mutation   *engine.StateMutation
	duration   time.Duration
	cost       float64
	err        error
}

type modeBHeap []modeBEvent

func (h modeBHeap) Len() int { return len(h) }
func (h modeBHeap) Less(i, j int) bool {
	if h[i].time.Equal(h[j].time) {
		return h[i].seq < h[j].seq
	}
	return h[i].time.Before(h[j].time)
}
func (h modeBHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *modeBHeap) Push(x any)   { *h = append(*h, x.(modeBEvent)) }
func (h *modeBHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
func (h *modeBHeap) push(ev modeBEvent) { heap.Push(h, ev) }
func (h *modeBHeap) pop() modeBEvent    { return heap.Pop(h).(modeBEvent) }

type queuedEntityNode struct {
	entityID   string
	nodeID     engine.NodeID
	inputs     map[string]any
	enqueuedAt time.Time
	patienceID uint64
	abandoned  bool
}

type modeBResource struct {
	name          string
	capacity      int
	inUse         int
	queue         []*queuedEntityNode
	utilIntegral  float64
	queueIntegral float64
	maxQueueLen   int
	lastUpdate    time.Time
}

func (r *modeBResource) advance(now time.Time) {
	if now.After(r.lastUpdate) {
		dt := now.Sub(r.lastUpdate).Seconds()
		r.utilIntegral += float64(r.inUse) * dt
		r.queueIntegral += float64(len(r.queue)) * dt
		r.lastUpdate = now
	}
}

type modeBEntity struct {
	id          string
	execID      engine.ExecutionID
	arrivedAt   time.Time
	state       *engine.GraphState
	detector    *reactive.TerminalDetector
	record      sim.RunRecord
	latents     map[string]any
	vars        map[string]any
	queueWait   time.Duration
	activeNodes map[engine.NodeID]string // nodeID -> resourceName (if held)
	completed   bool
	abandoned   bool
}

// RunModeB executes a Mode B timeline simulation replication.
func RunModeB(
	ctx context.Context,
	m *model.CompiledModel,
	world sim.World,
	policy sim.Policy,
	s sim.Sampler,
	opts ModeBOptions,
) (sim.ReplicationRecord, error) {
	if m == nil || m.Graph == nil {
		return sim.ReplicationRecord{}, errors.New("model or graph is nil")
	}

	horizon := m.Horizon
	if horizon <= 0 {
		// Default horizon if unspecified
		horizon = time.Hour
	}

	g, err := m.Graph.ToGraph()
	if err != nil {
		return sim.ReplicationRecord{}, err
	}

	wParams := worldParams(world)
	baseVars := map[string]any{"world": wParams}

	// Determine arrival process parameters
	var (
		poissonRate float64
		schedule    []model.CompiledScheduleWindow
		empirical   []float64
		hasArrivals bool
	)

	if m.CompiledArrivals != nil {
		hasArrivals = true
		if m.CompiledArrivals.Poisson != nil {
			rateVal, err := evalCompiledParam(m.CompiledArrivals.Poisson.Rate, baseVars)
			if err != nil {
				return sim.ReplicationRecord{}, fmt.Errorf("evaluating arrival rate: %w", err)
			}
			if rateVal <= 0 {
				return sim.ReplicationRecord{}, fmt.Errorf("arrival rate must be > 0, got %v", rateVal)
			}
			poissonRate = rateVal
		} else if len(m.CompiledArrivals.Schedule) > 0 {
			schedule = m.CompiledArrivals.Schedule
		} else if len(m.CompiledArrivals.Empirical) > 0 {
			empirical = m.CompiledArrivals.Empirical
		}
	} else {
		// Fallback single arrival at 0 if no arrival spec given
		poissonRate = 1.0
	}

	// Initialize resources
	resources := make(map[string]*modeBResource)
	for name, resDef := range m.Def.Resources {
		capVal := resDef.Capacity
		if capVal <= 0 {
			capVal = 1
		}
		resources[name] = &modeBResource{
			name:       name,
			capacity:   capVal,
			lastUpdate: epoch,
		}
	}

	// Setup node registry
	reg := engine.NewRegistry()
	if err := nodes.RegisterDefaults(reg, nodes.Dependencies{HTTPClient: noEffects{}, Signals: noEffects{}}); err != nil {
		return sim.ReplicationRecord{}, err
	}
	decisionsShared := make(map[engine.NodeID]string)
	if err := nodes.RegisterDecision(reg, &recordingChooser{policy: policy, decisions: decisionsShared, replication: uint64(opts.Replication)}); err != nil {
		return sim.ReplicationRecord{}, err
	}

	seqGen := reactive.NewSequenceGenerator(0)
	var eventSeq uint64
	h := &modeBHeap{}
	now := epoch
	horizonEnd := epoch.Add(horizon)

	// Helper to sample inter-arrival duration
	sampleInterArrival := func(at time.Time) (time.Duration, error) {
		if poissonRate > 0 {
			// Exponential inter-arrival with mean 1/rate
			f, err := s.Float("arrivals", sim.Distribution{
				Kind:   sim.DistKindExponential,
				Params: map[string]float64{"rate": poissonRate},
			})
			if err != nil {
				// Fallback to Uniform draw if exponential not directly supported
				u := 0.5
				return time.Duration(u / poissonRate * float64(time.Second)), nil
			}
			return time.Duration(f * float64(time.Second)), nil
		}
		if len(schedule) > 0 {
			elapsed := at.Sub(epoch)
			var curDuration time.Duration
			curRate := 1.0
			for _, w := range schedule {
				curDuration += w.Duration
				if elapsed < curDuration {
					r, err := evalCompiledParam(w.Rate, baseVars)
					if err == nil && r > 0 {
						curRate = r
					}
					break
				}
			}
			f, err := s.Float("arrivals", sim.Distribution{
				Kind:   sim.DistKindExponential,
				Params: map[string]float64{"rate": curRate},
			})
			if err != nil {
				return time.Second, nil
			}
			return time.Duration(f * float64(time.Second)), nil
		}
		if len(empirical) > 0 {
			f, err := s.Float("arrivals", sim.Distribution{
				Kind:   sim.DistKindEmpirical,
				Values: empirical,
			})
			if err != nil {
				return time.Second, nil
			}
			return time.Duration(f * float64(time.Second)), nil
		}
		return time.Second, nil
	}

	// Schedule first arrival at epoch
	eventSeq++
	h.push(modeBEvent{
		time: now,
		seq:  eventSeq,
		kind: eventKindArrival,
	})

	entities := make(map[string]*modeBEntity)
	var entityCounter int
	var patienceCounter uint64

	clock := func() time.Time { return now }
	emitter := reactive.NewTransitionEmitter(engine.ExecutionID("sim-mode-b"), seqGen, clock, opts.Events)
	scheduler := reactive.NewScheduler(g, reg, reactive.NewPreconditionResolver(cachedConditionEvaluator{}), emitter)

	// Start a node for an entity: acquire resource or queue
	startNode := func(ent *modeBEntity, nodeID engine.NodeID, inputs map[string]any) error {
		strID := string(nodeID)
		cb, simulated := m.CompiledBehaviors[strID]
		resName := ""
		if simulated {
			resName = cb.Resource
		}

		if resName != "" {
			res, ok := resources[resName]
			if ok && res.inUse >= res.capacity {

				patienceCounter++
				pID := patienceCounter
				qNode := &queuedEntityNode{
					entityID:   ent.id,
					nodeID:     nodeID,
					inputs:     inputs,
					enqueuedAt: now,
					patienceID: pID,
				}
				res.queue = append(res.queue, qNode)
				if len(res.queue) > res.maxQueueLen {
					res.maxQueueLen = len(res.queue)
				}

				if cb.Patience != nil {
					pVal, err := evalCompiledParam(cb.Patience, ent.vars)
					if err == nil && pVal > 0 {
						pDur := time.Duration(pVal * float64(time.Second))
						eventSeq++
						h.push(modeBEvent{
							time:       now.Add(pDur),
							seq:        eventSeq,
							kind:       eventKindPatienceExpired,
							entityID:   ent.id,
							nodeID:     nodeID,
							patienceID: pID,
						})
					}
				}
				return nil
			}
			if ok {
				res.inUse++
			}
		}

		if err := ent.transition(ctx, engine.StateMutation{NodeID: nodeID, Status: statusPtr(engine.StatusRunning)}); err != nil {
			return err
		}
		ent.activeNodes[nodeID] = resName

		if inputs == nil {
			inputs = make(map[string]any)
		}

		if !simulated {

			eventSeq++
			h.push(modeBEvent{
				time:     now,
				seq:      eventSeq,
				kind:     eventKindNodeComplete,
				entityID: ent.id,
				nodeID:   nodeID,
			})
			return nil
		}

		failClass := ""
		if b, ok := m.Def.Behaviors[strID]; ok && b.Fail != nil {
			failClass = b.Fail.Class
		}
		streamName := fmt.Sprintf("%s/%s", ent.id, nodeID)
		outcome, err := behavior.New(cb, failClass).Sample(ctx, s, sim.BehaviorInput{
			NodeID:      nodeID,
			Inputs:      inputs,
			Latents:     ent.latents,
			World:       world,
			Replication: uint64(opts.Replication),
		})
		if err != nil {
			outcome, _ = behavior.New(cb, failClass).Sample(ctx, s, sim.BehaviorInput{
				NodeID:      nodeID,
				Inputs:      inputs,
				Latents:     ent.latents,
				World:       world,
				Replication: uint64(opts.Replication),
			})
		}
		_ = streamName

		dur := outcome.Duration
		cost := outcome.Cost
		var nodeErr error
		var mut *engine.StateMutation

		if outcome.Abandoned {
			nodeErr = errAbandoned
		} else if outcome.Err != nil {
			nodeErr = outcome.Err
		} else {
			mut = &engine.StateMutation{
				NodeID: nodeID,
				Status: statusPtr(engine.StatusSucceeded),
				Set:    outcome.Output,
			}
		}

		eventSeq++
		h.push(modeBEvent{
			time:     now.Add(dur),
			seq:      eventSeq,
			kind:     eventKindNodeComplete,
			entityID: ent.id,
			nodeID:   nodeID,
			mutation: mut,
			duration: dur,
			cost:     cost,
			err:      nodeErr,
		})
		return nil
	}

	// Main discrete-event loop
	for h.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return sim.ReplicationRecord{}, err
		}

		ev := h.pop()
		// Advance all resource utilization tracking
		for _, res := range resources {
			res.advance(ev.time)
		}
		now = ev.time

		switch ev.kind {
		case eventKindArrival:
			if now.Before(horizonEnd) && (opts.MaxArrivals <= 0 || entityCounter < opts.MaxArrivals) {
				// Schedule next arrival
				nextDelta, err := sampleInterArrival(now)
				if err == nil && nextDelta > 0 && now.Add(nextDelta).Before(horizonEnd) {
					eventSeq++
					h.push(modeBEvent{
						time: now.Add(nextDelta),
						seq:  eventSeq,
						kind: eventKindArrival,
					})
				}
			}

			if opts.MaxArrivals > 0 && entityCounter >= opts.MaxArrivals {
				continue
			}

			entityCounter++
			entID := fmt.Sprintf("entity-%d", entityCounter)
			execID := engine.ExecutionID(fmt.Sprintf("sim-%s-%s-%s", policy.ID(), world.ID, entID))

			// Sample latents for this entity
			entVars := map[string]any{"world": wParams}
			entLatents, err := behavior.SampleLatents(m, s, entVars)
			if err != nil {
				entLatents = make(map[string]any)
			}
			entVars["latent"] = entLatents

			st, err := engine.NewGraphState(execID, g)
			if err != nil {
				return sim.ReplicationRecord{}, err
			}
			st, _ = st.WithStatus(st.Version(), engine.StatusReady)
			st, _ = st.WithStatus(st.Version(), engine.StatusRunning)

			ent := &modeBEntity{
				id:          entID,
				execID:      execID,
				arrivedAt:   now,
				state:       st,
				detector:    reactive.NewTerminalDetector(g, m.Graph.FailurePolicy, emitter),
				latents:     entLatents,
				vars:        entVars,
				activeNodes: make(map[engine.NodeID]string),
				record: sim.RunRecord{
					World:       world.ID,
					PolicyID:    policy.ID(),
					Replication: opts.Replication,
					EntityID:    entID,
					ArrivalTime: now.Sub(epoch),
					Weight:      1.0,
					Decisions:   make(map[engine.NodeID]string),
				},
			}
			entities[entID] = ent

			// Evaluate initial step for this entity
			plan, nextSt, err := scheduler.EvaluateStep(ctx, ent.state)
			if err != nil {
				return sim.ReplicationRecord{}, err
			}
			ent.state = nextSt

			for _, nID := range plan.ReadyNodes {
				if err := startNode(ent, nID, plan.ResolvedInputs[nID]); err != nil {
					return sim.ReplicationRecord{}, err
				}
			}

		case eventKindPatienceExpired:
			ent, ok := entities[ev.entityID]
			if !ok || ent.completed || ent.abandoned {
				continue
			}
			cb := m.CompiledBehaviors[string(ev.nodeID)]
			res, hasRes := resources[cb.Resource]
			if !hasRes {
				continue
			}
			// Search for entity in resource queue
			for idx, qItem := range res.queue {
				if qItem.patienceID == ev.patienceID && !qItem.abandoned {
					qItem.abandoned = true
					// Remove from queue
					res.queue = append(res.queue[:idx], res.queue[idx+1:]...)
					ent.abandoned = true
					ent.record.Abandoned = true

					// Record wait time up to abandonment
					ent.queueWait += now.Sub(qItem.enqueuedAt)
					msg := errAbandoned.Error()
					ent.detector.RecordNodeFailure(ev.nodeID, engine.NewNonRetryableError(errAbandoned))
					_ = ent.transition(ctx, engine.StateMutation{
						NodeID:       ev.nodeID,
						Status:       statusPtr(engine.StatusFailed),
						ErrorMessage: &msg,
					})
					assessment, _ := ent.detector.AssessState(ctx, ent.state)
					if assessment.Phase != reactive.PhaseRunning {
						ent.completed = true
						ent.record.Duration = now.Sub(ent.arrivedAt)
						ent.record.QueueWaitTime = ent.queueWait
						ent.record.FinalStatus = ent.state.Status()
					}
					break
				}
			}

		case eventKindNodeComplete:
			ent, ok := entities[ev.entityID]
			if !ok || ent.completed || ent.abandoned {
				continue
			}

			// Apply completion mutation
			ent.record.Path = append(ent.record.Path, ev.nodeID)
			ent.record.Cost += ev.cost

			if ev.err != nil {
				if errors.Is(ev.err, errAbandoned) {
					ent.record.Abandoned = true
					ent.abandoned = true
				}
				msg := ev.err.Error()
				ent.detector.RecordNodeFailure(ev.nodeID, engine.NewNonRetryableError(ev.err))
				_ = ent.transition(ctx, engine.StateMutation{
					NodeID:       ev.nodeID,
					Status:       statusPtr(engine.StatusFailed),
					ErrorMessage: &msg,
				})
			} else {
				if ev.mutation != nil {
					_ = ent.transition(ctx, *ev.mutation)
				} else {
					_ = ent.transition(ctx, engine.StateMutation{
						NodeID: ev.nodeID,
						Status: statusPtr(engine.StatusSucceeded),
					})
				}
			}

			// Release resource if held
			if resName, held := ent.activeNodes[ev.nodeID]; held && resName != "" {
				delete(ent.activeNodes, ev.nodeID)
				res, ok := resources[resName]
				if ok {
					res.inUse--
					// If waiting queue has entities, pop the next FIFO
					for len(res.queue) > 0 {
						nextWaiting := res.queue[0]
						res.queue = res.queue[1:]
						if nextWaiting.abandoned {
							continue
						}
						waitingEnt := entities[nextWaiting.entityID]
						if waitingEnt == nil || waitingEnt.abandoned || waitingEnt.completed {
							continue
						}
						// Accrue queue wait time
						waitDuration := now.Sub(nextWaiting.enqueuedAt)
						waitingEnt.queueWait += waitDuration

						// Start node for waiting entity
						_ = startNode(waitingEnt, nextWaiting.nodeID, nextWaiting.inputs)
						break
					}
				}
			}

			// Evaluate next scheduler step for this entity
			plan, nextSt, err := scheduler.EvaluateStep(ctx, ent.state)
			if err != nil {
				return sim.ReplicationRecord{}, err
			}
			ent.state = nextSt

			for _, nID := range plan.ReadyNodes {
				if err := startNode(ent, nID, plan.ResolvedInputs[nID]); err != nil {
					return sim.ReplicationRecord{}, err
				}
			}

			// Check terminal detector
			assessment, _ := ent.detector.AssessState(ctx, ent.state)
			if assessment.Phase != reactive.PhaseRunning {
				ent.completed = true
				ent.record.Duration = now.Sub(ent.arrivedAt)
				ent.record.QueueWaitTime = ent.queueWait
				ent.record.FinalStatus = ent.state.Status()
			}
		}

		if !hasArrivals && h.Len() == 0 {
			break
		}
	}

	// Finalize timeline utilization up to final now
	for _, res := range resources {
		res.advance(now)
	}

	totalSimTime := now.Sub(epoch).Seconds()
	if totalSimTime <= 0 {
		totalSimTime = 1.0
	}

	var (
		totalArrivals  int
		totalCompleted int
		totalAbandoned int
		totalWaitTime  time.Duration
		maxWaitTime    time.Duration
		totalCycleTime time.Duration
		maxCycleTime   time.Duration
		totalCost      float64
		entityRecords  []sim.RunRecord

		waitTimes  []time.Duration
		cycleTimes []time.Duration
	)

	// Collect entity statistics in deterministic order (sorted by entity sequence)
	keys := make([]string, 0, len(entities))
	for k := range entities {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		var n1, n2 int
		_, _ = fmt.Sscanf(keys[i], "entity-%d", &n1)
		_, _ = fmt.Sscanf(keys[j], "entity-%d", &n2)
		return n1 < n2
	})

	for _, k := range keys {
		ent := entities[k]
		totalArrivals++
		if ent.completed {
			totalCompleted++
		}
		if ent.abandoned || ent.record.Abandoned {
			totalAbandoned++
		}
		entWait := ent.queueWait
		totalWaitTime += entWait
		if entWait > maxWaitTime {
			maxWaitTime = entWait
		}
		waitTimes = append(waitTimes, entWait)

		if ent.completed || ent.abandoned || ent.record.Abandoned {
			cycle := ent.record.Duration
			totalCycleTime += cycle
			if cycle > maxCycleTime {
				maxCycleTime = cycle
			}
			cycleTimes = append(cycleTimes, cycle)
		}

		totalCost += ent.record.Cost

		ent.record.QueueWaitTime = entWait
		entityRecords = append(entityRecords, ent.record)
	}

	meanWait := time.Duration(0)
	if totalArrivals > 0 {
		meanWait = time.Duration(int64(totalWaitTime) / int64(totalArrivals))
	}

	meanCycle := time.Duration(0)
	if len(cycleTimes) > 0 {
		meanCycle = time.Duration(int64(totalCycleTime) / int64(len(cycleTimes)))
	}

	sort.Slice(waitTimes, func(i, j int) bool { return waitTimes[i] < waitTimes[j] })
	sort.Slice(cycleTimes, func(i, j int) bool { return cycleTimes[i] < cycleTimes[j] })

	quantile := func(times []time.Duration, p float64) time.Duration {
		if len(times) == 0 {
			return 0
		}
		idx := int(math.Ceil(float64(len(times))*p)) - 1
		if idx < 0 {
			idx = 0
		} else if idx >= len(times) {
			idx = len(times) - 1
		}
		return times[idx]
	}

	waitQuantiles := map[string]time.Duration{
		"p50": quantile(waitTimes, 0.50),
		"p90": quantile(waitTimes, 0.90),
		"p95": quantile(waitTimes, 0.95),
		"p99": quantile(waitTimes, 0.99),
	}

	cycleQuantiles := map[string]time.Duration{
		"p50": quantile(cycleTimes, 0.50),
		"p90": quantile(cycleTimes, 0.90),
		"p95": quantile(cycleTimes, 0.95),
		"p99": quantile(cycleTimes, 0.99),
	}

	throughput := float64(totalCompleted) / totalSimTime

	utilMap := make(map[string]float64)
	resMetrics := make(map[string]sim.ResourceMetric)
	for name, res := range resources {
		util := res.utilIntegral / (float64(res.capacity) * totalSimTime)
		if util > 1.0 {
			util = 1.0
		}
		util = math.Round(util*10000) / 10000
		utilMap[name] = util

		meanQLen := res.queueIntegral / totalSimTime
		resMetrics[name] = sim.ResourceMetric{
			Utilization:     util,
			MeanQueueLength: meanQLen,
			MaxQueueLength:  res.maxQueueLen,
		}
	}

	rec := sim.ReplicationRecord{
		World:              world.ID,
		PolicyID:           policy.ID(),
		Replication:        opts.Replication,
		Horizon:            horizon,
		Arrivals:           totalArrivals,
		Completed:          totalCompleted,
		Abandoned:          totalAbandoned,
		Throughput:         throughput,
		MeanQueueWait:      meanWait,
		MaxQueueWait:       maxWaitTime,
		MeanCycleTime:      meanCycle,
		MaxCycleTime:       maxCycleTime,
		WaitTimeQuantiles:  waitQuantiles,
		CycleTimeQuantiles: cycleQuantiles,
		ResourceUtil:       utilMap,
		ResourceMetrics:    resMetrics,
		Entities:           entityRecords,
	}

	vars := map[string]any{
		"world": wParams,
	}

	resVars := make(map[string]any)
	for name, rm := range resMetrics {
		resVars[name] = map[string]any{
			"utilization":      rm.Utilization,
			"queue_length":     rm.MeanQueueLength,
			"max_queue_length": float64(rm.MaxQueueLength),
		}
	}
	vars["resource"] = resVars
	vars["throughput"] = throughput
	vars["mean_wait"] = meanWait.Seconds()
	vars["p95_wait"] = waitQuantiles["p95"].Seconds()
	vars["mean_cycle_time"] = meanCycle.Seconds()
	vars["run"] = map[string]any{
		"duration":  totalSimTime,
		"cost":      totalCost,
		"abandoned": totalAbandoned > 0,
	}

	if m.CompiledObjective != nil {
		val, _, err := m.CompiledObjective.Eval(vars)
		if err != nil {
			return sim.ReplicationRecord{}, fmt.Errorf("objective: %w", err)
		}
		obj, err := toFloat(val.Value())
		if err != nil {
			return sim.ReplicationRecord{}, fmt.Errorf("objective: %w", err)
		}
		rec.Objective = obj
	}

	rec.Constraints = make(map[string]bool, len(m.CompiledConstraints))
	for name, prog := range m.CompiledConstraints {
		val, _, err := prog.Eval(vars)
		if err != nil {
			return sim.ReplicationRecord{}, fmt.Errorf("constraint %q: %w", name, err)
		}
		ok, ok2 := val.Value().(bool)
		if !ok2 {
			return sim.ReplicationRecord{}, fmt.Errorf("constraint %q: expected bool, got %T", name, val.Value())
		}
		rec.Constraints[name] = ok
	}

	if len(m.CompiledMetrics) > 0 {
		rec.Metrics = make(map[string]float64, len(m.CompiledMetrics))
		for name, prog := range m.CompiledMetrics {
			val, _, err := prog.Eval(vars)
			if err != nil {
				return sim.ReplicationRecord{}, fmt.Errorf("metric %q: %w", name, err)
			}
			fv, err := toFloat(val.Value())
			if err != nil {
				return sim.ReplicationRecord{}, fmt.Errorf("metric %q: %w", name, err)
			}
			rec.Metrics[name] = fv
		}
	}

	return rec, nil
}

func (ent *modeBEntity) transition(_ context.Context, mutation engine.StateMutation) error {
	mutation.ExpectedVersion = ent.state.Version()
	next, err := ent.state.Apply(mutation)
	if err != nil {
		return err
	}
	ent.state = next
	return nil
}

func evalCompiledParam(p *model.CompiledParam, vars map[string]any) (float64, error) {
	if p == nil {
		return 0, nil
	}
	if p.Program == nil {
		return p.Literal, nil
	}
	val, _, err := p.Program.Eval(vars)
	if err != nil {
		return 0, err
	}
	switch v := val.Value().(type) {
	case float64:
		return v, nil
	case int64:
		return float64(v), nil
	case int:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}
