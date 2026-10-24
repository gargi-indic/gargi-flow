package model

import (
	"fmt"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

func bindSim(tree any) (*Sim, error) {
	errs := &engine.ValidationErrors{}
	m, ok := tree.(map[string]any)
	if !ok {
		errs.Add("$", engine.CodeInvalidType, "expected object")
		return nil, errs
	}

	var sim Sim
	sim.APIVersion, _ = m["apiVersion"].(string)
	sim.ID, _ = m["id"].(string)
	sim.Graph, _ = m["graph"].(string)

	bindWorlds(m["worlds"], "$.worlds", &sim, errs)
	bindLatents(m["latents"], "$.latents", &sim, errs)
	bindBehaviors(m["behaviors"], "$.behaviors", &sim, errs)
	bindResources(m["resources"], "$.resources", &sim, errs)
	bindObjective(m["objective"], "$.objective", &sim, errs)
	if metrics, ok := m["metrics"]; ok {
		sim.Metrics = bindStringMap(metrics, "$.metrics", errs)
	}
	if h, ok := m["horizon"].(string); ok {
		sim.Horizon = h
	}
	bindArrivals(m["arrivals"], "$.arrivals", &sim, errs)

	checkUnknownFields(m, []string{"apiVersion", "id", "graph", "worlds", "latents", "behaviors", "resources", "objective", "metrics", "arrivals", "horizon"}, "$", errs)

	if errs.Len() > 0 {
		return &sim, errs
	}
	return &sim, nil
}

func bindWorlds(val any, path string, sim *Sim, errs *engine.ValidationErrors) {
	if val == nil {
		return
	}
	m, ok := val.(map[string]any)
	if !ok {
		errs.Add(path, engine.CodeInvalidType, "expected object")
		return
	}
	sim.Worlds = make(map[string]map[string]any)
	for k, v := range m {
		p := path + "." + k
		inner, ok := v.(map[string]any)
		if !ok {
			errs.Add(p, engine.CodeInvalidType, "expected object")
			continue
		}
		sim.Worlds[k] = inner
		for k2, v2 := range inner {
			if !isNumberStringBool(v2) {
				errs.Add(p+"."+k2, engine.CodeInvalidType, "expected number, string or boolean")
			}
		}
	}
}

func bindLatents(val any, path string, sim *Sim, errs *engine.ValidationErrors) {
	if val == nil {
		return
	}
	m, ok := val.(map[string]any)
	if !ok {
		errs.Add(path, engine.CodeInvalidType, "expected object")
		return
	}
	sim.Latents = make(map[string]ValueSpec)
	for k, v := range m {
		sim.Latents[k] = bindValueSpec(v, path+"."+k, errs)
	}
}

func bindBehaviors(val any, path string, sim *Sim, errs *engine.ValidationErrors) {
	if val == nil {
		return
	}
	m, ok := val.(map[string]any)
	if !ok {
		errs.Add(path, engine.CodeInvalidType, "expected object")
		return
	}
	sim.Behaviors = make(map[string]Behavior)
	for k, v := range m {
		p := path + "." + k
		bm, ok := v.(map[string]any)
		if !ok {
			errs.Add(p, engine.CodeInvalidType, "expected object")
			continue
		}
		var b Behavior
		if cost, ok := bm["cost"]; ok {
			if isNumberOrCEL(cost) {
				b.Cost = cost
			} else {
				errs.Add(p+".cost", engine.CodeInvalidType, "expected number or string")
			}
		}
		if dur, ok := bm["duration"]; ok {
			if isString(dur) {
				b.Duration = dur
			} else {
				b.Duration = bindValueSpec(dur, p+".duration", errs)
			}
		}
		if abandon, ok := bm["abandon"]; ok {
			if am, ok := abandon.(map[string]any); ok {
				var a Abandon
				if pval, ok := am["p"]; ok {
					if isNumberOrCEL(pval) {
						a.P = pval
					} else {
						errs.Add(p+".abandon.p", engine.CodeInvalidType, "expected number or string")
					}
				}
				checkUnknownFields(am, []string{"p"}, p+".abandon", errs)
				b.Abandon = &a
			} else {
				errs.Add(p+".abandon", engine.CodeInvalidType, "expected object")
			}
		}
		if fail, ok := bm["fail"]; ok {
			if fm, ok := fail.(map[string]any); ok {
				var f Fail
				if pval, ok := fm["p"]; ok {
					if isNumberOrCEL(pval) {
						f.P = pval
					} else {
						errs.Add(p+".fail.p", engine.CodeInvalidType, "expected number or string")
					}
				}
				if class, ok := fm["class"]; ok {
					if s, ok := class.(string); ok {
						f.Class = s
					} else {
						errs.Add(p+".fail.class", engine.CodeInvalidType, "expected string")
					}
				}
				checkUnknownFields(fm, []string{"p", "class"}, p+".fail", errs)
				b.Fail = &f
			} else {
				errs.Add(p+".fail", engine.CodeInvalidType, "expected object")
			}
		}
		if out, ok := bm["output"]; ok {
			if om, ok := out.(map[string]any); ok {
				b.Output = make(map[string]ValueSpec)
				for ok2, ov2 := range om {
					b.Output[ok2] = bindValueSpec(ov2, p+".output."+ok2, errs)
				}
			} else {
				errs.Add(p+".output", engine.CodeInvalidType, "expected object")
			}
		}
		if res, ok := bm["resource"]; ok {
			if s, ok := res.(string); ok {
				b.Resource = s
			} else {
				errs.Add(p+".resource", engine.CodeInvalidType, "expected string")
			}
		}
		if pat, ok := bm["patience"]; ok {
			if isNumberOrCEL(pat) {
				b.Patience = pat
			} else {
				errs.Add(p+".patience", engine.CodeInvalidType, "expected number or string")
			}
		}
		checkUnknownFields(bm, []string{"cost", "duration", "abandon", "fail", "output", "resource", "patience"}, p, errs)
		sim.Behaviors[k] = b
	}
}

func bindArrivals(val any, path string, sim *Sim, errs *engine.ValidationErrors) {
	if val == nil {
		return
	}
	m, ok := val.(map[string]any)
	if !ok {
		errs.Add(path, engine.CodeInvalidType, "expected object")
		return
	}
	var arr ArrivalsSpec
	if p, ok := m["poisson"]; ok {
		if pm, ok := p.(map[string]any); ok {
			var poisson PoissonArrivals
			if r, ok := pm["rate"]; ok {
				if isNumberOrCEL(r) {
					poisson.Rate = r
				} else {
					errs.Add(path+".poisson.rate", engine.CodeInvalidType, "expected number or string")
				}
			}
			checkUnknownFields(pm, []string{"rate"}, path+".poisson", errs)
			arr.Poisson = &poisson
		} else {
			errs.Add(path+".poisson", engine.CodeInvalidType, "expected object")
		}
	}
	if s, ok := m["schedule"]; ok {
		if sl, ok := s.([]any); ok {
			for i, elem := range sl {
				if em, ok := elem.(map[string]any); ok {
					var w ScheduleWindow
					if dur, ok := em["duration"].(string); ok {
						w.Duration = dur
					}
					if r, ok := em["rate"]; ok {
						if isNumberOrCEL(r) {
							w.Rate = r
						}
					}
					checkUnknownFields(em, []string{"duration", "rate"}, fmt.Sprintf("%s.schedule[%d]", path, i), errs)
					arr.Schedule = append(arr.Schedule, w)
				}
			}
		}
	}
	if emp, ok := m["empirical"]; ok {
		if el, ok := emp.([]any); ok {
			for _, elem := range el {
				if num, ok := getFloat(elem); ok {
					arr.Empirical = append(arr.Empirical, num)
				}
			}
		}
	}
	checkUnknownFields(m, []string{"poisson", "schedule", "empirical"}, path, errs)
	sim.Arrivals = &arr
}

func bindResources(val any, path string, sim *Sim, errs *engine.ValidationErrors) {
	if val == nil {
		return
	}
	m, ok := val.(map[string]any)
	if !ok {
		errs.Add(path, engine.CodeInvalidType, "expected object")
		return
	}
	sim.Resources = make(map[string]Resource)
	for k, v := range m {
		p := path + "." + k
		rm, ok := v.(map[string]any)
		if !ok {
			errs.Add(p, engine.CodeInvalidType, "expected object")
			continue
		}
		var r Resource
		if capVal, ok := rm["capacity"]; ok {
			if c, ok := getInt(capVal); ok {
				r.Capacity = c
			} else {
				errs.Add(p+".capacity", engine.CodeInvalidType, "expected integer")
			}
		}
		checkUnknownFields(rm, []string{"capacity"}, p, errs)
		sim.Resources[k] = r
	}
}

func bindObjective(val any, path string, sim *Sim, errs *engine.ValidationErrors) {
	if val == nil {
		return
	}
	m, ok := val.(map[string]any)
	if !ok {
		errs.Add(path, engine.CodeInvalidType, "expected object")
		return
	}
	var o Objective
	if n, ok := m["name"].(string); ok {
		o.Name = n
	}
	if v, ok := m["value"].(string); ok {
		o.Value = v
	}
	if maxVal, ok := m["maximize"].(string); ok {
		o.Maximize = maxVal
	}
	if cons, ok := m["constraints"]; ok {
		o.Constraints = bindStringMap(cons, path+".constraints", errs)
	}
	if reqs, ok := m["requirements"]; ok {
		o.Requirements = bindStringMap(reqs, path+".requirements", errs)
	}
	checkUnknownFields(m, []string{"name", "value", "maximize", "constraints", "requirements"}, path, errs)
	sim.Objective = o
}

// bindStringMap binds an object whose values are strings.
func bindStringMap(val any, path string, errs *engine.ValidationErrors) map[string]string {
	m, ok := val.(map[string]any)
	if !ok {
		errs.Add(path, engine.CodeInvalidType, "expected object")
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		} else {
			errs.Add(path+"."+k, engine.CodeInvalidType, "expected string")
		}
	}
	return out
}

