package report

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// Tournament holds the results of head-to-head comparisons between all evaluated policies.
type Tournament struct {
	// Alpha is the significance level of the paired-difference tests.
	Alpha float64 `json:"alpha"`
	// Rankings lists the policies sorted by overall objective mean across all worlds,
	// then by win rate, then by policy ID.
	Rankings []TournamentRanking `json:"rankings"`
	// Pairwise is the matrix of head-to-head matchups.
	// Pairwise[i][j] is the result of policy Rankings[i] vs Rankings[j].
	Pairwise [][]PairwiseMatch `json:"pairwise"`
}

// TournamentRanking summarizes a policy's performance in the tournament.
type TournamentRanking struct {
	// Rank is the 1-based rank of the policy.
	Rank int `json:"rank"`
	// PolicyID is the policy identifier.
	PolicyID string `json:"policyId"`
	// Mean is the overall expected objective mean across all evaluated worlds.
	Mean float64 `json:"mean"`
	// Wins is the number of head-to-head matchups this policy won (significantly).
	Wins int `json:"wins"`
	// Losses is the number of head-to-head matchups this policy lost.
	Losses int `json:"losses"`
	// Ties is the number of head-to-head matchups that were not statistically significant.
	Ties int `json:"ties"`
	// WinRate is the fraction of head-to-head matches won (Wins + 0.5*Ties) / Total.
	WinRate float64 `json:"winRate"`
}

// PairwiseMatch is the result of comparing PolicyA to PolicyB.
type PairwiseMatch struct {
	// PolicyA is the first policy ID.
	PolicyA string `json:"policyA"`
	// PolicyB is the second policy ID.
	PolicyB string `json:"policyB"`
	// MeanDiff is the mean of the paired differences (A - B).
	MeanDiff float64 `json:"meanDiff"`
	// CI is the paired difference confidence interval [LB, UB] at level 1-Alpha.
	CI [2]float64 `json:"ci"`
	// WinRate is the proportion of paired replications where A > B (ties count as 0.5).
	WinRate float64 `json:"winRate"`
	// Significant is true if 0 is outside the confidence interval.
	Significant bool `json:"significant"`
	// Winner is the policy ID of the significant winner, or empty if not significant.
	Winner string `json:"winner"`
}

// ComputeMatchup computes the head-to-head pairwise comparison between policy A and policy B.
func ComputeMatchup(policyA, policyB string, repsA, repsB []float64, alpha float64) (PairwiseMatch, error) {
	if alpha <= 0 || alpha >= 1 {
		return PairwiseMatch{}, fmt.Errorf("report: tournament alpha %v must be in (0, 1)", alpha)
	}
	n := len(repsA)
	if n == 0 {
		return PairwiseMatch{}, errors.New("report: no replications provided")
	}
	if len(repsB) != n {
		return PairwiseMatch{}, fmt.Errorf("report: replication counts do not match: %d vs %d", n, len(repsB))
	}

	sumD := 0.0
	wins := 0.0
	for i := 0; i < n; i++ {
		diff := repsA[i] - repsB[i]
		sumD += diff
		if repsA[i] > repsB[i] {
			wins += 1.0
		} else if repsA[i] == repsB[i] {
			wins += 0.5
		}
	}
	meanDiff := sumD / float64(n)
	winRate := wins / float64(n)

	var margin float64
	if n > 1 {
		sumSq := 0.0
		for i := 0; i < n; i++ {
			d := (repsA[i] - repsB[i]) - meanDiff
			sumSq += d * d
		}
		sd := math.Sqrt(sumSq / float64(n-1))
		se := sd / math.Sqrt(float64(n))
		tCrit := studentTQuantile(alpha/2.0, n-1)
		margin = tCrit * se
	}

	lb := meanDiff - margin
	ub := meanDiff + margin

	if meanDiff == 0 {
		meanDiff = 0
	}
	if lb == 0 {
		lb = 0
	}
	if ub == 0 {
		ub = 0
	}

	significant := false
	winner := ""
	if lb > 0 {
		significant = true
		winner = policyA
	} else if ub < 0 {
		significant = true
		winner = policyB
	}

	return PairwiseMatch{
		PolicyA:     policyA,
		PolicyB:     policyB,
		MeanDiff:    meanDiff,
		CI:          [2]float64{lb, ub},
		WinRate:     winRate,
		Significant: significant,
		Winner:      winner,
	}, nil
}

