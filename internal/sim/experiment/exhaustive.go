package experiment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/gargi-indic/gargi-flow/internal/sim/kernel"
	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/sampler"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"gopkg.in/yaml.v3"
)

// DefaultMaxPolicies is the policy cap of an exhaustive search when the
// experiment's max_policies is unset.
const DefaultMaxPolicies = 10_000

// ExhaustiveSource is a PolicySource (policies: {search: exhaustive}) that
// enumerates every reduced deterministic policy of a discrete model: every
// assignment of an option to each reachable information set, where two
// policies that differ only on information sets they never reach count once.
//
// An information set is a decision node together with the values of its
// observes paths, as the decision node observes them (a path of a node that
// did not run observes nil). Reachability is determined by running the
// kernel over every chance branch (sampler.NewEnumerator) of every world,
// under the partial policy built so far: the search assigns each option in
// turn to the first unassigned information set an episode reaches and
// recurses, so every assigned information set is reachable with positive
// probability under the policy and every reduced policy is found exactly
// once. Policies are returned in that depth-first order, which is
// deterministic.
type ExhaustiveSource struct {
	// Worlds are the worlds whose reachable information sets the policies
	// cover. Empty means every world of the model.
	Worlds []sim.World
	// MaxPolicies caps the number of policies; more is an error. Zero
	// selects DefaultMaxPolicies.
	MaxPolicies int
}

var _ PolicySource = ExhaustiveSource{}

// ExhaustiveSource returns an ExhaustiveSource over the experiment's worlds
// with the experiment's max_policies.
func (e *CompiledExperiment) ExhaustiveSource() ExhaustiveSource {
	return ExhaustiveSource{Worlds: e.Worlds, MaxPolicies: e.Def.MaxPolicies}
}

// RacingSource returns the experiment's RacingSource wrapping its candidate
// policy source.
func (e *CompiledExperiment) RacingSource() RacingSource {
	return RacingSource{
		Candidates: e.CandidateSource(),
		MinRuns:    e.Def.Policies.MinRuns,
		MaxRuns:    e.Def.Policies.MaxRuns,
		Factor:     e.Def.Policies.Factor,
		Alpha:      e.Def.Policies.Alpha,
	}
}

// CandidateSource returns the underlying PolicySource for racing candidates.
func (e *CompiledExperiment) CandidateSource() PolicySource {
	p := e.Def.Policies
	if p.Candidates != nil {
		c := p.Candidates
		switch c.Search {
		case "exhaustive":
			return ExhaustiveSource{Worlds: e.Worlds, MaxPolicies: e.Def.MaxPolicies}
		case "grid":
			return GridSource{Policy: e.Policy, MaxPolicies: e.Def.MaxPolicies}
		case "random":
			seed := e.Def.Seed
			if c.Seed != nil {
				seed = *c.Seed
			}
			return RandomSource{Policy: e.Policy, Samples: c.Samples, Seed: seed}
		default:
			if len(c.Files) > 0 {
				return FilesSource{BaseDir: e.BaseDir, FS: e.FS, Files: c.Files}
			}
			return ExhaustiveSource{Worlds: e.Worlds, MaxPolicies: e.Def.MaxPolicies}
		}
	}
	if p.Policy != "" {
		if p.Samples > 0 {
			seed := e.Def.Seed
			if p.Seed != nil {
				seed = *p.Seed
			}
			return RandomSource{Policy: e.Policy, Samples: p.Samples, Seed: seed}
		}
		return GridSource{Policy: e.Policy, MaxPolicies: e.Def.MaxPolicies}
	}
	if len(p.Files) > 0 {
		return FilesSource{BaseDir: e.BaseDir, FS: e.FS, Files: p.Files}
	}
	return ExhaustiveSource{Worlds: e.Worlds, MaxPolicies: e.Def.MaxPolicies}
}

