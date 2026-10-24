import React from 'react';
import type { RegretData } from '../sim/results';

export default function RegretPanel({ regret }: { regret?: RegretData }) {
  if (!regret) return <div>No regret data available</div>;

  return (
    <div data-testid="sim-regret" style={{ padding: '16px', overflowY: 'auto' }}>
      <h3>Regret Matrix</h3>
      <table style={{ width: '100%', borderCollapse: 'collapse', marginTop: '16px' }}>
        <thead>
          <tr>
            <th style={thStyle}>Policy (Tuned For)</th>
            {regret.worlds.map((w) => (
              <th key={w} style={thStyle}>
                World: {w}
              </th>
            ))}
            <th style={thStyle}>Max Regret</th>
          </tr>
        </thead>
        <tbody>
          {regret.rows.map((row) => (
            <tr key={row.policyId}>
              <td style={tdStyle}>
                <strong>{row.tunedFor}</strong>
                <br />
                <span style={{ fontSize: '0.8em', color: '#666' }}>{row.policyId}</span>
              </td>
              {regret.worlds.map((w) => {
                const cell = row.cells.find((c) => c.world === w);
                return (
                  <td key={w} style={tdStyle}>
                    {cell ? (
                      <div>
                        <div>Value: {cell.value.toFixed(1)}</div>
                        <div style={{ color: cell.regret > 0 ? '#d32f2f' : '#388e3c', fontWeight: 'bold' }}>
                          Regret: {cell.regret.toFixed(1)}
                        </div>
                      </div>
                    ) : (
                      '-'
                    )}
                  </td>
                );
              })}
              <td style={tdStyle}>
                <strong>{row.maxRegret.toFixed(1)}</strong>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const thStyle: React.CSSProperties = {
  border: '1px solid #ddd',
  padding: '8px',
  textAlign: 'left',
  background: '#f5f5f5',
};

const tdStyle: React.CSSProperties = {
  border: '1px solid #ddd',
  padding: '8px',
  textAlign: 'left',
};