// NewTournament computes the tournament report from a Table and world priors.
func NewTournament(t Table, prior []float64, alpha float64) (*Tournament, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	if alpha <= 0 || alpha >= 1 {
		return nil, fmt.Errorf("report: tournament alpha %v must be in (0, 1)", alpha)
	}
	nw := len(t.Worlds)
	np := len(t.Policies)
	if len(prior) == 0 {
		prior = make([]float64, nw)
		for w := range nw {
			prior[w] = 1.0 / float64(nw)
		}
	} else if len(prior) != nw {
		return nil, errors.New("report: prior needs one probability per world")
	}
	if len(t.Replications) == 0 {
		return nil, errors.New("report: tournament report requires Monte Carlo replications")
	}
	if len(t.Replications) != np {
		return nil, errors.New("report: Replications needs one row per policy")
	}
	for p := range np {
		if len(t.Replications[p]) != nw {
			return nil, fmt.Errorf("report: policy %q needs one replication slice per world", t.Policies[p])
		}
	}

	for w := range nw {
		nR := len(t.Replications[0][w])
		if nR == 0 {
			return nil, fmt.Errorf("report: world %q has 0 replications", t.Worlds[w])
		}
		for p := 1; p < np; p++ {
			if len(t.Replications[p][w]) != nR {
				return nil, fmt.Errorf("report: policy %q has %d replications in world %q, want %d",
					t.Policies[p], len(t.Replications[p][w]), t.Worlds[w], nR)
			}
		}
	}

	policyReps := make([][]float64, np)
	for p := range np {
		for w := range nw {
			policyReps[p] = append(policyReps[p], t.Replications[p][w]...)
		}
	}

	means := make([]float64, np)
	for p := range np {
		for w := range nw {
			means[p] += prior[w] * t.Mean[p][w]
		}
	}

	return newTournament(t.Policies, means, policyReps, alpha)
}

// NewTournamentFromReplications computes the tournament report from policy IDs,
// their overall objective means, and paired replication slices.
func NewTournamentFromReplications(policies []string, means []float64, reps [][]float64, alpha float64) (*Tournament, error) {
	if alpha <= 0 || alpha >= 1 {
		return nil, fmt.Errorf("report: tournament alpha %v must be in (0, 1)", alpha)
	}
	return newTournament(policies, means, reps, alpha)
}

func newTournament(policies []string, means []float64, reps [][]float64, alpha float64) (*Tournament, error) {
	m := len(policies)
	if m == 0 {
		return nil, errors.New("report: no policies provided")
	}
	if len(means) != m {
		return nil, errors.New("report: means length must match policies")
	}
	if len(reps) != m {
		return nil, errors.New("report: replications length must match policies")
	}
	n := len(reps[0])
	if n == 0 {
		return nil, errors.New("report: no replications provided")
	}
	for i := 1; i < m; i++ {
		if len(reps[i]) != n {
			return nil, fmt.Errorf("report: policy %q has %d replications, want %d", policies[i], len(reps[i]), n)
		}
	}

	pairwiseMap := make(map[[2]int]PairwiseMatch, m*m)
	for i := 0; i < m; i++ {
		for j := 0; j < m; j++ {
			match, err := ComputeMatchup(policies[i], policies[j], reps[i], reps[j], alpha)
			if err != nil {
				return nil, err
			}
			pairwiseMap[[2]int{i, j}] = match
		}
	}

	rankings := make([]TournamentRanking, m)
	for i := 0; i < m; i++ {
		wins, losses, ties := 0, 0, 0
		for j := 0; j < m; j++ {
			if i == j {
				continue
			}
			match := pairwiseMap[[2]int{i, j}]
			if match.Significant {
				if match.Winner == policies[i] {
					wins++
				} else {
					losses++
				}
			} else {
				ties++
			}
		}
		total := wins + losses + ties
		winRate := 0.0
		if total > 0 {
			winRate = (float64(wins) + 0.5*float64(ties)) / float64(total)
		}
		rankings[i] = TournamentRanking{
			PolicyID: policies[i],
			Mean:     means[i],
			Wins:     wins,
			Losses:   losses,
			Ties:     ties,
			WinRate:  winRate,
		}
	}

	sort.SliceStable(rankings, func(i, j int) bool {
		if rankings[i].Mean != rankings[j].Mean {
			return rankings[i].Mean > rankings[j].Mean
		}
		if rankings[i].WinRate != rankings[j].WinRate {
			return rankings[i].WinRate > rankings[j].WinRate
		}
		return rankings[i].PolicyID < rankings[j].PolicyID
	})

	for i := range rankings {
		rankings[i].Rank = i + 1
	}

	policyIdx := make(map[string]int, m)
	for idx, p := range policies {
		policyIdx[p] = idx
	}

	pairwise := make([][]PairwiseMatch, m)
	for i := 0; i < m; i++ {
		pairwise[i] = make([]PairwiseMatch, m)
		origI := policyIdx[rankings[i].PolicyID]
		for j := 0; j < m; j++ {
			origJ := policyIdx[rankings[j].PolicyID]
			pairwise[i][j] = pairwiseMap[[2]int{origI, origJ}]
		}
	}

	return &Tournament{
		Alpha:    alpha,
		Rankings: rankings,
		Pairwise: pairwise,
	}, nil
}

