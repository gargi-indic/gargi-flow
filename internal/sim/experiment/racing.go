package experiment

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/gargi-indic/gargi-flow/internal/sim/kernel"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/sampler"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// RacingSource is a PolicySource (policies: {search: racing}) that evaluates
// candidate policies using successive halving with Common Random Numbers (CRN)
// paired confidence intervals to eliminate inferior candidates early.
type RacingSource struct {
	Candidates PolicySource
	MinRuns    int
	MaxRuns    int
	Factor     float64
	Alpha      float64
}

var _ PolicySource = RacingSource{}

// Policies delegates candidate enumeration to the inner Candidates PolicySource.
func (s RacingSource) Policies(m *model.CompiledModel) ([]sim.Policy, error) {
	if s.Candidates == nil {
		return nil, errors.New("experiment: racing candidate policy source is nil")
	}
	return s.Candidates.Policies(m)
}

// RacingReport summarizes the racing optimizer run across all worlds.
type RacingReport struct {
	// TotalRuns is the total number of episode replications executed across all
	// worlds and candidates during racing.
	TotalRuns int `json:"totalRuns"`
	// FullRuns is the number of replications a full evaluation would have required
	// (len(worlds) * len(candidates) * Runs).
	FullRuns int `json:"fullRuns"`
	// Speedup is FullRuns / TotalRuns (>= 1.0).
	Speedup float64 `json:"speedup"`
	// EliminationHistory records the elimination history across rounds for the
	// primary / first world.
	EliminationHistory []EliminationRound `json:"eliminationHistory,omitempty"`
	// Worlds holds per-world racing results and elimination histories.
	Worlds []WorldRacingReport `json:"worlds,omitempty"`
}

// WorldRacingReport holds the racing elimination history for one world.
type WorldRacingReport struct {
	World              sim.WorldID        `json:"world"`
	Winner             string             `json:"winner"`
	TotalRuns          int                `json:"totalRuns"`
	FullRuns           int                `json:"fullRuns"`
	Speedup            float64            `json:"speedup"`
	EliminationHistory []EliminationRound `json:"eliminationHistory"`
}

// EliminationRound records the state and eliminations of one round of racing.
type EliminationRound struct {
	Round            int                  `json:"round"`
	RunsPerCandidate int                  `json:"runsPerCandidate"`
	ActiveCandidates int                  `json:"activeCandidates"`
	Eliminated       []string             `json:"eliminated,omitempty"`
	DifferenceStats  []CandidateDiffStats `json:"differenceStats,omitempty"`
}

// CandidateDiffStats records the paired difference statistics of one candidate
// compared against the leading candidate (p*).
type CandidateDiffStats struct {
	PolicyID   string  `json:"policyId"`
	MeanDiff   float64 `json:"meanDiff"`
	StdErr     float64 `json:"stdErr"`
	LowerBound float64 `json:"lowerBound"`
	Eliminated bool    `json:"eliminated"`
	Reason     string  `json:"reason,omitempty"`
}

// candState tracks the evaluation progress of a candidate policy in one world.
type candState struct {
	policy        sim.Policy
	stats         *policyStats
	objectives    []float64
	evaluatedRuns int
	eliminated    bool
}

