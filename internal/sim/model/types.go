package model

// Sim represents a simulation definition.
type Sim struct {
	APIVersion string                    `json:"apiVersion"`
	ID         string                    `json:"id"`
	Graph      string                    `json:"graph"`
	Arrivals   *ArrivalsSpec             `json:"arrivals,omitempty"`
	Horizon    string                    `json:"horizon,omitempty"`
	Worlds     map[string]map[string]any `json:"worlds"`
	Latents    map[string]ValueSpec      `json:"latents,omitempty"`
	Behaviors  map[string]Behavior       `json:"behaviors,omitempty"`
	Resources  map[string]Resource       `json:"resources,omitempty"`
	Objective  Objective                 `json:"objective"`
	// Metrics maps a metric name to a CEL expression evaluated once per run
	// in the objective environment (world, latent, nodes, run). The output
	// is a number (double, or int converted to double). Names match
	// ^[a-z_][a-z0-9_]*$; the built-in metric names (objective, cost,
	// duration) are reserved. Metrics are estimated like the objective and
	// can be referenced by aggregate requirements and Pareto reports.
	Metrics map[string]string `json:"metrics,omitempty"`
}

// Resource describes the constraints and capacity of a system resource.
type Resource struct {
	Capacity int `json:"capacity"`
}

// Objective defines the goal to maximize or minimize and its constraints.
type Objective struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Maximize string `json:"maximize"` // "mean"
	// Constraints maps a name to a per-run boolean CEL expression; the
	// estimates report the probability that each holds.
	Constraints map[string]string `json:"constraints,omitempty"`
	// Requirements maps a name to an aggregate requirement evaluated on the
	// estimates of each (policy, world), with the grammar
	//
	//	<agg>(<arg>) <op> <rhs>
	//
	// agg is mean, p50, p95 (arg is a metric name, built-in or declared) or
	// prob (arg is a per-run boolean CEL expression in the objective
	// environment); op is <=, <, >=, > or ==; rhs is a number or
	// duration('<Go duration>') in seconds. A policy that violates a
	// requirement in a world is infeasible there and ranks after every
	// feasible policy.
	Requirements map[string]string `json:"requirements,omitempty"`
}

// Behavior specifies the cost, duration, failure rates, and outputs of a node.
type Behavior struct {
	Cost     any                  `json:"cost,omitempty"`     // number or string (CEL)
	Duration any                  `json:"duration,omitempty"` // string ("48h") or ValueSpec
	Resource string               `json:"resource,omitempty"`
	Patience any                  `json:"patience,omitempty"`
	Abandon  *Abandon             `json:"abandon,omitempty"`
	Fail     *Fail                `json:"fail,omitempty"`
	Output   map[string]ValueSpec `json:"output,omitempty"`
}

// Abandon configures the probability of a task being abandoned.
type Abandon struct {
	P any `json:"p"` // number or string
}

// Fail configures the probability and classification of a failure.
type Fail struct {
	P     any    `json:"p"`     // number or string
	Class string `json:"class"` // "retryable", "non_retryable", "critical"
}

// ValueSpec exactly one of the fields should be set.
type ValueSpec struct {
	Fixed       any            `json:"fixed,omitempty"` // number or string
	Choice      map[string]any `json:"choice,omitempty"`
	Bernoulli   *Bernoulli     `json:"bernoulli,omitempty"`
	Normal      *Normal        `json:"normal,omitempty"`
	Lognormal   *Lognormal     `json:"lognormal,omitempty"`
	Uniform     *Uniform       `json:"uniform,omitempty"`
	Triangular  *Triangular    `json:"triangular,omitempty"`
	Exponential *Exponential   `json:"exponential,omitempty"`
	Empirical   []float64      `json:"empirical,omitempty"`
	Expr        string         `json:"expr,omitempty"`
}

// Bernoulli represents a Bernoulli distribution.
type Bernoulli struct {
	P any `json:"p"`
}

// Normal represents a Normal (Gaussian) distribution.
type Normal struct {
	Mean any `json:"mean"`
	SD   any `json:"sd"`
}

// Lognormal represents a Lognormal distribution.
type Lognormal struct {
	Mu    any `json:"mu"`
	Sigma any `json:"sigma"`
}

// Uniform represents a continuous Uniform distribution.
type Uniform struct {
	Min any `json:"min"`
	Max any `json:"max"`
}

// Triangular represents a Triangular distribution.
type Triangular struct {
	Min  any `json:"min"`
	Mode any `json:"mode"`
	Max  any `json:"max"`
}

// Exponential represents an Exponential distribution.
type Exponential struct {
	Rate any `json:"rate"`
}

// Policy represents a policy definition.
type Policy struct {
	APIVersion string `json:"apiVersion"`
	ID         string `json:"id"`
	Version    string `json:"version"`
	// Params declares the policy's tunable parameters by name. Rule when
	// expressions read them as params.<name>; optimizers (policies.search:
	// grid or random) bind candidate values through Parameterized.
	Params    map[string]ParamSpec `json:"params,omitempty"`
	Decisions map[string]Decision  `json:"decisions"`
}

