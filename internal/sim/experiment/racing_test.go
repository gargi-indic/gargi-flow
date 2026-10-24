package experiment

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/stretchr/testify/require"
)

// TestRacing_Statistics verifies the Student-t distribution critical values
// and cumulative probabilities against known reference values.
func TestRacing_Statistics(t *testing.T) {
	tests := []struct {
		alpha     float64
		df        int
		wantTCrit float64
	}{
		{alpha: 0.05, df: 1, wantTCrit: 6.31375},
		{alpha: 0.05, df: 2, wantTCrit: 2.91999},
		{alpha: 0.05, df: 9, wantTCrit: 1.83311},
		{alpha: 0.05, df: 19, wantTCrit: 1.72913},
		{alpha: 0.05, df: 99, wantTCrit: 1.66039},
		{alpha: 0.01, df: 9, wantTCrit: 2.82144},
		{alpha: 0.01, df: 19, wantTCrit: 2.53948},
	}

	for _, tt := range tests {
		got := studentTQuantile(tt.alpha, tt.df)
		require.InDelta(t, tt.wantTCrit, got, 1e-4, "tQuantile(alpha=%f, df=%d)", tt.alpha, tt.df)

		// Check round-trip CDF: P(T > got) == alpha
		cdf := studentTCDF(got, float64(tt.df))
		require.InDelta(t, 1.0-tt.alpha, cdf, 1e-6, "studentTCDF for alpha=%f, df=%d", tt.alpha, tt.df)
	}
}

// TestRacing_LoanModel verifies that racing on the loan model:
// 1. Finds the identical optimal policy as full exhaustive evaluation in both worlds.
// 2. Uses at least 3x fewer total runs than full evaluation.
func TestRacing_LoanModel(t *testing.T) {
	expFull := loanExperiment(t, loanDir, 500)
	repFull, err := Run(context.Background(), expFull, expFull.ExhaustiveSource(), Options{All: true, Workers: 4})
	require.NoError(t, err)

	// Build racing experiment with candidates: {search: exhaustive}
	expRacing := loanExperiment(t, loanDir, 500)
	expRacing.Def.Policies = model.Policies{
		Search:  "racing",
		MinRuns: 10,
		Factor:  2.0,
		Alpha:   0.05,
		Candidates: &model.Policies{
			Search: "exhaustive",
		},
	}
	src := expRacing.PolicySource()
	_, ok := src.(RacingSource)
	require.True(t, ok, "expected RacingSource")

	repRacing, err := Run(context.Background(), expRacing, src, Options{All: true, Workers: 4})
	require.NoError(t, err)

	require.NotNil(t, repRacing.Racing)
	require.Len(t, repRacing.Worlds, len(repFull.Worlds))

	// Assert racing finds the identical optimal policy in each world
	for w := range repFull.Worlds {
		wantBest := repFull.Worlds[w].Policies[0]
		gotBest := repRacing.Worlds[w].Policies[0]

		require.Equal(t, wantBest.PolicyID, gotBest.PolicyID, "world %s optimal policy mismatch", repFull.Worlds[w].World)
		require.Equal(t, wantBest.Description, gotBest.Description, "world %s optimal policy description mismatch", repFull.Worlds[w].World)
	}

	// Assert racing uses at least 3x fewer total runs than full evaluation
	speedup := repRacing.Racing.Speedup
	t.Logf("Loan racing total runs: %d, full runs: %d, speedup: %.2fx",
		repRacing.Racing.TotalRuns, repRacing.Racing.FullRuns, speedup)

	require.GreaterOrEqual(t, speedup, 3.0, "expected at least 3x speedup, got %.2fx", speedup)
	require.LessOrEqual(t, repRacing.Racing.TotalRuns*3, repRacing.Racing.FullRuns)
}

// TestRacing_CanaryModel verifies that racing on the canary rollout model:
// 1. Finds the identical best policy as full grid evaluation.
// 2. Uses at least 3x fewer total runs than full evaluation.
func TestRacing_CanaryModel(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	canaryDir := filepath.Join(root, "examples", "simulation", "canary")

	reg := registry(t)
	expFull, err := LoadFile(filepath.Join(canaryDir, "experiment.yaml"), reg)
	require.NoError(t, err)

	repFull, err := Run(context.Background(), expFull, expFull.GridSource(), Options{All: true, Workers: 4})
	require.NoError(t, err)

	// Build racing experiment using direct policy candidate specification
	expRacing, err := LoadFile(filepath.Join(canaryDir, "experiment.yaml"), reg)
	require.NoError(t, err)
	expRacing.Def.Policies = model.Policies{
		Search:  "racing",
		Policy:  expFull.Def.Policies.Policy,
		MinRuns: 10,
		Factor:  2.0,
		Alpha:   0.05,
	}

	src := expRacing.PolicySource()
	_, ok := src.(RacingSource)
	require.True(t, ok, "expected RacingSource")

	repRacing, err := Run(context.Background(), expRacing, src, Options{All: true, Workers: 4})
	require.NoError(t, err)

	require.NotNil(t, repRacing.Racing)
	require.Len(t, repRacing.Worlds, len(repFull.Worlds))

	// Assert racing finds the identical best policy in each world
	for w := range repFull.Worlds {
		wantBest := repFull.Worlds[w].Policies[0]
		gotBest := repRacing.Worlds[w].Policies[0]

		require.Equal(t, wantBest.PolicyID, gotBest.PolicyID, "canary world %s best policy ID mismatch", repFull.Worlds[w].World)
		require.Equal(t, wantBest.Description, gotBest.Description, "canary world %s best policy description mismatch", repFull.Worlds[w].World)
	}

	// Assert racing uses at least 3x fewer total runs than full evaluation
	speedup := repRacing.Racing.Speedup
	t.Logf("Canary racing total runs: %d, full runs: %d, speedup: %.2fx",
		repRacing.Racing.TotalRuns, repRacing.Racing.FullRuns, speedup)

	require.GreaterOrEqual(t, speedup, 3.0, "expected at least 3x speedup, got %.2fx", speedup)
	require.LessOrEqual(t, repRacing.Racing.TotalRuns*3, repRacing.Racing.FullRuns)
}