func bindValueSpec(val any, path string, errs *engine.ValidationErrors) ValueSpec {
	var vs ValueSpec
	m, ok := val.(map[string]any)
	if !ok {
		errs.Add(path, engine.CodeInvalidType, "expected object")
		return vs
	}

	if fixed, ok := m["fixed"]; ok {
		if isNumberOrCEL(fixed) {
			vs.Fixed = fixed
		} else {
			errs.Add(path+".fixed", engine.CodeInvalidType, "expected number or string")
		}
	}
	if choice, ok := m["choice"]; ok {
		if cm, ok := choice.(map[string]any); ok {
			vs.Choice = make(map[string]any)
			for ck, cv := range cm {
				if isNumberOrCEL(cv) {
					vs.Choice[ck] = cv
				} else {
					errs.Add(path+".choice."+ck, engine.CodeInvalidType, "expected number or string")
				}
			}
		} else {
			errs.Add(path+".choice", engine.CodeInvalidType, "expected object")
		}
	}
	if bernoulli, ok := m["bernoulli"]; ok {
		if bm, ok := bernoulli.(map[string]any); ok {
			var b Bernoulli
			if pval, ok := bm["p"]; ok {
				if isNumberOrCEL(pval) {
					b.P = pval
				} else {
					errs.Add(path+".bernoulli.p", engine.CodeInvalidType, "expected number or string")
				}
			}
			checkUnknownFields(bm, []string{"p"}, path+".bernoulli", errs)
			vs.Bernoulli = &b
		} else {
			errs.Add(path+".bernoulli", engine.CodeInvalidType, "expected object")
		}
	}
	if normal, ok := m["normal"]; ok {
		if nm, ok := normal.(map[string]any); ok {
			var n Normal
			if mean, ok := nm["mean"]; ok {
				if isNumberOrCEL(mean) {
					n.Mean = mean
				} else {
					errs.Add(path+".normal.mean", engine.CodeInvalidType, "expected number or string")
				}
			}
			if sd, ok := nm["sd"]; ok {
				if isNumberOrCEL(sd) {
					n.SD = sd
				} else {
					errs.Add(path+".normal.sd", engine.CodeInvalidType, "expected number or string")
				}
			}
			checkUnknownFields(nm, []string{"mean", "sd"}, path+".normal", errs)
			vs.Normal = &n
		} else {
			errs.Add(path+".normal", engine.CodeInvalidType, "expected object")
		}
	}
	if lognormal, ok := m["lognormal"]; ok {
		if lm, ok := lognormal.(map[string]any); ok {
			var l Lognormal
			if mu, ok := lm["mu"]; ok {
				if isNumberOrCEL(mu) {
					l.Mu = mu
				} else {
					errs.Add(path+".lognormal.mu", engine.CodeInvalidType, "expected number or string")
				}
			}
			if sigma, ok := lm["sigma"]; ok {
				if isNumberOrCEL(sigma) {
					l.Sigma = sigma
				} else {
					errs.Add(path+".lognormal.sigma", engine.CodeInvalidType, "expected number or string")
				}
			}
			checkUnknownFields(lm, []string{"mu", "sigma"}, path+".lognormal", errs)
			vs.Lognormal = &l
		} else {
			errs.Add(path+".lognormal", engine.CodeInvalidType, "expected object")
		}
	}
	if uniform, ok := m["uniform"]; ok {
		if um, ok := uniform.(map[string]any); ok {
			var u Uniform
			if minVal, ok := um["min"]; ok {
				if isNumberOrCEL(minVal) {
					u.Min = minVal
				} else {
					errs.Add(path+".uniform.min", engine.CodeInvalidType, "expected number or string")
				}
			}
			if maxVal, ok := um["max"]; ok {
				if isNumberOrCEL(maxVal) {
					u.Max = maxVal
				} else {
					errs.Add(path+".uniform.max", engine.CodeInvalidType, "expected number or string")
				}
			}
			checkUnknownFields(um, []string{"min", "max"}, path+".uniform", errs)
			vs.Uniform = &u
		} else {
			errs.Add(path+".uniform", engine.CodeInvalidType, "expected object")
		}
	}
	if triangular, ok := m["triangular"]; ok {
		if tm, ok := triangular.(map[string]any); ok {
			var t Triangular
			if minVal, ok := tm["min"]; ok {
				if isNumberOrCEL(minVal) {
					t.Min = minVal
				} else {
					errs.Add(path+".triangular.min", engine.CodeInvalidType, "expected number or string")
				}
			}
			if mode, ok := tm["mode"]; ok {
				if isNumberOrCEL(mode) {
					t.Mode = mode
				} else {
					errs.Add(path+".triangular.mode", engine.CodeInvalidType, "expected number or string")
				}
			}
			if maxVal, ok := tm["max"]; ok {
				if isNumberOrCEL(maxVal) {
					t.Max = maxVal
				} else {
					errs.Add(path+".triangular.max", engine.CodeInvalidType, "expected number or string")
				}
			}
			checkUnknownFields(tm, []string{"min", "mode", "max"}, path+".triangular", errs)
			vs.Triangular = &t
		} else {
			errs.Add(path+".triangular", engine.CodeInvalidType, "expected object")
		}
	}
	if empirical, ok := m["empirical"]; ok {
		if arr, ok := empirical.([]any); ok {
			var em []float64
			for i, v := range arr {
				if f, ok := getFloat(v); ok {
					em = append(em, f)
				} else {
					errs.Add(fmt.Sprintf("%s.empirical[%d]", path, i), engine.CodeInvalidType, "expected number")
				}
			}
			vs.Empirical = em
		} else {
			errs.Add(path+".empirical", engine.CodeInvalidType, "expected array")
		}
	}
	if exponential, ok := m["exponential"]; ok {
		if em, ok := exponential.(map[string]any); ok {
			var e Exponential
			if r, ok := em["rate"]; ok {
				if isNumberOrCEL(r) {
					e.Rate = r
				} else {
					errs.Add(path+".exponential.rate", engine.CodeInvalidType, "expected number or string")
				}
			}
			checkUnknownFields(em, []string{"rate"}, path+".exponential", errs)
			vs.Exponential = &e
		} else {
			errs.Add(path+".exponential", engine.CodeInvalidType, "expected object")
		}
	}
	if expr, ok := m["expr"]; ok {
		if s, ok := expr.(string); ok {
			vs.Expr = s
		} else {
			errs.Add(path+".expr", engine.CodeInvalidType, "expected string")
		}
	}

	checkUnknownFields(m, []string{"fixed", "choice", "bernoulli", "normal", "lognormal", "uniform", "triangular", "exponential", "empirical", "expr"}, path, errs)

	return vs
}

