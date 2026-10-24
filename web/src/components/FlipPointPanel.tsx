import React from 'react';
import type { FlipPointReportEntry } from '../sim/results';

export default function FlipPointPanel({ flipData }: { flipData?: FlipPointReportEntry[] }) {
  if (!flipData || flipData.length === 0) return <div>No Flip Point data available</div>;

  return (
    <div data-testid="sim-flip-point" style={{ padding: '16px', overflowY: 'auto' }}>
      <h3>Flip Points</h3>
      {flipData.map((item, index) => (
        <div key={index} style={{ marginBottom: '24px', padding: '16px', border: '1px solid #ccc', borderRadius: '8px' }}>
          <h4>
            Parameter: <strong>{item.param}</strong> (World: {item.baseWorld})
          </h4>
          <p>
            Bounds: [{item.lo}, {item.hi}]
          </p>

          {item.flips.length > 0 ? (
            <div>
              <h5>Transitions</h5>
              <table style={{ width: '100%', borderCollapse: 'collapse', marginTop: '8px' }}>
                <thead>
                  <tr>
                    <th style={thStyle}>Threshold</th>
                    <th style={thStyle}>From Policy</th>
                    <th style={thStyle}>To Policy</th>
                  </tr>
                </thead>
                <tbody>
                  {item.flips.map((flip, i) => {
                    const threshold = (flip.left.value + flip.right.value) / 2;
                    return (
                      <tr key={i}>
                        <td style={tdStyle}>{threshold.toFixed(4)}</td>
                        <td style={tdStyle}>
                          <div style={{ fontFamily: 'monospace', fontSize: '0.9em', whiteSpace: 'pre-wrap' }}>
                            {flip.from}
                          </div>
                          <div style={{ fontSize: '0.8em', color: '#666', marginTop: '4px' }}>
                            {flip.left.policyId}
                          </div>
                        </td>
                        <td style={tdStyle}>
                          <div style={{ fontFamily: 'monospace', fontSize: '0.9em', whiteSpace: 'pre-wrap' }}>
                            {flip.to}
                          </div>
                          <div style={{ fontSize: '0.8em', color: '#666', marginTop: '4px' }}>
                            {flip.right.policyId}
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          ) : (
            <p>No policy flips in this range.</p>
          )}
        </div>
      ))}
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
  verticalAlign: 'top',
};
