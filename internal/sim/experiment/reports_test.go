package experiment

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/sim/kernel"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/report"
	"github.com/gargi-indic/gargi-flow/internal/sim/sampler"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/require"
)

const (
	approveAllYAML = "apiVersion: policy/v1\nid: loan-underwriting\nversion: approve-all\ndecisions:\n  screen: {rules: [{else: skip}]}\n  route: {rules: [{else: approve}]}\n  final: {rules: [{else: reject}]}\n"
	// loanMaximize is the last line of the loan objective; tests insert
	// requirements after it.
	loanMaximize = "  maximize: mean\n"
)

// loanVariant writes the loan graph, the loan sim with the given text
// appended to the objective block (requirements) and to the document
// (metrics), the approve-all and reject-all policies, and experimentYAML
// into a temporary directory and loads the experiment.
func loanVariant(t *testing.T, objectiveExtra, simExtra, experimentYAML string) *CompiledExperiment {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "graph.yaml", readLoan(t, "graph.yaml"))
	s := readLoan(t, "sim.yaml")
	require.Contains(t, s, loanMaximize)
	s = strings.Replace(s, loanMaximize, loanMaximize+objectiveExtra, 1) + simExtra
	writeFile(t, dir, "sim.yaml", s)
	writeFile(t, dir, "approve.yaml", approveAllYAML)
	writeFile(t, dir, "reject.yaml", rejectAllYAML)
	writeFile(t, dir, "recession.yaml", readLoan(t, recessionFile))
	exp, err := LoadFile(writeFile(t, dir, "experiment.yaml", experimentYAML), registry(t))
	require.NoError(t, err)
	return exp
}

// TestMetrics_ExactAndMonteCarlo checks declared metrics against hand
// computed values. Under approve-all, bad applicants (20% base, 35%
// recession) lose world.loss and good ones pay 1000.
func TestMetrics_ExactAndMonteCarlo(t *testing.T) {
	runs := 2000
	if raceEnabled {
		runs = 600
	}
	exp := loanVariant(t, "",
		"metrics:\n  bad: \"latent.bad ? 1 : 0\"\n  loss_amount: \"latent.bad ? double(world.loss) : 0.0\"\n",
		"apiVersion: experiment/v1\nmodel: ./sim.yaml\nworlds: [base, recession]\npolicies: {files: [approve.yaml]}\n"+
			"evaluator: both\nruns: "+itoa(runs)+"\nseed: 42\n")
	rep, err := Run(context.Background(), exp, exp.FilesSource(), Options{Workers: 3})
	require.NoError(t, err)

	type world struct{ pBad, loss float64 }
	worlds := []world{{0.2, 4000}, {0.35, 6000}}
	for w, wr := range rep.Worlds {
		pBad, loss := worlds[w].pBad, worlds[w].loss
		r := wr.Policies[0]
		require.Equal(t, []string{"bad", "loss_amount"}, sortedKeys(r.Metrics))

		// Exact: Bernoulli(pBad) and loss·Bernoulli(pBad).
		bad := r.Metrics["bad"]
		require.True(t, bad.Exact)
		require.Equal(t, 2, bad.N)
		require.InDelta(t, pBad, bad.Mean, 1e-12)
		require.InDelta(t, math.Sqrt(pBad*(1-pBad)), bad.StdDev, 1e-12)
		require.Equal(t, 0.0, bad.P50)
		require.Equal(t, 1.0, bad.P95)
		require.Equal(t, 0.0, bad.Min)
		require.Equal(t, 1.0, bad.Max)
		require.Nil(t, bad.ConstraintProb)
		la := r.Metrics["loss_amount"]
		require.InDelta(t, pBad*loss, la.Mean, 1e-9)
		require.InDelta(t, loss*math.Sqrt(pBad*(1-pBad)), la.StdDev, 1e-9)
		require.Equal(t, loss, la.P95)

		// Monte Carlo: objective = 1000 - (1000+loss)·bad run by run, so
		// the metric means follow from the objective mean exactly.
		require.NotNil(t, r.MonteCarlo)
		mc := r.MonteCarlo.Metrics
		require.Equal(t, runs, mc["bad"].N)
		require.False(t, mc["bad"].Exact)
		wantBad := (1000 - r.MonteCarlo.Objective.Mean) / (1000 + loss)
		require.InDelta(t, wantBad, mc["bad"].Mean, 1e-9)
		require.InDelta(t, loss*wantBad, mc["loss_amount"].Mean, 1e-6)
		require.InDelta(t, pBad, mc["bad"].Mean, 4*math.Sqrt(pBad*(1-pBad)/float64(runs)))
		require.Equal(t, 0.0, mc["bad"].P50)
		require.Equal(t, 1.0, mc["bad"].P95)
		require.Equal(t, loss, mc["loss_amount"].Max)
	}

	// Monte Carlo alone reports the same metric estimates as the Monte
	// Carlo half of "both".
	mcOnly, err := Run(context.Background(), withEvaluator(exp, EvaluatorMonteCarlo, runs), exp.FilesSource(), Options{})
	require.NoError(t, err)
	require.Equal(t, rep.Worlds[1].Policies[0].MonteCarlo.Metrics, mcOnly.Worlds[1].Policies[0].Metrics)
	js := marshal(t, mcOnly)
	require.Contains(t, js, `"metrics":{"bad":{"n":`)
	require.NotContains(t, js, `"feasible"`)
	require.NotContains(t, js, `"requirements"`)
	require.NotContains(t, js, `"reports"`)

	// Run records carry the declared metrics (int converted to double).
	policies, err := exp.FilesSource().Policies(exp.Model)
	require.NoError(t, err)
	rec, err := kernel.RunEpisode(context.Background(), exp.Model, exp.Worlds[0], policies[0], sampler.NewMonteCarlo(1), kernel.Options{})
	require.NoError(t, err)
	require.Len(t, rec.Metrics, 2)
	require.Contains(t, []float64{0, 1}, rec.Metrics["bad"])
	require.Nil(t, rec.Events)
}