func bindPolicy(tree any) (*Policy, error) {
	errs := &engine.ValidationErrors{}
	m, ok := tree.(map[string]any)
	if !ok {
		errs.Add("$", engine.CodeInvalidType, "expected object")
		return nil, errs
	}

	var pol Policy
	pol.APIVersion, _ = m["apiVersion"].(string)
	pol.ID, _ = m["id"].(string)
	pol.Version, _ = m["version"].(string)

	if decs, ok := m["decisions"]; ok {
		if dm, ok := decs.(map[string]any); ok {
			pol.Decisions = make(map[string]Decision)
			for k, v := range dm {
				pol.Decisions[k] = bindDecision(v, "$.decisions."+k, errs)
			}
		} else {
			errs.Add("$.decisions", engine.CodeInvalidType, "expected object")
		}
	}

	if params, ok := m["params"]; ok {
		if pm, ok := params.(map[string]any); ok {
			pol.Params = make(map[string]ParamSpec, len(pm))
			for k, v := range pm {
				pol.Params[k] = bindParamSpec(v, "$.params."+k, errs)
			}
		} else {
			errs.Add("$.params", engine.CodeInvalidType, "expected object")
		}
	}

	checkUnknownFields(m, []string{"apiVersion", "id", "version", "params", "decisions"}, "$", errs)

	if errs.Len() > 0 {
		return &pol, errs
	}
	return &pol, nil
}

