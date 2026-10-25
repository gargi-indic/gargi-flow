package report

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// Atom is one value of a discrete distribution with its probability mass.
type Atom struct {
	Value  float64
	Weight float64
}

// Table is the input of every report: the estimates of every policy in
// every world. Slices indexed [p][w] follow Policies and Worlds.
type Table struct {
	// Worlds lists the evaluated worlds in experiment order.
	Worlds []sim.WorldID
	// Policies lists the distinct ids of the evaluated policies.
	Policies []string
	// Mean[p][w] is the objective mean of policy p in world w.
	Mean [][]float64
	// Feasible[p][w] reports whether policy p satisfies every aggregate
	// requirement in world w.
	Feasible [][]bool
	// Metrics maps a metric name to its means, indexed [p][w]. Only the
	// Pareto report reads it, and only for its two metrics.
	Metrics map[string][][]float64
	// Tails[p][w] is the lower tail of the objective distribution of policy
	// p in world w: atoms sorted by value ascending, with probabilities
	// within the world (they sum to 1 for the whole distribution). Only the
	// robust report reads it; each tail must hold at least the lowest
	// TailCover(prior(w), alpha) of the world's probability mass.
	Tails [][][]Atom
	// Replications[p][w] holds the replication objective values of policy p
	// in world w, in replication order. Only the tournament report reads it.
	Replications [][][]float64
}

// check validates the shape of the fields every report reads.
func (t Table) check() error {
	np, nw := len(t.Policies), len(t.Worlds)
	if np == 0 || nw == 0 {
		return errors.New("report: table has no policies or no worlds")
	}
	if len(t.Mean) != np || len(t.Feasible) != np {
		return errors.New("report: Mean and Feasible need one row per policy")
	}
	for p := range np {
		if len(t.Mean[p]) != nw || len(t.Feasible[p]) != nw {
			return fmt.Errorf("report: policy %q needs one Mean and Feasible entry per world", t.Policies[p])
		}
	}
	return nil
}

// RegretCell is the value and regret of a tuned policy in one world.
type RegretCell struct {
	// World is the world the policy is evaluated in (the column).
	World sim.WorldID `json:"world"`
	// PolicyID is the tuned policy.
	PolicyID string `json:"policyId"`
	// Value is the policy's objective mean in World.
	Value float64 `json:"value"`
	// Regret is the best feasible objective mean in World minus Value: 0 on
	// the diagonal, never negative unless the tuned policy is infeasible in
	// World. It is omitted when World has no feasible policy.
	Regret *float64 `json:"regret,omitempty"`
	// Feasible reports whether the policy satisfies every requirement in
	// World.
	Feasible bool `json:"feasible"`
}

// RegretRow is the policy tuned for one world evaluated in every world.
type RegretRow struct {
	// TunedFor is the world the policy is best in (the row).
	TunedFor sim.WorldID `json:"tunedFor"`
	// PolicyID is the first feasible policy of TunedFor's ranking: the
	// highest objective mean, ties to the lower policy id.
	PolicyID string `json:"policyId"`
	// Cells holds one cell per world, in Regret.Worlds order.
	Cells []RegretCell `json:"cells"`
	// MaxRegret is the largest regret of the row (0 when no cell has one).
	MaxRegret float64 `json:"maxRegret"`
}

// Regret is the cross-world regret matrix: the policy tuned for each world
// (row) evaluated in every world (column).
type Regret struct {
	// Worlds lists the columns, in experiment order.
	Worlds []sim.WorldID `json:"worlds"`
	// Rows holds one row per world that has a feasible policy, in
	// experiment order.
	Rows []RegretRow `json:"rows"`
	// NoFeasiblePolicy lists the worlds without any feasible policy; they
	// have no row, and cells in their column have no regret.
	NoFeasiblePolicy []sim.WorldID `json:"noFeasiblePolicy,omitempty"`
}

// bestFeasible returns, per world, the index of the feasible policy with
// the highest mean (ties to the lower policy id), or -1.
func bestFeasible(t Table) []int {
	best := make([]int, len(t.Worlds))
	for w := range t.Worlds {
		best[w] = -1
		for p := range t.Policies {
			if !t.Feasible[p][w] {
				continue
			}
			if b := best[w]; b < 0 || t.Mean[p][w] > t.Mean[b][w] ||
				(t.Mean[p][w] == t.Mean[b][w] && t.Policies[p] < t.Policies[b]) {
				best[w] = p
			}
		}
	}
	return best
}