func itoa(n int) string {
	data, _ := json.Marshal(n)
	return string(data)
}

const loanExhaustiveExact = "apiVersion: experiment/v1\nmodel: ./sim.yaml\nworlds: [base, recession]\npolicies: {search: exhaustive}\nevaluator: exact\nruns: 1\nseed: 42\n"

// TestRequirements_FeasibilityAndRanking requires a 24h P95 duration and a
// 50% approval rate. In a recession the optimum reviews (P95 48h), so it is
// infeasible and ranks after every feasible policy despite its higher mean.
func TestRequirements_FeasibilityAndRanking(t *testing.T) {
	exp := loanVariant(t,
		"  requirements:\n    fast: \"p95(duration) <= duration('24h')\"\n    approvals: \"prob(nodes.approve.status == 'Succeeded') >= 0.5\"\n",
		"", loanExhaustiveExact)
	policies, err := exp.PolicySource().Policies(exp.Model)
	require.NoError(t, err)
	rep, err := Run(context.Background(), exp, staticSource(policies), Options{All: true})
	require.NoError(t, err)

	for _, wr := range rep.Worlds {
		require.Len(t, wr.All, 42)
		for i, r := range wr.All {
			require.NotNil(t, r.Feasible, r.PolicyID)
			require.Len(t, r.Requirements, 2)
			require.Equal(t, "approvals", r.Requirements[0].Name)
			require.Equal(t, "fast", r.Requirements[1].Name)
			feasible := true
			for _, req := range r.Requirements {
				feasible = feasible && req.Satisfied
			}
			require.Equal(t, feasible, *r.Feasible)
			if i == 0 {
				continue
			}
			prev := wr.All[i-1]
			require.False(t, !prev.IsFeasible() && r.IsFeasible(), "feasible policies rank first")
			if prev.IsFeasible() == r.IsFeasible() {
				require.GreaterOrEqual(t, prev.Objective.Mean, r.Objective.Mean)
			}
		}
	}

	// Base: the unconstrained optimum (approve low scores) is feasible:
	// approval rate 0.8·0.9 + 0.2·0.3 = 0.78, no review.
	base := rep.Worlds[0].Policies[0]
	require.Equal(t, bestBaseDesc, base.Description)
	require.Equal(t, []RequirementResult{
		{Name: "approvals", Value: base.Requirements[0].Value, Op: ">=", Bound: 0.5, Satisfied: true},
		{Name: "fast", Value: 0, Op: "<=", Bound: 86400, Satisfied: true},
	}, base.Requirements)
	require.InDelta(t, 0.78, base.Requirements[0].Value, 1e-12)

	// Recession: the review policy (202.5) violates fast and ranks after a
	// feasible policy with a lower mean.
	rec := rep.Worlds[1]
	require.True(t, rec.Policies[0].IsFeasible())
	require.Less(t, rec.Policies[0].Objective.Mean, recessionValue)
	idx := -1
	for i, r := range rec.All {
		if r.Description == bestRecessionDesc {
			idx = i
			require.False(t, r.IsFeasible())
			require.Equal(t, RequirementResult{Name: "fast", Value: 172800, Op: "<=", Bound: 86400, Satisfied: false}, r.Requirements[1])
		}
	}
	require.Greater(t, idx, 0)
	js := marshal(t, rep)
	require.Contains(t, js, `"feasible":false`)
	require.Contains(t, js, `"feasible":true`)
	require.Contains(t, js, `"requirements":[{"name":"approvals","value":`)
}