// runRacing executes the racing optimizer over the experiment's worlds.
func runRacing(ctx context.Context, exp *CompiledExperiment, racing RacingSource, opts Options) (*Report, error) {
	evaluator := exp.Def.Evaluator
	if evaluator == "" {
		evaluator = EvaluatorMonteCarlo
	}
	switch evaluator {
	case EvaluatorMonteCarlo, EvaluatorExact, EvaluatorBoth:
	default:
		return nil, fmt.Errorf("experiment: unknown evaluator %q", evaluator)
	}
	useMC, useExact := evaluator != EvaluatorExact, evaluator != EvaluatorMonteCarlo
	if useMC && exp.Def.Runs < 1 {
		return nil, fmt.Errorf("experiment: runs must be >= 1, got %d", exp.Def.Runs)
	}
	if useExact {
		if err := requireDiscrete(exp.Model, "the exact evaluator"); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	policies, err := racing.Candidates.Policies(exp.Model)
	if err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return nil, errors.New("experiment: policy source returned no policies")
	}
	seen := make(map[string]bool, len(policies))
	for _, p := range policies {
		if seen[p.ID()] {
			return nil, fmt.Errorf("experiment: duplicate policy id %q", p.ID())
		}
		seen[p.ID()] = true
	}

	spec := newEvalSpec(exp)
	covers := tailCovers(exp)
	workers := workerCount(opts, exp)
	top := topCount(opts, exp)

	minRuns := racing.MinRuns
	if minRuns <= 0 {
		minRuns = min(10, exp.Def.Runs)
		if minRuns < 2 && exp.Def.Runs >= 2 {
			minRuns = 2
		}
	}
	maxRuns := racing.MaxRuns
	if maxRuns <= 0 {
		maxRuns = exp.Def.Runs
	}
	factor := racing.Factor
	if factor <= 1.0 {
		factor = 2.0
	}
	alpha := racing.Alpha
	if alpha <= 0 || alpha >= 1.0 {
		alpha = 0.05
	}

	var exactRes [][]estimatePair
	if useExact {
		var err error
		exactRes, err = runExact(ctx, exp, policies, spec, covers, workers, nil)
		if err != nil {
			return nil, err
		}
	}

	totalRunsAcrossWorlds := 0
	worldReports := make([]WorldRacingReport, len(exp.Worlds))
	mcRes := make([][]estimatePair, len(exp.Worlds))
	repWorlds := make([]WorldReport, 0, len(exp.Worlds))
	feasible := make([][]bool, len(exp.Worlds))

	for w, world := range exp.Worlds {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		cands := make([]*candData, len(policies))
		for i, p := range policies {
			cands[i] = &candData{
				policy:     p,
				stats:      newPolicyStats(spec),
				objectives: make([]float64, 0, exp.Def.Runs),
			}
		}

		active := make([]int, len(policies))
		for i := range active {
			active[i] = i
		}

		worldTotalRuns := 0
		rounds := make([]EliminationRound, 0, 8)
		currentRuns := minRuns
		roundNum := 0

		for len(active) > 1 && currentRuns <= maxRuns {
			nR := currentRuns
			deltaRuns := nR - cands[active[0]].evaluatedRuns
			if deltaRuns > 0 {
				if err := evaluateCandidatesWork(ctx, exp, world, active, cands, deltaRuns, workers, spec); err != nil {
					return nil, err
				}
				worldTotalRuns += len(active) * deltaRuns
			}

			// Rank active candidates: feasible first, then objective mean descending, then policy ID ascending
			type rankItem struct {
				idx      int
				feasible bool
				mean     float64
				id       string
			}
			ranked := make([]rankItem, len(active))
			for i, aIdx := range active {
				c := cands[aIdx]
				pair := c.stats.estimates(spec, 0)
				_, feas := spec.evaluateRequirements(pair)
				ranked[i] = rankItem{
					idx:      aIdx,
					feasible: feas,
					mean:     pair.objective.Mean,
					id:       c.policy.ID(),
				}
			}
			sort.SliceStable(ranked, func(i, j int) bool {
				if ranked[i].feasible != ranked[j].feasible {
					return ranked[i].feasible
				}
				if ranked[i].mean != ranked[j].mean {
					return ranked[i].mean > ranked[j].mean
				}
				return ranked[i].id < ranked[j].id
			})

			leaderIdx := ranked[0].idx
			leader := cands[leaderIdx]
			leaderFeasible := ranked[0].feasible

			diffStats := make([]CandidateDiffStats, 0, len(active))
			eliminatedInRound := make([]string, 0)
			nextActive := make([]int, 0, len(active))

			for _, rk := range ranked {
				aIdx := rk.idx
				c := cands[aIdx]
				if aIdx == leaderIdx {
					diffStats = append(diffStats, CandidateDiffStats{
						PolicyID:   c.policy.ID(),
						MeanDiff:   0,
						StdErr:     0,
						LowerBound: 0,
						Eliminated: false,
					})
					nextActive = append(nextActive, aIdx)
					continue
				}

				sumD := 0.0
				for rep := 0; rep < nR; rep++ {
					sumD += leader.objectives[rep] - c.objectives[rep]
				}
				meanD := sumD / float64(nR)

				sumSq := 0.0
				for rep := 0; rep < nR; rep++ {
					diff := (leader.objectives[rep] - c.objectives[rep]) - meanD
					sumSq += diff * diff
				}

				var se, lb float64
				if nR > 1 {
					sd := math.Sqrt(sumSq / float64(nR-1))
					se = sd / math.Sqrt(float64(nR))
					tCrit := studentTQuantile(alpha, nR-1)
					lb = meanD - tCrit*se
				} else {
					lb = meanD
				}

				elim := false
				reason := ""
				if leaderFeasible && !rk.feasible {
					elim = true
					reason = "constraint_violation"
				} else if lb > 0 {
					elim = true
					reason = "inferior_ci"
				}

				diffStats = append(diffStats, CandidateDiffStats{
					PolicyID:   c.policy.ID(),
					MeanDiff:   meanD,
					StdErr:     se,
					LowerBound: lb,
					Eliminated: elim,
					Reason:     reason,
				})

				if elim {
					c.eliminated = true
					eliminatedInRound = append(eliminatedInRound, c.policy.ID())
				} else {
					nextActive = append(nextActive, aIdx)
				}
			}

			// Successive halving budget cap: retain at most ceil(len(active) / factor) candidates
			maxSurviving := int(math.Ceil(float64(len(active)) / factor))
			if maxSurviving < 1 {
				maxSurviving = 1
			}
			if len(nextActive) > maxSurviving {
				for i := maxSurviving; i < len(nextActive); i++ {
					dropIdx := nextActive[i]
					c := cands[dropIdx]
					c.eliminated = true
					eliminatedInRound = append(eliminatedInRound, c.policy.ID())
					for j := range diffStats {
						if diffStats[j].PolicyID == c.policy.ID() {
							diffStats[j].Eliminated = true
							if diffStats[j].Reason == "" {
								diffStats[j].Reason = "successive_halving"
							}
							break
						}
					}
				}
				nextActive = nextActive[:maxSurviving]
			}

			rounds = append(rounds, EliminationRound{
				Round:            roundNum,
				RunsPerCandidate: nR,
				ActiveCandidates: len(active),
				Eliminated:       eliminatedInRound,
				DifferenceStats:  diffStats,
			})

			active = nextActive
			if len(active) <= 1 || nR >= maxRuns {
				break
			}

			roundNum++
			nextRuns := int(math.Ceil(float64(nR) * factor))
			if nextRuns <= nR {
				nextRuns = nR + 1
			}
			if nextRuns > maxRuns {
				nextRuns = maxRuns
			}
			currentRuns = nextRuns
		}

		winnerIdx := active[0]
		winner := cands[winnerIdx]

		// Complete winning policy's remaining runs up to exp.Def.Runs
		if winner.evaluatedRuns < exp.Def.Runs {
			remRuns := exp.Def.Runs - winner.evaluatedRuns
			first := winner.evaluatedRuns
			for rep := first; rep < exp.Def.Runs; rep++ {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				seed := sim.Seed(exp.Def.Seed, world.ID, rep)
				rec, err := kernel.RunEpisode(ctx, exp.Model, world, winner.policy, sampler.NewMonteCarlo(seed), kernel.Options{Replication: uint64(rep)})
				if err != nil {
					return nil, err
				}
				winner.stats.add(rec, spec, rep < MaxRetained)
				winner.objectives = append(winner.objectives, rec.Objective)
			}
			worldTotalRuns += remRuns
			winner.evaluatedRuns = exp.Def.Runs
		}

		totalRunsAcrossWorlds += worldTotalRuns
		fullRuns := len(policies) * exp.Def.Runs
		speedup := float64(fullRuns) / float64(worldTotalRuns)

		worldReports[w] = WorldRacingReport{
			World:              world.ID,
			Winner:             winner.policy.ID(),
			TotalRuns:          worldTotalRuns,
			FullRuns:           fullRuns,
			Speedup:            speedup,
			EliminationHistory: rounds,
		}

		// Compute estimates for all policies in this world
		mcRes[w] = make([]estimatePair, len(policies))
		cover := 0.0
		if covers != nil {
			cover = covers[w]
		}
		for i, c := range cands {
			mcRes[w][i] = c.stats.estimates(spec, cover)
		}

		// Build WorldReport
		primary := mcRes
		if useExact {
			primary = exactRes
		}
		list := make([]PolicyResult, len(policies))
		feasible[w] = make([]bool, len(policies))
		roundElim := make(map[string]int, len(policies))
		for _, c := range cands {
			if !c.eliminated {
				roundElim[c.policy.ID()] = 999999
			}
		}
		for rIdx, r := range rounds {
			for _, id := range r.Eliminated {
				roundElim[id] = rIdx
			}
		}

		for i, p := range policies {
			res := PolicyResult{PolicyID: p.ID(), Description: p.Describe()}
			if pp, ok := p.(model.Parameterized); ok {
				if params := pp.Params(); len(params) > 0 {
					res.Params = params
				}
			}
			pair := primary[w][i]
			res.Objective, res.Duration = pair.objective, pair.duration
			res.Metrics = spec.declaredMetrics(pair)
			var ok bool
			res.Requirements, ok = spec.evaluateRequirements(pair)
			if res.Requirements != nil {
				res.Feasible = &ok
			}
			feasible[w][i] = ok
			if useExact && useMC {
				mc := mcRes[w][i]
				res.MonteCarlo = &Estimates{Objective: mc.objective, Duration: mc.duration, Metrics: spec.declaredMetrics(mc)}
			}
			list[i] = res
		}
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].PolicyID == winner.policy.ID() {
				return true
			}
			if list[j].PolicyID == winner.policy.ID() {
				return false
			}
			if fi, fj := list[i].IsFeasible(), list[j].IsFeasible(); fi != fj {
				return fi
			}
			ri := roundElim[list[i].PolicyID]
			rj := roundElim[list[j].PolicyID]
			if ri != rj {
				return ri > rj
			}
			if list[i].Objective.Mean != list[j].Objective.Mean {
				return list[i].Objective.Mean > list[j].Objective.Mean
			}
			return list[i].PolicyID < list[j].PolicyID
		})
		wr := WorldReport{World: world.ID, Policies: list[:min(top, len(list))]}
		if opts.All {
			wr.All = list
		}
		repWorlds = append(repWorlds, wr)
	}

	overallFullRuns := len(exp.Worlds) * len(policies) * exp.Def.Runs
	overallSpeedup := float64(overallFullRuns) / float64(totalRunsAcrossWorlds)
	racingRep := &RacingReport{
		TotalRuns:          totalRunsAcrossWorlds,
		FullRuns:           overallFullRuns,
		Speedup:            overallSpeedup,
		EliminationHistory: worldReports[0].EliminationHistory,
		Worlds:             worldReports,
	}

	rep := &Report{
		Model:     exp.Model.Def.ID,
		Evaluator: evaluator,
		Runs:      exp.Def.Runs,
		Seed:      exp.Def.Seed,
		Top:       top,
		Worlds:    repWorlds,
		Racing:    racingRep,
	}

	primary := mcRes
	if useExact {
		primary = exactRes
	}
	if rep.Reports, err = buildReports(exp, spec, policies, primary, feasible); err != nil {
		return nil, err
	}
	if rep.Reports != nil {
		rep.Reports.Racing = racingRep
	}
	if hasExplainReports(exp) {
		x := &explainer{exp: exp, worlds: rep.Worlds, policies: policies, solution: nil, useExact: useExact, workers: workers}
		if err := buildExplainReports(ctx, rep.Reports, x); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

type candData = candState

// candidateWorkChunk defines a unit of replications evaluated for one candidate.
type candidateWorkChunk struct {
	candIdx    int
	repStart   int
	repEnd     int
	stats      *policyStats
	objectives []float64
	err        error
}

// evaluateCandidatesWork runs deltaRuns replications for each active candidate using workers.
// Results are accumulated in replication index order to preserve determinism and worker invariance.
func evaluateCandidatesWork(ctx context.Context, exp *CompiledExperiment, world sim.World, active []int, cands []*candData, deltaRuns, workers int, spec *evalSpec) error {
	type workJob struct {
		activeIdx int
		repStart  int
		repEnd    int
	}

	const subChunkSize = 500
	var jobs []workJob
	for aIdx := range active {
		c := cands[active[aIdx]]
		start := c.evaluatedRuns
		end := start + deltaRuns
		for rep := start; rep < end; rep += subChunkSize {
			chunkEnd := min(rep+subChunkSize, end)
			jobs = append(jobs, workJob{
				activeIdx: aIdx,
				repStart:  rep,
				repEnd:    chunkEnd,
			})
		}
	}

	numJobs := len(jobs)
	workers = min(workers, max(1, numJobs))
	if workers <= 1 {
		// Single-worker sequential path
		for aIdx := range active {
			c := cands[active[aIdx]]
			start := c.evaluatedRuns
			end := start + deltaRuns
			for rep := start; rep < end; rep++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				seed := sim.Seed(exp.Def.Seed, world.ID, rep)
				rec, err := kernel.RunEpisode(ctx, exp.Model, world, c.policy, sampler.NewMonteCarlo(seed), kernel.Options{Replication: uint64(rep)})
				if err != nil {
					return fmt.Errorf("experiment: world %q policy %q replication %d: %w", world.ID, c.policy.ID(), rep, err)
				}
				c.stats.add(rec, spec, rep < MaxRetained)
				c.objectives = append(c.objectives, rec.Objective)
			}
			c.evaluatedRuns = end
		}
		return nil
	}

	// Concurrent evaluation path
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]candidateWorkChunk, numJobs)
	var nextJob atomic.Int64
	var wg sync.WaitGroup

	for range workers {
		wg.Go(func() {
			for cctx.Err() == nil {
				jIdx := int(nextJob.Add(1) - 1)
				if jIdx >= numJobs {
					return
				}
				job := jobs[jIdx]
				c := cands[active[job.activeIdx]]
				count := job.repEnd - job.repStart
				chunk := candidateWorkChunk{
					candIdx:    job.activeIdx,
					repStart:   job.repStart,
					repEnd:     job.repEnd,
					stats:      newPolicyStats(spec),
					objectives: make([]float64, count),
				}
				for rep := job.repStart; rep < job.repEnd; rep++ {
					if err := cctx.Err(); err != nil {
						chunk.err = err
						results[jIdx] = chunk
						return
					}
					seed := sim.Seed(exp.Def.Seed, world.ID, rep)
					rec, err := kernel.RunEpisode(cctx, exp.Model, world, c.policy, sampler.NewMonteCarlo(seed), kernel.Options{Replication: uint64(rep)})
					if err != nil {
						chunk.err = fmt.Errorf("experiment: world %q policy %q replication %d: %w", world.ID, c.policy.ID(), rep, err)
						results[jIdx] = chunk
						cancel()
						return
					}
					chunk.stats.add(rec, spec, rep < MaxRetained)
					chunk.objectives[rep-job.repStart] = rec.Objective
				}
				results[jIdx] = chunk
			}
		})
	}

	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	// Check errors and merge results in strictly deterministic job order
	for jIdx, res := range results {
		if res.err != nil {
			return res.err
		}
		_ = jIdx
	}

	for jIdx, job := range jobs {
		res := results[jIdx]
		c := cands[active[job.activeIdx]]
		c.stats.merge(res.stats)
		c.objectives = append(c.objectives, res.objectives...)
	}

	for aIdx := range active {
		c := cands[active[aIdx]]
		c.evaluatedRuns += deltaRuns
	}
	return nil
}

