package experiment

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/stretchr/testify/require"
)

func TestBayesian_CanaryBenchmark(t *testing.T) {
	ctx := context.Background()
	reg := registry(t)

	canaryPath := filepath.Join("..", "..", "..", "examples", "simulation", "canary", "experiment.yaml")

	// Random search at equal budget (20 trials, seed 42)
	randExp, err := LoadFile(canaryPath, reg)
	require.NoError(t, err)
	randExp.Def.Policies.Search = "random"
	randExp.Def.Policies.Samples = 20
	randExp.Def.Policies.Seed = func(v uint64) *uint64 { return &v }(42)
	randSrc := RandomSource{Policy: randExp.Policy, Samples: 20, Seed: 42}
	randRep, err := Run(ctx, randExp, randSrc, Options{})
	require.NoError(t, err)

	// Bayesian TPE search at equal budget (20 trials, seed 42)
	bayesExp, err := LoadFile(canaryPath, reg)
	require.NoError(t, err)
	bayesExp.Def.Policies.Search = "bayesian"
	bayesExp.Def.Policies.Trials = 20
	bayesExp.Def.Policies.StartupTrials = 5
	bayesExp.Def.Policies.Seed = func(v uint64) *uint64 { return &v }(42)
	bayesSrc := bayesExp.BayesianSource()
	bayesRep, err := Run(ctx, bayesExp, bayesSrc, Options{})
	require.NoError(t, err)

	require.NotEmpty(t, randRep.Worlds)
	require.NotEmpty(t, bayesRep.Worlds)

	// Primary world objective
	randBest := randRep.Worlds[0].Policies[0].Objective.Mean
	bayesBest := bayesRep.Worlds[0].Policies[0].Objective.Mean

	t.Logf("Canary primary world - Random best: %f, Bayesian best: %f", randBest, bayesBest)
	require.GreaterOrEqual(t, bayesBest, randBest)
}

func TestBayesian_ContinuousThreshold(t *testing.T) {
	ctx := context.Background()
	exp := thresholdExperiment(t)
	m := exp.Model

	// Continuous parameter without step: theta in [0, 1]
	// Analytic maximum at theta = 0.4 with EV = 18.0
	when := "signal.output.score >= params.theta"
	tpl := thresholdPolicy(t, m, "  theta: {min: 0, max: 1}\n", when)
	exp.Policy = tpl

	src := BayesianSource{
		Policy:        tpl,
		Trials:        25,
		StartupTrials: 5,
		Gamma:         0.20,
		Seed:          42,
	}

	rep, err := Run(ctx, exp, src, Options{All: true})
	require.NoError(t, err)
	require.NotEmpty(t, rep.Worlds[0].Policies)

	for i, p := range rep.Worlds[0].All {
		t.Logf("Candidate %d: theta=%v, obj=%f", i, p.Params["theta"], p.Objective.Mean)
	}

	best := rep.Worlds[0].Policies[0]
	theta, ok := best.Params["theta"].(float64)
	require.True(t, ok)

	t.Logf("Continuous threshold best theta: %f, objective mean: %f", theta, best.Objective.Mean)
	// Theta should be close to 0.4 (within 0.15) and EV close to 18
	require.InDelta(t, 0.4, theta, 0.15)
	require.InDelta(t, 18.0, best.Objective.Mean, 1.0)
}

func TestBayesian_IntegerSteppedParam(t *testing.T) {
	ctx := context.Background()
	exp := thresholdExperiment(t)
	m := exp.Model

	// Discrete stepped parameter: theta in [0, 1] with step 0.05
	when := "signal.output.score >= params.theta"
	tpl := thresholdPolicy(t, m, "  theta: {min: 0, max: 1, step: 0.05}\n", when)
	exp.Policy = tpl
	exp.Def.Runs = 150

	src := BayesianSource{
		Policy:        tpl,
		Trials:        20,
		StartupTrials: 5,
		Seed:          123,
	}

	rep, err := Run(ctx, exp, src, Options{})
	require.NoError(t, err)
	require.NotEmpty(t, rep.Worlds[0].Policies)

	best := rep.Worlds[0].Policies[0]
	theta, ok := best.Params["theta"].(float64)
	require.True(t, ok)

	t.Logf("Stepped threshold best theta: %f, objective mean: %f", theta, best.Objective.Mean)
	require.InDelta(t, 0.4, theta, 0.1)
}

func TestBayesian_Determinism(t *testing.T) {
	ctx := context.Background()
	exp := thresholdExperiment(t)
	tpl := thresholdPolicy(t, exp.Model, "  theta: {min: 0, max: 1}\n", "signal.output.score >= params.theta")
	exp.Policy = tpl
	exp.Def.Runs = 100

	src1 := BayesianSource{Policy: tpl, Trials: 15, StartupTrials: 5, Seed: 9999}
	rep1, err := Run(ctx, exp, src1, Options{All: true})
	require.NoError(t, err)

	src2 := BayesianSource{Policy: tpl, Trials: 15, StartupTrials: 5, Seed: 9999}
	rep2, err := Run(ctx, exp, src2, Options{All: true})
	require.NoError(t, err)

	require.Equal(t, marshal(t, rep1), marshal(t, rep2))

	// Different seed yields different results
	srcDiff := BayesianSource{Policy: tpl, Trials: 15, StartupTrials: 5, Seed: 1111}
	repDiff, err := Run(ctx, exp, srcDiff, Options{All: true})
	require.NoError(t, err)
	require.NotEqual(t, marshal(t, rep1), marshal(t, repDiff))
}

func TestBayesian_PolicySourceWiringAndDefaults(t *testing.T) {
	exp := thresholdExperiment(t)
	def := *exp.Def
	def.Policies = model.Policies{
		Search: "bayesian",
		Policy: def.Policies.Policy,
	}
	exp.Def = &def

	src := exp.PolicySource()
	bayesSrc, ok := src.(BayesianSource)
	require.True(t, ok)
	require.Equal(t, 20, bayesSrc.Trials)
	require.Equal(t, 5, bayesSrc.StartupTrials)
	require.InDelta(t, 0.20, bayesSrc.Gamma, 1e-9)
	require.Equal(t, exp.Def.Seed, bayesSrc.Seed)
	require.False(t, bayesSrc.Racing)

	_, err := bayesSrc.Policies(exp.Model)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bayesian search must be evaluated dynamically by Run")
}

func TestBayesian_RacingComposition(t *testing.T) {
	ctx := context.Background()
	exp := thresholdExperiment(t)
	tpl := thresholdPolicy(t, exp.Model, "  theta: {min: 0, max: 1, step: 0.1}\n", "signal.output.score >= params.theta")
	exp.Policy = tpl
	exp.Def.Runs = 100

	src := BayesianSource{
		Policy:        tpl,
		Trials:        15,
		StartupTrials: 5,
		Racing:        true,
		Seed:          42,
	}

	rep, err := Run(ctx, exp, src, Options{})
	require.NoError(t, err)
	require.NotNil(t, rep)
	require.NotEmpty(t, rep.Worlds)
	require.NotEmpty(t, rep.Worlds[0].Policies)
	require.NotNil(t, rep.Racing)
	require.Greater(t, rep.Racing.TotalRuns, 0)
}