// TestRequirements_MonteCarloProb checks prob() and the built-in cost metric
// under Monte Carlo: the approval probability equals the approved fraction
// of the runs and p50(cost) is read from the tracked cost.
func TestRequirements_MonteCarloProb(t *testing.T) {
	exp := loanVariant(t,
		"  requirements:\n    approvals: \"prob(nodes.approve.status == 'Succeeded') > 0.7\"\n    cheap: \"p50(cost) <= 20\"\n",
		"metrics:\n  approved: \"nodes.approve.status == 'Succeeded' ? 1.0 : 0.0\"\n",
		"apiVersion: experiment/v1\nmodel: ./sim.yaml\nworlds: [recession]\npolicies: {files: [recession.yaml, reject.yaml]}\nruns: 700\nseed: 5\n")
	rep, err := Run(context.Background(), exp, exp.FilesSource(), Options{All: true})
	require.NoError(t, err)
	for _, r := range rep.Worlds[0].All {
		require.InDelta(t, r.Metrics["approved"].Mean, r.Requirements[0].Value, 1e-12, r.PolicyID)
		require.Equal(t, r.Requirements[0].Value > 0.7, r.Requirements[0].Satisfied)
	}
	// recession-v1 scores everyone (cost 20) and reviews (150) the low
	// band: P50 cost is 170 or 20; reject-all costs nothing.
	all := rep.Worlds[0].All
	require.Equal(t, "loan-underwriting@reject-all", all[len(all)-1].PolicyID)
	require.Equal(t, 0.0, all[len(all)-1].Requirements[1].Value)
	require.False(t, all[len(all)-1].IsFeasible(), "reject-all approves nobody")
}

// independentCVaR enumerates every chance branch of p in every world with
// the kernel directly and returns CVaR_alpha of the prior-weighted mixture.
func independentCVaR(t *testing.T, exp *CompiledExperiment, p sim.Policy, prior []float64, alpha float64) float64 {
	t.Helper()
	type atom struct{ v, w float64 }
	var mix []atom
	for w, world := range exp.Worlds {
		enum := sampler.NewEnumerator()
		for enum.Next() {
			rec, err := kernel.RunEpisode(context.Background(), exp.Model, world, p, enum.Sampler(), kernel.Options{})
			require.NoError(t, err)
			mix = append(mix, atom{rec.Objective, prior[w] * enum.Weight()})
		}
	}
	return cvarOf(mix, alpha, func(a atom) (float64, float64) { return a.v, a.w })
}

// cvarOf integrates the lowest alpha mass of atoms.
func cvarOf[A any](atoms []A, alpha float64, get func(A) (float64, float64)) float64 {
	sort.SliceStable(atoms, func(i, j int) bool {
		vi, _ := get(atoms[i])
		vj, _ := get(atoms[j])
		return vi < vj
	})
	remaining, sum := alpha, 0.0
	for _, a := range atoms {
		v, w := get(a)
		take := math.Min(w, remaining)
		sum += take * v
		remaining -= take
		if remaining <= 0 {
			break
		}
	}
	return sum / alpha
}