// bindParamSpec binds one entry of a policy's params. Type problems are
// reported here; the range/values rules are checked by validateParams.
func bindParamSpec(val any, path string, errs *engine.ValidationErrors) ParamSpec {
	var ps ParamSpec
	m, ok := val.(map[string]any)
	if !ok {
		errs.Add(path, engine.CodeInvalidType, "expected object")
		return ps
	}
	number := func(field string) *float64 {
		v, ok := m[field]
		if !ok {
			return nil
		}
		f, ok := getFloat(v)
		if !ok {
			errs.Add(path+"."+field, engine.CodeInvalidType, "expected number")
			return nil
		}
		return &f
	}
	ps.Min = number("min")
	ps.Max = number("max")
	ps.Step = number("step")
	if values, ok := m["values"]; ok {
		if arr, ok := values.([]any); ok {
			ps.Values = make([]any, 0, len(arr))
			for i, v := range arr {
				if isNumberOrCEL(v) {
					ps.Values = append(ps.Values, v)
				} else {
					errs.Add(fmt.Sprintf("%s.values[%d]", path, i), engine.CodeInvalidType, "expected number or string")
				}
			}
		} else {
			errs.Add(path+".values", engine.CodeInvalidType, "expected array")
		}
	}
	if def, ok := m["default"]; ok {
		if isNumberOrCEL(def) {
			ps.Default = def
		} else {
			errs.Add(path+".default", engine.CodeInvalidType, "expected number or string")
		}
	}
	checkUnknownFields(m, []string{"min", "max", "step", "values", "default"}, path, errs)
	return ps
}

