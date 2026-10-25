package model

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	"github.com/gargi-indic/gargi-flow/internal/graph"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/ast"
)

// runtimePolicy implements sim.Policy and Parameterized for a compiled
// policy/v1 document. Copies made by WithParams share decisions and specs.
type runtimePolicy struct {
	id          string
	description string
	// baseID and baseDescription are id and description without params.
	baseID          string
	baseDescription string
	decisions       map[string]*compiledDecision
	// specs holds the declared params; values their bound values (never
	// nil, read-only once bound).
	specs  map[string]ParamSpec
	values map[string]any
}

type compiledDecision struct {
	// Observes lists the decision node's observed paths (its information set).
	Observes []string
	Rules    []compiledRule
	Table    []compiledTableRow
	Default  string
	IsTable  bool
	Agent    *AgentConfig
	// BindsParams reports whether the rule environment declares the params
	// variable (it does unless an observed node is itself named params).
	BindsParams bool
}

// AgentDecisionChooser handles executing decisions with an agent configuration.
// It is registered by internal/sim/agent to break package import cycles.
var AgentDecisionChooser func(ctx context.Context, config *AgentConfig, decision string, options []string, observed map[string]any, replication uint64) (string, error)

type compiledRule struct {
	When   cel.Program
	Choose string
	Else   string
}

type compiledTableRow struct {
	// Observed holds the row's expected values with numbers normalized to float64.
	Observed map[string]any
	Choose   string
}

var _ sim.Policy = (*runtimePolicy)(nil)

// ID returns <policy id>@<version>.
func (p *runtimePolicy) ID() string {
	return p.id
}

// Describe returns a human-readable description of the policy.
func (p *runtimePolicy) Describe() string {
	return p.description
}

// Choose picks the option for decision from obs. Only the decision's observed
// paths are read; an observed path missing from obs is treated as nil. Rules
// return the choice of the first rule whose when is true, otherwise the else
// choice. Tables return the choice of the first row whose observed values all
// equal the observation exactly (nil matches null, numbers are compared as
// float64), otherwise the default.
func (p *runtimePolicy) Choose(ctx context.Context, decision engine.NodeID, obs sim.Observation, options []string) (string, error) {
	nodeID := string(decision)
	d, ok := p.decisions[nodeID]
	if !ok {
		return "", fmt.Errorf("no policy for decision %q", nodeID)
	}

	if d.Agent != nil {
		if AgentDecisionChooser == nil {
			return "", fmt.Errorf("agent decision chooser not registered")
		}
		rep := sim.ReplicationFromContext(ctx)
		return AgentDecisionChooser(ctx, d.Agent, nodeID, options, obs, rep)
	}

	if d.IsTable {
		for _, row := range d.Table {
			if rowMatches(row, obs) {
				return row.Choose, nil
			}
		}
		if d.Default == "" {
			return "", fmt.Errorf("no matching table row and no default for %q", nodeID)
		}
		return d.Default, nil
	}

	activation := buildActivation(d.Observes, obs)
	if d.BindsParams {
		activation[paramsVar] = p.values
	}
	for i, rule := range d.Rules {
		if rule.When == nil {
			if rule.Else != "" {
				return rule.Else, nil
			}
			continue
		}
		out, _, err := rule.When.ContextEval(ctx, activation)
		if err != nil {
			return "", fmt.Errorf("decision %q rule %d: failed to evaluate when: %w", nodeID, i, err)
		}
		val, ok := out.Value().(bool)
		if !ok {
			return "", fmt.Errorf("decision %q rule %d: when evaluated to %T, want bool", nodeID, i, out.Value())
		}
		if val {
			return rule.Choose, nil
		}
	}
	return "", fmt.Errorf("no matching rule for %q", nodeID)
}

// buildActivation nests the observed paths into maps keyed by node id, for
// example "a.output.x" becomes {"a": {"output": {"x": v}}}.
func buildActivation(observes []string, obs sim.Observation) map[string]any {
	info := make(map[string]any)
	for _, k := range observes {
		parts := strings.Split(k, ".")
		current := info
		for i, part := range parts {
			if i == len(parts)-1 {
				current[part] = obs[k]
				break
			}
			next, ok := current[part].(map[string]any)
			if !ok {
				next = make(map[string]any)
				current[part] = next
			}
			current = next
		}
	}
	return info
}