// TestReports_LoanRegretRobustPareto checks the ARCHITECTURE.md case 1
// numbers: the base-tuned policy loses 267.5 in a recession, the
// recession-tuned one 142.2 in the base world; under a 70/30 prior the
// expected-value pick is the base-tuned policy and the maximin pick the
// recession-tuned one.
func TestReports_LoanRegretRobustPareto(t *testing.T) {
	exp := loanVariant(t, "", "", loanExhaustiveExact+
		"reports: [regret, robust, 'pareto:cost:min,objective']\nworld_prior: {base: 0.7, recession: 0.3}\n")
	policies, err := exp.PolicySource().Policies(exp.Model)
	require.NoError(t, err)
	rep, err := Run(context.Background(), exp, staticSource(policies), Options{})
	require.NoError(t, err)
	require.NotNil(t, rep.Reports)
	desc := map[string]string{}
	byID := map[string]sim.Policy{}
	for _, p := range policies {
		desc[p.ID()] = p.Describe()
		byID[p.ID()] = p
	}

	rg := rep.Reports.Regret
	require.NotNil(t, rg)
	require.Equal(t, []sim.WorldID{"base", "recession"}, rg.Worlds)
	require.Len(t, rg.Rows, 2)
	require.Empty(t, rg.NoFeasiblePolicy)
	baseRow, recRow := rg.Rows[0], rg.Rows[1]
	require.Equal(t, sim.WorldID("base"), baseRow.TunedFor)
	require.Equal(t, bestBaseDesc, desc[baseRow.PolicyID])
	require.Equal(t, sim.WorldID("recession"), recRow.TunedFor)
	require.Equal(t, bestRecessionDesc, desc[recRow.PolicyID])
	round1 := func(x float64) float64 { return math.Round(x*10) / 10 }

	// Diagonal: the tuned policy in its own world, regret 0.
	require.InDelta(t, 460.0, baseRow.Cells[0].Value, 1e-9)
	require.Zero(t, *baseRow.Cells[0].Regret)
	require.InDelta(t, 202.525, recRow.Cells[1].Value, 1e-9)
	require.Zero(t, *recRow.Cells[1].Regret)
	// Off-diagonal: base-tuned in a recession is -65 (regret 267.525);
	// recession-tuned in the base world is 317.8 (regret 142.2).
	require.InDelta(t, -65.0, baseRow.Cells[1].Value, 1e-9)
	require.InDelta(t, 267.525, *baseRow.Cells[1].Regret, 1e-9)
	require.Equal(t, 267.5, round1(*baseRow.Cells[1].Regret))
	require.InDelta(t, 317.8, recRow.Cells[0].Value, 1e-9)
	require.Equal(t, 142.2, round1(*recRow.Cells[0].Regret))
	require.Equal(t, *baseRow.Cells[1].Regret, baseRow.MaxRegret)
	require.Equal(t, *recRow.Cells[0].Regret, recRow.MaxRegret)
	for _, row := range rg.Rows {
		for _, c := range row.Cells {
			require.Equal(t, row.PolicyID, c.PolicyID)
			require.True(t, c.Feasible)
		}
	}

	rb := rep.Reports.Robust
	require.NotNil(t, rb)
	require.Equal(t, map[sim.WorldID]float64{"base": 0.7, "recession": 0.3}, rb.Prior)
	require.Equal(t, model.DefaultCVaRAlpha, rb.CVaRAlpha)
	require.Equal(t, 42, rb.Eligible)
	// EV: 0.7·460 + 0.3·(-65) = 302.5 beats the recession-tuned policy's
	// 0.7·317.8 + 0.3·202.525 = 283.2175.
	require.Equal(t, baseRow.PolicyID, rb.ExpectedValue.PolicyID)
	require.InDelta(t, 302.5, rb.ExpectedValue.Score, 1e-9)
	require.InDelta(t, -65.0, rb.ExpectedValue.Scores.WorstCase, 1e-9)
	// Maximin: the recession-tuned policy's worst world is 202.525; no
	// policy does better in a recession.
	require.Equal(t, recRow.PolicyID, rb.Maximin.PolicyID)
	require.InDelta(t, 202.525, rb.Maximin.Score, 1e-9)
	require.InDelta(t, 283.2175, rb.Maximin.Scores.ExpectedValue, 1e-9)

	// CVaR: recompute every policy's CVaR from fully enumerated branches.
	prior := []float64{0.7, 0.3}
	bestID, bestCVaR := "", math.Inf(-1)
	for _, p := range policies {
		c := independentCVaR(t, exp, p, prior, model.DefaultCVaRAlpha)
		if c > bestCVaR+1e-9 || (math.Abs(c-bestCVaR) <= 1e-9 && p.ID() < bestID) {
			bestID, bestCVaR = p.ID(), c
		}
		if p.ID() == rb.ExpectedValue.PolicyID {
			require.InDelta(t, c, rb.ExpectedValue.Scores.CVaR, 1e-9)
		}
		if p.ID() == rb.Maximin.PolicyID {
			require.InDelta(t, c, rb.Maximin.Scores.CVaR, 1e-9)
		}
	}
	require.Equal(t, bestID, rb.CVaR.PolicyID)
	require.InDelta(t, bestCVaR, rb.CVaR.Score, 1e-9)
	t.Logf("CVaR(0.05) pick %q = %.4f; EV pick CVaR %.4f; maximin pick CVaR %.4f",
		desc[rb.CVaR.PolicyID], rb.CVaR.Score, rb.ExpectedValue.Scores.CVaR, rb.Maximin.Scores.CVaR)

	// Pareto (cost min, objective max): in the base world the two free
	// policies (approve-all and reject-all skip the score, EV 0) and the
	// base-tuned policy (cost 20, EV 460).
	require.Len(t, rep.Reports.Pareto, 1)
	pf := rep.Reports.Pareto[0]
	require.Equal(t, report.Axis{Metric: "cost", Goal: report.GoalMin}, pf.X)
	require.Equal(t, report.Axis{Metric: "objective", Goal: report.GoalMax}, pf.Y)
	basePts := pf.Worlds[0].Points
	require.Len(t, basePts, 3)
	require.Less(t, basePts[0].PolicyID, basePts[1].PolicyID)
	for _, pt := range basePts[:2] {
		require.InDelta(t, 0.0, pt.X, 1e-12)
		require.InDelta(t, 0.0, pt.Y, 1e-9)
	}
	require.Equal(t, baseRow.PolicyID, basePts[2].PolicyID)
	require.InDelta(t, 20.0, basePts[2].X, 1e-9)
	require.InDelta(t, 460.0, basePts[2].Y, 1e-9)
	recPts := pf.Worlds[1].Points
	require.Len(t, recPts, 2)
	require.Equal(t, recRow.PolicyID, recPts[1].PolicyID)
	require.InDelta(t, 202.525, recPts[1].Y, 1e-9)

	js := marshal(t, rep)
	require.Contains(t, js, `"reports":{"regret":{"worlds":["base","recession"],"rows":[{"tunedFor":"base"`)
	require.Contains(t, js, `"robust":{"prior":{"base":0.7,"recession":0.3},"cvarAlpha":0.05,"eligible":42,"expectedValue":{"policyId":"`+baseRow.PolicyID+`"`)
}

