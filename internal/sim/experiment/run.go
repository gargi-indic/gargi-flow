package experiment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/gargi-indic/gargi-flow/internal/sim/kernel"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/report"
	"github.com/gargi-indic/gargi-flow/internal/sim/sampler"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

const (
	// ChunkSize is the number of replications evaluated and summarized as one
	// unit of work. It is fixed so that aggregation does not depend on the
	// worker count.
	ChunkSize = 1000

	// MaxRetained is the number of replications per (world, policy) whose
	// objective and duration values are retained for exact P50/P95. Runs
	// beyond the cap still count towards every other statistic.
	MaxRetained = 1_000_000

	// DefaultTop is the number of policies kept per world when neither
	// Options.Top nor the experiment's top is set.
	DefaultTop = 5

	// EvaluatorMonteCarlo is the evaluator name reported for Monte Carlo
	// estimates.
	EvaluatorMonteCarlo = "monte_carlo"

	// EvaluatorExact is the evaluator name reported for exact estimates
	// obtained by enumerating every chance branch.
	EvaluatorExact = "exact"

	// EvaluatorBoth is the evaluator name reported when exact and Monte
	// Carlo estimates are reported side by side.
	EvaluatorBoth = "both"
)

// Options configures Run.
type Options struct {
	// Workers is the size of the worker pool. Zero selects the experiment's
	// workers, or runtime.GOMAXPROCS(0) when that is unset too. The report
	// does not depend on it.
	Workers int
	// Top is the number of best policies kept per world in
	// WorldReport.Policies. Zero selects the experiment's top, or DefaultTop.
	Top int
	// All also fills WorldReport.All with every policy result, not only the
	// top ones.
	All bool
	// Progress, when set, is called after each completed unit of work with
	// the number of units done and the total number of units. A Monte Carlo
	// unit is one episode (reported per completed chunk); an exact unit is
	// the enumeration of one (world, policy) pair. With evaluator "both" the
	// exact units come first and the total covers both. Calls are
	// serialized, done is non-decreasing, and on success the final call has
	// done == total.
	Progress func(done, total int)
	// ResumeFrom is the checkpoint to resume execution from.
	ResumeFrom *sim.ExperimentCheckpoint
	// Checkpoint, when set, is called after a chunk finishes to allow
	// persisting progress.
	Checkpoint func(sim.ExperimentCheckpoint)
	// RecordSink, when set, receives each evaluated episode record encoded as
	// JSONL. Concurrent writes are serialized.
	RecordSink io.Writer
	// ChunkSize overrides the default chunk size (1000).
	ChunkSize int
}

// Report is the JSON-stable result of an experiment.
type Report struct {
	// Model is the id of the simulated sim/v1 model.
	Model string `json:"model"`
	// Evaluator names the evaluator that produced the estimates:
	// EvaluatorMonteCarlo, EvaluatorExact or EvaluatorBoth.
	Evaluator string `json:"evaluator"`
	// Runs is the number of Monte Carlo replications per world and policy;
	// it is 0 for the exact evaluator.
	Runs int `json:"runs"`
	// Seed is the experiment seed.
	Seed uint64 `json:"seed"`
	// Top is the maximum number of policies listed per world.
	Top int `json:"top"`
	// Worlds holds one entry per evaluated world, in experiment order.
	Worlds []WorldReport `json:"worlds"`
	// Reports holds the reports requested by the experiment's reports
	// list, computed from the primary estimates (exact for evaluator both).
	// It is omitted when no report is requested.
	Reports *ReportSet `json:"reports,omitempty"`
	// Racing holds the elimination history and speedup of racing search.
	Racing *RacingReport `json:"racing,omitempty"`
	// Tournament holds the tournament report if requested.
	Tournament *report.Tournament `json:"tournament,omitempty"`
}