// ParamSpec declares one tunable policy parameter. A parameter is either a
// range (Min and Max set, Min <= Max, optional Step > 0) or a values list
// (Values non-empty, each a number or a string, no duplicates), never both.
// Range parameters are bound as CEL doubles; values-list parameters keep
// their document type (numbers as doubles, strings as strings).
//
// Default is the value bound until an optimizer rebinds the parameter. It
// is optional and defaults to Min (range) or Values[0] (values list); when
// set it must lie in [Min, Max] (range) or be one of Values.
type ParamSpec struct {
	// Min is the lower bound of a range parameter (inclusive).
	Min *float64 `json:"min,omitempty"`
	// Max is the upper bound of a range parameter (inclusive).
	Max *float64 `json:"max,omitempty"`
	// Step is the grid spacing of a range parameter: grid search evaluates
	// Min, Min+Step, ... up to Max. Only grid search requires it.
	Step *float64 `json:"step,omitempty"`
	// Values lists the admissible values of a values-list parameter.
	Values []any `json:"values,omitempty"`
	// Default is the initially bound value.
	Default any `json:"default,omitempty"`
}

// Decision contains the rules or table used to make a policy choice.
type Decision struct {
	Rules   []Rule     `json:"rules,omitempty"`
	Table   []TableRow `json:"table,omitempty"`
	Default string     `json:"default,omitempty"`
}

// Rule represents a conditional choice in a policy decision.
type Rule struct {
	When   string `json:"when,omitempty"`
	Choose string `json:"choose,omitempty"`
	Else   string `json:"else,omitempty"`
}

// TableRow represents a single observation and its corresponding choice.
type TableRow struct {
	Observed map[string]any `json:"observed"`
	Choose   string         `json:"choose"`
}

// Experiment represents an experiment definition.
type Experiment struct {
	APIVersion string   `json:"apiVersion"`
	Model      string   `json:"model"`
	Worlds     []string `json:"worlds,omitempty"` // default all
	Policies   Policies `json:"policies"`
	Evaluator  string   `json:"evaluator,omitempty"` // "monte_carlo" | "exact" | "both", default "monte_carlo"
	Runs       int      `json:"runs"`
	Seed       uint64   `json:"seed"`
	Workers    int      `json:"workers,omitempty"`
	Top        int      `json:"top,omitempty"` // default 5
	// MaxPolicies caps the number of policies enumerated by search:
	// exhaustive and the number of candidates of search: grid; 0 selects
	// the default (10,000). It does not apply to search: exact, which never
	// enumerates policies, nor to search: random, which samples bounds.
	MaxPolicies int `json:"max_policies,omitempty"`
	// Reports lists the decision-quality reports computed over the
	// evaluated policies: "regret", "robust",
	// "pareto:<metricA>[:min|max],<metricB>[:min|max]", "voi:<node>",
	// "flip_point:<param>[:<lo>,<hi>]", "path_heatmap" and "policy_tree".
	// regret and robust need at least two worlds; voi, flip_point and
	// policy_tree need a discrete model.
	Reports []string `json:"reports,omitempty"`
	// WorldPrior is the probability of each experiment world used by the
	// robust report. Keys are experiment worlds, values are >= 0 and sum to
	// 1; worlds left out have probability 0. Nil means uniform over the
	// experiment's worlds.
	WorldPrior map[string]float64 `json:"world_prior,omitempty"`
	// CVaRAlpha is the tail mass of the robust report's CVaR criterion, in
	// (0, 1]; 0 selects DefaultCVaRAlpha (0.05).
	CVaRAlpha float64 `json:"cvar_alpha,omitempty"`
}

// DefaultCVaRAlpha is the CVaR tail mass used when cvar_alpha is unset.
const DefaultCVaRAlpha = 0.05

// Policies defines how policies are discovered or supplied to an experiment.
type Policies struct {
	// Search is "exhaustive" (enumerate every reduced deterministic policy),
	// "exact" (solve for each world's optimal policy by backward
	// induction), "grid" (every combination of the params of Policy),
	// "random" (Samples random draws of the params of Policy) or
	// "racing" (successive halving with paired confidence intervals).
	Search string   `json:"search,omitempty"`
	Files  []string `json:"files,omitempty"`
	// Policy is the parameterized policy/v1 document that grid and random
	// search vary, relative to the experiment. It is required for those
	// searches and forbidden otherwise.
	Policy string `json:"policy,omitempty"`
	// Samples is the number of candidates random search draws (>= 1). It is
	// required for random search and forbidden otherwise.
	Samples int `json:"samples,omitempty"`
	// Seed seeds random search's draws; nil selects the experiment seed. It
	// is only allowed with random search.
	Seed *uint64 `json:"seed,omitempty"`

	// Racing optimizer configuration (only allowed with search: racing):
	MinRuns    int       `json:"min_runs,omitempty"`
	MaxRuns    int       `json:"max_runs,omitempty"`
	Factor     float64   `json:"factor,omitempty"`
	Alpha      float64   `json:"alpha,omitempty"`
	Candidates *Policies `json:"candidates,omitempty"`
}

// ArrivalsSpec specifies the arrival process for Mode B simulation.
type ArrivalsSpec struct {
	Poisson   *PoissonArrivals `json:"poisson,omitempty"`
	Schedule  []ScheduleWindow `json:"schedule,omitempty"`
	Empirical []float64        `json:"empirical,omitempty"`
}

// PoissonArrivals defines a Poisson arrival process.
type PoissonArrivals struct {
	Rate any `json:"rate"` // number or string (CEL)
}

// ScheduleWindow defines an arrival window with a constant rate.
type ScheduleWindow struct {
	Duration string `json:"duration"` // string ("48h")
	Rate     any    `json:"rate"`     // number or string (CEL)
}