// TestReports_MonteCarloCVaR recomputes the Monte Carlo CVaR from the
// replications themselves: each run of world w weighs prior(w)/runs.
func TestReports_MonteCarloCVaR(t *testing.T) {
	const runs, alpha = 900, 0.2
	exp := loanVariant(t, "", "",
		"apiVersion: experiment/v1\nmodel: ./sim.yaml\nworlds: [base, recession]\npolicies: {files: [recession.yaml, approve.yaml, reject.yaml]}\n"+
			"runs: 900\nseed: 11\nreports: [robust]\nworld_prior: {base: 0.7, recession: 0.3}\ncvar_alpha: 0.2\n")
	policies, err := exp.FilesSource().Policies(exp.Model)
	require.NoError(t, err)
	rep, err := Run(context.Background(), exp, staticSource(policies), Options{Workers: 4})
	require.NoError(t, err)
	rb := rep.Reports.Robust
	require.Equal(t, alpha, rb.CVaRAlpha)
	require.Equal(t, 3, rb.Eligible)

	prior := []float64{0.7, 0.3}
	type atom struct{ v, w float64 }
	cvar := map[string]float64{}
	ev := map[string]float64{}
	for _, p := range policies {
		var mix []atom
		for w, world := range exp.Worlds {
			for r := range runs {
				rec, err := kernel.RunEpisode(context.Background(), exp.Model, world, p, sampler.NewMonteCarlo(sim.Seed(exp.Def.Seed, world.ID, r)), kernel.Options{})
				require.NoError(t, err)
				mix = append(mix, atom{rec.Objective, prior[w] / runs})
				ev[p.ID()] += prior[w] * rec.Objective / runs
			}
		}
		cvar[p.ID()] = cvarOf(mix, alpha, func(a atom) (float64, float64) { return a.v, a.w })
	}
	for _, pick := range []*report.RobustPick{rb.ExpectedValue, rb.Maximin, rb.CVaR} {
		require.InDelta(t, cvar[pick.PolicyID], pick.Scores.CVaR, 1e-6, pick.PolicyID)
		require.InDelta(t, ev[pick.PolicyID], pick.Scores.ExpectedValue, 1e-6, pick.PolicyID)
	}
	best := ""
	for id, c := range cvar {
		if best == "" || c > cvar[best] || (c == cvar[best] && id < best) {
			best = id
		}
	}
	require.Equal(t, best, rb.CVaR.PolicyID)
	t.Logf("Monte Carlo CVaR(0.2): %v", cvar)
}