// ----------------------------------------------------------------------------
// Statistics & Distributions
// ----------------------------------------------------------------------------

// studentTQuantile computes the critical value t such that P(T_{df} > t) = alpha,
// where alpha in (0, 0.5) and df >= 1.
func studentTQuantile(alpha float64, df int) float64 {
	if df < 1 {
		return 0
	}
	p := 1.0 - alpha
	if df == 1 {
		return math.Tan(math.Pi * (p - 0.5))
	}
	if df == 2 {
		return (2.0*p - 1.0) / math.Sqrt(2.0*p*(1.0-p))
	}

	v := float64(df)
	z := normalCDFInv(p)
	// Cornish-Fisher initial approximation
	z3 := z * z * z
	z5 := z3 * z * z
	t := z + (z3+z)/(4.0*v) + (5.0*z5+16.0*z3+3.0*z)/(96.0*v*v)

	// Refine with Newton-Raphson iterations
	for range 4 {
		cdf := studentTCDF(t, v)
		pdf := studentTPDF(t, v)
		if pdf <= 0 {
			break
		}
		diff := cdf - p
		step := diff / pdf
		t -= step
		if math.Abs(step) < 1e-12 {
			break
		}
	}
	return t
}

// studentTCDF computes the cumulative distribution function of Student's t distribution with df degrees of freedom.
func studentTCDF(t, df float64) float64 {
	if t == 0 {
		return 0.5
	}
	x := df / (df + t*t)
	ib := incBeta(x, df/2.0, 0.5)
	if t > 0 {
		return 1.0 - 0.5*ib
	}
	return 0.5 * ib
}