// BayesianSource returns the experiment's BayesianSource wrapping its policy template.
func (e *CompiledExperiment) BayesianSource() BayesianSource {
	p := e.Def.Policies
	trials := p.Trials
	if trials <= 0 {
		trials = 20
	}
	startupTrials := p.StartupTrials
	if startupTrials <= 0 {
		startupTrials = 5
	}
	gamma := p.Gamma
	if gamma <= 0 {
		gamma = 0.20
	}
	seed := e.Def.Seed
	if p.Seed != nil {
		seed = *p.Seed
	}
	return BayesianSource{
		Policy:        e.Policy,
		Trials:        trials,
		StartupTrials: startupTrials,
		Gamma:         gamma,
		Racing:        p.Racing,
		Seed:          seed,
	}
}

// PolicySource returns the experiment's policy source: an ExhaustiveSource
// for policies.search: exhaustive, an ExactSource for policies.search:
// exact, a GridSource for grid, a RandomSource for random, a RacingSource
// for racing, a BayesianSource for bayesian, otherwise a FilesSource.
func (e *CompiledExperiment) PolicySource() PolicySource {
	switch e.Def.Policies.Search {
	case "exhaustive":
		return e.ExhaustiveSource()
	case "exact":
		return e.ExactSource()
	case "grid":
		return e.GridSource()
	case "random":
		return e.RandomSource()
	case "racing":
		return e.RacingSource()
	case "bayesian":
		return e.BayesianSource()
	default:
		return e.FilesSource()
	}
}

// Policies enumerates the reduced deterministic policies of m. A model that
// is not discrete is rejected with *engine.ValidationErrors listing every
// reason (sim.CodeModelNotDiscrete); more than MaxPolicies policies is a
// *engine.ValidationErrors with sim.CodeTooManyPolicies. Every policy is a
// *TablePolicy.
func (s ExhaustiveSource) Policies(m *model.CompiledModel) ([]sim.Policy, error) {
	if m == nil || m.Graph == nil {
		return nil, errors.New("experiment: compiled model is nil")
	}
	if err := requireDiscrete(m, "exhaustive policy search"); err != nil {
		return nil, err
	}
	worlds := s.Worlds
	if len(worlds) == 0 {
		worlds = modelWorlds(m)
	}
	limit := s.MaxPolicies
	if limit <= 0 {
		limit = DefaultMaxPolicies
	}

	sp := newSearchSpace(m)
	st := &searchState{
		space:    sp,
		worlds:   worlds,
		limit:    limit,
		assigned: map[string]string{},
		sets:     map[string]infoSet{},
	}
	if err := st.explore(context.Background()); err != nil {
		return nil, err
	}
	return st.policies, nil
}

// modelWorlds returns every world of m, sorted by id.
func modelWorlds(m *model.CompiledModel) []sim.World {
	worlds := make([]sim.World, 0, len(m.Def.Worlds))
	for _, id := range sortedKeys(m.Def.Worlds) {
		worlds = append(worlds, sim.World{ID: sim.WorldID(id), Params: m.Def.Worlds[id]})
	}
	return worlds
}

// decisionInfo describes one decision node of the model graph.
type decisionInfo struct {
	id string
	// node is the index of the decision in the model graph's nodes.
	node int
	// observes lists the observed paths in graph order.
	observes []string
	// options lists the options in graph order.
	options []string
}

// searchSpace holds the decision nodes of a model in graph order.
type searchSpace struct {
	m         *model.CompiledModel
	decisions []decisionInfo
	index     map[string]int
}

func newSearchSpace(m *model.CompiledModel) *searchSpace {
	sp := &searchSpace{m: m, index: map[string]int{}}
	for i, n := range m.Graph.Nodes {
		if n.Type != "decision" {
			continue
		}
		d := decisionInfo{id: string(n.ID), node: i}
		obs, _ := n.Config["observes"].([]any)
		for _, o := range obs {
			if s, ok := o.(string); ok {
				d.observes = append(d.observes, s)
			}
		}
		opts, _ := n.Config["options"].([]any)
		for _, o := range opts {
			switch v := o.(type) {
			case string:
				d.options = append(d.options, v)
			case map[string]any:
				if s, ok := v["value"].(string); ok {
					d.options = append(d.options, s)
				}
			}
		}
		sp.index[d.id] = len(sp.decisions)
		sp.decisions = append(sp.decisions, d)
	}
	return sp
}

