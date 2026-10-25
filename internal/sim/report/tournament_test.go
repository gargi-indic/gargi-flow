package report

import (
	"testing"

	"github.com/gargi-indic/gargi-flow/pkg/sim"
	"github.com/stretchr/testify/require"
)

func TestStudentTQuantile(t *testing.T) {
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

	require.Equal(t, 0.0, studentTQuantile(0.025, 0))
}

func TestTournament_SyntheticOrdering(t *testing.T) {
	// Synthetic policies P1 > P2 > P3
	n := 100
	reps1 := make([]float64, n)
	reps2 := make([]float64, n)
	reps3 := make([]float64, n)
	for i := 0; i < n; i++ {
		noise := float64(i%5 - 2)
		reps1[i] = 100.0 + noise
		reps2[i] = 50.0 + noise
		reps3[i] = 10.0 + noise
	}

	table := Table{
		Worlds:   []sim.WorldID{"w1"},
		Policies: []string{"p1", "p2", "p3"},
		Mean:     [][]float64{{100.0}, {50.0}, {10.0}},
		Feasible: [][]bool{{true}, {true}, {true}},
		Replications: [][][]float64{
			{reps1},
			{reps2},
			{reps3},
		},
	}

	tourn, err := NewTournament(table, nil, 0.05)
	require.NoError(t, err)
	require.NotNil(t, tourn)
	require.Equal(t, 0.05, tourn.Alpha)

	// Verify exact ranking order: p1 > p2 > p3
	require.Len(t, tourn.Rankings, 3)
	require.Equal(t, 1, tourn.Rankings[0].Rank)
	require.Equal(t, "p1", tourn.Rankings[0].PolicyID)
	require.Equal(t, 100.0, tourn.Rankings[0].Mean)
	require.Equal(t, 2, tourn.Rankings[0].Wins)
	require.Equal(t, 0, tourn.Rankings[0].Losses)
	require.Equal(t, 0, tourn.Rankings[0].Ties)
	require.Equal(t, 1.0, tourn.Rankings[0].WinRate)

	require.Equal(t, 2, tourn.Rankings[1].Rank)
	require.Equal(t, "p2", tourn.Rankings[1].PolicyID)
	require.Equal(t, 50.0, tourn.Rankings[1].Mean)
	require.Equal(t, 1, tourn.Rankings[1].Wins)
	require.Equal(t, 1, tourn.Rankings[1].Losses)
	require.Equal(t, 0, tourn.Rankings[1].Ties)
	require.Equal(t, 0.5, tourn.Rankings[1].WinRate)

	require.Equal(t, 3, tourn.Rankings[2].Rank)
	require.Equal(t, "p3", tourn.Rankings[2].PolicyID)
	require.Equal(t, 10.0, tourn.Rankings[2].Mean)
	require.Equal(t, 0, tourn.Rankings[2].Wins)
	require.Equal(t, 2, tourn.Rankings[2].Losses)
	require.Equal(t, 0, tourn.Rankings[2].Ties)
	require.Equal(t, 0.0, tourn.Rankings[2].WinRate)

	// Verify Pairwise matrix dimensions: 3x3
	require.Len(t, tourn.Pairwise, 3)
	for i := range tourn.Pairwise {
		require.Len(t, tourn.Pairwise[i], 3)
	}

	// Verify pairwise symmetry across all pairs
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			mAB := tourn.Pairwise[i][j]
			mBA := tourn.Pairwise[j][i]

			// Policy IDs match rankings
			require.Equal(t, tourn.Rankings[i].PolicyID, mAB.PolicyA)
			require.Equal(t, tourn.Rankings[j].PolicyID, mAB.PolicyB)

			// Mean diff symmetry: d_{A,B} = -d_{B,A}
			require.InDelta(t, mAB.MeanDiff, -mBA.MeanDiff, 1e-9)

			// CI symmetry: CI_{A,B} = [-UB_{B,A}, -LB_{B,A}]
			require.InDelta(t, mAB.CI[0], -mBA.CI[1], 1e-9)
			require.InDelta(t, mAB.CI[1], -mBA.CI[0], 1e-9)

			// Win rate symmetry: WinRate(A,B) + WinRate(B,A) = 1.0
			require.InDelta(t, 1.0, mAB.WinRate+mBA.WinRate, 1e-9)

			// Diagonal checks (A vs A)
			if i == j {
				require.Equal(t, 0.0, mAB.MeanDiff)
				require.Equal(t, [2]float64{0, 0}, mAB.CI)
				require.Equal(t, 0.5, mAB.WinRate)
				require.False(t, mAB.Significant)
				require.Empty(t, mAB.Winner)
			}
		}
	}

	// Verify p1 beats p2 and p2 beats p3
	// Pairwise[0][1] is p1 vs p2
	p1vsp2 := tourn.Pairwise[0][1]
	require.Equal(t, 50.0, p1vsp2.MeanDiff)
	require.Equal(t, 1.0, p1vsp2.WinRate)
	require.True(t, p1vsp2.Significant)
	require.Equal(t, "p1", p1vsp2.Winner)
	require.True(t, p1vsp2.CI[0] > 0)

	// Pairwise[1][2] is p2 vs p3
	p2vsp3 := tourn.Pairwise[1][2]
	require.Equal(t, 40.0, p2vsp3.MeanDiff)
	require.Equal(t, 1.0, p2vsp3.WinRate)
	require.True(t, p2vsp3.Significant)
	require.Equal(t, "p2", p2vsp3.Winner)
	require.True(t, p2vsp3.CI[0] > 0)
}

