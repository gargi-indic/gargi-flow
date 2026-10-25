package report

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Kind names a report of experiment/v1 reports.
type Kind string

// Report kinds.
const (
	// KindRegret is the cross-world regret matrix.
	KindRegret Kind = "regret"
	// KindRobust is the expected-value, maximin and CVaR picks.
	KindRobust Kind = "robust"
	// KindTournament is the ranking and pairwise win rates.
	KindTournament Kind = "tournament"
	// KindPareto is the per-world Pareto frontier of two metrics.
	KindPareto Kind = "pareto"
	// KindVOI is the value of observing a node, per world.
	KindVOI Kind = "voi"
	// KindFlipPoint is where the optimal policy changes as one world param
	// is swept.
	KindFlipPoint Kind = "flip_point"
	// KindPathHeatmap is the probability mass of every node and edge under
	// each world's best policy.
	KindPathHeatmap Kind = "path_heatmap"
	// KindPolicyTree is each world's optimal table policy as a tree of
	// information sets.
	KindPolicyTree Kind = "policy_tree"
)

// Goal is the direction in which a Pareto axis improves.
type Goal string

// Pareto goals.
const (
	// GoalMax means larger values are better.
	GoalMax Goal = "max"
	// GoalMin means smaller values are better.
	GoalMin Goal = "min"
)

// better reports whether a is strictly better than b under g.
func (g Goal) better(a, b float64) bool {
	if g == GoalMin {
		return a < b
	}
	return a > b
}

// Axis is one metric of a Pareto report and its goal.
type Axis struct {
	// Metric is a built-in (objective, cost, duration) or declared metric.
	Metric string `json:"metric"`
	// Goal is max or min.
	Goal Goal `json:"goal"`
}

// Spec is one parsed entry of experiment/v1 reports. It is comparable, so
// equal entries are detected as duplicates.
type Spec struct {
	// Kind is the report kind.
	Kind Kind
	// X and Y are the axes of a Pareto report; zero for other kinds.
	X, Y Axis
	// Node is the observed node of a voi report; empty for other kinds.
	Node string
	// Param is the swept world param of a flip_point report; empty for
	// other kinds.
	Param string
	// HasRange reports whether a flip_point report gives an explicit sweep
	// range [Lo, Hi]. Without one the range is the minimum and maximum of
	// Param across the experiment's worlds.
	HasRange bool
	// Lo and Hi are the explicit sweep range of a flip_point report.
	Lo, Hi float64
	// Alpha is the significance level of a tournament report (default 0.05).
	Alpha float64
}