// infoSet is one information set: a decision and the values it observes.
type infoSet struct {
	decision string
	// values holds the observed values in the decision's observes order,
	// with numbers normalized to float64; nil marks a node that did not run.
	values []any
	// key identifies the information set.
	key string
	// label renders values for Describe, e.g. "[score,low]".
	label string
	// options are the options offered by the decision node.
	options []string
}

// infoSetOf builds the information set of decision for obs.
func (sp *searchSpace) infoSetOf(decision string, obs sim.Observation, options []string) (infoSet, error) {
	i, ok := sp.index[decision]
	if !ok {
		return infoSet{}, fmt.Errorf("experiment: unknown decision node %q", decision)
	}
	d := sp.decisions[i]
	is := infoSet{decision: decision, values: make([]any, len(d.observes)), options: append([]string(nil), options...)}
	labels := make([]string, len(d.observes))
	for j, path := range d.observes {
		v, err := normalizeObserved(obs[path])
		if err != nil {
			return infoSet{}, fmt.Errorf("experiment: decision %q observes %s: %w", decision, path, err)
		}
		is.values[j] = v
		labels[j] = renderValue(v)
	}
	key, err := json.Marshal(is.values)
	if err != nil {
		return infoSet{}, fmt.Errorf("experiment: decision %q: %w", decision, err)
	}
	is.key = decision + "\x00" + string(key)
	if len(d.observes) > 0 {
		is.label = "[" + strings.Join(labels, ",") + "]"
	}
	return is, nil
}

// normalizeObserved maps an observed value onto the values a policy table
// can express: nil, string, bool or float64.
func normalizeObserved(v any) (any, error) {
	switch n := v.(type) {
	case nil, string, bool, float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int8:
		return float64(n), nil
	case int16:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case uint:
		return float64(n), nil
	case uint8:
		return float64(n), nil
	case uint16:
		return float64(n), nil
	case uint32:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	default:
		return nil, fmt.Errorf("value of type %T cannot be used in a policy table", v)
	}
}

// renderValue formats an observed value for Describe. Strings that could be
// confused with the surrounding syntax, with nil or with another type are
// quoted.
func renderValue(v any) string {
	switch n := v.(type) {
	case nil:
		return "nil"
	case bool:
		return strconv.FormatBool(n)
	case float64:
		return strconv.FormatFloat(n, 'g', -1, 64)
	case string:
		if n == "" || n == "nil" || n == "true" || n == "false" || strings.ContainsAny(n, ",;[]=\" ") {
			return strconv.Quote(n)
		}
		if _, err := strconv.ParseFloat(n, 64); err == nil {
			return strconv.Quote(n)
		}
		return n
	default:
		return fmt.Sprint(n)
	}
}

// errUnassigned is returned by probePolicy for an information set the
// partial policy does not cover yet.
var errUnassigned = errors.New("information set not assigned")

// probePolicy follows a partial policy and records the first information
// set it does not cover.
type probePolicy struct {
	space    *searchSpace
	assigned map[string]string
	missing  *infoSet
	err      error
}

func (*probePolicy) ID() string       { return "exhaustive-probe" }
func (*probePolicy) Describe() string { return "exhaustive search probe" }

func (p *probePolicy) Choose(_ context.Context, decision engine.NodeID, obs sim.Observation, options []string) (string, error) {
	is, err := p.space.infoSetOf(string(decision), obs, options)
	if err != nil {
		if p.err == nil {
			p.err = err
		}
		return "", err
	}
	if c, ok := p.assigned[is.key]; ok {
		return c, nil
	}
	if p.missing == nil {
		p.missing = &is
	}
	return "", errUnassigned
}

// searchState is the mutable state of one depth-first policy search.
type searchState struct {
	space    *searchSpace
	worlds   []sim.World
	limit    int
	assigned map[string]string
	sets     map[string]infoSet
	policies []sim.Policy
}

