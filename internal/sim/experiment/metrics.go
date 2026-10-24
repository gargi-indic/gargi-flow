package experiment

import (
	"fmt"
	"math"
	"sort"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/report"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// priorTolerance is the largest accepted deviation of the sum of
// world_prior from 1.
const priorTolerance = 1e-9

// RequirementResult is the outcome of one aggregate requirement for one
// policy in one world.
type RequirementResult struct {
	// Name is the requirement's key in objective.requirements.
	Name string `json:"name"`
	// Value is the estimated aggregate: the mean, P50 or P95 of the metric,
	// or the probability of the prob() condition.
	Value float64 `json:"value"`
	// Op is the comparison operator.
	Op string `json:"op"`
	// Bound is the right-hand side (durations in seconds).
	Bound float64 `json:"bound"`
	// Satisfied reports whether Value Op Bound holds.
	Satisfied bool `json:"satisfied"`
}

// ReportSet holds the decision-quality reports requested by the
// experiment's reports list; only requested reports are set.
type ReportSet struct {
	// Regret is the cross-world regret matrix.
	Regret *report.Regret `json:"regret,omitempty"`
	// Robust holds the expected-value, maximin and CVaR picks.
	Robust *report.Robust `json:"robust,omitempty"`
	// Pareto holds one entry per requested pareto report, in request order.
	Pareto []report.Pareto `json:"pareto,omitempty"`
	// VOI holds, per requested voi report (in request order), one entry
	// per world (in experiment order).
	VOI []report.VOI `json:"voi,omitempty"`
	// FlipPoints holds one entry per requested flip_point report, in
	// request order.
	FlipPoints []report.FlipPoint `json:"flipPoints,omitempty"`
	// PathHeatmap holds one entry per world, in experiment order.
	PathHeatmap []report.PathHeatmap `json:"pathHeatmap,omitempty"`
	// PolicyTree holds one entry per world, in experiment order.
	PolicyTree []report.PolicyTree `json:"policyTree,omitempty"`
	// Racing holds the elimination history and speedup of racing search.
	Racing *RacingReport `json:"racing,omitempty"`
}

// evalSpec lists what is extracted from every run record of an
// experiment, beyond the objective and the duration.
type evalSpec struct {
	// constraints are the model's per-run constraint names, sorted.
	constraints []string
	// metrics are the tracked metrics: the declared metrics (sorted), then
	// the built-in cost when a requirement or report reads it.
	metrics []string
	// metricIndex maps a tracked metric name to its index in metrics.
	metricIndex map[string]int
	// declared is the number of declared metrics at the front of metrics.
	declared int
	// events are the names of the prob() requirements, sorted.
	events []string
	// requirements are the model's aggregate requirements, sorted by name.
	requirements []model.CompiledRequirement
}

// newEvalSpec returns the extraction plan of exp.
func newEvalSpec(exp *CompiledExperiment) *evalSpec {
	m := exp.Model
	s := &evalSpec{
		constraints:  sortedKeys(m.CompiledConstraints),
		metrics:      append([]string(nil), m.MetricOrder...),
		declared:     len(m.MetricOrder),
		requirements: m.Requirements,
	}
	needCost := false
	for _, r := range m.Requirements {
		if r.Agg == model.AggProb {
			s.events = append(s.events, r.Name)
		} else if r.Arg == model.MetricCost {
			needCost = true
		}
	}
	for _, spec := range exp.Reports {
		for _, name := range spec.Metrics() {
			needCost = needCost || name == model.MetricCost
		}
	}
	if needCost {
		s.metrics = append(s.metrics, model.MetricCost)
	}
	s.metricIndex = make(map[string]int, len(s.metrics))
	for i, name := range s.metrics {
		s.metricIndex[name] = i
	}
	return s
}

// metricValue returns tracked metric i of rec.
func (s *evalSpec) metricValue(rec sim.RunRecord, i int) float64 {
	if i >= s.declared {
		return rec.Cost
	}
	return rec.Metrics[s.metrics[i]]
}

// metric returns the estimate of a built-in or tracked metric.
func (s *evalSpec) metric(pair estimatePair, name string) sim.Estimate {
	switch name {
	case model.MetricObjective:
		return pair.objective
	case model.MetricDuration:
		return pair.duration
	}
	return pair.metrics[s.metricIndex[name]]
}

// declaredMetrics returns the declared metric estimates of pair by name, or
// nil when the model declares none.
func (s *evalSpec) declaredMetrics(pair estimatePair) map[string]sim.Estimate {
	if s.declared == 0 {
		return nil
	}
	out := make(map[string]sim.Estimate, s.declared)
	for i, name := range s.metrics[:s.declared] {
		out[name] = pair.metrics[i]
	}
	return out
}

// evaluateRequirements evaluates every requirement on the estimates of
// pair. It returns nil results when the model has no requirements.
func (s *evalSpec) evaluateRequirements(pair estimatePair) ([]RequirementResult, bool) {
	if len(s.requirements) == 0 {
		return nil, true
	}
	out := make([]RequirementResult, len(s.requirements))
	feasible := true
	event := 0
	for i, r := range s.requirements {
		var v float64
		switch r.Agg {
		case model.AggProb:
			v = pair.events[event]
			event++
		case model.AggMean:
			v = s.metric(pair, r.Arg).Mean
		case model.AggP50:
			v = s.metric(pair, r.Arg).P50
		case model.AggP95:
			v = s.metric(pair, r.Arg).P95
		}
		ok := r.Op.Holds(v, r.Bound)
		out[i] = RequirementResult{Name: r.Name, Value: v, Op: string(r.Op), Bound: r.Bound, Satisfied: ok}
		feasible = feasible && ok
	}
	return out, feasible
}

// tailCovers returns the fraction of each world's objective distribution
// the robust report's CVaR needs, or nil when robust is not requested.
func tailCovers(exp *CompiledExperiment) []float64 {
	for _, spec := range exp.Reports {
		if spec.Kind == report.KindRobust {
			prior, alpha := exp.WorldPrior(), exp.CVaRAlpha()
			covers := make([]float64, len(exp.Worlds))
			for w := range exp.Worlds {
				covers[w] = report.TailCover(prior[w], alpha)
			}
			return covers
		}
	}
	return nil
}

// sortedTail returns the lowest values of sorted (ascending) that cover at
// least the fraction cover of them, merged into atoms of equal value, each
// value weighing 1/len(sorted).
func sortedTail(sorted []float64, cover float64) []report.Atom {
	n := len(sorted)
	if n == 0 || cover <= 0 {
		return nil
	}
	need := min(n, int(math.Ceil(cover*float64(n))))
	var tail []report.Atom
	w := 1 / float64(n)
	for i := 0; i < n; i++ {
		if i >= need && (i == 0 || sorted[i] != sorted[i-1]) {
			break
		}
		if k := len(tail) - 1; k >= 0 && tail[k].Value == sorted[i] {
			tail[k].Weight += w
		} else {
			tail = append(tail, report.Atom{Value: sorted[i], Weight: w})
		}
	}
	return tail
}

// exactTail returns the lowest values of the exact objective distribution
// of outs that cover at least the fraction cover of its probability, merged
// into atoms of equal value with probabilities normalized by total.
func exactTail(outs []outcome, total, cover float64) []report.Atom {
	if len(outs) == 0 || cover <= 0 {
		return nil
	}
	dist := make([]weighted, len(outs))
	for i, o := range outs {
		dist[i] = weighted{value: o.objective, weight: o.weight / total}
	}
	sort.SliceStable(dist, func(i, j int) bool { return dist[i].value < dist[j].value })
	var tail []report.Atom
	cum := 0.0
	for i, d := range dist {
		if cum >= cover && (i == 0 || d.value != dist[i-1].value) {
			break
		}
		if k := len(tail) - 1; k >= 0 && tail[k].Value == d.value {
			tail[k].Weight += d.weight
		} else {
			tail = append(tail, report.Atom{Value: d.value, Weight: d.weight})
		}
		cum += d.weight
	}
	return tail
}

// validateReports checks the experiment's reports, world_prior and
// cvar_alpha against its worlds and model, and fills exp.Reports.
func validateReports(exp *CompiledExperiment, errs *engine.ValidationErrors) {
	def := exp.Def
	reportsAt := engine.PathRoot.Field("reports")
	seen := make(map[report.Spec]bool, len(def.Reports))
	notDiscrete := map[report.Kind]bool{}
	for i, raw := range def.Reports {
		at := reportsAt.Index(i).String()
		spec, err := report.ParseSpec(raw)
		if err != nil {
			errs.Add(at, engine.CodeInvalidValue, err.Error())
			continue
		}
		if seen[spec] {
			errs.Add(at, engine.CodeDuplicateID, fmt.Sprintf("duplicate report %q", raw))
			continue
		}
		seen[spec] = true
		bad := false
		if len(exp.Worlds) < spec.MinWorlds() {
			errs.Add(at, engine.CodeInvalidValue, fmt.Sprintf("report %s needs at least %d worlds, the experiment has %d", spec.Kind, spec.MinWorlds(), len(exp.Worlds)))
			bad = true
		}
		for _, name := range spec.Metrics() {
			if !exp.Model.HasMetric(name) {
				errs.Add(at, engine.CodeUnresolvedReference, fmt.Sprintf("unknown metric %q: want objective, cost, duration or a declared metric", name))
				bad = true
			}
		}
		if !validateExplainReport(exp, &spec, at, errs, notDiscrete) {
			bad = true
		}
		if !bad {
			exp.Reports = append(exp.Reports, spec)
		}
	}

	if def.CVaRAlpha != 0 && !(def.CVaRAlpha > 0 && def.CVaRAlpha <= 1) {
		errs.Add(engine.PathRoot.Field("cvar_alpha").String(), engine.CodeInvalidValue, "cvar_alpha must be in (0, 1]")
	}

	if def.WorldPrior == nil {
		return
	}
	index := make(map[string]bool, len(exp.Worlds))
	for _, world := range exp.Worlds {
		index[string(world.ID)] = true
	}
	priorAt := engine.PathRoot.Field("world_prior")
	sum := 0.0
	for _, name := range sortedKeys(def.WorldPrior) {
		p := def.WorldPrior[name]
		at := priorAt.Field(name).String()
		switch {
		case !index[name]:
			errs.Add(at, engine.CodeUnresolvedReference, fmt.Sprintf("world %q is not an experiment world", name))
		case math.IsNaN(p) || math.IsInf(p, 0) || p < 0:
			errs.Add(at, engine.CodeInvalidValue, "prior probability must be a finite number >= 0")
		default:
			sum += p
		}
	}
	if math.Abs(sum-1) > priorTolerance {
		errs.Add(priorAt.String(), engine.CodeInvalidValue, fmt.Sprintf("prior probabilities must sum to 1, got %.12g", sum))
	}
}

// buildReports computes the requested reports from the primary estimates
// pairs[w][p] of policies.
func buildReports(exp *CompiledExperiment, spec *evalSpec, policies []sim.Policy, pairs [][]estimatePair, feasible [][]bool) (*ReportSet, error) {
	if len(exp.Reports) == 0 {
		return nil, nil
	}
	np, nw := len(policies), len(exp.Worlds)
	t := report.Table{
		Worlds:   make([]sim.WorldID, nw),
		Policies: make([]string, np),
		Mean:     make([][]float64, np),
		Feasible: make([][]bool, np),
		Metrics:  map[string][][]float64{},
		Tails:    make([][][]report.Atom, np),
	}
	for w, world := range exp.Worlds {
		t.Worlds[w] = world.ID
	}
	var axes []string
	for _, r := range exp.Reports {
		axes = append(axes, r.Metrics()...)
	}
	for _, name := range axes {
		t.Metrics[name] = make([][]float64, np)
	}
	for p, pol := range policies {
		t.Policies[p] = pol.ID()
		t.Mean[p] = make([]float64, nw)
		t.Feasible[p] = make([]bool, nw)
		t.Tails[p] = make([][]report.Atom, nw)
		for _, name := range axes {
			t.Metrics[name][p] = make([]float64, nw)
		}
		for w := range exp.Worlds {
			pair := pairs[w][p]
			t.Mean[p][w] = pair.objective.Mean
			t.Feasible[p][w] = feasible[w][p]
			t.Tails[p][w] = pair.tail
			for _, name := range axes {
				t.Metrics[name][p][w] = spec.metric(pair, name).Mean
			}
		}
	}

	set := &ReportSet{}
	for _, r := range exp.Reports {
		var err error
		switch r.Kind {
		case report.KindRegret:
			set.Regret, err = report.NewRegret(t)
		case report.KindRobust:
			set.Robust, err = report.NewRobust(t, exp.WorldPrior(), exp.CVaRAlpha())
		case report.KindPareto:
			var p *report.Pareto
			if p, err = report.NewPareto(t, r.X, r.Y); err == nil {
				set.Pareto = append(set.Pareto, *p)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("experiment: %s report: %w", r.Kind, err)
		}
	}
	return set, nil
}