// studentTPDF computes the probability density function of Student's t distribution with df degrees of freedom.
func studentTPDF(t, df float64) float64 {
	lg1, _ := math.Lgamma((df + 1.0) / 2.0)
	lg2, _ := math.Lgamma(df / 2.0)
	logVal := lg1 - lg2 - 0.5*(math.Log(math.Pi)+math.Log(df)) - ((df+1.0)/2.0)*math.Log(1.0+t*t/df)
	return math.Exp(logVal)
}

// incBeta computes the regularized incomplete beta function I_x(a, b).
func incBeta(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}

	lgA, _ := math.Lgamma(a)
	lgB, _ := math.Lgamma(b)
	lgAB, _ := math.Lgamma(a + b)
	logBeta := lgA + lgB - lgAB

	if x < (a+1.0)/(a+b+2.0) {
		front := math.Exp(a*math.Log(x)+b*math.Log(1.0-x)-logBeta) / a
		return front * incBetaCF(x, a, b)
	}
	front := math.Exp(b*math.Log(1.0-x)+a*math.Log(x)-logBeta) / b
	return 1.0 - front*incBetaCF(1.0-x, b, a)
}

// incBetaCF computes the continued fraction component of the incomplete beta function (Lentz's method).
func incBetaCF(x, a, b float64) float64 {
	const (
		maxIter = 200
		eps     = 1e-15
		fpMin   = 1e-30
	)

	qab := a + b
	qap := a + 1.0
	qam := a - 1.0
	c := 1.0
	d := 1.0 - qab*x/qap
	if math.Abs(d) < fpMin {
		d = fpMin
	}
	d = 1.0 / d
	h := d

	for m := 1; m <= maxIter; m++ {
		mF := float64(m)
		m2 := 2.0 * mF

		// Even step
		aa := mF * (b - mF) * x / ((qam + m2) * (a + m2))
		d = 1.0 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1.0 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1.0 / d
		h *= d * c

		// Odd step
		aa = -(a + mF) * (qab + mF) * x / ((a + m2) * (qap + m2))
		d = 1.0 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1.0 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1.0 / d
		del := d * c
		h *= del
		if math.Abs(del-1.0) < eps {
			break
		}
	}
	return h
}

