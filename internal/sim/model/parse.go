package model

import (
	"fmt"
	"sort"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/graph"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

func newValidationError(path string, code engine.ErrorCode, message string) error {
	errs := &engine.ValidationErrors{}
	errs.Add(path, code, message)
	return errs
}

// ParseSimModel parses a simulation definition from bytes.
func ParseSimModel(data []byte, format engine.Format) (*Sim, error) {
	tree, err := graph.DecodeStrict(data, format)
	if err != nil {
		return nil, err
	}

	m, ok := tree.(map[string]any)
	if !ok {
		return nil, newValidationError("$", engine.CodeInvalidType, "root must be an object")
	}

	if apiVersion, ok := m["apiVersion"].(string); !ok || apiVersion != "sim/v1" {
		return nil, newValidationError("$.apiVersion", engine.CodeUnsupportedVersion, fmt.Sprintf("unsupported apiVersion: %v", m["apiVersion"]))
	}

	errs := &engine.ValidationErrors{}
	sim, bindErr := bindSim(tree)
	if bindErr != nil {
		errs.Merge(bindErr.(*engine.ValidationErrors))
	}

	if valErr := validateSim(sim); valErr != nil {
		errs.Merge(valErr.(*engine.ValidationErrors))
	}

	if errs.Len() > 0 {
		return nil, sortErrors(errs)
	}
	return sim, nil
}

// ParsePolicy parses a policy definition from bytes.
func ParsePolicy(data []byte, format engine.Format) (*Policy, error) {
	tree, err := graph.DecodeStrict(data, format)
	if err != nil {
		return nil, err
	}

	m, ok := tree.(map[string]any)
	if !ok {
		return nil, newValidationError("$", engine.CodeInvalidType, "root must be an object")
	}

	if apiVersion, ok := m["apiVersion"].(string); !ok || apiVersion != "policy/v1" {
		return nil, newValidationError("$.apiVersion", engine.CodeUnsupportedVersion, fmt.Sprintf("unsupported apiVersion: %v", m["apiVersion"]))
	}

	errs := &engine.ValidationErrors{}
	pol, bindErr := bindPolicy(tree)
	if bindErr != nil {
		errs.Merge(bindErr.(*engine.ValidationErrors))
	}

	if valErr := validatePolicy(pol); valErr != nil {
		errs.Merge(valErr.(*engine.ValidationErrors))
	}

	if errs.Len() > 0 {
		return nil, sortErrors(errs)
	}
	return pol, nil
}

// ParseExperiment parses an experiment definition from bytes.
func ParseExperiment(data []byte, format engine.Format) (*Experiment, error) {
	tree, err := graph.DecodeStrict(data, format)
	if err != nil {
		return nil, err
	}

	m, ok := tree.(map[string]any)
	if !ok {
		return nil, newValidationError("$", engine.CodeInvalidType, "root must be an object")
	}

	if apiVersion, ok := m["apiVersion"].(string); !ok || apiVersion != "experiment/v1" {
		return nil, newValidationError("$.apiVersion", engine.CodeUnsupportedVersion, fmt.Sprintf("unsupported apiVersion: %v", m["apiVersion"]))
	}

	errs := &engine.ValidationErrors{}
	exp, bindErr := bindExperiment(tree)
	if bindErr != nil {
		errs.Merge(bindErr.(*engine.ValidationErrors))
	}

	if exp.Evaluator == "" {
		exp.Evaluator = "monte_carlo"
	}
	if exp.Top == 0 {
		exp.Top = 5
	}

	if valErr := validateExperiment(exp); valErr != nil {
		errs.Merge(valErr.(*engine.ValidationErrors))
	}

	if errs.Len() > 0 {
		return nil, sortErrors(errs)
	}
	return exp, nil
}

func sortErrors(err error) error {
	verrs, ok := err.(*engine.ValidationErrors)
	if !ok {
		return err
	}
	issues := verrs.Issues()
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Path != issues[j].Path {
			return issues[i].Path < issues[j].Path
		}
		if issues[i].Code != issues[j].Code {
			return issues[i].Code < issues[j].Code
		}
		return issues[i].Message < issues[j].Message
	})
	sorted := &engine.ValidationErrors{}
	for _, issue := range issues {
		sorted.AddIssue(issue)
	}
	return sorted
}

