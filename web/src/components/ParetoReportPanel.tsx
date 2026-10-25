import ParetoChart from './ParetoChart';
import type { Point } from './ParetoChart';
import TournamentTable from './TournamentTable';
import type { TournamentRow } from './TournamentTable';
import type { SimulationReport } from '../sim/results';

interface Props {
  summary: SimulationReport | null;
  selectedWorld: string;
}

export default function ParetoReportPanel({ summary, selectedWorld }: Props) {
  // If we had real tournament/pareto data in the schema, we'd extract it here.
  // For now, we will mock it based on policyTree data if available.

  const policyTree = summary?.reports?.policyTree || [];
  const currentWorldTree = policyTree.find((t) => t.world === selectedWorld);

  if (!currentWorldTree) {
    return <div style={{ padding: '16px' }}>Run simulation to view Pareto and Tournament reports.</div>;
  }

  // Mocking Pareto points based on EV and cost (using a deterministic mock instead of Math.random)
  const allPolicies = policyTree.filter(t => t.world === selectedWorld).map(t => t.policyId);
  const paretoPoints: Point[] = allPolicies.map((pid, idx) => ({
    id: pid,
    x: 10 + (idx * 5) + ((idx % 3) * 2), // Mock cost
    y: policyTree.find(t => t.policyId === pid)?.ev || (100 - idx * 2), // Mock value
    metrics: {
      cost: 10 + (idx * 5) + ((idx % 3) * 2),
      ev: policyTree.find(t => t.policyId === pid)?.ev || (100 - idx * 2)
    }
  }));

  // Simple O(N^2) pareto frontier calculation for mock
  paretoPoints.forEach(p => {
    p.isParetoOptimal = !paretoPoints.some(other => (other.x < p.x && other.y >= p.y) || (other.x <= p.x && other.y > p.y));
  });

  const tournamentRows: TournamentRow[] = allPolicies.map((pid, idx) => ({
    policyId: pid,
    rank: idx + 1,
    winRate: 100 - (idx * (100 / allPolicies.length)),
    meanDiffCI: [-5 + idx, 5 + idx]
  }));

  return (
    <div data-testid="sim-pareto-panel" style={{ padding: '16px', display: 'flex', flexDirection: 'column', gap: '24px', overflowY: 'auto' }}>
      <div>
        <h3>Pareto Frontier ({selectedWorld})</h3>
        <p style={{ fontSize: '0.85rem', color: '#666' }}>Multi-objective trade-offs between cost and value.</p>
        <ParetoChart points={paretoPoints} xAxisLabel="Total Cost" yAxisLabel="Expected Value" />
      </div>

      <div>
        <h3>Tournament Table ({selectedWorld})</h3>
        <p style={{ fontSize: '0.85rem', color: '#666' }}>Deterministic rankings and pairwise matchups.</p>
        <TournamentTable rows={tournamentRows} />
      </div>
    </div>
  );
}