func bindDecision(val any, path string, errs *engine.ValidationErrors) Decision {
	var d Decision
	m, ok := val.(map[string]any)
	if !ok {
		errs.Add(path, engine.CodeInvalidType, "expected object")
		return d
	}
	if rules, ok := m["rules"]; ok {
		if arr, ok := rules.([]any); ok {
			for i, r := range arr {
				p := fmt.Sprintf("%s.rules[%d]", path, i)
				if rm, ok := r.(map[string]any); ok {
					var rule Rule
					if w, ok := rm["when"].(string); ok {
						rule.When = w
					}
					if c, ok := rm["choose"].(string); ok {
						rule.Choose = c
					}
					if e, ok := rm["else"].(string); ok {
						rule.Else = e
					}
					checkUnknownFields(rm, []string{"when", "choose", "else"}, p, errs)
					d.Rules = append(d.Rules, rule)
				} else {
					errs.Add(p, engine.CodeInvalidType, "expected object")
				}
			}
		} else {
			errs.Add(path+".rules", engine.CodeInvalidType, "expected array")
		}
	}
	if table, ok := m["table"]; ok {
		if arr, ok := table.([]any); ok {
			for i, r := range arr {
				p := fmt.Sprintf("%s.table[%d]", path, i)
				if rm, ok := r.(map[string]any); ok {
					var row TableRow
					if obs, ok := rm["observed"]; ok {
						if om, ok := obs.(map[string]any); ok {
							row.Observed = make(map[string]any)
							for ok2, ov2 := range om {
								if isNumberStringBool(ov2) {
									row.Observed[ok2] = ov2
								} else {
									errs.Add(p+".observed."+ok2, engine.CodeInvalidType, "expected number, string or boolean")
								}
							}
						} else {
							errs.Add(p+".observed", engine.CodeInvalidType, "expected object")
						}
					}
					if c, ok := rm["choose"].(string); ok {
						row.Choose = c
					}
					checkUnknownFields(rm, []string{"observed", "choose"}, p, errs)
					d.Table = append(d.Table, row)
				} else {
					errs.Add(p, engine.CodeInvalidType, "expected object")
				}
			}
		} else {
			errs.Add(path+".table", engine.CodeInvalidType, "expected array")
		}
	}
	if def, ok := m["default"]; ok {
		if s, ok := def.(string); ok {
			d.Default = s
		} else {
			errs.Add(path+".default", engine.CodeInvalidType, "expected string")
		}
	}
	checkUnknownFields(m, []string{"rules", "table", "default"}, path, errs)
	return d
}