// normalCDFInv computes the inverse of the standard normal cumulative distribution function (Acklam algorithm).
func normalCDFInv(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(1)
	}

	a := [6]float64{
		-3.969683028665376e+01, 2.204091108189051e+02,
		-2.755581241086124e+02, 1.383577518672690e+02,
		-3.066479806614716e+01, 2.506628277459239e+00,
	}
	b := [5]float64{
		-5.447609879822406e+01, 1.615858368580409e+02,
		-1.556989798598866e+02, 6.680131188771972e+01,
		-1.328068155288572e+01,
	}
	c := [6]float64{
		-7.784894002430293e-03, -3.223964580411365e-01,
		-2.400758277161838e+00, -2.549732539343734e+00,
		4.374664141464968e+00, 2.938163982698783e+00,
	}
	d := [4]float64{
		7.784695709041462e-03, 3.224671290700398e-01,
		2.445134137142996e+00, 3.754408661907416e+00,
	}

	const (
		pLow  = 0.02425
		pHigh = 1.0 - pLow
	)

	if p < pLow {
		q := math.Sqrt(-2.0 * math.Log(p))
		return (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1.0)
	}
	if p <= pHigh {
		q := p - 0.5
		r := q * q
		return (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q /
			(((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1.0)
	}
	q := math.Sqrt(-2.0 * math.Log(1.0-p))
	return -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
		((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1.0)
}