func TestTournament_SignificanceDetection(t *testing.T) {
	n := 20
	// Case 1: Significant difference
	repsA := make([]float64, n)
	repsB := make([]float64, n)
	for i := 0; i < n; i++ {
		repsA[i] = 100.0 + float64(i)
		repsB[i] = 50.0 + float64(i)
	}
	mSig, err := ComputeMatchup("A", "B", repsA, repsB, 0.05)
	require.NoError(t, err)
	require.True(t, mSig.Significant)
	require.Equal(t, "A", mSig.Winner)
	require.True(t, mSig.CI[0] > 0)

	// Case 2: Overlapping / Not significant difference (noise crosses 0)
	repsC := make([]float64, n)
	repsD := make([]float64, n)
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			repsC[i] = 10.0
			repsD[i] = 20.0
		} else {
			repsC[i] = 20.0
			repsD[i] = 10.0
		}
	}
	mNotSig, err := ComputeMatchup("C", "D", repsC, repsD, 0.05)
	require.NoError(t, err)
	require.False(t, mNotSig.Significant)
	require.Empty(t, mNotSig.Winner)
	require.True(t, mNotSig.CI[0] <= 0 && mNotSig.CI[1] >= 0)
	require.Equal(t, 0.5, mNotSig.WinRate)

	// Case 3: Alpha widening: 99% CI (alpha=0.01) is strictly wider than 90% CI (alpha=0.10)
	m99, err := ComputeMatchup("C", "D", repsC, repsD, 0.01)
	require.NoError(t, err)
	m90, err := ComputeMatchup("C", "D", repsC, repsD, 0.10)
	require.NoError(t, err)
	width99 := m99.CI[1] - m99.CI[0]
	width90 := m90.CI[1] - m90.CI[0]
	require.True(t, width99 > width90, "99%% CI width %v should be greater than 90%% CI width %v", width99, width90)
}

func TestTournament_TiesAndTieBreaking(t *testing.T) {
	// Two policies with identical means but different win rates against others
	pA := []float64{10, 10, 10, 10}
	pB := []float64{10, 10, 10, 10}
	pC := []float64{5, 5, 5, 5}

	tourn, err := NewTournamentFromReplications(
		[]string{"b_pol", "a_pol", "c_pol"},
		[]float64{10.0, 10.0, 5.0},
		[][]float64{pB, pA, pC},
		0.05,
	)
	require.NoError(t, err)

	// Both a_pol and b_pol have mean 10.0, both beat c_pol, tie with each other
	// Primary: Mean (10.0 vs 5.0 -> a_pol & b_pol beat c_pol)
	// Secondary: WinRate (both have same win rate)
	// Tertiary: PolicyID ascending -> "a_pol" < "b_pol"
	require.Equal(t, "a_pol", tourn.Rankings[0].PolicyID)
	require.Equal(t, 1, tourn.Rankings[0].Rank)
	require.Equal(t, "b_pol", tourn.Rankings[1].PolicyID)
	require.Equal(t, 2, tourn.Rankings[1].Rank)
	require.Equal(t, "c_pol", tourn.Rankings[2].PolicyID)
	require.Equal(t, 3, tourn.Rankings[2].Rank)
}

func TestTournament_MultiWorldWithPrior(t *testing.T) {
	// World 1 (prior 0.7) and World 2 (prior 0.3)
	// Policy A: W1 mean=10, W2 mean=20 -> overall expected = 0.7*10 + 0.3*20 = 7 + 6 = 13
	// Policy B: W1 mean=15, W2 mean=5  -> overall expected = 0.7*15 + 0.3*5 = 10.5 + 1.5 = 12
	table := Table{
		Worlds:   []sim.WorldID{"w1", "w2"},
		Policies: []string{"A", "B"},
		Mean:     [][]float64{{10.0, 20.0}, {15.0, 5.0}},
		Feasible: [][]bool{{true, true}, {true, true}},
		Replications: [][][]float64{
			{{10, 10}, {20, 20}},
			{{15, 15}, {5, 5}},
		},
	}

	tourn, err := NewTournament(table, []float64{0.7, 0.3}, 0.05)
	require.NoError(t, err)

	// Overall expected mean determines ranking: A (13.0) > B (12.0)
	require.Equal(t, "A", tourn.Rankings[0].PolicyID)
	require.InDelta(t, 13.0, tourn.Rankings[0].Mean, 1e-9)
	require.Equal(t, "B", tourn.Rankings[1].PolicyID)
	require.InDelta(t, 12.0, tourn.Rankings[1].Mean, 1e-9)
}

func TestTournament_ValidationErrors(t *testing.T) {
	_, err := ComputeMatchup("A", "B", []float64{}, []float64{}, 0.05)
	require.Error(t, err)

	_, err = ComputeMatchup("A", "B", []float64{1, 2}, []float64{1}, 0.05)
	require.Error(t, err)

	_, err = ComputeMatchup("A", "B", []float64{1, 2}, []float64{1, 2}, -0.1)
	require.Error(t, err)

	_, err = ComputeMatchup("A", "B", []float64{1, 2}, []float64{1, 2}, 1.5)
	require.Error(t, err)

	table := Table{
		Worlds:   []sim.WorldID{"w1"},
		Policies: []string{"A"},
		Mean:     [][]float64{{10}},
		Feasible: [][]bool{{true}},
	}
	// Missing replications
	_, err = NewTournament(table, nil, 0.05)
	require.Error(t, err)
}
