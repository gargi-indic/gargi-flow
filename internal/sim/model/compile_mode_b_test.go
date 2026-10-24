package model_test

import (
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/nodes"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/stretchr/testify/require"
)

func newTestRegistry(t *testing.T) *engine.Registry {
	t.Helper()
	reg := engine.NewRegistry()
	require.NoError(t, nodes.RegisterDefaults(reg, nodes.Dependencies{}))
	return reg
}

func testGraph() *engine.GraphDefinition {
	return &engine.GraphDefinition{
		APIVersion: "graph/v1",
		ID:         "test-graph",
		Nodes: []engine.NodeDefinition{
			{ID: "step1", Type: "passthrough"},
		},
	}
}

func TestCompile_ModeBFields(t *testing.T) {
	reg := newTestRegistry(t)
	g := testGraph()

	t.Run("valid arrivals and resources", func(t *testing.T) {
		def := &model.Sim{
			APIVersion: "sim/v1",
			ID:         "mode-b-model",
			Graph:      "./graph.yaml",
			Horizon:    "1h",
			Arrivals: &model.ArrivalsSpec{
				Poisson: &model.PoissonArrivals{Rate: 5.0},
			},
			Resources: map[string]model.Resource{
				"servers": {Capacity: 2},
			},
			Behaviors: map[string]model.Behavior{
				"step1": {
					Cost:     10.0,
					Duration: "5s",
					Resource: "servers",
					Patience: "30s",
				},
			},
			Objective: model.Objective{
				Name:     "profit",
				Value:    "100.0 - run.cost",
				Maximize: "mean",
			},
		}

		m, errs := model.CompileGraph(def, g, reg)
		require.Equal(t, 0, errs.Len(), "%v", errs.Error())
		require.NotNil(t, m.CompiledArrivals)
		require.NotNil(t, m.CompiledArrivals.Poisson)
		require.Equal(t, "servers", m.CompiledBehaviors["step1"].Resource)
		require.NotNil(t, m.CompiledBehaviors["step1"].Patience)
	})

	t.Run("unresolved resource", func(t *testing.T) {
		def := &model.Sim{
			APIVersion: "sim/v1",
			ID:         "mode-b-model",
			Graph:      "./graph.yaml",
			Behaviors: map[string]model.Behavior{
				"step1": {
					Resource: "missing_resource",
				},
			},
			Objective: model.Objective{
				Name:     "profit",
				Value:    "1.0",
				Maximize: "mean",
			},
		}

		_, errs := model.CompileGraph(def, g, reg)
		require.Greater(t, errs.Len(), 0)
		require.Contains(t, errs.Error(), "unresolved resource \"missing_resource\"")
	})

	t.Run("invalid resource capacity", func(t *testing.T) {
		def := &model.Sim{
			APIVersion: "sim/v1",
			ID:         "mode-b-model",
			Graph:      "./graph.yaml",
			Resources: map[string]model.Resource{
				"workers": {Capacity: 0},
			},
			Behaviors: map[string]model.Behavior{
				"step1": {Resource: "workers"},
			},
			Objective: model.Objective{
				Name:     "profit",
				Value:    "1.0",
				Maximize: "mean",
			},
		}

		_, errs := model.CompileGraph(def, g, reg)
		require.Greater(t, errs.Len(), 0)
		require.Contains(t, errs.Error(), "capacity must be >= 1")
	})
}