// NewRegret computes the regret matrix of t. The policy tuned for world w is
// the feasible policy with the highest objective mean in w (ties to the
// lower policy id), which is the first feasible policy of w's ranking. Each
// cell holds the tuned policy's mean in the column world and its regret
// there: the best feasible mean of that world minus the policy's mean.
func NewRegret(t Table) (*Regret, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	best := bestFeasible(t)
	r := &Regret{Worlds: append([]sim.WorldID(nil), t.Worlds...), Rows: []RegretRow{}}
	for w, world := range t.Worlds {
		p := best[w]
		if p < 0 {
			r.NoFeasiblePolicy = append(r.NoFeasiblePolicy, world)
			continue
		}
		row := RegretRow{TunedFor: world, PolicyID: t.Policies[p], Cells: make([]RegretCell, len(t.Worlds))}
		for c, col := range t.Worlds {
			cell := RegretCell{World: col, PolicyID: t.Policies[p], Value: t.Mean[p][c], Feasible: t.Feasible[p][c]}
			if b := best[c]; b >= 0 {
				regret := t.Mean[b][c] - t.Mean[p][c]
				cell.Regret = &regret
				row.MaxRegret = math.Max(row.MaxRegret, regret)
			}
			row.Cells[c] = cell
		}
		r.Rows = append(r.Rows, row)
	}
	return r, nil
}

// RobustScores are the three robustness scores of one policy.
type RobustScores struct {
	// ExpectedValue is Σ_w prior(w)·mean(p, w).
	ExpectedValue float64 `json:"expectedValue"`
	// WorstCase is min_w mean(p, w), the maximin criterion.
	WorstCase float64 `json:"worstCase"`
	// CVaR is the expected objective over the worst alpha probability mass
	// of the prior-weighted mixture of the policy's outcome distributions.
	CVaR float64 `json:"cvar"`
}

// RobustPick is the best policy under one criterion.
type RobustPick struct {
	// PolicyID is the picked policy.
	PolicyID string `json:"policyId"`
	// Score is the policy's score under the criterion.
	Score float64 `json:"score"`
	// Scores holds the policy's scores under all three criteria.
	Scores RobustScores `json:"scores"`
}

// Robust holds the best policy by expected value under the world prior, by
// worst-case (maximin) mean, and by CVaR of the objective. Only policies
// feasible in every world are eligible; ties go to the lower policy id.
type Robust struct {
	// Prior is the probability of every experiment world.
	Prior map[sim.WorldID]float64 `json:"prior"`
	// CVaRAlpha is the tail mass of the CVaR criterion.
	CVaRAlpha float64 `json:"cvarAlpha"`
	// Eligible is the number of policies feasible in every world.
	Eligible int `json:"eligible"`
	// ExpectedValue maximizes RobustScores.ExpectedValue.
	ExpectedValue *RobustPick `json:"expectedValue,omitempty"`
	// Maximin maximizes RobustScores.WorstCase.
	Maximin *RobustPick `json:"maximin,omitempty"`
	// CVaR maximizes RobustScores.CVaR.
	CVaR *RobustPick `json:"cvar,omitempty"`
}

// TailCover returns the fraction of a world's probability mass that the
// lowest alpha mass of a mixture can draw from a world of probability
// prior: min(1, alpha/prior), or 0 when prior is 0.
func TailCover(prior, alpha float64) float64 {
	if prior <= 0 {
		return 0
	}
	return math.Min(1, alpha/prior)
}

// NewRobust computes the robust picks of t under prior (one probability per
// world of t, summing to 1) and the CVaR tail mass alpha in (0, 1].
func NewRobust(t Table, prior []float64, alpha float64) (*Robust, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	if len(prior) != len(t.Worlds) {
		return nil, errors.New("report: prior needs one probability per world")
	}
	if !(alpha > 0 && alpha <= 1) {
		return nil, fmt.Errorf("report: cvar alpha %v is not in (0, 1]", alpha)
	}
	if len(t.Tails) != len(t.Policies) {
		return nil, errors.New("report: Tails needs one row per policy")
	}
	r := &Robust{Prior: make(map[sim.WorldID]float64, len(t.Worlds)), CVaRAlpha: alpha}
	for w, world := range t.Worlds {
		r.Prior[world] = prior[w]
	}

	pick := func(cur *RobustPick, id string, score float64, s RobustScores) *RobustPick {
		if cur == nil || score > cur.Score || (score == cur.Score && id < cur.PolicyID) {
			return &RobustPick{PolicyID: id, Score: score, Scores: s}
		}
		return cur
	}
	for p, id := range t.Policies {
		eligible := true
		for w := range t.Worlds {
			eligible = eligible && t.Feasible[p][w]
		}
		if !eligible {
			continue
		}
		if len(t.Tails[p]) != len(t.Worlds) {
			return nil, fmt.Errorf("report: policy %q needs one tail per world", id)
		}
		r.Eligible++
		s := RobustScores{WorstCase: math.Inf(1)}
		var mixture []Atom
		for w := range t.Worlds {
			s.ExpectedValue += prior[w] * t.Mean[p][w]
			s.WorstCase = math.Min(s.WorstCase, t.Mean[p][w])
			if prior[w] > 0 {
				for _, a := range t.Tails[p][w] {
					mixture = append(mixture, Atom{Value: a.Value, Weight: prior[w] * a.Weight})
				}
			}
		}
		s.CVaR = CVaR(mixture, alpha)
		r.ExpectedValue = pick(r.ExpectedValue, id, s.ExpectedValue, s)
		r.Maximin = pick(r.Maximin, id, s.WorstCase, s)
		r.CVaR = pick(r.CVaR, id, s.CVaR, s)
	}
	return r, nil
}