var (
	// metricName is the syntax of a metric name.
	metricName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	// nodeName is the syntax of a graph/v1 node id.
	nodeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	// paramName is the syntax of a world param swept by flip_point.
	paramName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ParseSpec parses one entry of experiment/v1 reports:
//
//	regret
//	robust
//	tournament[:<alpha>]
//	pareto:<metricA>[:min|max],<metricB>[:min|max]
//	voi:<node>
//	flip_point:<param>[:<lo>,<hi>]
//	path_heatmap
//	policy_tree
//
// Pareto goals default to max; the two metrics must differ. A flip_point
// range must be finite with lo < hi. Whether the metrics, the node and the
// param exist is checked by the caller against the model and the worlds.
func ParseSpec(s string) (Spec, error) {
	kind, args, hasArgs := strings.Cut(s, ":")
	switch Kind(kind) {
	case KindRegret, KindRobust, KindPathHeatmap, KindPolicyTree:
		if hasArgs {
			return Spec{}, fmt.Errorf("report %q takes no arguments", kind)
		}
		return Spec{Kind: Kind(kind)}, nil
	case KindTournament:
		alpha := 0.05
		if hasArgs {
			v, err := strconv.ParseFloat(args, 64)
			if err != nil || v <= 0 || v >= 1 {
				return Spec{}, fmt.Errorf("tournament alpha %q must be in (0, 1)", args)
			}
			alpha = v
		}
		return Spec{Kind: KindTournament, Alpha: alpha}, nil
	case KindPareto:
		if !hasArgs {
			return Spec{}, errors.New("pareto needs two metrics: pareto:<metricA>[:min|max],<metricB>[:min|max]")
		}
		parts := strings.Split(args, ",")
		if len(parts) != 2 {
			return Spec{}, fmt.Errorf("pareto needs exactly two metrics, got %d", len(parts))
		}
		x, err := parseAxis(parts[0])
		if err != nil {
			return Spec{}, err
		}
		y, err := parseAxis(parts[1])
		if err != nil {
			return Spec{}, err
		}
		if x.Metric == y.Metric {
			return Spec{}, fmt.Errorf("pareto needs two different metrics, got %q twice", x.Metric)
		}
		return Spec{Kind: KindPareto, X: x, Y: y}, nil
	case KindVOI:
		node := strings.TrimSpace(args)
		if !hasArgs || node == "" {
			return Spec{}, errors.New("voi needs a node: voi:<node>")
		}
		if !nodeName.MatchString(node) {
			return Spec{}, fmt.Errorf("voi node %q must match %s", node, nodeName)
		}
		return Spec{Kind: KindVOI, Node: node}, nil
	case KindFlipPoint:
		return parseFlipPoint(args, hasArgs)
	default:
		return Spec{}, fmt.Errorf("unknown report %q: want regret, robust, tournament[:<alpha>], pareto:<metricA>[:min|max],<metricB>[:min|max], voi:<node>, flip_point:<param>[:<lo>,<hi>], path_heatmap or policy_tree", s)
	}
}

// parseFlipPoint parses the arguments of flip_point:<param>[:<lo>,<hi>].
func parseFlipPoint(args string, hasArgs bool) (Spec, error) {
	param, rng, hasRange := strings.Cut(args, ":")
	param = strings.TrimSpace(param)
	if !hasArgs || param == "" {
		return Spec{}, errors.New("flip_point needs a world param: flip_point:<param>[:<lo>,<hi>]")
	}
	if !paramName.MatchString(param) {
		return Spec{}, fmt.Errorf("flip_point param %q must match %s", param, paramName)
	}
	spec := Spec{Kind: KindFlipPoint, Param: param}
	if !hasRange {
		return spec, nil
	}
	bounds := strings.Split(rng, ",")
	if len(bounds) != 2 {
		return Spec{}, fmt.Errorf("flip_point range %q must be <lo>,<hi>", rng)
	}
	var vals [2]float64
	for i, b := range bounds {
		b = strings.TrimSpace(b)
		v, err := strconv.ParseFloat(b, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return Spec{}, fmt.Errorf("flip_point bound %q must be a finite number", b)
		}
		vals[i] = v
	}
	if !(vals[0] < vals[1]) {
		return Spec{}, fmt.Errorf("flip_point range needs lo < hi, got %g,%g", vals[0], vals[1])
	}
	spec.HasRange, spec.Lo, spec.Hi = true, vals[0], vals[1]
	return spec, nil
}

// parseAxis parses <metric>[:min|max].
func parseAxis(s string) (Axis, error) {
	name, goal, hasGoal := strings.Cut(strings.TrimSpace(s), ":")
	name, goal = strings.TrimSpace(name), strings.TrimSpace(goal)
	if !metricName.MatchString(name) {
		return Axis{}, fmt.Errorf("pareto metric %q must match %s", name, metricName)
	}
	a := Axis{Metric: name, Goal: GoalMax}
	if hasGoal {
		switch Goal(goal) {
		case GoalMax, GoalMin:
			a.Goal = Goal(goal)
		default:
			return Axis{}, fmt.Errorf("pareto goal %q of metric %q must be min or max", goal, name)
		}
	}
	return a, nil
}

// MinWorlds returns the number of experiment worlds the report needs: 2 for
// regret and robust (they compare worlds), 1 otherwise.
func (s Spec) MinWorlds() int {
	if s.Kind == KindRegret || s.Kind == KindRobust {
		return 2
	}
	return 1
}

// NeedsSolver reports whether the report runs the exact solver and so needs
// a discrete model: voi, flip_point and policy_tree.
func (s Spec) NeedsSolver() bool {
	return s.Kind == KindVOI || s.Kind == KindFlipPoint || s.Kind == KindPolicyTree
}

// Metrics returns the metrics the report reads, in axis order.
func (s Spec) Metrics() []string {
	if s.Kind == KindPareto {
		return []string{s.X.Metric, s.Y.Metric}
	}
	return nil
}