func rowMatches(row compiledTableRow, obs sim.Observation) bool {
	for k, want := range row.Observed {
		if !reflect.DeepEqual(normalizeNumber(obs[k]), want) {
			return false
		}
	}
	return true
}

// normalizeNumber converts every numeric type to float64 so that a YAML 1
// equals an observed 1.0. Other values are returned unchanged.
func normalizeNumber(v any) any {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int8:
		return float64(n)
	case int16:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case uint:
		return float64(n)
	case uint8:
		return float64(n)
	case uint16:
		return float64(n)
	case uint32:
		return float64(n)
	case uint64:
		return float64(n)
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f
		}
		return n.String()
	}
	return v
}

func getSelectPath(expr ast.Expr) string {
	switch expr.Kind() {
	case ast.IdentKind:
		return expr.AsIdent()
	case ast.SelectKind:
		sel := expr.AsSelect()
		if sel.IsTestOnly() {
			return ""
		}
		operandPath := getSelectPath(sel.Operand())
		if operandPath == "" {
			return ""
		}
		return operandPath + "." + sel.FieldName()
	}
	return ""
}

func decisionOptions(node *engine.NodeDefinition) map[string]bool {
	optsRaw, _ := node.Config["options"].([]any)
	options := make(map[string]bool, len(optsRaw))
	for _, o := range optsRaw {
		switch v := o.(type) {
		case string:
			options[v] = true
		case map[string]any:
			if s, ok := v["value"].(string); ok {
				options[s] = true
			}
		}
	}
	return options
}