func bindExperiment(tree any) (*Experiment, error) {
	errs := &engine.ValidationErrors{}
	m, ok := tree.(map[string]any)
	if !ok {
		errs.Add("$", engine.CodeInvalidType, "expected object")
		return nil, errs
	}

	var exp Experiment
	exp.APIVersion, _ = m["apiVersion"].(string)
	exp.Model, _ = m["model"].(string)

	if worlds, ok := m["worlds"]; ok {
		if arr, ok := worlds.([]any); ok {
			for i, v := range arr {
				if s, ok := v.(string); ok {
					exp.Worlds = append(exp.Worlds, s)
				} else {
					errs.Add(fmt.Sprintf("$.worlds[%d]", i), engine.CodeInvalidType, "expected string")
				}
			}
		} else {
			errs.Add("$.worlds", engine.CodeInvalidType, "expected array")
		}
	}

	if policies, ok := m["policies"]; ok {
		if pm, ok := policies.(map[string]any); ok {
			bindPolicies(pm, &exp.Policies, "$.policies", errs)
		} else {
			errs.Add("$.policies", engine.CodeInvalidType, "expected object")
		}
	}

	if e, ok := m["evaluator"].(string); ok {
		exp.Evaluator = e
	}

	if r, ok := m["runs"]; ok {
		if c, ok := getInt(r); ok {
			exp.Runs = c
		} else {
			errs.Add("$.runs", engine.CodeInvalidType, "expected integer")
		}
	}
	if s, ok := m["seed"]; ok {
		if c, ok := getInt(s); ok {
			exp.Seed = uint64(c)
		} else {
			errs.Add("$.seed", engine.CodeInvalidType, "expected integer")
		}
	}
	if w, ok := m["workers"]; ok {
		if c, ok := getInt(w); ok {
			exp.Workers = c
		} else {
			errs.Add("$.workers", engine.CodeInvalidType, "expected integer")
		}
	}
	if top, ok := m["top"]; ok {
		if c, ok := getInt(top); ok {
			exp.Top = c
		} else {
			errs.Add("$.top", engine.CodeInvalidType, "expected integer")
		}
	}

	if mp, ok := m["max_policies"]; ok {
		if c, ok := getInt(mp); ok {
			exp.MaxPolicies = c
		} else {
			errs.Add("$.max_policies", engine.CodeInvalidType, "expected integer")
		}
	}

	if reports, ok := m["reports"]; ok {
		if arr, ok := reports.([]any); ok {
			exp.Reports = make([]string, 0, len(arr))
			for i, v := range arr {
				if s, ok := v.(string); ok {
					exp.Reports = append(exp.Reports, s)
				} else {
					errs.Add(fmt.Sprintf("$.reports[%d]", i), engine.CodeInvalidType, "expected string")
				}
			}
		} else {
			errs.Add("$.reports", engine.CodeInvalidType, "expected array")
		}
	}
	if prior, ok := m["world_prior"]; ok {
		if pm, ok := prior.(map[string]any); ok {
			exp.WorldPrior = make(map[string]float64, len(pm))
			for k, v := range pm {
				if f, ok := getFloat(v); ok {
					exp.WorldPrior[k] = f
				} else {
					errs.Add("$.world_prior."+k, engine.CodeInvalidType, "expected number")
				}
			}
		} else {
			errs.Add("$.world_prior", engine.CodeInvalidType, "expected object")
		}
	}
	if alpha, ok := m["cvar_alpha"]; ok {
		f, ok := getFloat(alpha)
		switch {
		case !ok:
			errs.Add("$.cvar_alpha", engine.CodeInvalidType, "expected number")
		case !(f > 0 && f <= 1):
			// 0 would otherwise silently select the default.
			errs.Add("$.cvar_alpha", engine.CodeInvalidValue, "cvar_alpha must be in (0, 1]")
		default:
			exp.CVaRAlpha = f
		}
	}

	checkUnknownFields(m, []string{
		"apiVersion", "model", "worlds", "policies", "evaluator", "runs", "seed", "workers", "top", "max_policies",
		"reports", "world_prior", "cvar_alpha",
	}, "$", errs)

	if errs.Len() > 0 {
		return &exp, errs
	}
	return &exp, nil
}

