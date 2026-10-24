package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/gargi-indic/gargi-flow/pkg/engine"
)

// opError is a patch_model operation that cannot be applied.
type opError struct {
	code    engine.ErrorCode
	message string
	// hint overrides the code's repair hint when set.
	hint string
}

func (e *opError) Error() string { return e.message }

func invalidOp(hint, format string, args ...any) *opError {
	return &opError{code: engine.CodeInvalidValue, message: fmt.Sprintf(format, args...), hint: hint}
}

func unresolved(hint, format string, args ...any) *opError {
	return &opError{code: engine.CodeUnresolvedReference, message: fmt.Sprintf(format, args...), hint: hint}
}

// patchOp is one typed patch_model operation.
type patchOp interface {
	apply(d *modelDocs) *opError
}

// namePattern restricts world, param, latent, node and policy names.
var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// opNames lists the supported operations, for error messages and the tool
// description.
const opNames = "set_world_param, remove_world, set_latent, remove_latent, set_behavior, remove_behavior, " +
	"set_objective, add_node, remove_node, add_edge, remove_edge, set_decision, set_policy, remove_policy"

// newOp returns an empty operation of kind op.
func newOp(op string) patchOp {
	switch op {
	case "set_world_param":
		return &setWorldParamOp{}
	case "remove_world":
		return &removeWorldOp{}
	case "set_latent":
		return &setLatentOp{}
	case "remove_latent":
		return &removeLatentOp{}
	case "set_behavior":
		return &setBehaviorOp{}
	case "remove_behavior":
		return &removeBehaviorOp{}
	case "set_objective":
		return &setObjectiveOp{}
	case "add_node":
		return &addNodeOp{}
	case "remove_node":
		return &removeNodeOp{}
	case "add_edge":
		return &addEdgeOp{}
	case "remove_edge":
		return &removeEdgeOp{}
	case "set_decision":
		return &setDecisionOp{}
	case "set_policy":
		return &setPolicyOp{}
	case "remove_policy":
		return &removePolicyOp{}
	default:
		return nil
	}
}

// parseOp decodes one operation. The op field selects the operation; any
// field the operation does not define is rejected. Numbers decode as int
// when integral and float64 otherwise, as YAML decoding does.
func parseOp(raw json.RawMessage) (patchOp, *opError) {
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, invalidOp("", "operation must be an object: %v", err)
	}
	op := newOp(head.Op)
	if op == nil {
		return nil, invalidOp("op is one of: "+opNames, "unknown op %q", head.Op)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(op); err != nil {
		return nil, invalidOp("", "%s: %v", head.Op, err)
	}
	normalize(op)
	return op, nil
}

// normalize converts the json.Number values of op's free-form fields.
func normalize(op patchOp) {
	switch o := op.(type) {
	case *setWorldParamOp:
		o.Value = normalizeValue(o.Value)
	case *setLatentOp:
		o.Spec = normalizeValue(o.Spec)
	case *setBehaviorOp:
		o.Spec = normalizeValue(o.Spec)
	case *setObjectiveOp:
		o.Spec = normalizeValue(o.Spec)
	case *addNodeOp:
		o.Config = normalizeValue(o.Config)
	case *setPolicyOp:
		o.Document = normalizeValue(o.Document)
	}
}

// normalizeValue replaces json.Number with int or float64, recursively.
func normalizeValue(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case map[string]any:
		for k, e := range t {
			t[k] = normalizeValue(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = normalizeValue(e)
		}
		return t
	default:
		return v
	}
}

func checkName(field, v string) *opError {
	if v == "" {
		return invalidOp("", "%s is required", field)
	}
	if !namePattern.MatchString(v) {
		return invalidOp("", "%s %q must match %s", field, v, namePattern)
	}
	return nil
}

// object returns v as a document object.
func object(field string, v any) (map[string]any, *opError) {
	m, ok := v.(map[string]any)
	if !ok {
		if v == nil {
			return nil, invalidOp("", "%s is required", field)
		}
		return nil, invalidOp("", "%s must be an object", field)
	}
	return m, nil
}

// section returns the object field name of doc, creating it when create is
// set and it is missing.
func section(doc map[string]any, name string, create bool) map[string]any {
	m, ok := doc[name].(map[string]any)
	if !ok && create {
		m = map[string]any{}
		doc[name] = m
	}
	return m
}

// list returns the list field name of doc ([] when missing).
func list(doc map[string]any, name string) []any {
	l, _ := doc[name].([]any)
	return l
}

func toList(s []string) []any {
	l := make([]any, len(s))
	for i, v := range s {
		l[i] = v
	}
	return l
}

// setWorldParamOp sets world parameter param of world (creating the world).
type setWorldParamOp struct {
	Op    string `json:"op"`
	World string `json:"world"`
	Param string `json:"param"`
	Value any    `json:"value"`
}