// TestReports_WorkerCountIndependent runs metrics, requirements and every
// report under evaluator both on 1 and 8 workers.
func TestReports_WorkerCountIndependent(t *testing.T) {
	runs := 2100 // two full chunks and a partial one
	if raceEnabled {
		runs = 1100
	}
	exp := loanVariant(t,
		"  requirements:\n    fast: \"p95(duration) <= duration('24h')\"\n    approvals: \"prob(nodes.approve.status == 'Succeeded') >= 0.3\"\n    wait: \"mean(wait_hours) < 30\"\n",
		"metrics:\n  wait_hours: \"run.duration / 3600.0\"\n  approved: \"nodes.approve.status == 'Succeeded' ? 1.0 : 0.0\"\n",
		"apiVersion: experiment/v1\nmodel: ./sim.yaml\nworlds: [base, recession]\npolicies: {files: [recession.yaml, approve.yaml, reject.yaml]}\n"+
			"evaluator: both\nruns: "+itoa(runs)+"\nseed: 3\n"+
			"reports: [regret, robust, 'pareto:cost:min,objective', 'pareto:wait_hours:min,approved']\nworld_prior: {base: 0.6, recession: 0.4}\ncvar_alpha: 0.1\n")
	r1, err := Run(context.Background(), exp, exp.FilesSource(), Options{Workers: 1, All: true})
	require.NoError(t, err)
	r8, err := Run(context.Background(), exp, exp.FilesSource(), Options{Workers: 8, All: true})
	require.NoError(t, err)
	js := marshal(t, r1)
	require.Equal(t, js, marshal(t, r8))
	require.NotNil(t, r1.Reports.Regret)
	require.NotNil(t, r1.Reports.Robust)
	require.Len(t, r1.Reports.Pareto, 2)
	require.Equal(t, "wait_hours", r1.Reports.Pareto[1].X.Metric)
	require.Contains(t, js, `"monteCarlo":{"objective":`)
	require.Contains(t, js, `"metrics":{"approved":`)
}