// TestRacing_EliminationHistoryStructure verifies that elimination rounds, difference stats,
// and speedup are accurately populated in Report.Racing and ReportSet.Racing.
func TestRacing_EliminationHistoryStructure(t *testing.T) {
	exp := loanExperiment(t, loanDir, 200)
	exp.Def.Policies = model.Policies{
		Search:  "racing",
		MinRuns: 10,
		Factor:  2.0,
		Alpha:   0.05,
		Candidates: &model.Policies{
			Search: "exhaustive",
		},
	}

	rep, err := Run(context.Background(), exp, exp.PolicySource(), Options{All: true, Workers: 2})
	require.NoError(t, err)

	rc := rep.Racing
	require.NotNil(t, rc)
	require.Positive(t, rc.TotalRuns)
	require.Positive(t, rc.FullRuns)
	require.Greater(t, rc.Speedup, 1.0)
	require.NotEmpty(t, rc.EliminationHistory)
	require.Len(t, rc.Worlds, len(exp.Worlds))

	// Verify each round's elimination history
	for _, round := range rc.EliminationHistory {
		require.GreaterOrEqual(t, round.RunsPerCandidate, 10)
		require.Positive(t, round.ActiveCandidates)
		require.Len(t, round.DifferenceStats, round.ActiveCandidates)

		// Check difference statistics
		hasLeader := false
		for _, ds := range round.DifferenceStats {
			require.NotEmpty(t, ds.PolicyID)
			if ds.MeanDiff == 0 && ds.StdErr == 0 && ds.LowerBound == 0 {
				hasLeader = true
				require.False(t, ds.Eliminated)
			}
			if ds.Eliminated {
				require.NotEmpty(t, ds.Reason)
			}
		}
		require.True(t, hasLeader, "round must have leader policy stats")
	}

	// Verify Reports.Racing matches rep.Racing when reports requested
	if rep.Reports != nil {
		require.Equal(t, rc, rep.Reports.Racing)
	}
}

// TestRacing_WorkerCountInvariance verifies that racing execution is 100% deterministic
// and invariant to worker count (Workers: 1 vs Workers: 4).
func TestRacing_WorkerCountInvariance(t *testing.T) {
	exp1 := loanExperiment(t, loanDir, 200)
	exp1.Def.Policies = model.Policies{
		Search:  "racing",
		MinRuns: 10,
		Factor:  2.0,
		Alpha:   0.05,
	}

	exp4 := loanExperiment(t, loanDir, 200)
	exp4.Def.Policies = model.Policies{
		Search:  "racing",
		MinRuns: 10,
		Factor:  2.0,
		Alpha:   0.05,
	}

	r1, err := Run(context.Background(), exp1, exp1.PolicySource(), Options{Workers: 1, All: true})
	require.NoError(t, err)

	r4, err := Run(context.Background(), exp4, exp4.PolicySource(), Options{Workers: 4, All: true})
	require.NoError(t, err)

	require.Equal(t, r1.Racing.TotalRuns, r4.Racing.TotalRuns)
	require.Equal(t, r1.Racing.FullRuns, r4.Racing.FullRuns)
	require.Equal(t, r1.Racing.Speedup, r4.Racing.Speedup)
	require.Equal(t, len(r1.Racing.EliminationHistory), len(r4.Racing.EliminationHistory))

	for i := range r1.Racing.EliminationHistory {
		h1 := r1.Racing.EliminationHistory[i]
		h4 := r4.Racing.EliminationHistory[i]
		require.Equal(t, h1.Round, h4.Round)
		require.Equal(t, h1.RunsPerCandidate, h4.RunsPerCandidate)
		require.Equal(t, h1.ActiveCandidates, h4.ActiveCandidates)
		require.Equal(t, h1.Eliminated, h4.Eliminated)
		require.Equal(t, len(h1.DifferenceStats), len(h4.DifferenceStats))
		for j := range h1.DifferenceStats {
			require.Equal(t, h1.DifferenceStats[j].PolicyID, h4.DifferenceStats[j].PolicyID)
			require.InDelta(t, h1.DifferenceStats[j].MeanDiff, h4.DifferenceStats[j].MeanDiff, 1e-12)
			require.InDelta(t, h1.DifferenceStats[j].StdErr, h4.DifferenceStats[j].StdErr, 1e-12)
			require.InDelta(t, h1.DifferenceStats[j].LowerBound, h4.DifferenceStats[j].LowerBound, 1e-12)
			require.Equal(t, h1.DifferenceStats[j].Eliminated, h4.DifferenceStats[j].Eliminated)
			require.Equal(t, h1.DifferenceStats[j].Reason, h4.DifferenceStats[j].Reason)
		}
	}

	for w := range r1.Worlds {
		require.Equal(t, r1.Worlds[w].Policies[0].PolicyID, r4.Worlds[w].Policies[0].PolicyID)
		require.InDelta(t, r1.Worlds[w].Policies[0].Objective.Mean, r4.Worlds[w].Policies[0].Objective.Mean, 1e-12)
	}
}
