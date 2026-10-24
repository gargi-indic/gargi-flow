import type { SimulationReport } from '../sim/results';

interface ResultsBarProps {
  summary: SimulationReport;
  world: string;
  onWorldChange: (world: string) => void;
}

export default function ResultsBar({ summary, world, onWorldChange }: ResultsBarProps) {
  const policyTree = summary.reports?.policyTree || [];
  
  const currentWorldTree = policyTree.find((t) => t.world === world);
  
  return (
    <div data-testid="sim-results" style={{ padding: '8px', background: '#e0f7fa', display: 'flex', gap: '16px', alignItems: 'center', borderBottom: '1px solid #ccc' }}>
      <div style={{ display: 'flex', gap: '8px' }}>
        <strong>Worlds:</strong>
        {policyTree.map((t) => (
          <button
            key={t.world}
            data-testid={`results-world-${t.world}`}
            onClick={() => onWorldChange(t.world)}
            style={{
              padding: '4px 8px',
              border: '1px solid #ccc',
              borderRadius: '4px',
              background: t.world === world ? '#00796b' : 'white',
              color: t.world === world ? 'white' : 'black',
              cursor: 'pointer',
            }}
          >
            {t.world}
          </button>
        ))}
      </div>
      
      {currentWorldTree && (
        <div>
          <strong>Best Policy:</strong> {currentWorldTree.policyId} (EV: {currentWorldTree.ev})
        </div>
      )}
    </div>
  );
}