func validateSim(s *Sim) error {
	errs := &engine.ValidationErrors{}

	if s.ID == "" {
		errs.Add("$.id", engine.CodeMissingRequired, "missing required property id")
	}
	if s.Graph == "" {
		errs.Add("$.graph", engine.CodeMissingRequired, "missing required property graph")
	}
	if s.Objective.Name == "" {
		errs.Add("$.objective.name", engine.CodeMissingRequired, "missing required property name")
	}
	if s.Objective.Value == "" {
		errs.Add("$.objective.value", engine.CodeMissingRequired, "missing required property value")
	}

	if len(s.Worlds) == 0 {
		errs.Add("$.worlds", engine.CodeMissingRequired, "must have at least one world")
	}

	for name, vs := range s.Latents {
		if checkValueSpec(vs) != 1 {
			errs.Add(fmt.Sprintf("$.latents.%s", name), engine.CodeInvalidValue, "value spec must have exactly one key")
		}
	}
	for bName, b := range s.Behaviors {
		if b.Duration != nil {
			if sDur, ok := b.Duration.(string); ok {
				if _, err := time.ParseDuration(sDur); err != nil {
					errs.Add(fmt.Sprintf("$.behaviors.%s.duration", bName), engine.CodeInvalidValue, "invalid duration format")
				}
			} else if vs, ok := b.Duration.(ValueSpec); ok {
				if checkValueSpec(vs) != 1 {
					errs.Add(fmt.Sprintf("$.behaviors.%s.duration", bName), engine.CodeInvalidValue, "value spec must have exactly one key")
				}
			}
		}
		if b.Fail != nil {
			class := b.Fail.Class
			if class != "retryable" && class != "non_retryable" && class != "critical" {
				errs.Add(fmt.Sprintf("$.behaviors.%s.fail.class", bName), engine.CodeInvalidValue, "invalid class enum")
			}
		}
		for outName, vs := range b.Output {
			if checkValueSpec(vs) != 1 {
				errs.Add(fmt.Sprintf("$.behaviors.%s.output.%s", bName, outName), engine.CodeInvalidValue, "value spec must have exactly one key")
			}
		}
	}
	for rName, r := range s.Resources {
		if r.Capacity < 1 {
			errs.Add(fmt.Sprintf("$.resources.%s.capacity", rName), engine.CodeInvalidValue, "capacity must be >= 1")
		}
	}

	if s.Objective.Maximize != "mean" {
		errs.Add("$.objective.maximize", engine.CodeInvalidValue, "objective.maximize must be 'mean'")
	}

	if errs.Len() > 0 {
		return errs
	}
	return nil
}

func validatePolicy(p *Policy) error {
	errs := &engine.ValidationErrors{}
	if p.ID == "" {
		errs.Add("$.id", engine.CodeMissingRequired, "missing required property id")
	}
	if p.Version == "" {
		errs.Add("$.version", engine.CodeMissingRequired, "missing required property version")
	}
	if p.Decisions == nil {
		errs.Add("$.decisions", engine.CodeMissingRequired, "missing required property decisions")
	}
	validateParams(p.Params, errs)

	for dName, d := range p.Decisions {
		path := fmt.Sprintf("$.decisions.%s", dName)
		hasRules := len(d.Rules) > 0
		hasTable := len(d.Table) > 0

		if (hasRules && hasTable) || (!hasRules && !hasTable) {
			errs.Add(path, engine.CodeInvalidValue, "decision must have exactly one of rules or table")
		}

		if hasRules {
			if d.Default != "" {
				errs.Add(path+".default", engine.CodeInvalidValue, "default is only allowed with table")
			}
			for i, r := range d.Rules {
				rp := fmt.Sprintf("%s.rules[%d]", path, i)
				if r.When != "" && r.Choose != "" {
					if r.Else != "" {
						errs.Add(rp, engine.CodeInvalidValue, "rule cannot mix when/choose with else")
					}
				} else if r.Else != "" {
					if r.When != "" || r.Choose != "" {
						errs.Add(rp, engine.CodeInvalidValue, "rule cannot mix when/choose with else")
					}
					if i != len(d.Rules)-1 {
						errs.Add(rp, engine.CodeInvalidValue, "else must be the last rule")
					}
				} else {
					errs.Add(rp, engine.CodeInvalidValue, "rule must have either when/choose or else")
				}
			}
		}
		if hasTable {
			if d.Default == "" {
				errs.Add(path+".default", engine.CodeMissingRequired, "missing required property default")
			}
			for i, r := range d.Table {
				rp := fmt.Sprintf("%s.table[%d]", path, i)
				if len(r.Observed) == 0 {
					errs.Add(rp+".observed", engine.CodeMissingRequired, "missing required property observed")
				}
				if r.Choose == "" {
					errs.Add(rp+".choose", engine.CodeMissingRequired, "missing required property choose")
				}
			}
		}
	}
	if errs.Len() > 0 {
		return errs
	}
	return nil
}

