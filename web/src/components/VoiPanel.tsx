import React from 'react';
import type { VoiReportEntry } from '../sim/results';

export default function VoiPanel({ voiData }: { voiData?: VoiReportEntry[] }) {
  if (!voiData || voiData.length === 0) return <div>No VOI data available</div>;

  return (
    <div data-testid="sim-voi" style={{ padding: '16px', overflowY: 'auto' }}>
      <h3>Value of Information (VOI)</h3>
      <table style={{ width: '100%', borderCollapse: 'collapse', marginTop: '16px' }}>
        <thead>
          <tr>
            <th style={thStyle}>World</th>
            <th style={thStyle}>Node</th>
            <th style={thStyle}>With Observation EV</th>
            <th style={thStyle}>Without Observation EV</th>
            <th style={thStyle}>VOI</th>
          </tr>
        </thead>
        <tbody>
          {voiData.map((item, i) => (
            <tr key={`${item.world}-${item.node}-${i}`}>
              <td style={tdStyle}>{item.world}</td>
              <td style={tdStyle} data-testid={`voi-node-${item.node}`}>
                <strong>{item.node}</strong>
              </td>
              <td style={tdStyle}>
                {item.withObservation.ev.toFixed(1)}
                <div style={{ fontSize: '0.8em', color: '#666' }}>{item.withObservation.policyId}</div>
              </td>
              <td style={tdStyle}>
                {item.withoutObservation.ev.toFixed(1)}
                <div style={{ fontSize: '0.8em', color: '#666' }}>{item.withoutObservation.policyId}</div>
              </td>
              <td style={tdStyle}>
                <strong style={{ color: '#1976d2' }}>{item.voi.toFixed(1)}</strong>
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
