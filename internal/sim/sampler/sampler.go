package sampler

import (
	"errors"
	"hash/fnv"
	"math"
	"math/rand/v2"

	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

type monteCarloSampler struct {
	seed uint64
	rngs map[string]*rand.Rand
}

// NewMonteCarlo returns a new Monte Carlo sampler seeded with the given seed.
// Each stream name gets its own PCG generator seeded from `(seed, fnv64(stream))`,
// so adding a node never perturbs other nodes' draws.
func NewMonteCarlo(seed uint64) sim.Sampler {
	return &monteCarloSampler{seed: seed, rngs: make(map[string]*rand.Rand)}
}

func (s *monteCarloSampler) generator(stream string) *rand.Rand {
	if r, ok := s.rngs[stream]; ok {
		return r
	}
	h := fnv.New64a()
	h.Write([]byte(stream))
	streamHash := h.Sum64()

	r := rand.New(rand.NewPCG(s.seed, streamHash))
	s.rngs[stream] = r
	return r
}

// Choose selects an index from a set of discrete probabilities.
// The weights must be non-negative and sum to more than 0.
func (s *monteCarloSampler) Choose(stream string, weights []float64) (int, error) {
	if len(weights) == 0 {
		return 0, errors.New("empty weights")
	}

	sum := 0.0
	for _, w := range weights {
		if w < 0 {
			return 0, errors.New("negative weight")
		}
		sum += w
	}
	if sum <= 0 {
		return 0, errors.New("zero sum of weights")
	}

	rng := s.generator(stream)
	val := rng.Float64() * sum

	cum := 0.0
	for i, w := range weights {
		if w == 0 {
			continue
		}
		cum += w
		if val < cum {
			return i, nil
		}
	}

	// Should not reach here due to float precision, but just in case, return last non-zero
	for i := len(weights) - 1; i >= 0; i-- {
		if weights[i] > 0 {
			return i, nil
		}
	}
	return 0, errors.New("failed to choose")
}

// Float generates a continuous value sampled from the given Distribution.
func (s *monteCarloSampler) Float(stream string, d sim.Distribution) (float64, error) {
	if err := d.Validate(); err != nil {
		return 0, err
	}

	rng := s.generator(stream)

	switch d.Kind {
	case sim.DistKindNormal:
		return d.Params["mean"] + rng.NormFloat64()*d.Params["sd"], nil
	case sim.DistKindLogNormal:
		return math.Exp(d.Params["mu"] + rng.NormFloat64()*d.Params["sigma"]), nil
	case sim.DistKindUniform:
		minVal, maxVal := d.Params["min"], d.Params["max"]
		return minVal + rng.Float64()*(maxVal-minVal), nil
	case sim.DistKindTriangular:
		minVal, mode, maxVal := d.Params["min"], d.Params["mode"], d.Params["max"]
		u := rng.Float64()
		if maxVal == minVal {
			return minVal, nil
		}
		fc := (mode - minVal) / (maxVal - minVal)
		if u < fc {
			return minVal + math.Sqrt(u*(maxVal-minVal)*(mode-minVal)), nil
		}
		return maxVal - math.Sqrt((1-u)*(maxVal-minVal)*(maxVal-mode)), nil
	case sim.DistKindExponential:
		return rng.ExpFloat64() / d.Params["rate"], nil
	case sim.DistKindEmpirical:
		idx := rng.IntN(len(d.Values))
		return d.Values[idx], nil
	default:
		return 0, errors.New("unknown distribution")
	}
}