// probe returns the first information set an episode reaches that the
// partial policy does not cover, or nil when the policy is complete.
func (st *searchState) probe(ctx context.Context) (*infoSet, error) {
	for _, w := range st.worlds {
		enum := sampler.NewEnumerator()
		for enum.Next() {
			pp := &probePolicy{space: st.space, assigned: st.assigned}
			_, err := kernel.RunEpisode(ctx, st.space.m, w, pp, enum.Sampler(), kernel.Options{})
			if pp.err != nil {
				return nil, pp.err
			}
			// An unassigned decision fails its node, so the rest of the
			// episode (for example an objective that reads that decision's
			// output) may fail too; the missing information set is what
			// counts.
			if pp.missing != nil {
				return pp.missing, nil
			}
			if err != nil {
				return nil, fmt.Errorf("experiment: exhaustive search: world %q: %w", w.ID, err)
			}
		}
	}
	return nil, nil
}

// explore assigns every option to the first uncovered information set and
// recurses; a complete partial policy becomes a TablePolicy.
func (st *searchState) explore(ctx context.Context) error {
	missing, err := st.probe(ctx)
	if err != nil {
		return err
	}
	if missing == nil {
		if len(st.policies) >= st.limit {
			errs := &engine.ValidationErrors{}
			errs.Add(engine.PathRoot.Field("policies").Field("search").String(), sim.CodeTooManyPolicies,
				fmt.Sprintf("exhaustive search found more than %d policies (max_policies %d); use the backward-induction exact solver (policies: {search: exact}) for policy spaces this large, or raise max_policies",
					st.limit, st.limit))
			return errs
		}
		p, err := st.space.tablePolicy(st.assigned, st.sets)
		if err != nil {
			return err
		}
		st.policies = append(st.policies, p)
		return nil
	}
	if len(missing.options) == 0 {
		return fmt.Errorf("experiment: decision %q offers no options", missing.decision)
	}
	st.sets[missing.key] = *missing
	for _, opt := range missing.options {
		st.assigned[missing.key] = opt
		if err := st.explore(ctx); err != nil {
			return err
		}
	}
	delete(st.assigned, missing.key)
	delete(st.sets, missing.key)
	return nil
}

// TablePolicy is a deterministic policy enumerated by ExhaustiveSource or
// solved by ExactSource. It
// is compiled with model.CompilePolicy from a policy/v1 document of table
// decisions (EmitPolicy writes that document), so it behaves exactly like
// the same document loaded from a file.
type TablePolicy struct {
	def         *model.Policy
	policy      sim.Policy
	description string
}

var _ sim.Policy = (*TablePolicy)(nil)

// ID returns "<model id>@exhaustive-<hash>", where hash is derived from
// Describe, so equal assignments always get the same id, whichever search
// (exhaustive or exact) found them.
func (p *TablePolicy) ID() string { return p.policy.ID() }

// Describe lists the option of every reachable information set, decisions
// in graph order and information sets in label order, for example
// "screen=score; route[score,high]=reject; route[score,low]=approve". A
// label lists the observed values in observes order; nil marks a node that
// did not run.
func (p *TablePolicy) Describe() string { return p.description }

// Choose returns the option of the information set obs belongs to.
func (p *TablePolicy) Choose(ctx context.Context, decision engine.NodeID, obs sim.Observation, options []string) (string, error) {
	return p.policy.Choose(ctx, decision, obs, options)
}

