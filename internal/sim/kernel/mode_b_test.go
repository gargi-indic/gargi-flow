package kernel_test

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/sim/kernel"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/sampler"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/require"
)

func TestModeB_MMcQueueAnalyticMatch(t *testing.T) {
	reg := compileRegistry(t)

	// Single-node graph: service
	gDef := &engine.GraphDefinition{
		APIVersion: "graph/v1",
		ID:         "mmc-graph",
		Nodes: []engine.NodeDefinition{
			{ID: "service", Type: "passthrough"},
		},
	}

	// M/M/c queue specification:
	// lambda = 2.0/s, mu = 1.0/s (mean service time 1.0s), c = 3 servers
	// rho = lambda / (c * mu) = 2.0 / 3.0 = 0.6667 < 1
	// Erlang-C C(3, 2) = 4/9
	// Theoretical Wq = (4/9) / (3*1 - 2) = 4/9 s = 0.4444... s
	simDef := &model.Sim{
		APIVersion: "sim/v1",
		ID:         "mmc-sim",
		Graph:      "./graph.yaml",
		Horizon:    "50000s",
		Arrivals: &model.ArrivalsSpec{
			Poisson: &model.PoissonArrivals{Rate: 2.0},
		},
		Resources: map[string]model.Resource{
			"servers": {Capacity: 3},
		},
		Behaviors: map[string]model.Behavior{
			"service": {
				Resource: "servers",
				Duration: model.ValueSpec{
					Exponential: &model.Exponential{Rate: 1.0},
				},
			},
		},
		Objective: model.Objective{
			Name:     "utilization",
			Value:    "1.0",
			Maximize: "mean",
		},
	}

	m, errs := model.CompileGraph(simDef, gDef, reg)
	require.Equal(t, 0, errs.Len(), "%v", errs.Error())

	// Run with 25,000 arrivals
	s := sampler.NewMonteCarlo(42)
	rec, err := kernel.RunModeB(context.Background(), m, sim.World{ID: "base"}, fixedPolicy{}, s, kernel.ModeBOptions{
		MaxArrivals: 25000,
	})
	require.NoError(t, err)
	require.Equal(t, 25000, rec.Arrivals)
	require.Equal(t, 25000, rec.Completed)
	require.Equal(t, 0, rec.Abandoned)

	// Discard initial warm-up transient (first 2,000 entities) to evaluate steady-state wait time
	const warmup = 2000
	var postWarmupTotalWait time.Duration
	for _, ent := range rec.Entities[warmup:] {
		postWarmupTotalWait += ent.QueueWaitTime
	}
	postWarmupMeanWait := float64(postWarmupTotalWait) / float64(len(rec.Entities)-warmup) / float64(time.Second)

	theoreticalMeanWait := 4.0 / 9.0 // 0.4444...

	t.Logf("Theoretical Wq:       %.4f s", theoreticalMeanWait)
	t.Logf("Overall Empirical Wq: %.4f s", rec.MeanQueueWait.Seconds())
	t.Logf("Steady-state Wq:      %.4f s", postWarmupMeanWait)
	t.Logf("Resource util:        %v", rec.ResourceUtil)

	// Verify steady-state queue wait time matches theoretical within 5%
	relError := math.Abs(postWarmupMeanWait-theoreticalMeanWait) / theoreticalMeanWait
	t.Logf("Relative error: %.2f%%", relError*100)
	require.Less(t, relError, 0.05, "empirical queue wait %.4fs should match theoretical %.4fs within 5%%", postWarmupMeanWait, theoreticalMeanWait)

	// Also verify utilization matches rho = 2/3 = 0.6667 within 5%
	serversUtil, ok := rec.ResourceUtil["servers"]
	require.True(t, ok)
	utilRelError := math.Abs(serversUtil-(2.0/3.0)) / (2.0 / 3.0)
	require.Less(t, utilRelError, 0.05, "servers utilization %.4f should match theoretical 0.6667 within 5%%", serversUtil)
}