func TestCompile_ReportsValidation(t *testing.T) {
	reg := registry(t)
	compile := func(mutate func(*model.Experiment)) (*CompiledExperiment, []engine.ValidationIssue) {
		def := &model.Experiment{
			APIVersion: "experiment/v1", Model: "./sim.yaml", Worlds: []string{"base", "recession"},
			Policies: model.Policies{Files: []string{recessionFile}}, Evaluator: "monte_carlo", Runs: 1, Seed: 1, Top: 5,
		}
		mutate(def)
		exp, errs := Compile(def, loanDir, reg)
		if errs == nil {
			return exp, nil
		}
		return exp, stripped(t, errs.Issues())
	}

	exp, issues := compile(func(d *model.Experiment) {
		d.Reports = []string{"regret", "robust", "pareto:cost:min,objective", "pareto:duration,cost:min"}
		d.WorldPrior = map[string]float64{"base": 0.7, "recession": 0.3 + 5e-10}
		d.CVaRAlpha = 1
	})
	require.Nil(t, issues)
	require.Equal(t, []report.Spec{
		{Kind: report.KindRegret},
		{Kind: report.KindRobust},
		{Kind: report.KindPareto, X: report.Axis{Metric: "cost", Goal: report.GoalMin}, Y: report.Axis{Metric: "objective", Goal: report.GoalMax}},
		{Kind: report.KindPareto, X: report.Axis{Metric: "duration", Goal: report.GoalMax}, Y: report.Axis{Metric: "cost", Goal: report.GoalMin}},
	}, exp.Reports)
	require.Equal(t, 1.0, exp.CVaRAlpha())
	require.InDeltaSlice(t, []float64{0.7, 0.3}, exp.WorldPrior(), 1e-9)

	// Defaults: uniform prior, alpha 0.05; worlds left out of the prior get 0.
	exp, issues = compile(func(*model.Experiment) {})
	require.Nil(t, issues)
	require.Equal(t, []float64{0.5, 0.5}, exp.WorldPrior())
	require.Equal(t, 0.05, exp.CVaRAlpha())
	require.Nil(t, exp.Reports)
	exp, issues = compile(func(d *model.Experiment) { d.WorldPrior = map[string]float64{"recession": 1} })
	require.Nil(t, issues)
	require.Equal(t, []float64{0, 1}, exp.WorldPrior())

	for _, tc := range []struct {
		name   string
		mutate func(*model.Experiment)
		want   []engine.ValidationIssue
	}{
		{"reports", func(d *model.Experiment) {
			d.Reports = []string{"regret", "bogus", "pareto:cost,ghost", "regret", "pareto:cost:min,objective", "pareto:cost:min,objective", "voi:"}
		}, []engine.ValidationIssue{
			{Path: "$.reports[1]", Code: engine.CodeInvalidValue},
			{Path: "$.reports[2]", Code: engine.CodeUnresolvedReference},
			{Path: "$.reports[3]", Code: engine.CodeDuplicateID},
			{Path: "$.reports[5]", Code: engine.CodeDuplicateID},
			{Path: "$.reports[6]", Code: engine.CodeInvalidValue},
		}},
		{"one world", func(d *model.Experiment) {
			d.Worlds = []string{"base"}
			d.Reports = []string{"regret", "robust", "pareto:cost,objective"}
		}, []engine.ValidationIssue{
			{Path: "$.reports[0]", Code: engine.CodeInvalidValue},
			{Path: "$.reports[1]", Code: engine.CodeInvalidValue},
		}},
		{"prior", func(d *model.Experiment) {
			d.WorldPrior = map[string]float64{"base": 0.5, "mars": 0.2, "recession": -0.1}
		}, []engine.ValidationIssue{
			{Path: "$.world_prior", Code: engine.CodeInvalidValue},
			{Path: "$.world_prior.mars", Code: engine.CodeUnresolvedReference},
			{Path: "$.world_prior.recession", Code: engine.CodeInvalidValue},
		}},
		{"prior sum", func(d *model.Experiment) {
			d.WorldPrior = map[string]float64{"base": 0.7, "recession": 0.31}
		}, []engine.ValidationIssue{{Path: "$.world_prior", Code: engine.CodeInvalidValue}}},
		{"prior NaN", func(d *model.Experiment) {
			d.WorldPrior = map[string]float64{"base": math.NaN(), "recession": 1}
		}, []engine.ValidationIssue{{Path: "$.world_prior.base", Code: engine.CodeInvalidValue}}},
		{"empty prior", func(d *model.Experiment) {
			d.WorldPrior = map[string]float64{}
		}, []engine.ValidationIssue{{Path: "$.world_prior", Code: engine.CodeInvalidValue}}},
		{"alpha high", func(d *model.Experiment) { d.CVaRAlpha = 1.5 }, []engine.ValidationIssue{{Path: "$.cvar_alpha", Code: engine.CodeInvalidValue}}},
		{"alpha negative", func(d *model.Experiment) { d.CVaRAlpha = -0.5 }, []engine.ValidationIssue{{Path: "$.cvar_alpha", Code: engine.CodeInvalidValue}}},
		{"alpha NaN", func(d *model.Experiment) { d.CVaRAlpha = math.NaN() }, []engine.ValidationIssue{{Path: "$.cvar_alpha", Code: engine.CodeInvalidValue}}},
	} {
		_, issues := compile(tc.mutate)
		require.Equal(t, tc.want, issues, tc.name)
	}

	// The same checks through the experiment file.
	dir := t.TempDir()
	writeFile(t, dir, "graph.yaml", readLoan(t, "graph.yaml"))
	writeFile(t, dir, "sim.yaml", readLoan(t, "sim.yaml"))
	_, err := LoadFile(writeFile(t, dir, "experiment.yaml", loanExhaustiveExact+"reports: [robust, 'pareto:cost:best,objective']\nworld_prior: {base: 2}\n"), reg)
	var ve *engine.ValidationErrors
	require.ErrorAs(t, err, &ve)
	require.Equal(t, []engine.ValidationIssue{
		{Path: "$.reports[1]", Code: engine.CodeInvalidValue},
		{Path: "$.world_prior", Code: engine.CodeInvalidValue},
	}, stripped(t, ve.Issues()))
}

func stripped(t *testing.T, issues []engine.ValidationIssue) []engine.ValidationIssue {
	t.Helper()
	out := make([]engine.ValidationIssue, len(issues))
	for i, is := range issues {
		require.NotEmpty(t, is.Message)
		out[i] = engine.ValidationIssue{Path: is.Path, Code: is.Code}
	}
	return out
}

