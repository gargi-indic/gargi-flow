package sim

import (
	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

// DistKind identifies the shape of a probability distribution.
type DistKind string

const (
	// DistKindNormal represents a normal distribution parameterized by "mean" and "sd".
	DistKindNormal DistKind = "normal"
	// DistKindLogNormal represents a log-normal distribution parameterized by "mu" and "sigma".
	DistKindLogNormal DistKind = "lognormal"
	// DistKindUniform represents a uniform distribution parameterized by "min" and "max".
	DistKindUniform DistKind = "uniform"
	// DistKindTriangular represents a triangular distribution parameterized by "min", "mode", and "max".
	DistKindTriangular DistKind = "triangular"
	// DistKindExponential represents an exponential distribution parameterized by "rate".
	DistKindExponential DistKind = "exponential"
	// DistKindEmpirical represents an empirical distribution sampled from Values.
	DistKindEmpirical DistKind = "empirical"
)

// Distribution represents a probability distribution used to sample random values in a simulation.
type Distribution struct {
	// Kind specifies the shape of the distribution.
	Kind DistKind
	// Params holds parameters specific to the continuous distribution kind.
	Params map[string]float64
	// Values holds discrete values for an empirical distribution.
	Values []float64
}

// Validate checks if the distribution is well-formed according to its Kind.
func (d Distribution) Validate() error {
	errs := &engine.ValidationErrors{}

	switch d.Kind {
	case DistKindNormal:
		if _, ok := d.Params["mean"]; !ok {
			errs.Add(engine.Path("mean").String(), engine.CodeInvalidValue, "missing mean parameter")
		}
		sd, ok := d.Params["sd"]
		if !ok {
			errs.Add(engine.Path("sd").String(), engine.CodeInvalidValue, "missing sd parameter")
		} else if sd <= 0 {
			errs.Add(engine.Path("sd").String(), engine.CodeInvalidValue, "sd must be greater than 0")
		}
	case DistKindLogNormal:
		if _, ok := d.Params["mu"]; !ok {
			errs.Add(engine.Path("mu").String(), engine.CodeInvalidValue, "missing mu parameter")
		}
		sigma, ok := d.Params["sigma"]
		if !ok {
			errs.Add(engine.Path("sigma").String(), engine.CodeInvalidValue, "missing sigma parameter")
		} else if sigma <= 0 {
			errs.Add(engine.Path("sigma").String(), engine.CodeInvalidValue, "sigma must be greater than 0")
		}
	case DistKindUniform:
		minVal, ok1 := d.Params["min"]
		if !ok1 {
			errs.Add(engine.Path("min").String(), engine.CodeInvalidValue, "missing min parameter")
		}
		maxVal, ok2 := d.Params["max"]
		if !ok2 {
			errs.Add(engine.Path("max").String(), engine.CodeInvalidValue, "missing max parameter")
		}
		if ok1 && ok2 && minVal >= maxVal {
			errs.Add(engine.Path("min").String(), engine.CodeInvalidValue, "min must be less than max")
		}
	case DistKindTriangular:
		minVal, ok1 := d.Params["min"]
		if !ok1 {
			errs.Add(engine.Path("min").String(), engine.CodeInvalidValue, "missing min parameter")
		}
		mode, ok2 := d.Params["mode"]
		if !ok2 {
			errs.Add(engine.Path("mode").String(), engine.CodeInvalidValue, "missing mode parameter")
		}
		maxVal, ok3 := d.Params["max"]
		if !ok3 {
			errs.Add(engine.Path("max").String(), engine.CodeInvalidValue, "missing max parameter")
		}
		if ok1 && ok2 && minVal > mode {
			errs.Add(engine.Path("mode").String(), engine.CodeInvalidValue, "min must be less than or equal to mode")
		}
		if ok2 && ok3 && mode > maxVal {
			errs.Add(engine.Path("max").String(), engine.CodeInvalidValue, "mode must be less than or equal to max")
		}
	case DistKindExponential:
		rate, ok := d.Params["rate"]
		if !ok {
			errs.Add(engine.Path("rate").String(), engine.CodeInvalidValue, "missing rate parameter")
		} else if rate <= 0 {
			errs.Add(engine.Path("rate").String(), engine.CodeInvalidValue, "rate must be greater than 0")
		}
	case DistKindEmpirical:
		if len(d.Values) == 0 {
			errs.Add(engine.Path("values").String(), engine.CodeInvalidValue, "empirical distribution must have at least one value")
		}
	default:
		errs.Add(engine.Path("kind").String(), engine.CodeInvalidValue, "unknown distribution kind")
	}

	if errs.Len() > 0 {
		return errs
	}
	return nil
}