func TestModeB_Determinism(t *testing.T) {
	reg := compileRegistry(t)

	gDef := &engine.GraphDefinition{
		APIVersion: "graph/v1",
		ID:         "det-graph",
		Nodes: []engine.NodeDefinition{
			{ID: "step1", Type: "passthrough"},
		},
	}

	simDef := &model.Sim{
		APIVersion: "sim/v1",
		ID:         "det-sim",
		Graph:      "./graph.yaml",
		Horizon:    "500s",
		Arrivals: &model.ArrivalsSpec{
			Poisson: &model.PoissonArrivals{Rate: 3.0},
		},
		Resources: map[string]model.Resource{
			"workers": {Capacity: 2},
		},
		Behaviors: map[string]model.Behavior{
			"step1": {
				Resource: "workers",
				Duration: model.ValueSpec{
					Exponential: &model.Exponential{Rate: 2.0},
				},
			},
		},
		Objective: model.Objective{
			Name:     "cost",
			Value:    "1.0",
			Maximize: "mean",
		},
	}

	m, errs := model.CompileGraph(simDef, gDef, reg)
	require.Equal(t, 0, errs.Len(), "%v", errs.Error())

	const seed = 9999
	rec1, err := kernel.RunModeB(context.Background(), m, sim.World{ID: "w"}, fixedPolicy{}, sampler.NewMonteCarlo(seed), kernel.ModeBOptions{
		MaxArrivals: 500,
	})
	require.NoError(t, err)

	rec2, err := kernel.RunModeB(context.Background(), m, sim.World{ID: "w"}, fixedPolicy{}, sampler.NewMonteCarlo(seed), kernel.ModeBOptions{
		MaxArrivals: 500,
	})
	require.NoError(t, err)

	require.Equal(t, rec1.Arrivals, rec2.Arrivals)
	require.Equal(t, rec1.Completed, rec2.Completed)
	require.Equal(t, rec1.Abandoned, rec2.Abandoned)
	require.Equal(t, rec1.MeanQueueWait, rec2.MeanQueueWait)
	require.Equal(t, rec1.MaxQueueWait, rec2.MaxQueueWait)
	require.Equal(t, rec1.ResourceUtil, rec2.ResourceUtil)
	require.Equal(t, len(rec1.Entities), len(rec2.Entities))

	for i := range rec1.Entities {
		require.Equal(t, rec1.Entities[i].EntityID, rec2.Entities[i].EntityID)
		require.Equal(t, rec1.Entities[i].ArrivalTime, rec2.Entities[i].ArrivalTime)
		require.Equal(t, rec1.Entities[i].QueueWaitTime, rec2.Entities[i].QueueWaitTime)
		require.Equal(t, rec1.Entities[i].Duration, rec2.Entities[i].Duration)
	}
}

func TestModeB_WorkerInvariance(t *testing.T) {
	reg := compileRegistry(t)

	gDef := &engine.GraphDefinition{
		APIVersion: "graph/v1",
		ID:         "inv-graph",
		Nodes: []engine.NodeDefinition{
			{ID: "node1", Type: "passthrough"},
		},
	}

	simDef := &model.Sim{
		APIVersion: "sim/v1",
		ID:         "inv-sim",
		Graph:      "./graph.yaml",
		Horizon:    "100s",
		Arrivals: &model.ArrivalsSpec{
			Poisson: &model.PoissonArrivals{Rate: 2.0},
		},
		Resources: map[string]model.Resource{
			"pool": {Capacity: 2},
		},
		Behaviors: map[string]model.Behavior{
			"node1": {
				Resource: "pool",
				Duration: model.ValueSpec{
					Exponential: &model.Exponential{Rate: 1.5},
				},
			},
		},
		Objective: model.Objective{
			Name:     "cost",
			Value:    "1.0",
			Maximize: "mean",
		},
	}

	m, errs := model.CompileGraph(simDef, gDef, reg)
	require.Equal(t, 0, errs.Len(), "%v", errs.Error())

	const (
		numReplications = 4
		baseSeed        = 12345
	)

	// Sequential run (1 worker)
	seqRecords := make([]sim.ReplicationRecord, numReplications)
	for r := 0; r < numReplications; r++ {
		seed := sim.Seed(baseSeed, "w", r)
		rec, err := kernel.RunModeB(context.Background(), m, sim.World{ID: "w"}, fixedPolicy{}, sampler.NewMonteCarlo(seed), kernel.ModeBOptions{
			Replication: r,
			MaxArrivals: 100,
		})
		require.NoError(t, err)
		seqRecords[r] = rec
	}

	// Concurrent run (simulating 4 parallel workers)
	parRecords := make([]sim.ReplicationRecord, numReplications)
	var wg sync.WaitGroup
	for r := 0; r < numReplications; r++ {
		wg.Add(1)
		go func(rep int) {
			defer wg.Done()
			seed := sim.Seed(baseSeed, "w", rep)
			rec, err := kernel.RunModeB(context.Background(), m, sim.World{ID: "w"}, fixedPolicy{}, sampler.NewMonteCarlo(seed), kernel.ModeBOptions{
				Replication: rep,
				MaxArrivals: 100,
			})
			require.NoError(t, err)
			parRecords[rep] = rec
		}(r)
	}
	wg.Wait()

	// Every replication must match bit-for-bit
	for r := 0; r < numReplications; r++ {
		require.Equal(t, seqRecords[r].Arrivals, parRecords[r].Arrivals)
		require.Equal(t, seqRecords[r].Completed, parRecords[r].Completed)
		require.Equal(t, seqRecords[r].Abandoned, parRecords[r].Abandoned)
		require.Equal(t, seqRecords[r].MeanQueueWait, parRecords[r].MeanQueueWait)
		require.Equal(t, seqRecords[r].ResourceUtil, parRecords[r].ResourceUtil)
	}
}