func validateExperiment(e *Experiment) error {
	errs := &engine.ValidationErrors{}
	if e.Model == "" {
		errs.Add("$.model", engine.CodeMissingRequired, "missing required property model")
	}
	if e.Runs < 1 {
		errs.Add("$.runs", engine.CodeInvalidValue, "runs must be >= 1")
	}
	if e.Evaluator != "monte_carlo" && e.Evaluator != "exact" && e.Evaluator != "both" {
		errs.Add("$.evaluator", engine.CodeInvalidValue, "invalid evaluator enum")
	}
	if e.Policies.Search != "" && len(e.Policies.Files) > 0 {
		if e.Policies.Search != "racing" {
			errs.Add("$.policies", engine.CodeInvalidValue, "policies must have exactly one of search or files")
		}
	} else if e.Policies.Search == "" && len(e.Policies.Files) == 0 {
		errs.Add("$.policies", engine.CodeInvalidValue, "policies must have exactly one of search or files")
	}
	validatePolicySearch(e.Policies, errs)
	if e.Workers < 0 {
		errs.Add("$.workers", engine.CodeInvalidValue, "workers must be >= 1")
	}
	if e.Top < 1 {
		errs.Add("$.top", engine.CodeInvalidValue, "top must be >= 1")
	}
	if e.MaxPolicies < 0 {
		errs.Add("$.max_policies", engine.CodeInvalidValue, "max_policies must be >= 1")
	}

	if errs.Len() > 0 {
		return errs
	}
	return nil
}

// validatePolicySearch checks policies.search and the fields that depend on
// it: policy (grid and random only, required there), samples (random only,
// required, >= 1) and seed (random only), as well as racing parameters.
func validatePolicySearch(p Policies, errs *engine.ValidationErrors) {
	search := p.Search
	switch search {
	case "", "exhaustive", "exact", "grid", "random", "racing":
	default:
		errs.Add("$.policies.search", engine.CodeInvalidValue, "search must be exhaustive, exact, grid, random or racing")
	}

	if search != "racing" {
		if p.Candidates != nil {
			errs.Add("$.policies.candidates", engine.CodeInvalidValue, "candidates is only allowed with search: racing")
		}
		if p.MinRuns != 0 {
			errs.Add("$.policies.min_runs", engine.CodeInvalidValue, "min_runs is only allowed with search: racing")
		}
		if p.MaxRuns != 0 {
			errs.Add("$.policies.max_runs", engine.CodeInvalidValue, "max_runs is only allowed with search: racing")
		}
		if p.Factor != 0 {
			errs.Add("$.policies.factor", engine.CodeInvalidValue, "factor is only allowed with search: racing")
		}
		if p.Alpha != 0 {
			errs.Add("$.policies.alpha", engine.CodeInvalidValue, "alpha is only allowed with search: racing")
		}

		parameterized := search == "grid" || search == "random"
		switch {
		case parameterized && p.Policy == "":
			errs.Add("$.policies.policy", engine.CodeMissingRequired, fmt.Sprintf("search: %s requires policy", search))
		case !parameterized && p.Policy != "":
			errs.Add("$.policies.policy", engine.CodeInvalidValue, "policy is only allowed with search: grid or random")
		}
		switch {
		case search == "random" && p.Samples < 1:
			errs.Add("$.policies.samples", engine.CodeInvalidValue, "search: random requires samples >= 1")
		case search != "random" && p.Samples != 0:
			errs.Add("$.policies.samples", engine.CodeInvalidValue, "samples is only allowed with search: random")
		}
		if search != "random" && p.Seed != nil {
			errs.Add("$.policies.seed", engine.CodeInvalidValue, "seed is only allowed with search: random")
		}
		return
	}

	validateRacing(p, errs)
}