func (o *setWorldParamOp) apply(d *modelDocs) *opError {
	if err := checkName("world", o.World); err != nil {
		return err
	}
	if err := checkName("param", o.Param); err != nil {
		return err
	}
	if o.Value == nil {
		return invalidOp("", "value is required")
	}
	worlds := section(d.sim, "worlds", true)
	w, ok := worlds[o.World].(map[string]any)
	if !ok {
		w = map[string]any{}
		worlds[o.World] = w
	}
	w[o.Param] = o.Value
	return nil
}

// removeWorldOp removes a world.
type removeWorldOp struct {
	Op    string `json:"op"`
	World string `json:"world"`
}

func (o *removeWorldOp) apply(d *modelDocs) *opError {
	worlds := section(d.sim, "worlds", false)
	if _, ok := worlds[o.World]; !ok {
		return unresolved("", "unknown world %q", o.World)
	}
	delete(worlds, o.World)
	return nil
}

// setLatentOp sets the spec of a latent variable.
type setLatentOp struct {
	Op   string `json:"op"`
	Name string `json:"name"`
	Spec any    `json:"spec"`
}

func (o *setLatentOp) apply(d *modelDocs) *opError {
	if err := checkName("name", o.Name); err != nil {
		return err
	}
	spec, err := object("spec", o.Spec)
	if err != nil {
		return err
	}
	section(d.sim, "latents", true)[o.Name] = spec
	return nil
}

// removeLatentOp removes a latent variable.
type removeLatentOp struct {
	Op   string `json:"op"`
	Name string `json:"name"`
}

func (o *removeLatentOp) apply(d *modelDocs) *opError {
	latents := section(d.sim, "latents", false)
	if _, ok := latents[o.Name]; !ok {
		return unresolved("", "unknown latent %q", o.Name)
	}
	delete(latents, o.Name)
	return nil
}

// setBehaviorOp sets the behavior of a graph node.
type setBehaviorOp struct {
	Op   string `json:"op"`
	Node string `json:"node"`
	Spec any    `json:"spec"`
}

func (o *setBehaviorOp) apply(d *modelDocs) *opError {
	if err := checkName("node", o.Node); err != nil {
		return err
	}
	spec, err := object("spec", o.Spec)
	if err != nil {
		return err
	}
	section(d.sim, "behaviors", true)[o.Node] = spec
	return nil
}

// removeBehaviorOp removes the behavior of a graph node.
type removeBehaviorOp struct {
	Op   string `json:"op"`
	Node string `json:"node"`
}

func (o *removeBehaviorOp) apply(d *modelDocs) *opError {
	behaviors := section(d.sim, "behaviors", false)
	if _, ok := behaviors[o.Node]; !ok {
		return unresolved("", "node %q has no behavior", o.Node)
	}
	delete(behaviors, o.Node)
	return nil
}

// setObjectiveOp sets the objective.
type setObjectiveOp struct {
	Op   string `json:"op"`
	Spec any    `json:"spec"`
}

func (o *setObjectiveOp) apply(d *modelDocs) *opError {
	spec, err := object("spec", o.Spec)
	if err != nil {
		return err
	}
	d.sim["objective"] = spec
	return nil
}

// findNode returns the index of node id in nodes, or -1.
func findNode(nodes []any, id string) int {
	for i, n := range nodes {
		if m, ok := n.(map[string]any); ok && m["id"] == id {
			return i
		}
	}
	return -1
}

