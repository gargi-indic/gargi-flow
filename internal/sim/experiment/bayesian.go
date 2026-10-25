package experiment

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// BayesianSource is a PolicySource (policies: {search: bayesian, policy: ...})
// that optimizes continuous and integer parameters using Tree-structured Parzen
// Estimators (TPE).
type BayesianSource struct {
	Policy        model.Parameterized
	Trials        int
	StartupTrials int
	Gamma         float64
	Racing        bool
	Seed          uint64
}

var _ PolicySource = BayesianSource{}

// Policies returns an error because Bayesian search evaluates policies dynamically.
func (s BayesianSource) Policies(_ *model.CompiledModel) ([]sim.Policy, error) {
	return nil, errors.New("experiment: bayesian search must be evaluated dynamically by Run")
}

// StaticSource is a PolicySource that supplies a fixed slice of policies.
type StaticSource []sim.Policy

var _ PolicySource = StaticSource{}

// Policies returns the fixed slice of policies.
func (s StaticSource) Policies(_ *model.CompiledModel) ([]sim.Policy, error) {
	return s, nil
}

type paramDomain struct {
	name string
	min  float64
	max  float64
	step *float64
	vals []any
}

type evalRecord struct {
	params map[string]any
	obj    float64
	policy sim.Policy
}

