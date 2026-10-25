export type TournamentRow = {
  policyId: string;
  rank: number;
  winRate: number; // 0-100
  meanDiffCI?: [number, number]; // [lower, upper]
};

interface Props {
  rows: TournamentRow[];
}

export default function TournamentTable({ rows }: Props) {
  // Sort rows by rank
  const sortedRows = [...rows].sort((a, b) => a.rank - b.rank);

  return (
    <div data-testid="tournament-table" style={{ width: '100%', overflowX: 'auto', background: '#fff', border: '1px solid #ddd' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left', fontSize: '0.9rem' }}>
        <thead>
          <tr style={{ borderBottom: '2px solid #ddd', background: '#f5f5f5' }}>
            <th style={{ padding: '8px' }}>Rank</th>
            <th style={{ padding: '8px' }}>Policy ID</th>
            <th style={{ padding: '8px' }}>Win Rate</th>
            <th style={{ padding: '8px' }}>Mean Diff (95% CI)</th>
          </tr>
        </thead>
        <tbody>
          {sortedRows.length === 0 ? (
            <tr><td colSpan={4} style={{ padding: '8px', textAlign: 'center', color: '#666' }}>No tournament data available</td></tr>
          ) : (
            sortedRows.map(row => (
              <tr key={row.policyId} style={{ borderBottom: '1px solid #eee' }}>
                <td style={{ padding: '8px', fontWeight: 'bold' }}>{row.rank}</td>
                <td style={{ padding: '8px' }}>{row.policyId}</td>
                <td style={{ padding: '8px' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                    <span style={{ width: '40px' }}>{row.winRate.toFixed(1)}%</span>
                    <div style={{ flex: 1, background: '#e0e0e0', height: '6px', borderRadius: '3px', minWidth: '50px', maxWidth: '100px' }}>
                      <div style={{ background: '#4caf50', height: '100%', borderRadius: '3px', width: `${row.winRate}%` }} />
                    </div>
                  </div>
                </td>
                <td style={{ padding: '8px' }}>
                  {row.meanDiffCI ? `[${row.meanDiffCI[0].toFixed(2)}, ${row.meanDiffCI[1].toFixed(2)}]` : 'N/A'}
                </td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}
