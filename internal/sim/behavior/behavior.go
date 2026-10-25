// Package behavior provides the logic to simulate a node's execution.
package behavior

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/sim/agent"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

type compiledBehavior struct {
	cb        model.CompiledBehavior
	failClass string
}

// New returns a sim.Behavior that executes the given compiled behavior model.
// Evaluates CEL params against `world`, `latent` and `inputs`, then samples in a fixed order with fixed stream names:
// `<node>/abandon`, `<node>/fail`, `<node>/duration`, `<node>/output/<field>` (fields sorted).
func New(cb model.CompiledBehavior, failClass string) sim.Behavior {
	return &compiledBehavior{cb: cb, failClass: failClass}
}

func (b *compiledBehavior) Sample(ctx context.Context, s sim.Sampler, in sim.BehaviorInput) (sim.BehaviorOutcome, error) {
	out := sim.BehaviorOutcome{
		Output: make(map[string]any),
	}

	nodeStr := string(in.NodeID)
	envVars := map[string]any{
		"world":  in.World.Params,
		"latent": in.Latents,
		"inputs": in.Inputs,
	}

	// 1. Abandon
	if b.cb.AbandonP != nil {
		p, err := evalParam(b.cb.AbandonP, envVars)
		if err != nil {
			return out, fmt.Errorf("abandon eval: %w", err)
		}

		idx, err := s.Choose(nodeStr+"/abandon", []float64{1 - p, p})
		if err != nil {
			return out, fmt.Errorf("abandon sample: %w", err)
		}
		if idx == 1 {
			out.Abandoned = true
			return out, nil // abandon true -> nothing else sampled
		}
	}

	// 2. Fail
	if b.cb.FailP != nil {
		p, err := evalParam(b.cb.FailP, envVars)
		if err != nil {
			return out, fmt.Errorf("fail eval: %w", err)
		}

		idx, err := s.Choose(nodeStr+"/fail", []float64{1 - p, p})
		if err != nil {
			return out, fmt.Errorf("fail sample: %w", err)
		}
		if idx == 1 {
			var e *engine.NodeError
			switch engine.ErrorClass(b.failClass) {
			case engine.ClassRetryable:
				e = engine.NewRetryableError(fmt.Errorf("simulated failure"))
			case engine.ClassCritical:
				e = engine.NewCriticalError(fmt.Errorf("simulated failure"))
			default:
				e = engine.NewNonRetryableError(fmt.Errorf("simulated failure"))
			}
			out.Err = e
			return out, nil
		}
	}

	if b.cb.Agent != nil {
		return b.sampleAgent(ctx, in)
	}

	// 3. Duration
	if b.cb.Duration != nil {
		dur, err := sampleValue(b.cb.Duration, nodeStr+"/duration", envVars, s)
		if err != nil {
			return out, fmt.Errorf("duration sample: %w", err)
		}

		switch v := dur.(type) {
		case float64:
			out.Duration = time.Duration(v * float64(time.Second))
		case time.Duration:
			out.Duration = v
		case string:
			d, err := time.ParseDuration(v)
			if err != nil {
				return out, fmt.Errorf("invalid duration string: %v", err)
			}
			out.Duration = d
		default:
			return out, fmt.Errorf("invalid duration type %T", dur)
		}
	}

	// 4. Output
	// Fields sorted
	keys := make([]string, 0, len(b.cb.Outputs))
	for k := range b.cb.Outputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		cv := b.cb.Outputs[k]
		val, err := sampleValue(&cv, fmt.Sprintf("%s/output/%s", nodeStr, k), envVars, s)
		if err != nil {
			return out, fmt.Errorf("output %s sample: %w", k, err)
		}
		out.Output[k] = val
	}

	// cost
	if b.cb.Cost != nil {
		c, err := evalParam(b.cb.Cost, envVars)
		if err != nil {
			return out, fmt.Errorf("cost eval: %w", err)
		}
		out.Cost = c
	}

	return out, nil
}