// addNodeOp appends a node to the graph.
type addNodeOp struct {
	Op       string `json:"op"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Config   any    `json:"config,omitempty"`
	JoinMode string `json:"joinMode,omitempty"`
}

func (o *addNodeOp) apply(d *modelDocs) *opError {
	if err := checkName("id", o.ID); err != nil {
		return err
	}
	if o.Type == "" {
		return invalidOp("", "type is required")
	}
	nodes := list(d.graph, "nodes")
	if findNode(nodes, o.ID) >= 0 {
		return invalidOp("remove the node first with remove_node, or pick another id", "node %q already exists", o.ID)
	}
	node := map[string]any{"id": o.ID, "type": o.Type}
	if o.Config != nil {
		cfg, err := object("config", o.Config)
		if err != nil {
			return err
		}
		node["config"] = cfg
	}
	if o.JoinMode != "" {
		node["joinMode"] = o.JoinMode
	}
	d.graph["nodes"] = append(nodes, node)
	return nil
}

// removeNodeOp removes a node and every edge touching it.
type removeNodeOp struct {
	Op string `json:"op"`
	ID string `json:"id"`
}

func (o *removeNodeOp) apply(d *modelDocs) *opError {
	nodes := list(d.graph, "nodes")
	i := findNode(nodes, o.ID)
	if i < 0 {
		return unresolved("", "unknown node %q", o.ID)
	}
	d.graph["nodes"] = append(nodes[:i:i], nodes[i+1:]...)
	edges := list(d.graph, "edges")
	kept := make([]any, 0, len(edges))
	for _, e := range edges {
		if m, ok := e.(map[string]any); ok && (m["from"] == o.ID || m["to"] == o.ID) {
			continue
		}
		kept = append(kept, e)
	}
	if _, ok := d.graph["edges"]; ok {
		d.graph["edges"] = kept
	}
	return nil
}

// findEdge returns the index of the edge from -> to in edges, or -1.
func findEdge(edges []any, from, to string) int {
	for i, e := range edges {
		if m, ok := e.(map[string]any); ok && m["from"] == from && m["to"] == to {
			return i
		}
	}
	return -1
}

// addEdgeOp appends an edge between two existing nodes.
type addEdgeOp struct {
	Op        string `json:"op"`
	From      string `json:"from"`
	To        string `json:"to"`
	Condition string `json:"condition,omitempty"`
}

func (o *addEdgeOp) apply(d *modelDocs) *opError {
	nodes := list(d.graph, "nodes")
	for _, id := range []string{o.From, o.To} {
		if findNode(nodes, id) < 0 {
			return unresolved("add the node first with add_node", "unknown node %q", id)
		}
	}
	edges := list(d.graph, "edges")
	if findEdge(edges, o.From, o.To) >= 0 {
		return invalidOp("remove it first with remove_edge", "edge %s -> %s already exists", o.From, o.To)
	}
	edge := map[string]any{"from": o.From, "to": o.To}
	if o.Condition != "" {
		edge["condition"] = o.Condition
	}
	d.graph["edges"] = append(edges, edge)
	return nil
}

// removeEdgeOp removes the edge from -> to.
type removeEdgeOp struct {
	Op   string `json:"op"`
	From string `json:"from"`
	To   string `json:"to"`
}

func (o *removeEdgeOp) apply(d *modelDocs) *opError {
	edges := list(d.graph, "edges")
	i := findEdge(edges, o.From, o.To)
	if i < 0 {
		return unresolved("", "no edge %s -> %s", o.From, o.To)
	}
	d.graph["edges"] = append(edges[:i:i], edges[i+1:]...)
	return nil
}

// setDecisionOp sets the options and observes of a decision node.
type setDecisionOp struct {
	Op       string   `json:"op"`
	Node     string   `json:"node"`
	Options  []string `json:"options"`
	Observes []string `json:"observes"`
}

func (o *setDecisionOp) apply(d *modelDocs) *opError {
	nodes := list(d.graph, "nodes")
	i := findNode(nodes, o.Node)
	if i < 0 {
		return unresolved("add the decision first with add_node (type: decision)", "unknown node %q", o.Node)
	}
	node := nodes[i].(map[string]any)
	if node["type"] != "decision" {
		return invalidOp("set_decision applies to nodes of type decision", "node %q has type %v, not decision", o.Node, node["type"])
	}
	if len(o.Options) == 0 {
		return invalidOp("", "options must not be empty")
	}
	cfg := section(node, "config", true)
	cfg["options"] = toList(o.Options)
	if o.Observes == nil {
		o.Observes = []string{}
	}
	cfg["observes"] = toList(o.Observes)
	return nil
}

// setPolicyOp sets a policy/v1 document.
type setPolicyOp struct {
	Op       string `json:"op"`
	Name     string `json:"name"`
	Document any    `json:"document"`
}

// policyName returns name without a ".yaml" suffix, checked.
func policyName(name string) (string, *opError) {
	name = strings.TrimSuffix(name, ".yaml")
	if name == "" || !namePattern.MatchString(name) {
		return "", invalidOp("", "policy name %q must match %s (an optional .yaml suffix is dropped)", name, namePattern)
	}
	return name, nil
}

func (o *setPolicyOp) apply(d *modelDocs) *opError {
	name, err := policyName(o.Name)
	if err != nil {
		return err
	}
	doc, err := object("document", o.Document)
	if err != nil {
		return err
	}
	d.policies[name] = doc
	return nil
}

// removePolicyOp removes a policy document.
type removePolicyOp struct {
	Op   string `json:"op"`
	Name string `json:"name"`
}

func (o *removePolicyOp) apply(d *modelDocs) *opError {
	name := strings.TrimSuffix(o.Name, ".yaml")
	if _, ok := d.policies[name]; !ok {
		return unresolved("", "unknown policy %q", o.Name)
	}
	delete(d.policies, name)
	return nil
}

// applyOps decodes and applies ops to d in order. On failure it returns the
// index of the failing operation and its error; d is then partially
// patched, so callers apply ops to a copy.
func applyOps(d *modelDocs, ops []json.RawMessage) (int, *opError) {
	for i, raw := range ops {
		op, err := parseOp(raw)
		if err != nil {
			return i, err
		}
		if err := op.apply(d); err != nil {
			return i, err
		}
	}
	return -1, nil
}