func validateRacing(p Policies, errs *engine.ValidationErrors) {
	if p.MinRuns < 0 || (p.MinRuns != 0 && p.MinRuns < 1) {
		errs.Add("$.policies.min_runs", engine.CodeInvalidValue, "min_runs must be >= 1")
	}
	if p.MaxRuns < 0 || (p.MaxRuns != 0 && p.MaxRuns < 1) {
		errs.Add("$.policies.max_runs", engine.CodeInvalidValue, "max_runs must be >= 1")
	}
	if p.MinRuns > 0 && p.MaxRuns > 0 && p.MinRuns > p.MaxRuns {
		errs.Add("$.policies.min_runs", engine.CodeInvalidValue, "min_runs must be <= max_runs")
	}
	if p.Factor != 0 && p.Factor <= 1 {
		errs.Add("$.policies.factor", engine.CodeInvalidValue, "factor must be > 1")
	}
	if p.Alpha != 0 && (p.Alpha <= 0 || p.Alpha >= 1) {
		errs.Add("$.policies.alpha", engine.CodeInvalidValue, "alpha must be in (0, 1)")
	}

	sources := 0
	if p.Candidates != nil {
		sources++
	}
	if p.Policy != "" {
		sources++
	}
	if len(p.Files) > 0 {
		sources++
	}
	if sources > 1 {
		errs.Add("$.policies", engine.CodeInvalidValue, "racing candidates must be specified via only one of candidates, policy, or files")
	}

	if p.Candidates != nil {
		c := *p.Candidates
		switch c.Search {
		case "", "exhaustive", "grid", "random":
		default:
			errs.Add("$.policies.candidates.search", engine.CodeInvalidValue, "candidates search must be exhaustive, grid or random")
		}
		if c.Search != "" && len(c.Files) > 0 {
			errs.Add("$.policies.candidates", engine.CodeInvalidValue, "candidates must have exactly one of search or files")
		} else if c.Search == "" && len(c.Files) == 0 {
			errs.Add("$.policies.candidates", engine.CodeInvalidValue, "candidates must have exactly one of search or files")
		}
		param := c.Search == "grid" || c.Search == "random"
		switch {
		case param && c.Policy == "":
			errs.Add("$.policies.candidates.policy", engine.CodeMissingRequired, fmt.Sprintf("search: %s requires policy", c.Search))
		case !param && c.Policy != "":
			errs.Add("$.policies.candidates.policy", engine.CodeInvalidValue, "policy is only allowed with search: grid or random")
		}
		switch {
		case c.Search == "random" && c.Samples < 1:
			errs.Add("$.policies.candidates.samples", engine.CodeInvalidValue, "search: random requires samples >= 1")
		case c.Search != "random" && c.Samples != 0:
			errs.Add("$.policies.candidates.samples", engine.CodeInvalidValue, "samples is only allowed with search: random")
		}
		if c.Search != "random" && c.Seed != nil {
			errs.Add("$.policies.candidates.seed", engine.CodeInvalidValue, "seed is only allowed with search: random")
		}
		if c.Candidates != nil {
			errs.Add("$.policies.candidates.candidates", engine.CodeInvalidValue, "candidates cannot be nested")
		}
		if c.MinRuns != 0 {
			errs.Add("$.policies.candidates.min_runs", engine.CodeInvalidValue, "min_runs is only allowed on top-level racing search")
		}
		if c.MaxRuns != 0 {
			errs.Add("$.policies.candidates.max_runs", engine.CodeInvalidValue, "max_runs is only allowed on top-level racing search")
		}
		if c.Factor != 0 {
			errs.Add("$.policies.candidates.factor", engine.CodeInvalidValue, "factor is only allowed on top-level racing search")
		}
		if c.Alpha != 0 {
			errs.Add("$.policies.candidates.alpha", engine.CodeInvalidValue, "alpha is only allowed on top-level racing search")
		}
	} else if p.Policy != "" {
		if p.Samples < 0 {
			errs.Add("$.policies.samples", engine.CodeInvalidValue, "samples must be >= 1")
		}
		if p.Samples == 0 && p.Seed != nil {
			errs.Add("$.policies.seed", engine.CodeInvalidValue, "seed is only allowed with search: random")
		}
	} else {
		if p.Samples != 0 {
			errs.Add("$.policies.samples", engine.CodeInvalidValue, "samples is only allowed with search: random")
		}
		if p.Seed != nil {
			errs.Add("$.policies.seed", engine.CodeInvalidValue, "seed is only allowed with search: random")
		}
	}
}

func checkValueSpec(vs ValueSpec) int {
	c := 0
	if vs.Fixed != nil {
		c++
	}
	if vs.Choice != nil {
		c++
	}
	if vs.Bernoulli != nil {
		c++
	}
	if vs.Normal != nil {
		c++
	}
	if vs.Lognormal != nil {
		c++
	}
	if vs.Uniform != nil {
		c++
	}
	if vs.Triangular != nil {
		c++
	}
	if vs.Empirical != nil {
		c++
	}
	if vs.Expr != "" {
		c++
	}
	return c
}