func checkUnknownFields(m map[string]any, allowed []string, path string, errs *engine.ValidationErrors) {
	for k := range m {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
				break
			}
		}
		if !found {
			errs.Add(path+"."+k, engine.CodeUnknownField, fmt.Sprintf("unknown field '%s'", k))
		}
	}
}

func isNumberStringBool(v any) bool {
	switch v.(type) {
	case string, float64, int, int64, bool:
		return true
	}
	return false
}

func isNumberOrCEL(v any) bool {
	switch v.(type) {
	case string, float64, int, int64:
		return true
	}
	return false
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

func getInt(v any) (int, bool) {
	switch val := v.(type) {
	case float64:
		if float64(int(val)) == val {
			return int(val), true
		}
	case int:
		return val, true
	case int64:
		return int(val), true
	}
	return 0, false
}

func getFloat(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	}
	return 0, false
}

func bindPolicies(pm map[string]any, p *Policies, path string, errs *engine.ValidationErrors) {
	if s, ok := pm["search"].(string); ok {
		p.Search = s
	} else if v, ok := pm["search"]; ok && v != nil {
		errs.Add(path+".search", engine.CodeInvalidType, "expected string")
	}
	if files, ok := pm["files"]; ok {
		if arr, ok := files.([]any); ok {
			for i, v := range arr {
				if s, ok := v.(string); ok {
					p.Files = append(p.Files, s)
				} else {
					errs.Add(fmt.Sprintf("%s.files[%d]", path, i), engine.CodeInvalidType, "expected string")
				}
			}
		} else {
			errs.Add(path+".files", engine.CodeInvalidType, "expected array")
		}
	}
	if v, ok := pm["policy"]; ok {
		if s, ok := v.(string); ok {
			p.Policy = s
		} else {
			errs.Add(path+".policy", engine.CodeInvalidType, "expected string")
		}
	}
	if v, ok := pm["samples"]; ok {
		if c, ok := getInt(v); ok {
			p.Samples = c
		} else {
			errs.Add(path+".samples", engine.CodeInvalidType, "expected integer")
		}
	}
	if v, ok := pm["seed"]; ok {
		c, ok := getInt(v)
		switch {
		case !ok:
			errs.Add(path+".seed", engine.CodeInvalidType, "expected integer")
		case c < 0:
			errs.Add(path+".seed", engine.CodeInvalidValue, "seed must be >= 0")
		default:
			seed := uint64(c)
			p.Seed = &seed
		}
	}
	if v, ok := pm["min_runs"]; ok {
		if c, ok := getInt(v); ok {
			p.MinRuns = c
		} else {
			errs.Add(path+".min_runs", engine.CodeInvalidType, "expected integer")
		}
	}
	if v, ok := pm["max_runs"]; ok {
		if c, ok := getInt(v); ok {
			p.MaxRuns = c
		} else {
			errs.Add(path+".max_runs", engine.CodeInvalidType, "expected integer")
		}
	}
	if v, ok := pm["factor"]; ok {
		if f, ok := getFloat(v); ok {
			p.Factor = f
		} else {
			errs.Add(path+".factor", engine.CodeInvalidType, "expected number")
		}
	}
	if v, ok := pm["alpha"]; ok {
		if f, ok := getFloat(v); ok {
			p.Alpha = f
		} else {
			errs.Add(path+".alpha", engine.CodeInvalidType, "expected number")
		}
	}
	if v, ok := pm["candidates"]; ok {
		if cm, ok := v.(map[string]any); ok {
			p.Candidates = &Policies{}
			bindPolicies(cm, p.Candidates, path+".candidates", errs)
			checkUnknownFields(cm, []string{"search", "files", "policy", "samples", "seed"}, path+".candidates", errs)
		} else {
			errs.Add(path+".candidates", engine.CodeInvalidType, "expected object")
		}
	}
	checkUnknownFields(pm, []string{"search", "files", "policy", "samples", "seed", "min_runs", "max_runs", "factor", "alpha", "candidates"}, path, errs)
}
