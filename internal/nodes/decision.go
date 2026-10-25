package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/google/cel-go/cel"
	"gopkg.in/yaml.v3"
)

const defaultConditionCostLimit uint64 = 1000

type policyDocument struct {
	APIVersion string                     `json:"apiVersion" yaml:"apiVersion"`
	ID         string                     `json:"id" yaml:"id"`
	Version    string                     `json:"version" yaml:"version"`
	Params     map[string]policyParamSpec `json:"params,omitempty" yaml:"params,omitempty"`
	Decisions  map[string]policyDecision  `json:"decisions" yaml:"decisions"`
}

type policyParamSpec struct {
	Min     *float64 `json:"min,omitempty" yaml:"min,omitempty"`
	Max     *float64 `json:"max,omitempty" yaml:"max,omitempty"`
	Step    *float64 `json:"step,omitempty" yaml:"step,omitempty"`
	Values  []any    `json:"values,omitempty" yaml:"values,omitempty"`
	Default any      `json:"default,omitempty" yaml:"default,omitempty"`
}

type policyDecision struct {
	Rules   []policyRule     `json:"rules,omitempty" yaml:"rules,omitempty"`
	Table   []policyTableRow `json:"table,omitempty" yaml:"table,omitempty"`
	Default string           `json:"default,omitempty" yaml:"default,omitempty"`
}

type policyRule struct {
	When   string `json:"when,omitempty" yaml:"when,omitempty"`
	Choose string `json:"choose,omitempty" yaml:"choose,omitempty"`
	Else   string `json:"else,omitempty" yaml:"else,omitempty"`
}

type policyTableRow struct {
	Observed map[string]any `json:"observed" yaml:"observed"`
	Choose   string         `json:"choose" yaml:"choose"`
}

