package experiment

import (
	"math"
	"sort"

	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// z95 is the two-sided 95% quantile of the standard normal distribution.
const z95 = 1.959963984540054

// accumulator summarizes a stream of values with Welford's algorithm and
// retains values for exact percentiles. Accumulators are merged with Chan's
// parallel update; merging in a fixed order gives bit-identical results.
type accumulator struct {
	N      int       `json:"n"`
	Mean   float64   `json:"mean"`
	M2     float64   `json:"m2"`
	Min    float64   `json:"min"`
	Max    float64   `json:"max"`
	Values []float64 `json:"values"`
}

// add records x, retaining it for percentiles when retain is true.
func (a *accumulator) add(x float64, retain bool) {
	a.N++
	if a.N == 1 {
		a.Min, a.Max = x, x
	} else {
		a.Min = math.Min(a.Min, x)
		a.Max = math.Max(a.Max, x)
	}
	delta := x - a.Mean
	a.Mean += delta / float64(a.N)
	a.M2 += delta * (x - a.Mean)
	if retain {
		a.Values = append(a.Values, x)
	}
}

// merge folds b into a (Chan et al. parallel variance update).
func (a *accumulator) merge(b *accumulator) {
	if b.N == 0 {
		return
	}
	a.Values = append(a.Values, b.Values...)
	if a.N == 0 {
		a.N, a.Mean, a.M2, a.Min, a.Max = b.N, b.Mean, b.M2, b.Min, b.Max
		return
	}
	na, nb := float64(a.N), float64(b.N)
	n := na + nb
	delta := b.Mean - a.Mean
	a.Mean += delta * nb / n
	a.M2 += b.M2 + delta*delta*na*nb/n
	a.N += b.N
	a.Min = math.Min(a.Min, b.Min)
	a.Max = math.Max(a.Max, b.Max)
}

// estimate converts the accumulator into a Monte Carlo sim.Estimate. StdDev
// is the sample standard deviation (n-1 denominator, 0 when n < 2); the 95%
// confidence interval of the mean is the normal approximation
// mean ± 1.96·StdDev/√n. P50 and P95 are nearest-rank percentiles of the
// retained values (sorted in place). constraintProb is stored as is.
func (a *accumulator) estimate(constraintProb map[string]float64) sim.Estimate {
	e := sim.Estimate{
		N:              a.N,
		TotalWeight:    float64(a.N),
		ConstraintProb: constraintProb,
	}
	if a.N == 0 {
		return e
	}
	e.Mean = a.Mean
	if a.N > 1 {
		e.StdDev = math.Sqrt(a.M2 / float64(a.N-1))
	}
	half := z95 * e.StdDev / math.Sqrt(float64(a.N))
	e.CI95Low, e.CI95High = a.Mean-half, a.Mean+half
	e.Min, e.Max = a.Min, a.Max

	sorted := a.sortedValues()
	e.P50 = percentile(sorted, 0.50)
	e.P95 = percentile(sorted, 0.95)
	return e
}

// sortedValues sorts the retained values in place and returns them. The
// accumulator's statistics do not depend on their order.
func (a *accumulator) sortedValues() []float64 {
	sort.Float64s(a.Values)
	return a.Values
}

// percentile returns the nearest-rank p-th percentile of sorted: the
// smallest value such that at least p·len(sorted) values are less than or
// equal to it. It returns 0 for an empty slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p * float64(len(sorted))))
	rank = max(rank, 1)
	rank = min(rank, len(sorted))
	return sorted[rank-1]
}

// policyStats aggregates the run records of one policy in one world.
type policyStats struct {
	Objective accumulator `json:"objective"`
	Duration  accumulator `json:"duration"`
	// Satisfied counts the runs that satisfied each constraint, indexed like
	// evalSpec.constraints.
	Satisfied []int `json:"satisfied"`
	// Metrics accumulates each tracked metric, indexed like
	// evalSpec.metrics.
	Metrics []accumulator `json:"metrics"`
	// Events counts the runs in which each prob() requirement condition
	// held, indexed like evalSpec.events.
	Events []int `json:"events"`
}

func newPolicyStats(spec *evalSpec) *policyStats {
	return &policyStats{
		Satisfied: make([]int, len(spec.constraints)),
		Metrics:   make([]accumulator, len(spec.metrics)),
		Events:    make([]int, len(spec.events)),
	}
}

// add records one run; retain controls value retention for percentiles.
func (s *policyStats) add(rec sim.RunRecord, spec *evalSpec, retain bool) {
	s.Objective.add(rec.Objective, retain)
	s.Duration.add(rec.Duration.Seconds(), retain)
	for i, name := range spec.constraints {
		if rec.Constraints[name] {
			s.Satisfied[i]++
		}
	}
	for i := range spec.metrics {
		s.Metrics[i].add(spec.metricValue(rec, i), retain)
	}
	for i, name := range spec.events {
		if rec.Events[name] {
			s.Events[i]++
		}
	}
}

// merge folds o into s.
func (s *policyStats) merge(o *policyStats) {
	s.Objective.merge(&o.Objective)
	s.Duration.merge(&o.Duration)
	for i, c := range o.Satisfied {
		s.Satisfied[i] += c
	}
	for i := range o.Metrics {
		s.Metrics[i].merge(&o.Metrics[i])
	}
	for i, c := range o.Events {
		s.Events[i] += c
	}
}

// estimates returns the objective estimate (with constraint probabilities),
// the duration estimate in seconds, the tracked metric estimates and the
// prob() event probabilities. When cover > 0 it also returns the lower tail
// of the retained objective values covering that fraction of them, each
// value weighted 1/(retained values), for the robust report's CVaR.
func (s *policyStats) estimates(spec *evalSpec, cover float64) estimatePair {
	n := float64(s.Objective.N)
	frac := func(c int) float64 {
		if n == 0 {
			return 0
		}
		return float64(c) / n
	}
	probs := make(map[string]float64, len(spec.constraints))
	for i, name := range spec.constraints {
		probs[name] = frac(s.Satisfied[i])
	}
	pair := estimatePair{objective: s.Objective.estimate(probs), duration: s.Duration.estimate(nil)}
	if len(spec.metrics) > 0 {
		pair.metrics = make([]sim.Estimate, len(spec.metrics))
		for i := range s.Metrics {
			pair.metrics[i] = s.Metrics[i].estimate(nil)
		}
	}
	if len(spec.events) > 0 {
		pair.events = make([]float64, len(spec.events))
		for i, c := range s.Events {
			pair.events[i] = frac(c)
		}
	}
	if cover > 0 {
		pair.tail = sortedTail(s.Objective.sortedValues(), cover)
	}
	return pair
}