// studentTQuantile computes the critical value t such that P(T_{df} > t) = alpha,
// where alpha in (0, 0.5) and df >= 1.
func studentTQuantile(alpha float64, df int) float64 {
	if df < 1 {
		return 0
	}
	p := 1.0 - alpha
	if df == 1 {
		return math.Tan(math.Pi * (p - 0.5))
	}
	if df == 2 {
		return (2.0*p - 1.0) / math.Sqrt(2.0*p*(1.0-p))
	}

	v := float64(df)
	z := normalCDFInv(p)
	// Cornish-Fisher initial approximation
	z3 := z * z * z
	z5 := z3 * z * z
	t := z + (z3+z)/(4.0*v) + (5.0*z5+16.0*z3+3.0*z)/(96.0*v*v)

	// Refine with Newton-Raphson iterations
	for range 4 {
		cdf := studentTCDF(t, v)
		pdf := studentTPDF(t, v)
		if pdf <= 0 {
			break
		}
		diff := cdf - p
		step := diff / pdf
		t -= step
		if math.Abs(step) < 1e-12 {
			break
		}
	}
	return t
}

// studentTCDF computes the cumulative distribution function of Student's t distribution with df degrees of freedom.
func studentTCDF(t, df float64) float64 {
	if t == 0 {
		return 0.5
	}
	x := df / (df + t*t)
	ib := incBeta(x, df/2.0, 0.5)
	if t > 0 {
		return 1.0 - 0.5*ib
	}
	return 0.5 * ib
}

// studentTPDF computes the probability density function of Student's t distribution with df degrees of freedom.
func studentTPDF(t, df float64) float64 {
	lg1, _ := math.Lgamma((df + 1.0) / 2.0)
	lg2, _ := math.Lgamma(df / 2.0)
	logVal := lg1 - lg2 - 0.5*(math.Log(math.Pi)+math.Log(df)) - ((df+1.0)/2.0)*math.Log(1.0+t*t/df)
	return math.Exp(logVal)
}

// incBeta computes the regularized incomplete beta function I_x(a, b).
func incBeta(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}

	lgA, _ := math.Lgamma(a)
	lgB, _ := math.Lgamma(b)
	lgAB, _ := math.Lgamma(a + b)
	logBeta := lgA + lgB - lgAB

	if x < (a+1.0)/(a+b+2.0) {
		front := math.Exp(a*math.Log(x)+b*math.Log(1.0-x)-logBeta) / a
		return front * incBetaCF(x, a, b)
	}
	front := math.Exp(b*math.Log(1.0-x)+a*math.Log(x)-logBeta) / b
	return 1.0 - front*incBetaCF(1.0-x, b, a)
}

// incBetaCF computes the continued fraction component of the incomplete beta function (Lentz's method).
func incBetaCF(x, a, b float64) float64 {
	const (
		maxIter = 200
		eps     = 1e-15
		fpMin   = 1e-30
	)

	qab := a + b
	qap := a + 1.0
	qam := a - 1.0
	c := 1.0
	d := 1.0 - qab*x/qap
	if math.Abs(d) < fpMin {
		d = fpMin
	}
	d = 1.0 / d
	h := d

	for m := 1; m <= maxIter; m++ {
		mF := float64(m)
		m2 := 2.0 * mF

		// Even step
		aa := mF * (b - mF) * x / ((qam + m2) * (a + m2))
		d = 1.0 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1.0 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1.0 / d
		h *= d * c

		// Odd step
		aa = -(a + mF) * (qab + mF) * x / ((a + m2) * (qap + m2))
		d = 1.0 + aa*d
		if math.Abs(d) < fpMin {
			d = fpMin
		}
		c = 1.0 + aa/c
		if math.Abs(c) < fpMin {
			c = fpMin
		}
		d = 1.0 / d
		del := d * c
		h *= del
		if math.Abs(del-1.0) < eps {
			break
		}
	}
	return h
}

// normalCDFInv computes the inverse of the standard normal cumulative distribution function (Acklam algorithm).
func normalCDFInv(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(1)
	}

	a := [6]float64{
		-3.969683028665376e+01, 2.204091108189051e+02,
		-2.755581241086124e+02, 1.383577518672690e+02,
		-3.066479806614716e+01, 2.506628277459239e+00,
	}
	b := [5]float64{
		-5.447609879822406e+01, 1.615858368580409e+02,
		-1.556989798598866e+02, 6.680131188771972e+01,
		-1.328068155288572e+01,
	}
	c := [6]float64{
		-7.784894002430293e-03, -3.223964580411365e-01,
		-2.400758277161838e+00, -2.549732539343734e+00,
		4.374664141464968e+00, 2.938163982698783e+00,
	}
	d := [4]float64{
		7.784695709041462e-03, 3.224671290700398e-01,
		2.445134137142996e+00, 3.754408661907416e+00,
	}

	const (
		pLow  = 0.02425
		pHigh = 1.0 - pLow
	)

	if p < pLow {
		q := math.Sqrt(-2.0 * math.Log(p))
		return (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1.0)
	}
	if p <= pHigh {
		q := p - 0.5
		r := q * q
		return (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q /
			(((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1.0)
	}
	q := math.Sqrt(-2.0 * math.Log(1.0-p))
	return -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
		((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1.0)
}