func parsePolicyDocument(doc []byte, format engine.Format) (*policyDocument, error) {
	var p policyDocument
	if format == engine.FormatJSON || (len(doc) > 0 && doc[0] == '{') {
		if err := json.Unmarshal(doc, &p); err != nil {
			return nil, fmt.Errorf("failed to parse JSON policy document: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(doc, &p); err != nil {
			return nil, fmt.Errorf("failed to parse YAML policy document: %w", err)
		}
	}
	return &p, nil
}

type nodeContextKeyType struct{}

var nodeCtxKey = nodeContextKeyType{}

// WithNodeContext returns a new context carrying the given NodeContext.
func WithNodeContext(ctx context.Context, nc engine.NodeContext) context.Context {
	return context.WithValue(ctx, nodeCtxKey, nc)
}

// NodeContextFromContext extracts the NodeContext from ctx, if present.
func NodeContextFromContext(ctx context.Context) (engine.NodeContext, bool) {
	nc, ok := ctx.Value(nodeCtxKey).(engine.NodeContext)
	return nc, ok
}

// DecisionChooser represents a strategy for making decisions.
type DecisionChooser interface {
	Choose(ctx context.Context, decision engine.NodeID, obs map[string]any, options []string) (string, error)
}

type decisionConfig struct {
	Options  []string `json:"options"`
	Observes []string `json:"observes"`
	Policy   string   `json:"policy,omitempty"`
}

func (c *decisionConfig) Validate() error {
	if len(c.Options) == 0 {
		return errors.New("at least one option is required")
	}

	seenOptions := make(map[string]struct{})
	for _, opt := range c.Options {
		if _, exists := seenOptions[opt]; exists {
			return fmt.Errorf("duplicate option: %s", opt)
		}
		seenOptions[opt] = struct{}{}
	}

	for _, obs := range c.Observes {
		parts := strings.Split(obs, ".")
		if len(parts) < 2 {
			return fmt.Errorf("invalid observe path %q: must be <nodeId>.output.<field> or <nodeId>.status", obs)
		}

		switch parts[1] {
		case "status":
			if len(parts) != 2 {
				return fmt.Errorf("invalid observe path %q: status paths must be exactly <nodeId>.status", obs)
			}
		case "output":
			if len(parts) < 3 {
				return fmt.Errorf("invalid observe path %q: output paths must be <nodeId>.output.<field>", obs)
			}
		default:
			return fmt.Errorf("invalid observe path %q: must be <nodeId>.output.<field> or <nodeId>.status", obs)
		}
	}

	return nil
}

type decisionNode struct {
	*engine.BaseNode
	cfg     decisionConfig
	chooser DecisionChooser
}

var _ engine.NodeInterface = (*decisionNode)(nil)

// ShadowResult captures the result of evaluating a shadow policy.
type ShadowResult struct {
	PolicyID      string
	PolicyVersion string
	Choice        string
}

// ShadowDecisionChooser is implemented by choosers that evaluate shadow policies alongside the primary choice.
type ShadowDecisionChooser interface {
	DecisionChooser
	ChooseWithShadow(ctx context.Context, decision engine.NodeID, obs map[string]any, options []string) (string, *ShadowResult, error)
}

func (n *decisionNode) Execute(ctx context.Context, nc engine.NodeContext) (*engine.StateMutation, error) {
	ctx = WithNodeContext(ctx, nc)
	state := nc.State()
	obs := make(map[string]any)

	for _, path := range n.cfg.Observes {
		parts := strings.Split(path, ".")
		nodeID := engine.NodeID(parts[0])
		nodeState := state.NodeState(nodeID)

		if nodeState == nil {
			obs[path] = nil
			continue
		}

		switch parts[1] {
		case "status":
			obs[path] = string(nodeState.Status())
		case "output":
			if nodeState.Status() == engine.StatusSucceeded {
				outputs := nodeState.Output()
				fieldPath := strings.Join(parts[2:], ".")
				if val, exists := outputs[fieldPath]; exists {
					obs[path] = val
				} else {
					obs[path] = nil
				}
			} else {
				obs[path] = nil
			}
		}
	}

	var choice string
	var shadow *ShadowResult
	if sc, ok := n.chooser.(ShadowDecisionChooser); ok {
		c, sh, err := sc.ChooseWithShadow(ctx, nc.NodeID(), obs, n.cfg.Options)
		if err != nil {
			return nil, engine.NewNonRetryableError(fmt.Errorf("chooser failed: %w", err))
		}
		choice = c
		shadow = sh
	} else {
		c, err := n.chooser.Choose(ctx, nc.NodeID(), obs, n.cfg.Options)
		if err != nil {
			return nil, engine.NewNonRetryableError(fmt.Errorf("chooser failed: %w", err))
		}
		choice = c
	}

	isValidChoice := false
	for _, opt := range n.cfg.Options {
		if opt == choice {
			isValidChoice = true
			break
		}
	}

	if !isValidChoice {
		return nil, engine.NewNonRetryableError(fmt.Errorf("chooser returned invalid option %q", choice))
	}

	outputs := map[string]any{
		"choice": choice,
	}
	if pr, ok := nc.(interface {
		PolicyID() string
		PolicyVersion() string
	}); ok {
		if pid := pr.PolicyID(); pid != "" {
			outputs["policy_id"] = pid
		}
		if pver := pr.PolicyVersion(); pver != "" {
			outputs["policy_version"] = pver
		}
	}
	if shadow != nil {
		outputs["shadow_choice"] = shadow.Choice
		outputs["shadow_policy_id"] = shadow.PolicyID
		outputs["shadow_policy_version"] = shadow.PolicyVersion
	}

	mut := engine.NewStateMutation(nc.NodeID(), 0, nil, outputs, nil, nil)

	return &mut, nil
}

// RegisterDecision registers the decision node type.
func RegisterDecision(reg *engine.Registry, chooser DecisionChooser) error {
	if reg == nil {
		return errors.New("registry cannot be nil")
	}
	if chooser == nil {
		return errors.New("chooser cannot be nil")
	}

	return reg.Register("decision", func(config any) (engine.NodeInterface, error) {
		return &decisionNode{
			BaseNode: &engine.BaseNode{},
			cfg:      *config.(*decisionConfig),
			chooser:  chooser,
		}, nil
	}, &decisionConfig{})
}

// PolicyDecisionChooser evaluates decisions backed by an engine.PolicyStore.
type PolicyDecisionChooser struct {
	store engine.PolicyStore
}

// NewPolicyDecisionChooser creates a new PolicyDecisionChooser.
func NewPolicyDecisionChooser(store engine.PolicyStore) *PolicyDecisionChooser {
	return &PolicyDecisionChooser{store: store}
}

// Choose implements DecisionChooser backed by PolicyStore.
func (c *PolicyDecisionChooser) Choose(ctx context.Context, decision engine.NodeID, obs map[string]any, options []string) (string, error) {
	choice, _, err := c.ChooseWithShadow(ctx, decision, obs, options)
	return choice, err
}

// ChooseWithShadow implements ShadowDecisionChooser backed by PolicyStore.
func (c *PolicyDecisionChooser) ChooseWithShadow(ctx context.Context, decision engine.NodeID, obs map[string]any, options []string) (string, *ShadowResult, error) {
	if c.store == nil {
		return "", nil, errors.New("policy store is not configured")
	}

	nc, ok := NodeContextFromContext(ctx)
	if !ok || nc == nil {
		return "", nil, errors.New("node context is required for policy decision chooser")
	}

	var graphID string
	if st := nc.State(); st != nil {
		graphID = st.GraphID()
	}

	cfg := nc.Config()
	policyID := nc.PolicyID()
	if policyID == "" && cfg != nil {
		if p, ok := cfg["policy"].(string); ok && p != "" {
			policyID = p
		}
	}

	version := nc.PolicyVersion()

	// If version is empty, check if pinned policies map contains the policy ID
	if version == "" && cfg != nil {
		if pinned, ok := cfg["pinnedPolicies"].(map[string]string); ok && policyID != "" {
			version = pinned[policyID]
		}
	}

	var dep *engine.PolicyDeployment
	if graphID != "" {
		d, err := c.store.GetDeployment(ctx, graphID)
		if err == nil {
			dep = d
		} else if !errors.Is(err, engine.ErrDeploymentNotFound) {
			return "", nil, fmt.Errorf("failed to resolve policy deployment for graph %q: %w", graphID, err)
		}
	}

	// Resolve primary policy and version
	if policyID == "" || version == "" {
		if dep != nil && dep.Mode == engine.DeploymentLive {
			if policyID != "" && dep.PolicyID != policyID {
				return "", nil, fmt.Errorf("deployed policy %q does not match configured policy %q", dep.PolicyID, policyID)
			}
			if policyID == "" {
				policyID = dep.PolicyID
			}
			if version == "" {
				version = dep.PolicyVersion
			}
		} else {
			// No live deployment: if dep is shadow or nil, primary proceeds with live/unpinned policy choice
			if policyID == "" {
				return "", nil, fmt.Errorf("no live deployment found for graph %q", graphID)
			}
			if version == "" {
				rec, err := c.store.GetPolicy(ctx, graphID, policyID, "")
				if err != nil {
					return "", nil, fmt.Errorf("failed to retrieve unpinned policy %s: %w", policyID, err)
				}
				version = rec.Version
			}
		}
	}

	if policyID == "" {
		return "", nil, errors.New("policy ID could not be resolved")
	}
	if version == "" {
		return "", nil, fmt.Errorf("policy version for %q could not be resolved", policyID)
	}

	// Update recorder if supported
	type policyRecorder interface {
		SetResolvedPolicy(policyID, version string)
	}
	if rec, ok := nc.(policyRecorder); ok {
		rec.SetResolvedPolicy(policyID, version)
	}

	primaryChoice, err := c.evaluatePolicy(ctx, graphID, policyID, version, decision, obs, options)
	if err != nil {
		return "", nil, err
	}

	var shadow *ShadowResult
	if dep != nil && dep.Mode == engine.DeploymentShadow {
		shadowChoice, err := c.evaluatePolicy(ctx, graphID, dep.PolicyID, dep.PolicyVersion, decision, obs, options)
		if err != nil {
			return "", nil, fmt.Errorf("shadow policy evaluation failed: %w", err)
		}
		shadow = &ShadowResult{
			PolicyID:      dep.PolicyID,
			PolicyVersion: dep.PolicyVersion,
			Choice:        shadowChoice,
		}

		type shadowPolicyRecorder interface {
			SetShadowPolicy(policyID, version, choice string)
		}
		if srec, ok := nc.(shadowPolicyRecorder); ok {
			srec.SetShadowPolicy(shadow.PolicyID, shadow.PolicyVersion, shadow.Choice)
		}
	}

	return primaryChoice, shadow, nil
}

func (c *PolicyDecisionChooser) evaluatePolicy(ctx context.Context, graphID, policyID, version string, decision engine.NodeID, obs map[string]any, options []string) (string, error) {
	rec, err := c.store.GetPolicy(ctx, graphID, policyID, version)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve policy %s@%s: %w", policyID, version, err)
	}

	format := engine.FormatYAML
	if rec.Format == "json" || rec.Format == string(engine.FormatJSON) || (len(rec.Document) > 0 && rec.Document[0] == '{') {
		format = engine.FormatJSON
	}

	pol, err := parsePolicyDocument(rec.Document, format)
	if err != nil {
		return "", fmt.Errorf("failed to parse policy document: %w", err)
	}

	decisionName := string(decision)
	d, exists := pol.Decisions[decisionName]
	if !exists && len(pol.Decisions) == 1 {
		for _, dec := range pol.Decisions {
			d = dec
			exists = true
		}
	}
	if !exists {
		return "", fmt.Errorf("no decision definition for %q in policy %q", decisionName, policyID)
	}

	return evaluateDecision(ctx, d, pol, obs, options)
}