// TestReports_NoOptInNoChange checks that a model and experiment without
// metrics, requirements or reports produce none of the new report fields.
func TestReports_NoOptInNoChange(t *testing.T) {
	exp := withEvaluator(loadLoanFile(t), EvaluatorBoth, 50)
	rep, err := Run(context.Background(), exp, FilesSource{Files: []string{filepath.Join(loanDir, recessionFile)}}, Options{All: true})
	require.NoError(t, err)
	require.Nil(t, rep.Reports)
	js := marshal(t, rep)
	for _, key := range []string{`"reports"`, `"feasible"`, `"requirements"`, `"metrics"`} {
		require.NotContains(t, js, key)
	}
	for _, w := range rep.Worlds {
		require.True(t, w.Policies[0].IsFeasible())
		require.Nil(t, w.Policies[0].Feasible)
	}
}

// TestReports_TournamentIntegration tests running an experiment with tournament reports.
func TestReports_TournamentIntegration(t *testing.T) {
	expYaml := "apiVersion: experiment/v1\nmodel: ./sim.yaml\nworlds: [base, recession]\n" +
		"policies: {files: [recession.yaml, approve.yaml, reject.yaml]}\n" +
		"runs: 100\nseed: 42\nreports: [tournament]\nworld_prior: {base: 0.7, recession: 0.3}\n"
	exp := loanVariant(t, "", "", expYaml)
	policies, err := exp.FilesSource().Policies(exp.Model)
	require.NoError(t, err)

	rep, err := Run(context.Background(), exp, staticSource(policies), Options{Workers: 4, All: true})
	require.NoError(t, err)
	require.NotNil(t, rep.Reports)
	require.NotNil(t, rep.Reports.Tournament)
	require.NotNil(t, rep.Tournament)
	require.Equal(t, rep.Reports.Tournament, rep.Tournament)

	tourn := rep.Tournament
	require.Equal(t, 0.05, tourn.Alpha)
	require.Len(t, tourn.Rankings, 3)
	require.Len(t, tourn.Pairwise, 3)

	// Check rankings order and ranks 1, 2, 3
	for i, rk := range tourn.Rankings {
		require.Equal(t, i+1, rk.Rank)
		if i > 0 {
			prev := tourn.Rankings[i-1]
			require.True(t, prev.Mean >= rk.Mean)
		}
	}

	// Verify pairwise symmetry
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			mAB := tourn.Pairwise[i][j]
			mBA := tourn.Pairwise[j][i]

			require.Equal(t, tourn.Rankings[i].PolicyID, mAB.PolicyA)
			require.Equal(t, tourn.Rankings[j].PolicyID, mAB.PolicyB)

			require.InDelta(t, mAB.MeanDiff, -mBA.MeanDiff, 1e-9)
			require.InDelta(t, mAB.CI[0], -mBA.CI[1], 1e-9)
			require.InDelta(t, mAB.CI[1], -mBA.CI[0], 1e-9)
			require.InDelta(t, 1.0, mAB.WinRate+mBA.WinRate, 1e-9)
		}
	}

	// Test with custom alpha: tournament:0.01
	expYaml01 := "apiVersion: experiment/v1\nmodel: ./sim.yaml\nworlds: [base, recession]\n" +
		"policies: {files: [recession.yaml, approve.yaml, reject.yaml]}\n" +
		"runs: 100\nseed: 42\nreports: ['tournament:0.01']\nworld_prior: {base: 0.7, recession: 0.3}\n"
	exp01 := loanVariant(t, "", "", expYaml01)
	rep01, err := Run(context.Background(), exp01, staticSource(policies), Options{Workers: 4, All: true})
	require.NoError(t, err)
	require.NotNil(t, rep01.Tournament)
	require.Equal(t, 0.01, rep01.Tournament.Alpha)

	// Alpha=0.01 CI should be strictly wider than alpha=0.05 CI
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if i == j {
				continue
			}
			w05 := tourn.Pairwise[i][j].CI[1] - tourn.Pairwise[i][j].CI[0]
			w01 := rep01.Tournament.Pairwise[i][j].CI[1] - rep01.Tournament.Pairwise[i][j].CI[0]
			require.True(t, w01 > w05, "pair (%d, %d): w01=%v should be > w05=%v", i, j, w01, w05)
		}
	}
}