// WorldReport holds the policy results of one world.
type WorldReport struct {
	// World is the world id.
	World sim.WorldID `json:"world"`
	// Policies lists the best policies, feasible ones first, then sorted by
	// objective mean descending and policy id ascending, truncated to
	// Report.Top. With evaluator "both" the exact estimates are the sort
	// key.
	Policies []PolicyResult `json:"policies"`
	// All lists every policy in the same order. It is only set when
	// Options.All is true.
	All []PolicyResult `json:"all,omitempty"`
	// Solution is the world's exact solution (optimal policy, its exact EV
	// and the action values of every information set). It is only set when
	// the policy source is a Solver (policies: {search: exact}); the
	// solution's policy is then also listed in Policies, usually first (with
	// evaluator monte_carlo, sampling noise can rank another world's optimum
	// above it).
	Solution *WorldSolution `json:"solution,omitempty"`
}

// PolicyResult holds the estimates of one policy in one world.
type PolicyResult struct {
	// PolicyID is the policy's ID().
	PolicyID string `json:"policyId"`
	// Description is the policy's Describe().
	Description string `json:"description"`
	// Params holds the policy's bound param values by name (numbers as
	// float64, strings as strings). It is only set for a model.Parameterized
	// policy that declares params, such as a grid or random candidate.
	Params map[string]any `json:"params,omitempty"`
	// Objective estimates the objective value; its ConstraintProb maps every
	// model constraint to the fraction of runs (for exact estimates, the
	// probability) that satisfied it. It is the exact estimate for the exact
	// and both evaluators and the Monte Carlo estimate otherwise.
	Objective sim.Estimate `json:"objective"`
	// Duration estimates the simulated run duration in seconds; its
	// ConstraintProb is nil. It comes from the same evaluator as Objective.
	Duration sim.Estimate `json:"duration"`
	// Metrics estimates every metric declared by the model (sim/v1
	// metrics) by name, like the objective and from the same evaluator;
	// their ConstraintProb is nil. It is omitted when the model declares no
	// metrics.
	Metrics map[string]sim.Estimate `json:"metrics,omitempty"`
	// Requirements holds the outcome of every aggregate requirement of the
	// model (objective.requirements), sorted by name, evaluated on the
	// estimates above. It is omitted when the model has no requirements.
	Requirements []RequirementResult `json:"requirements,omitempty"`
	// Feasible reports whether every requirement is satisfied. It is only
	// set (true or false) when the model has requirements; use IsFeasible.
	Feasible *bool `json:"feasible,omitempty"`
	// MonteCarlo holds the Monte Carlo estimates next to the exact ones in
	// Objective, Duration and Metrics. It is only set for evaluator "both".
	MonteCarlo *Estimates `json:"monteCarlo,omitempty"`
}

// IsFeasible reports whether the policy satisfies every aggregate
// requirement; it is true when the model has none.
func (r PolicyResult) IsFeasible() bool {
	return r.Feasible == nil || *r.Feasible
}

// Estimates pairs the objective and duration estimates of one evaluator.
type Estimates struct {
	// Objective estimates the objective value, with constraint probabilities.
	Objective sim.Estimate `json:"objective"`
	// Duration estimates the simulated run duration in seconds.
	Duration sim.Estimate `json:"duration"`
	// Metrics estimates the declared metrics; omitted when there are none.
	Metrics map[string]sim.Estimate `json:"metrics,omitempty"`
}

// job identifies one chunk of replications of one world.
type job struct {
	World int `json:"world"`
	Chunk int `json:"chunk"`
}

// chunkResult is the summary of one job, one entry per policy.
type chunkResult struct {
	Job      job            `json:"job"`
	Stats    []*policyStats `json:"stats"`
	Episodes int            `json:"episodes"`
	Err      error          `json:"-"`
}