func runBayesian(ctx context.Context, exp *CompiledExperiment, src BayesianSource, opts Options) (*Report, error) {
	if src.Policy == nil {
		return nil, errors.New("experiment: bayesian search has no policy")
	}
	trials := src.Trials
	if trials <= 0 {
		trials = 20
	}
	startupTrials := src.StartupTrials
	if startupTrials <= 0 {
		startupTrials = 5
	}
	if startupTrials > trials {
		startupTrials = trials
	}
	gamma := src.Gamma
	if gamma <= 0 || gamma >= 1 {
		gamma = 0.20
	}

	rng := rand.New(rand.NewPCG(src.Seed, randomStream))
	paramSpecs := src.Policy.ParamSpecs()
	var domains []paramDomain
	for name, spec := range paramSpecs {
		d := paramDomain{name: name}
		if spec.IsRange() && spec.Min != nil && spec.Max != nil {
			d.min = *spec.Min
			d.max = *spec.Max
			d.step = spec.Step
		} else if len(spec.Values) > 0 {
			d.vals = spec.Values
		}
		domains = append(domains, d)
	}
	sort.SliceStable(domains, func(i, j int) bool { return domains[i].name < domains[j].name })

	var history []evalRecord
	seen := make(map[string]bool)

	evalCand := func(p map[string]any) (bool, error) {
		candPolicy, err := src.Policy.WithParams(p)
		if err != nil {
			return false, err
		}

		if seen[candPolicy.ID()] {
			return false, nil
		}
		seen[candPolicy.ID()] = true

		evalExp := *exp
		rep, err := Run(ctx, &evalExp, StaticSource{candPolicy}, opts)
		if err != nil {
			return false, err
		}

		var obj float64
		priors := exp.WorldPrior()
		for w, wr := range rep.Worlds {
			if len(wr.Policies) > 0 {
				pw := 1.0 / float64(len(rep.Worlds))
				if w < len(priors) {
					pw = priors[w]
				}
				obj += pw * wr.Policies[0].Objective.Mean
			}
		}
		history = append(history, evalRecord{params: p, obj: obj, policy: candPolicy})
		return true, nil
	}

	drawUniform := func() map[string]any {
		p := make(map[string]any, len(domains))
		for _, d := range domains {
			if len(d.vals) > 0 {
				p[d.name] = d.vals[rng.IntN(len(d.vals))]
			} else {
				v := d.min + rng.Float64()*(d.max-d.min)
				if d.step != nil {
					steps := math.Floor((d.max-d.min) / *d.step + gridEpsilon)
					stepIdx := math.Floor((v-d.min) / *d.step + 0.5)
					if stepIdx < 0 {
						stepIdx = 0
					}
					if stepIdx > steps {
						stepIdx = steps
					}
					v = min(roundParam(d.min+stepIdx*(*d.step)), d.max)
				} else {
					v = min(roundParam(v), d.max)
				}
				p[d.name] = v
			}
		}
		return p
	}

	// 1. Startup phase: uniform random draws
	for i := 0; i < startupTrials; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p := drawUniform()
		ok, err := evalCand(p)
		if err != nil {
			return nil, err
		}
		if ok {
			i++
		}
	}

	isMaximize := true
	if exp.Model != nil {
		if strings.EqualFold(exp.Model.Def.Objective.Maximize, "min") || strings.EqualFold(exp.Model.Def.Objective.Maximize, "minimize") {
			isMaximize = false
		}
	}

	// 2. Iterative TPE phase
	for t := len(history); t < trials; t++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		sortedIdx := make([]int, len(history))
		for i := range sortedIdx {
			sortedIdx[i] = i
		}
		sort.SliceStable(sortedIdx, func(i, j int) bool {
			if isMaximize {
				return history[sortedIdx[i]].obj > history[sortedIdx[j]].obj
			}
			return history[sortedIdx[i]].obj < history[sortedIdx[j]].obj
		})

		lSize := int(math.Ceil(gamma * float64(len(history))))
		if lSize < 1 {
			lSize = 1
		}
		if lSize > len(history)-1 {
			lSize = len(history) - 1
		}
		if lSize < 1 {
			lSize = 1
		}

		lKde := make([]kde1d, len(domains))
		gKde := make([]kde1d, len(domains))
		lCat := make([]catDist, len(domains))
		gCat := make([]catDist, len(domains))

		for dIdx, d := range domains {
			if len(d.vals) > 0 {
				var lVals, gVals []any
				for i := 0; i < lSize; i++ {
					lVals = append(lVals, history[sortedIdx[i]].params[d.name])
				}
				for i := lSize; i < len(history); i++ {
					gVals = append(gVals, history[sortedIdx[i]].params[d.name])
				}
				lCat[dIdx] = buildCatDist(d.vals, lVals)
				gCat[dIdx] = buildCatDist(d.vals, gVals)
			} else {
				var lPts, gPts []float64
				for i := 0; i < lSize; i++ {
					lPts = append(lPts, toFloat64(history[sortedIdx[i]].params[d.name]))
				}
				for i := lSize; i < len(history); i++ {
					gPts = append(gPts, toFloat64(history[sortedIdx[i]].params[d.name]))
				}
				lKde[dIdx] = buildKDE(d.min, d.max, lPts)
				gKde[dIdx] = buildKDE(d.min, d.max, gPts)
			}
		}

		sampleCandidate := func() (map[string]any, float64) {
			c := make(map[string]any, len(domains))
			var logRatio float64
			for dIdx, d := range domains {
				if len(d.vals) > 0 {
					val := lCat[dIdx].sample(rng)
					c[d.name] = val
					lp := lCat[dIdx].pmf(val)
					gp := gCat[dIdx].pmf(val)
					logRatio += math.Log(math.Max(lp, 1e-12)) - math.Log(math.Max(gp, 1e-12))
				} else {
					v := lKde[dIdx].sample(rng)
					if d.step != nil {
						steps := math.Floor((d.max-d.min) / *d.step + gridEpsilon)
						stepIdx := math.Floor((v-d.min) / *d.step + 0.5)
						if stepIdx < 0 {
							stepIdx = 0
						}
						if stepIdx > steps {
							stepIdx = steps
						}
						v = min(roundParam(d.min+stepIdx*(*d.step)), d.max)
					} else {
						v = min(roundParam(v), d.max)
					}
					c[d.name] = v
					lp := lKde[dIdx].pdf(v)
					gp := gKde[dIdx].pdf(v)
					logRatio += math.Log(math.Max(lp, 1e-12)) - math.Log(math.Max(gp, 1e-12))
				}
			}
			return c, logRatio
		}

		bestScore := -math.MaxFloat64
		var bestCand map[string]any

		for m := 0; m < 24; m++ {
			c, score := sampleCandidate()
			if score > bestScore {
				bestScore = score
				bestCand = c
			}
		}

		ok, err := evalCand(bestCand)
		if err != nil {
			return nil, err
		}
		if !ok {
			evaluated := false
			// Try re-sampling from l(x)
			for retry := 0; retry < 20; retry++ {
				c, _ := sampleCandidate()
				ok, err = evalCand(c)
				if err != nil {
					return nil, err
				}
				if ok {
					evaluated = true
					break
				}
			}
			if !evaluated {
				// Re-draw uniformly
				for retry := 0; retry < 100; retry++ {
					p := drawUniform()
					ok, err = evalCand(p)
					if err != nil {
						return nil, err
					}
					if ok {
						evaluated = true
						break
					}
				}
			}
			if !evaluated {
				// Search space is fully exhausted
				break
			}
		}
	}

	if len(history) == 0 {
		return nil, errors.New("experiment: bayesian search evaluated 0 policies")
	}

	var allPolicies []sim.Policy
	for _, r := range history {
		allPolicies = append(allPolicies, r.policy)
	}

	if src.Racing {
		racingSrc := RacingSource{
			Candidates: StaticSource(allPolicies),
		}
		return runRacing(ctx, exp, racingSrc, opts)
	}

	return Run(ctx, exp, StaticSource(allPolicies), opts)
}