func TestModeB_PatienceAndAbandonment(t *testing.T) {
	reg := compileRegistry(t)

	gDef := &engine.GraphDefinition{
		APIVersion: "graph/v1",
		ID:         "patience-graph",
		Nodes: []engine.NodeDefinition{
			{ID: "heavy_task", Type: "passthrough"},
		},
	}

	// 1 server, arrival rate 10/s, service duration 500ms
	// Queue builds up very quickly.
	// Patience is set to 50ms, so most queued entities abandon!
	simDef := &model.Sim{
		APIVersion: "sim/v1",
		ID:         "patience-sim",
		Graph:      "./graph.yaml",
		Horizon:    "50s",
		Arrivals: &model.ArrivalsSpec{
			Poisson: &model.PoissonArrivals{Rate: 10.0},
		},
		Resources: map[string]model.Resource{
			"single_server": {Capacity: 1},
		},
		Behaviors: map[string]model.Behavior{
			"heavy_task": {
				Resource: "single_server",
				Duration: "500ms",
				Patience: "50ms",
			},
		},
		Objective: model.Objective{
			Name:     "obj",
			Value:    "1.0",
			Maximize: "mean",
		},
	}

	m, errs := model.CompileGraph(simDef, gDef, reg)
	require.Equal(t, 0, errs.Len(), "%v", errs.Error())

	rec, err := kernel.RunModeB(context.Background(), m, sim.World{ID: "w"}, fixedPolicy{}, sampler.NewMonteCarlo(777), kernel.ModeBOptions{
		MaxArrivals: 50,
	})
	require.NoError(t, err)

	require.Equal(t, 50, rec.Arrivals)
	t.Logf("Arrivals: %d, Completed: %d, Abandoned: %d", rec.Arrivals, rec.Completed, rec.Abandoned)
	require.Greater(t, rec.Abandoned, 10, "entities under high load with short patience should abandon")
	require.Equal(t, rec.Arrivals, rec.Completed+rec.Abandoned)

	// Check entity records
	hasAbandonedRecord := false
	for _, ent := range rec.Entities {
		if ent.Abandoned {
			hasAbandonedRecord = true
			require.Greater(t, ent.QueueWaitTime, time.Duration(0))
		}
	}
	require.True(t, hasAbandonedRecord)
}

func TestModeB_ScheduledSurge(t *testing.T) {
	reg := compileRegistry(t)

	gDef := &engine.GraphDefinition{
		APIVersion: "graph/v1",
		ID:         "surge-graph",
		Nodes: []engine.NodeDefinition{
			{ID: "task", Type: "passthrough"},
		},
	}

	// Schedule: 10s at rate 1.0, then 10s at surge rate 20.0
	simDef := &model.Sim{
		APIVersion: "sim/v1",
		ID:         "surge-sim",
		Graph:      "./graph.yaml",
		Horizon:    "20s",
		Arrivals: &model.ArrivalsSpec{
			Schedule: []model.ScheduleWindow{
				{Duration: "10s", Rate: 1.0},
				{Duration: "10s", Rate: 20.0},
			},
		},
		Resources: map[string]model.Resource{
			"res": {Capacity: 5},
		},
		Behaviors: map[string]model.Behavior{
			"task": {
				Resource: "res",
				Duration: "100ms",
			},
		},
		Objective: model.Objective{
			Name:     "obj",
			Value:    "1.0",
			Maximize: "mean",
		},
	}

	m, errs := model.CompileGraph(simDef, gDef, reg)
	require.Equal(t, 0, errs.Len(), "%v", errs.Error())

	rec, err := kernel.RunModeB(context.Background(), m, sim.World{ID: "w"}, fixedPolicy{}, sampler.NewMonteCarlo(42), kernel.ModeBOptions{})
	require.NoError(t, err)

	t.Logf("Schedule surge arrivals: %d, completed: %d", rec.Arrivals, rec.Completed)
	// Window 1: ~10 arrivals; Window 2: ~200 arrivals. Total ~150-250 arrivals.
	require.Greater(t, rec.Arrivals, 100, "surge window should produce significantly more arrivals than base window")
}