func decisionObserves(node *engine.NodeDefinition) []string {
	raw, _ := node.Config["observes"].([]any)
	out := make([]string, 0, len(raw))
	for _, o := range raw {
		if s, ok := o.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// CompilePolicy compiles a policy/v1 document against a compiled model. Rule
// when expressions are type-checked in an environment that declares only the
// decision's observed node ids, and the checked AST is walked to reject any
// reference that is not a field selection of an observed path. Table keys
// must be observed paths, every choice must be one of the decision's options,
// every decision node must be covered and unknown decisions are rejected. It
// returns nil errors on success.
//
// Declared params are validated (engine.CodeInvalidValue at
// $.params.<name>) and bound to their defaults. Rule environments also
// declare params as map(string, dyn): params.<name> is allowed for a
// declared name and params.<undeclared> is sim.CodePolicyUnknownParam.
// Params are constants, so they never widen the information set. The
// returned policy implements Parameterized.
func CompilePolicy(p *Policy, m *CompiledModel) (sim.Policy, *engine.ValidationErrors) {
	errs := &engine.ValidationErrors{}
	if p == nil {
		errs.Add(engine.PathRoot.String(), engine.CodeMissingRequired, "policy definition is nil")
		return nil, sortedIssues(errs)
	}
	if m == nil || m.Graph == nil {
		errs.Add(engine.PathRoot.String(), engine.CodeMissingRequired, "compiled model is nil")
		return nil, sortedIssues(errs)
	}

	validateParams(p.Params, errs)
	declared := make(map[string]bool, len(p.Params))
	defaults := make(map[string]any, len(p.Params))
	for name, spec := range p.Params {
		declared[name] = true
		defaults[name] = spec.DefaultValue()
	}

	rp := &runtimePolicy{
		baseID:          fmt.Sprintf("%s@%s", p.ID, p.Version),
		baseDescription: fmt.Sprintf("policy %s version %s", p.ID, p.Version),
		decisions:       make(map[string]*compiledDecision, len(p.Decisions)),
		specs:           maps.Clone(p.Params),
	}

	decisionNodes := make(map[string]*engine.NodeDefinition)
	for i := range m.Graph.Nodes {
		n := &m.Graph.Nodes[i]
		if n.Type == "decision" {
			decisionNodes[string(n.ID)] = n
		}
	}

	decisionsPath := engine.PathRoot.Field("decisions")
	for _, dID := range sortedKeys(decisionNodes) {
		if _, ok := p.Decisions[dID]; !ok {
			errs.Add(decisionsPath.Field(dID).String(), sim.CodePolicyMissingDecision, fmt.Sprintf("missing decision for node %q", dID))
		}
	}

	for _, id := range sortedKeys(p.Decisions) {
		pd := p.Decisions[id]
		path := decisionsPath.Field(id)
		node, ok := decisionNodes[id]
		if !ok {
			errs.Add(path.String(), sim.CodePolicyUnknownDecision, fmt.Sprintf("unknown decision node %q", id))
			continue
		}

		options := decisionOptions(node)
		checkOption := func(choice string, at engine.Path) {
			if !options[choice] {
				errs.Add(at.String(), sim.CodePolicyUnknownOption, fmt.Sprintf("unknown option %q for decision %q", choice, id))
			}
		}

		cd := &compiledDecision{
			Observes: decisionObserves(node),
			IsTable:  len(pd.Table) > 0,
			Agent:    pd.Agent,
		}
		observed := make(map[string]bool, len(cd.Observes))
		for _, o := range cd.Observes {
			observed[o] = true
		}

		if cd.Agent == nil {
			if cd.IsTable {
				compileTable(pd, path, observed, checkOption, cd, errs)
			} else {
				compileRules(pd, path, cd.Observes, observed, declared, checkOption, cd, errs)
			}
		}
		rp.decisions[id] = cd
	}

	if errs.Len() > 0 {
		return nil, sortedIssues(errs)
	}
	return rp.bind(defaults), nil
}

func compileTable(pd Decision, path engine.Path, observed map[string]bool, checkOption func(string, engine.Path), cd *compiledDecision, errs *engine.ValidationErrors) {
	if pd.Default != "" {
		checkOption(pd.Default, path.Field("default"))
	}
	cd.Default = pd.Default

	for i, row := range pd.Table {
		rowPath := path.Field("table").Index(i)
		checkOption(row.Choose, rowPath.Field("choose"))

		norm := make(map[string]any, len(row.Observed))
		for _, k := range sortedKeys(row.Observed) {
			if !observed[k] {
				errs.Add(rowPath.Field("observed").Field(k).String(), sim.CodePolicyUnobservedReference, fmt.Sprintf("unobserved reference %q", k))
			}
			norm[k] = normalizeNumber(row.Observed[k])
		}
		cd.Table = append(cd.Table, compiledTableRow{Observed: norm, Choose: row.Choose})
	}
}

func compileRules(pd Decision, path engine.Path, observes []string, observed, declared map[string]bool, checkOption func(string, engine.Path), cd *compiledDecision, errs *engine.ValidationErrors) {
	idSet := make(map[string]bool)
	for _, o := range observes {
		idSet[strings.SplitN(o, ".", 2)[0]] = true
	}
	ids := sortedKeys(idSet)
	opts := make([]cel.EnvOption, 0, len(ids)+1)
	for _, id := range ids {
		opts = append(opts, mapVar(id))
	}
	// params is declared unless an observed node has that id, in which case
	// params.<x> keeps meaning the node's fields.
	var params map[string]bool
	if idSet[paramsVar] {
		if len(declared) > 0 {
			errs.Add(path.String(), engine.CodeInvalidValue, fmt.Sprintf("decision observes node %q, which shadows the policy params", paramsVar))
		}
	} else {
		cd.BindsParams = true
		params = declared
		opts = append(opts, mapVar(paramsVar))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		errs.Add(path.String(), engine.CodeInvalidValue, fmt.Sprintf("failed to create env for decision: %v", err))
		return
	}

	for i, rule := range pd.Rules {
		rulePath := path.Field("rules").Index(i)
		cr := compiledRule{Choose: rule.Choose, Else: rule.Else}

		if rule.When != "" {
			whenPath := rulePath.Field("when").String()
			celAst, issues := env.Compile(rule.When)
			switch {
			case issues != nil && issues.Err() != nil:
				errs.Add(whenPath, sim.CodeInvalidExpression, issues.Err().Error())
			case !celAst.OutputType().IsExactType(cel.BoolType) && !celAst.OutputType().IsExactType(cel.DynType):
				errs.Add(whenPath, sim.CodeInvalidExpression, fmt.Sprintf("when must evaluate to bool, got %v", celAst.OutputType()))
			default:
				before := errs.Len()
				walkAST(celAst.NativeRep().Expr(), observed, params, whenPath, errs)
				if errs.Len() == before {
					prog, err := env.Program(celAst, cel.CostLimit(graph.DefaultConditionCostLimit), cel.EvalOptions(cel.OptTrackCost))
					if err != nil {
						errs.Add(whenPath, sim.CodeInvalidExpression, err.Error())
					}
					cr.When = prog
				}
			}
		}

		if rule.Choose != "" {
			checkOption(rule.Choose, rulePath.Field("choose"))
		}
		if rule.Else != "" {
			checkOption(rule.Else, rulePath.Field("else"))
		}
		cd.Rules = append(cd.Rules, cr)
	}
}

func isAllowedPath(p string, allowed map[string]bool) bool {
	for a := range allowed {
		if p == a || strings.HasPrefix(p, a+".") {
			return true
		}
	}
	return false
}

// walkAST rejects every reference in expr that is not a field-selection chain
// starting with an observed path, or exactly params.<name> for a declared
// param. params is nil when the rule environment does not declare the params
// variable; params.<undeclared> is sim.CodePolicyUnknownParam.
func walkAST(expr ast.Expr, allowed, params map[string]bool, path string, errs *engine.ValidationErrors) {
	if expr == nil {
		return
	}

	switch expr.Kind() {
	case ast.SelectKind:
		sel := expr.AsSelect()
		if sel.IsTestOnly() {
			// has(x.y.z): the tested path must itself be observed.
			p := getSelectPath(sel.Operand())
			if p == "" || !isAllowedPath(p+"."+sel.FieldName(), allowed) {
				errs.Add(path, sim.CodePolicyUnobservedReference, "has() must test an observed path")
			}
			return
		}
		p := getSelectPath(expr)
		if p == "" {
			errs.Add(path, sim.CodePolicyUnobservedReference, "complex select or index access not allowed")
			return
		}
		if params != nil && strings.HasPrefix(p, paramsVar+".") {
			parts := strings.SplitN(p, ".", 3)
			switch {
			case !params[parts[1]]:
				errs.Add(path, sim.CodePolicyUnknownParam, fmt.Sprintf("unknown param %q", parts[1]))
			case len(parts) > 2:
				errs.Add(path, sim.CodePolicyUnobservedReference, fmt.Sprintf("params are scalars; %s is not allowed", p))
			}
			return
		}
		if !isAllowedPath(p, allowed) {
			errs.Add(path, sim.CodePolicyUnobservedReference, fmt.Sprintf("unobserved reference: %s", p))
		}
	case ast.IdentKind:
		ident := expr.AsIdent()
		if !isAllowedPath(ident, allowed) {
			errs.Add(path, sim.CodePolicyUnobservedReference, fmt.Sprintf("unobserved reference: %s", ident))
		}
	case ast.CallKind:
		call := expr.AsCall()
		if call.IsMemberFunction() {
			walkAST(call.Target(), allowed, params, path, errs)
		}
		for _, arg := range call.Args() {
			walkAST(arg, allowed, params, path, errs)
		}
	case ast.ListKind:
		for _, e := range expr.AsList().Elements() {
			walkAST(e, allowed, params, path, errs)
		}
	case ast.MapKind:
		for _, e := range expr.AsMap().Entries() {
			me := e.AsMapEntry()
			walkAST(me.Key(), allowed, params, path, errs)
			walkAST(me.Value(), allowed, params, path, errs)
		}
	case ast.StructKind:
		for _, f := range expr.AsStruct().Fields() {
			walkAST(f.AsStructField().Value(), allowed, params, path, errs)
		}
	case ast.ComprehensionKind:
		// Comprehensions introduce iteration variables that cannot be
		// checked against the information set, so they are rejected.
		errs.Add(path, sim.CodePolicyUnobservedReference, "comprehensions are not allowed in policy rules")
	}
}