func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

type kde1d struct {
	lower float64
	upper float64
	pts   []float64
	bw    float64
}

func buildKDE(lower, upper float64, pts []float64) kde1d {
	n := len(pts)
	if n == 0 {
		return kde1d{lower: lower, upper: upper, bw: 1.0}
	}

	sorted := make([]float64, n)
	copy(sorted, pts)
	sort.Float64s(sorted)

	mean := 0.0
	for _, v := range sorted {
		mean += v
	}
	mean /= float64(n)

	variance := 0.0
	if n > 1 {
		for _, v := range sorted {
			diff := v - mean
			variance += diff * diff
		}
		variance /= float64(n - 1)
	}
	std := math.Sqrt(variance)

	q1 := sorted[int(math.Floor(0.25*float64(n)))]
	q3 := sorted[int(math.Floor(0.75*float64(n)))]
	iqr := q3 - q1

	s := std
	if iqr > 0 && iqr/1.34 < s {
		s = iqr / 1.34
	}
	if s <= 0 {
		s = (upper - lower) / 2.0
	}
	if s <= 0 {
		s = 1.0
	}

	bw := 1.06 * s * math.Pow(float64(n), -0.2)
	minBw := (upper - lower) / 10.0
	if minBw <= 0 {
		minBw = 1e-6
	}
	if bw < minBw {
		bw = minBw
	}

	return kde1d{lower: lower, upper: upper, pts: pts, bw: bw}
}

func (k kde1d) pdf(x float64) float64 {
	w := k.upper - k.lower
	if w <= 0 {
		w = 1.0
	}
	unif := 1.0 / w
	if len(k.pts) == 0 {
		return unif
	}

	sum := 0.0
	for _, pt := range k.pts {
		z := (x - pt) / k.bw
		pdfZ := math.Exp(-0.5*z*z) / (math.Sqrt(2*math.Pi) * k.bw)

		zMin := (k.lower - pt) / k.bw
		zMax := (k.upper - pt) / k.bw
		cdfZMin := 0.5 * (1.0 + math.Erf(zMin/math.Sqrt2))
		cdfZMax := 0.5 * (1.0 + math.Erf(zMax/math.Sqrt2))
		norm := cdfZMax - cdfZMin
		if norm < 1e-12 {
			norm = 1e-12
		}

		sum += pdfZ / norm
	}
	mixture := sum / float64(len(k.pts))
	return 0.1*unif + 0.9*mixture
}

func (k kde1d) sample(rng *rand.Rand) float64 {
	if len(k.pts) == 0 || rng.Float64() < 0.1 {
		return k.lower + rng.Float64()*(k.upper-k.lower)
	}

	idx := rng.IntN(len(k.pts))
	pt := k.pts[idx]

	for i := 0; i < 50; i++ {
		v := pt + rng.NormFloat64()*k.bw
		if v >= k.lower && v <= k.upper {
			return v
		}
	}
	return k.lower + rng.Float64()*(k.upper-k.lower)
}

type catDist struct {
	values []any
	probs  []float64
}

func buildCatDist(choices []any, samples []any) catDist {
	k := len(choices)
	if k == 0 {
		return catDist{}
	}
	counts := make(map[any]int, k)
	for _, s := range samples {
		counts[s]++
	}
	total := float64(len(samples))
	probs := make([]float64, k)
	unif := 1.0 / float64(k)
	for i, c := range choices {
		var freq float64
		if total > 0 {
			freq = float64(counts[c]) / total
		}
		probs[i] = 0.1*unif + 0.9*freq
	}
	return catDist{values: choices, probs: probs}
}

func (c catDist) pmf(val any) float64 {
	for i, v := range c.values {
		if fmt.Sprint(v) == fmt.Sprint(val) {
			return c.probs[i]
		}
	}
	if len(c.values) == 0 {
		return 1.0
	}
	return 1.0 / float64(len(c.values))
}

func (c catDist) sample(rng *rand.Rand) any {
	if len(c.values) == 0 {
		return nil
	}
	r := rng.Float64()
	cum := 0.0
	for i, p := range c.probs {
		cum += p
		if r <= cum || i == len(c.probs)-1 {
			return c.values[i]
		}
	}
	return c.values[0]
}