// Run evaluates every policy of src in every world of exp with the
// experiment's evaluator and returns the aggregated report.
//
// The Monte Carlo evaluator runs exp.Def.Runs replications: replication r of
// world w uses sampler.NewMonteCarlo(sim.Seed(exp.Def.Seed, w, r)),
// re-created for each policy (common random numbers). The exact evaluator
// enumerates every chance branch with sampler.NewEnumerator and weights each
// episode by its branch probability; it requires CompiledModel.Discrete and
// otherwise returns *engine.ValidationErrors listing every reason. Evaluator
// "both" runs the two and reports them side by side (PolicyResult.MonteCarlo).
//
// When src is a Solver (ExactSource, policies: {search: exact}), Run calls
// its Solve with the experiment's worlds before evaluating anything, then
// evaluates the distinct solved policies in every world with the
// experiment's evaluator and reports each world's solution in
// WorldReport.Solution. Solving is sequential and not counted by
// Options.Progress.
//
// The model's declared metrics are estimated next to the objective, its
// aggregate requirements are evaluated on each (world, policy)'s primary
// estimates (infeasible policies rank last), and the reports requested by
// the experiment (CompiledExperiment.Reports) are computed into
// Report.Reports once every pair is evaluated. The voi, flip_point and
// policy_tree reports run the exact solver (reusing the solution when src
// is a Solver) and path_heatmap traces each world's best policy with the
// kernel (see buildExplainReports); this work is not counted by
// Options.Progress.
//
// Work runs on a pool of opts.Workers goroutines; the report is identical
// for any worker count. Run stops promptly when ctx is done and returns
// ctx.Err().
func Run(ctx context.Context, exp *CompiledExperiment, src PolicySource, opts Options) (*Report, error) {
	if exp == nil || exp.Def == nil || exp.Model == nil {
		return nil, errors.New("experiment: compiled experiment is nil")
	}
	if src == nil {
		return nil, errors.New("experiment: policy source is nil")
	}
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

	if racing, ok := src.(RacingSource); ok {
		return runRacing(ctx, exp, racing, opts)
	}
	if racing, ok := src.(*RacingSource); ok {
		return runRacing(ctx, exp, *racing, opts)
	}
	if bayesian, ok := src.(BayesianSource); ok {
		return runBayesian(ctx, exp, bayesian, opts)
	}
	if bayesian, ok := src.(*BayesianSource); ok {
		return runBayesian(ctx, exp, *bayesian, opts)
	}

	var (
		policies []sim.Policy
		solution *Solution
		err      error
	)
	if solver, ok := src.(Solver); ok {
		if solution, err = solver.Solve(ctx, exp.Model, exp.Worlds); err != nil {
			return nil, err
		}
		policies, err = solution.Policies(exp.Model)
	} else {
		policies, err = src.Policies(exp.Model)
	}
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
	pairs := len(exp.Worlds) * len(policies)
	exactTotal, mcTotal := 0, 0
	if useExact {
		exactTotal = pairs
	}
	if useMC {
		mcTotal = pairs * exp.Def.Runs
	}
	progress := func(offset int) func(done, total int) {
		if opts.Progress == nil {
			return nil
		}
		return func(done, _ int) { opts.Progress(offset+done, exactTotal+mcTotal) }
	}

	var exactRes, mcRes [][]estimatePair
	if useExact {
		if exactRes, err = runExact(ctx, exp, policies, spec, covers, workers, progress(0)); err != nil {
			return nil, err
		}
	}
	runs := 0
	var mcReplications [][][]float64
	if useMC {
		chunkSize := ChunkSize
		if opts.ChunkSize > 0 {
			chunkSize = opts.ChunkSize
		}
		r := &runner{
			exp:        exp,
			policies:   policies,
			spec:       spec,
			runs:       exp.Def.Runs,
			chunkSize:  chunkSize,
			chunks:     (exp.Def.Runs + chunkSize - 1) / chunkSize,
			recordSink: opts.RecordSink,
		}
		if !useExact {
			r.covers = covers
		}
		results, err := r.run(ctx, workers, opts, progress(exactTotal))
		if err != nil {
			return nil, err
		}
		if hasTournamentReport(exp) {
			mcReplications = r.replications(results)
		}
		mcRes = r.estimates(results)
		runs = exp.Def.Runs
	}

	top := topCount(opts, exp)
	rep := &Report{
		Model:     exp.Model.Def.ID,
		Evaluator: evaluator,
		Runs:      runs,
		Seed:      exp.Def.Seed,
		Top:       top,
		Worlds:    make([]WorldReport, 0, len(exp.Worlds)),
	}
	primary := mcRes
	if useExact {
		primary = exactRes
	}
	feasible := make([][]bool, len(exp.Worlds))
	for w, world := range exp.Worlds {
		list := make([]PolicyResult, len(policies))
		feasible[w] = make([]bool, len(policies))
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
		sortResults(list)
		wr := WorldReport{World: world.ID, Policies: list[:min(top, len(list))]}
		if opts.All {
			wr.All = list
		}
		if solution != nil {
			wr.Solution = &solution.Worlds[w]
		}
		rep.Worlds = append(rep.Worlds, wr)
	}
	if rep.Reports, err = buildReports(exp, spec, policies, primary, feasible, mcReplications); err != nil {
		return nil, err
	}
	if rep.Reports != nil && rep.Reports.Tournament != nil {
		rep.Tournament = rep.Reports.Tournament
	}
	if hasExplainReports(exp) {
		x := &explainer{exp: exp, worlds: rep.Worlds, policies: policies, solution: solution, useExact: useExact, workers: workers}
		if err := buildExplainReports(ctx, rep.Reports, x); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

func workerCount(opts Options, exp *CompiledExperiment) int {
	switch {
	case opts.Workers > 0:
		return opts.Workers
	case exp.Def.Workers > 0:
		return exp.Def.Workers
	default:
		return runtime.GOMAXPROCS(0)
	}
}

func topCount(opts Options, exp *CompiledExperiment) int {
	switch {
	case opts.Top > 0:
		return opts.Top
	case exp.Def.Top > 0:
		return exp.Def.Top
	default:
		return DefaultTop
	}
}

// runner holds the immutable state shared by the workers of one Monte Carlo
// evaluation.
type runner struct {
	exp      *CompiledExperiment
	policies []sim.Policy
	spec     *evalSpec
	// covers, when set, holds the CVaR tail cover of each world.
	covers     []float64
	runs       int
	chunkSize  int
	chunks     int
	recordSink io.Writer
	recordMu   sync.Mutex
}

// run evaluates every job on a pool of workers and returns the chunk
// summaries indexed by [world][chunk].
func (r *runner) run(parent context.Context, workers int, opts Options, progress func(done, total int)) ([][]*chunkResult, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	jobs := len(r.exp.Worlds) * r.chunks
	workers = min(workers, jobs)
	out := make(chan chunkResult, workers)
	var next atomic.Int64

	results := make([][]*chunkResult, len(r.exp.Worlds))
	for w := range results {
		results[w] = make([]*chunkResult, r.chunks)
	}
	total := len(r.exp.Worlds) * r.runs * len(r.policies)
	done := 0
	completedChunks := 0

	completedJobs := make([]bool, jobs)
	if opts.ResumeFrom != nil && len(opts.ResumeFrom.PartialResults) > 0 {
		if err := json.Unmarshal(opts.ResumeFrom.PartialResults, &results); err == nil {
			completedChunks = opts.ResumeFrom.CompletedChunks
			done = opts.ResumeFrom.EvaluatedRuns
			for w := range results {
				for c := range results[w] {
					if results[w][c] != nil {
						completedJobs[w*r.chunks+c] = true
					}
				}
			}
			if completedChunks == 0 {
				for w := range results {
					for c := range results[w] {
						if results[w][c] != nil {
							completedChunks++
							done += results[w][c].Episodes
						}
					}
				}
			}
			if progress != nil {
				progress(done, total)
			}
		}
	}
	firstIncomplete := jobs
	for i := 0; i < jobs; i++ {
		if !completedJobs[i] {
			firstIncomplete = i
			break
		}
	}
	next.Store(int64(firstIncomplete))

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for ctx.Err() == nil {
				i := int(next.Add(1) - 1)
				if i >= jobs {
					return
				}
				if completedJobs[i] {
					continue
				}
				j := job{World: i / r.chunks, Chunk: i % r.chunks}
				res := r.runChunk(ctx, j)
				out <- res
				if res.Err != nil {
					return
				}
			}
		})
	}
	go func() {
		wg.Wait()
		close(out)
	}()

	var firstErr error
	for res := range out {
		if res.Err != nil {
			if firstErr == nil {
				firstErr = res.Err
				cancel()
			}
			continue
		}
		if firstErr != nil {
			continue
		}
		results[res.Job.World][res.Job.Chunk] = &res
		done += res.Episodes
		completedChunks++
		if opts.Checkpoint != nil {
			data, _ := json.Marshal(results)
			opts.Checkpoint(sim.ExperimentCheckpoint{
				CompletedChunks: completedChunks,
				TotalChunks:     jobs,
				EvaluatedRuns:   done,
				PartialResults:  data,
			})
		}
		if progress != nil {
			progress(done, total)
		}
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// runChunk runs every policy over the replications of one chunk of one
// world, sequentially and in replication order.
func (r *runner) runChunk(ctx context.Context, j job) chunkResult {
	res := chunkResult{Job: j, Stats: make([]*policyStats, len(r.policies))}
	for i := range res.Stats {
		res.Stats[i] = newPolicyStats(r.spec)
	}
	world := r.exp.Worlds[j.World]
	first := j.Chunk * r.chunkSize
	last := min(first+r.chunkSize, r.runs)
	for rep := first; rep < last; rep++ {
		seed := sim.Seed(r.exp.Def.Seed, world.ID, rep)
		retain := rep < MaxRetained
		for i, p := range r.policies {
			if err := ctx.Err(); err != nil {
				res.Err = err
				return res
			}
			rec, err := kernel.RunEpisode(ctx, r.exp.Model, world, p, sampler.NewMonteCarlo(seed), kernel.Options{Replication: uint64(rep)})
			if err != nil {
				res.Err = fmt.Errorf("experiment: world %q policy %q replication %d: %w", world.ID, p.ID(), rep, err)
				return res
			}
			if r.recordSink != nil {
				data, err := json.Marshal(rec)
				if err != nil {
					res.Err = fmt.Errorf("experiment: export jsonl: %w", err)
					return res
				}
				data = append(data, '\n')
				r.recordMu.Lock()
				_, writeErr := r.recordSink.Write(data)
				r.recordMu.Unlock()
				if writeErr != nil {
					res.Err = fmt.Errorf("experiment: export jsonl: %w", writeErr)
					return res
				}
			}
			res.Stats[i].add(rec, r.spec, retain)
			res.Episodes++
		}
	}
	return res
}

// replications extracts the replication objective values for each policy in
// each world across all chunks, indexed [policy][world].
func (r *runner) replications(results [][]*chunkResult) [][][]float64 {
	np := len(r.policies)
	nw := len(r.exp.Worlds)
	out := make([][][]float64, np)
	for p := range np {
		out[p] = make([][]float64, nw)
		for w := range nw {
			out[p][w] = make([]float64, 0, r.runs)
			for _, chunk := range results[w] {
				out[p][w] = append(out[p][w], chunk.Stats[p].Objective.Values...)
			}
		}
	}
	return out
}

// estimates merges the chunk summaries in chunk-index order and returns the
// Monte Carlo estimates indexed by [world][policy]. The retained values of a
// pair are released as soon as its estimates (and, for the robust report,
// its lower objective tail) are computed.
func (r *runner) estimates(results [][]*chunkResult) [][]estimatePair {
	out := make([][]estimatePair, len(r.exp.Worlds))
	for w := range r.exp.Worlds {
		out[w] = make([]estimatePair, len(r.policies))
		cover := 0.0
		if r.covers != nil {
			cover = r.covers[w]
		}
		for i := range r.policies {
			agg := newPolicyStats(r.spec)
			for _, chunk := range results[w] {
				agg.merge(chunk.Stats[i])
				chunk.Stats[i] = nil // release retained values early
			}
			out[w][i] = agg.estimates(r.spec, cover)
		}
	}
	return out
}

// sortResults orders results feasible first, then by objective mean
// descending, then by policy id ascending.
func sortResults(list []PolicyResult) {
	sort.SliceStable(list, func(i, j int) bool {
		if fi, fj := list[i].IsFeasible(), list[j].IsFeasible(); fi != fj {
			return fi
		}
		if list[i].Objective.Mean != list[j].Objective.Mean {
			return list[i].Objective.Mean > list[j].Objective.Mean
		}
		return list[i].PolicyID < list[j].PolicyID
	})
}