// CVaR returns the conditional value at risk of a distribution: the
// expected value over its lowest alpha probability mass. atoms hold the
// distribution, or at least its lowest alpha mass, with probabilities (the
// full distribution sums to 1); they need not be sorted. The atom at the
// alpha boundary counts with the fraction of its mass that lies below
// alpha. When the atoms hold less than alpha mass in total (alpha = 1 with
// rounding), the result is their weighted mean. It returns NaN for no mass.
func CVaR(atoms []Atom, alpha float64) float64 {
	sorted := append([]Atom(nil), atoms...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Value < sorted[j].Value })
	cum, sum := 0.0, 0.0
	for _, a := range sorted {
		take := math.Min(a.Weight, alpha-cum)
		if take <= 0 {
			break
		}
		sum += a.Value * take
		cum += take
	}
	if cum <= 0 {
		return math.NaN()
	}
	return sum / cum
}

// Point is one policy on a Pareto frontier.
type Point struct {
	// PolicyID is the policy.
	PolicyID string `json:"policyId"`
	// X is the policy's mean of the first metric.
	X float64 `json:"x"`
	// Y is the policy's mean of the second metric.
	Y float64 `json:"y"`
}

// ParetoWorld is the Pareto frontier of one world.
type ParetoWorld struct {
	// World is the world id.
	World sim.WorldID `json:"world"`
	// Points lists the non-dominated feasible policies, best X first (in
	// the goal of X), then by policy id.
	Points []Point `json:"points"`
}

// Pareto is the per-world Pareto frontier of two metrics.
type Pareto struct {
	// X is the first metric and its goal.
	X Axis `json:"x"`
	// Y is the second metric and its goal.
	Y Axis `json:"y"`
	// Worlds holds one frontier per world, in experiment order.
	Worlds []ParetoWorld `json:"worlds"`
}

// NewPareto computes, per world, the frontier of the feasible policies of t
// on the means of the metrics of x and y.
func NewPareto(t Table, x, y Axis) (*Pareto, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	xs, ys := t.Metrics[x.Metric], t.Metrics[y.Metric]
	if len(xs) != len(t.Policies) || len(ys) != len(t.Policies) {
		return nil, fmt.Errorf("report: missing means of metric %q or %q", x.Metric, y.Metric)
	}
	r := &Pareto{X: x, Y: y, Worlds: make([]ParetoWorld, len(t.Worlds))}
	for w, world := range t.Worlds {
		var points []Point
		for p, id := range t.Policies {
			if !t.Feasible[p][w] {
				continue
			}
			if len(xs[p]) != len(t.Worlds) || len(ys[p]) != len(t.Worlds) {
				return nil, fmt.Errorf("report: policy %q needs one metric mean per world", id)
			}
			points = append(points, Point{PolicyID: id, X: xs[p][w], Y: ys[p][w]})
		}
		r.Worlds[w] = ParetoWorld{World: world, Points: Frontier(points, x.Goal, y.Goal)}
	}
	return r, nil
}

// Frontier returns the points not dominated by any other point: A dominates
// B iff A is at least as good as B on both axes (under their goals) and
// strictly better on one. Equal points are all kept. The result is sorted
// best X first, then by policy id, and is never nil.
func Frontier(points []Point, xGoal, yGoal Goal) []Point {
	sorted := append([]Point(nil), points...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.X != b.X {
			return xGoal.better(a.X, b.X)
		}
		if a.Y != b.Y {
			return yGoal.better(a.Y, b.Y)
		}
		return a.PolicyID < b.PolicyID
	})
	out := []Point{}
	// bestY is the best Y among points with a strictly better X.
	var bestY float64
	haveBest := false
	for i := 0; i < len(sorted); {
		j := i
		for j < len(sorted) && sorted[j].X == sorted[i].X {
			j++
		}
		// sorted[i] has the best Y of the group of equal X.
		groupY := sorted[i].Y
		if !haveBest || yGoal.better(groupY, bestY) {
			for _, pt := range sorted[i:j] {
				if pt.Y == groupY {
					out = append(out, pt)
				}
			}
			bestY, haveBest = groupY, true
		}
		i = j
	}
	return out
}