func evaluateDecision(ctx context.Context, d policyDecision, pol *policyDocument, obs map[string]any, _ []string) (string, error) {
	if len(d.Table) > 0 {
		for _, row := range d.Table {
			if rowMatches(row, obs) {
				return row.Choose, nil
			}
		}
		if d.Default != "" {
			return d.Default, nil
		}
		return "", errors.New("no matching table row and no default configured")
	}

	if len(d.Rules) > 0 {
		activation := buildActivation(obs)
		if len(pol.Params) > 0 {
			paramsMap := make(map[string]any)
			for pName, pSpec := range pol.Params {
				if pSpec.Default != nil {
					paramsMap[pName] = pSpec.Default
				} else if pSpec.Min != nil {
					paramsMap[pName] = *pSpec.Min
				} else if len(pSpec.Values) > 0 {
					paramsMap[pName] = pSpec.Values[0]
				}
			}
			activation["params"] = paramsMap
		}

		if _, ok := activation["upstream"]; !ok {
			activation["upstream"] = map[string]any{}
		}
		if _, ok := activation["nodes"]; !ok {
			activation["nodes"] = map[string]any{}
		}
		if _, ok := activation["state"]; !ok {
			activation["state"] = map[string]any{}
		}

		var opts []cel.EnvOption
		for k := range activation {
			opts = append(opts, cel.Variable(k, cel.MapType(cel.StringType, cel.DynType)))
		}
		env, err := cel.NewEnv(opts...)
		if err != nil {
			return "", fmt.Errorf("failed to create CEL env: %w", err)
		}

		for i, rule := range d.Rules {
			if rule.When == "" {
				if rule.Else != "" {
					return rule.Else, nil
				}
				continue
			}

			ast, issues := env.Compile(rule.When)
			if issues != nil && issues.Err() != nil {
				return "", fmt.Errorf("rule %d failed to compile when %q: %w", i, rule.When, issues.Err())
			}

			prg, err := env.Program(ast, cel.CostLimit(defaultConditionCostLimit))
			if err != nil {
				return "", fmt.Errorf("rule %d failed to create program: %w", i, err)
			}

			out, _, err := prg.ContextEval(ctx, activation)
			if err != nil {
				return "", fmt.Errorf("rule %d evaluation failed: %w", i, err)
			}

			val, ok := out.Value().(bool)
			if !ok {
				return "", fmt.Errorf("rule %d when evaluated to %T, want bool", i, out.Value())
			}

			if val {
				return rule.Choose, nil
			}
		}

		return "", errors.New("no matching rule in policy")
	}

	return "", errors.New("decision has neither table nor rules configured")
}

func buildActivation(obs map[string]any) map[string]any {
	info := make(map[string]any)
	for k, v := range obs {
		parts := strings.Split(k, ".")
		current := info
		for i, part := range parts {
			if i == len(parts)-1 {
				current[part] = v
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

func rowMatches(row policyTableRow, obs map[string]any) bool {
	for k, want := range row.Observed {
		obsVal, exists := obs[k]
		if !exists {
			return false
		}
		if !reflect.DeepEqual(normalizeNumber(obsVal), normalizeNumber(want)) {
			return false
		}
	}
	return true
}

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
	default:
		return v
	}
}