func sampleValue(cv *model.CompiledValue, stream string, vars map[string]any, s sim.Sampler) (any, error) {
	switch cv.Kind {
	case model.KindFixed:
		return cv.Fixed, nil
	case model.KindChoice:
		weights := make([]float64, len(cv.Choice))
		for i, c := range cv.Choice {
			w, err := evalParam(&c.Weight, vars)
			if err != nil {
				return nil, err
			}
			weights[i] = w
		}
		idx, err := s.Choose(stream, weights)
		if err != nil {
			return nil, err
		}
		return cv.Choice[idx].Value, nil
	case model.KindBernoulli:
		p, err := evalParam(getParam(cv, model.ParamP), vars)
		if err != nil {
			return nil, err
		}
		idx, err := s.Choose(stream, []float64{1 - p, p})
		if err != nil {
			return nil, err
		}
		return idx == 1, nil
	case model.KindNormal:
		mean, err := evalParam(getParam(cv, model.ParamMean), vars)
		if err != nil {
			return nil, err
		}
		sd, err := evalParam(getParam(cv, model.ParamSD), vars)
		if err != nil {
			return nil, err
		}
		return s.Float(stream, sim.Distribution{
			Kind:   sim.DistKindNormal,
			Params: map[string]float64{"mean": mean, "sd": sd},
		})
	case model.KindLognormal:
		mu, err := evalParam(getParam(cv, model.ParamMu), vars)
		if err != nil {
			return nil, err
		}
		sigma, err := evalParam(getParam(cv, model.ParamSigma), vars)
		if err != nil {
			return nil, err
		}
		return s.Float(stream, sim.Distribution{
			Kind:   sim.DistKindLogNormal,
			Params: map[string]float64{"mu": mu, "sigma": sigma},
		})
	case model.KindUniform:
		minVal, err := evalParam(getParam(cv, model.ParamMin), vars)
		if err != nil {
			return nil, err
		}
		maxVal, err := evalParam(getParam(cv, model.ParamMax), vars)
		if err != nil {
			return nil, err
		}
		return s.Float(stream, sim.Distribution{
			Kind:   sim.DistKindUniform,
			Params: map[string]float64{"min": minVal, "max": maxVal},
		})
	case model.KindTriangular:
		minVal, err := evalParam(getParam(cv, model.ParamMin), vars)
		if err != nil {
			return nil, err
		}
		mode, err := evalParam(getParam(cv, model.ParamMode), vars)
		if err != nil {
			return nil, err
		}
		maxVal, err := evalParam(getParam(cv, model.ParamMax), vars)
		if err != nil {
			return nil, err
		}
		return s.Float(stream, sim.Distribution{
			Kind:   sim.DistKindTriangular,
			Params: map[string]float64{"min": minVal, "mode": mode, "max": maxVal},
		})
	case model.KindExponential:
		rate, err := evalParam(getParam(cv, model.ParamRate), vars)
		if err != nil {
			return nil, err
		}
		return s.Float(stream, sim.Distribution{
			Kind:   sim.DistKindExponential,
			Params: map[string]float64{"rate": rate},
		})
	case model.KindEmpirical:
		return s.Float(stream, sim.Distribution{
			Kind:   sim.DistKindEmpirical,
			Values: cv.Empirical,
		})
	case model.KindExpr:
		val, _, err := cv.Expr.Eval(vars)
		if err != nil {
			return nil, err
		}
		return val.Value(), nil
	default:
		return nil, fmt.Errorf("unknown value kind %q", cv.Kind)
	}
}

func evalParam(p *model.CompiledParam, vars map[string]any) (float64, error) {
	if p.Program == nil {
		return p.Literal, nil
	}
	val, _, err := p.Program.Eval(vars)
	if err != nil {
		return 0, err
	}

	switch v := val.Value().(type) {
	case float64:
		return v, nil
	case int64:
		return float64(v), nil
	case int:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}

func getParam(cv *model.CompiledValue, name string) *model.CompiledParam {
	if p, ok := cv.Params[name]; ok {
		return &p
	}
	return nil
}

// SampleLatents is a helper to sample latents. It mutates vars["latent"].
func SampleLatents(m *model.CompiledModel, s sim.Sampler, vars map[string]any) (map[string]any, error) {
	latents := make(map[string]any)
	for _, name := range m.LatentOrder {
		cv := m.CompiledLatents[name]
		val, err := sampleValue(&cv, "latent/"+name, vars, s)
		if err != nil {
			return nil, fmt.Errorf("latent %s sample: %w", name, err)
		}
		latents[name] = val
		vars["latent"] = latents // Update vars for subsequent latents
	}
	return latents, nil
}

func (b *compiledBehavior) sampleAgent(ctx context.Context, in sim.BehaviorInput) (sim.BehaviorOutcome, error) {
	out := sim.BehaviorOutcome{
		Output: make(map[string]any),
	}
	cfg := b.cb.Agent
	client := agent.ClientFor(cfg)

	worldParams := in.World.Params
	if worldParams == nil {
		worldParams = make(map[string]any)
	}
	inputs := in.Inputs
	if inputs == nil {
		inputs = make(map[string]any)
	}

	req := &agent.BehaviorRequest{
		Node:   string(in.NodeID),
		Inputs: inputs,
		World:  worldParams,
		Model:  cfg.Model,
	}

	hash, err := agent.GenerateInputHash(req)
	if err != nil {
		return out, fmt.Errorf("generate input hash: %w", err)
	}

	cacheKey := agent.CacheKey{
		InputHash:   hash,
		Model:       cfg.Model,
		Replication: in.Replication,
	}

	var resp agent.BehaviorResponse
	if agent.GlobalCache.Get(cacheKey, &resp) {
		// Cache hit: zero external HTTP calls and zero spend
		var d time.Duration
		if resp.Duration != "" {
			d, err = time.ParseDuration(resp.Duration)
			if err != nil {
				return out, fmt.Errorf("invalid duration in cached response: %w", err)
			}
		}
		out.Output = resp.Output
		out.Cost = resp.Cost
		out.Duration = d
		return out, nil
	}

	// Cache miss: call agent HTTP endpoint
	r, err := client.CallBehavior(ctx, req)
	if err != nil {
		return out, err
	}

	// Check guardrails
	if g := client.Guardrail(); g != nil {
		if err := g.AddRequest(r.Cost); err != nil {
			return out, err
		}
	}

	// Record in cache
	agent.GlobalCache.Set(cacheKey, *r)

	var d time.Duration
	if r.Duration != "" {
		d, err = time.ParseDuration(r.Duration)
		if err != nil {
			return out, fmt.Errorf("invalid duration in agent response: %w", err)
		}
	}
	out.Output = r.Output
	out.Cost = r.Cost
	out.Duration = d
	return out, nil
}