// tablePolicy builds and compiles the policy/v1 document of a complete
// assignment.
//
// Each decision becomes a table with one row per assigned information set.
// A row lists the information set's non-nil observed values (policy/v1 table
// rows cannot hold null); rows are ordered by descending number of keys, so
// the first row matching a reachable observation is that observation's own
// row: a row with more keys than the observation's non-nil values cannot
// match it, and a row with as many keys matches only if it is the same. The
// information set whose observed values are all nil (or that has no observed
// paths) uses the default; otherwise the default is the first option and
// only applies to unreachable information sets. A decision without rows is
// written as a single else rule.
func (sp *searchSpace) tablePolicy(assigned map[string]string, sets map[string]infoSet) (*TablePolicy, error) {
	byDecision := make([][]infoSet, len(sp.decisions))
	for key, is := range sets {
		if _, ok := assigned[key]; ok {
			i := sp.index[is.decision]
			byDecision[i] = append(byDecision[i], is)
		}
	}

	def := &model.Policy{
		APIVersion: "policy/v1",
		ID:         sp.m.Def.ID,
		Decisions:  make(map[string]model.Decision, len(sp.decisions)),
	}
	var parts []string
	for i, d := range sp.decisions {
		list := byDecision[i]
		sort.Slice(list, func(a, b int) bool { return list[a].label < list[b].label })

		dflt := ""
		if len(d.options) > 0 {
			dflt = d.options[0]
		}
		type row struct {
			label string
			row   model.TableRow
		}
		var rows []row
		for _, is := range list {
			choice := assigned[is.key]
			parts = append(parts, fmt.Sprintf("%s%s=%s", d.id, is.label, choice))
			observed := map[string]any{}
			for j, path := range d.observes {
				if is.values[j] != nil {
					observed[path] = is.values[j]
				}
			}
			if len(observed) == 0 {
				dflt = choice
				continue
			}
			rows = append(rows, row{label: is.label, row: model.TableRow{Observed: observed, Choose: choice}})
		}
		sort.SliceStable(rows, func(a, b int) bool {
			return len(rows[a].row.Observed) > len(rows[b].row.Observed)
		})
		if len(rows) == 0 {
			def.Decisions[d.id] = model.Decision{Rules: []model.Rule{{Else: dflt}}}
			continue
		}
		dec := model.Decision{Default: dflt}
		for _, r := range rows {
			dec.Table = append(dec.Table, r.row)
		}
		def.Decisions[d.id] = dec
	}
	description := strings.Join(parts, "; ")
	sum := sha256.Sum256([]byte(description))
	def.Version = "exhaustive-" + hex.EncodeToString(sum[:8])

	compiled, errs := model.CompilePolicy(def, sp.m)
	if errs.Len() > 0 {
		return nil, fmt.Errorf("experiment: compile enumerated policy %q: %w", description, errs)
	}
	return &TablePolicy{def: def, policy: compiled, description: description}, nil
}

// emitDocument is the policy/v1 layout written by EmitPolicy.
type emitDocument struct {
	APIVersion string                  `yaml:"apiVersion"`
	ID         string                  `yaml:"id"`
	Version    string                  `yaml:"version"`
	Decisions  map[string]emitDecision `yaml:"decisions"`
}

type emitDecision struct {
	Rules   []emitRule     `yaml:"rules,omitempty"`
	Table   []emitTableRow `yaml:"table,omitempty"`
	Default string         `yaml:"default,omitempty"`
}

type emitRule struct {
	Else string `yaml:"else"`
}

type emitTableRow struct {
	Observed map[string]any `yaml:"observed"`
	Choose   string         `yaml:"choose"`
}

// EmitPolicy writes p as a policy/v1 YAML document, preceded by a comment
// holding p.Describe(). The document parses with model.ParsePolicy and
// compiles with model.CompilePolicy to a policy with the same ID that makes
// the same choices. p must be a *TablePolicy from ExhaustiveSource or
// ExactSource.
func EmitPolicy(p sim.Policy, w io.Writer) error {
	tp, ok := p.(*TablePolicy)
	if !ok || tp == nil || tp.def == nil {
		return fmt.Errorf("experiment: EmitPolicy supports policies enumerated by ExhaustiveSource or solved by ExactSource, got %T", p)
	}
	doc := emitDocument{
		APIVersion: tp.def.APIVersion,
		ID:         tp.def.ID,
		Version:    tp.def.Version,
		Decisions:  make(map[string]emitDecision, len(tp.def.Decisions)),
	}
	for id, d := range tp.def.Decisions {
		ed := emitDecision{Default: d.Default}
		for _, r := range d.Rules {
			ed.Rules = append(ed.Rules, emitRule{Else: r.Else})
		}
		for _, r := range d.Table {
			ed.Table = append(ed.Table, emitTableRow{Observed: r.Observed, Choose: r.Choose})
		}
		doc.Decisions[id] = ed
	}

	if _, err := fmt.Fprintf(w, "# %s\n", tp.description); err != nil {
		return err
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	return enc.Close()
}
