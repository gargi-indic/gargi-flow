import React, { useState } from 'react';
import { setArrivals } from '../sim/simDoc';

type Props = {
  text: string;
  arrivals: { distribution?: string; parameters?: Record<string, number> };
  onChange: (newText: string) => void;
};

const DISTRIBUTION_PARAMS: Record<string, string[]> = {
  poisson: ['rate'],
  constant: ['interval'],
  exponential: ['rate'],
};

export default function ArrivalsPanel({ text, arrivals, onChange }: Props) {
  const dist = arrivals.distribution || 'poisson';
  const params = arrivals.parameters || {};
  
  // Local state for params so user can type freely, sync on apply
  const [localParams, setLocalParams] = useState<Record<string, number>>(params);

  // Sync if external arrivals prop changes
  const [prevParams, setPrevParams] = useState(params);
  if (params !== prevParams) {
    setLocalParams(params);
    setPrevParams(params);
  }

  const handleDistChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const newDist = e.target.value;
    const newParams: Record<string, number> = {};
    (DISTRIBUTION_PARAMS[newDist] || []).forEach(p => newParams[p] = 0);
    setLocalParams(newParams);
    onChange(setArrivals(text, { distribution: newDist, parameters: newParams }));
  };

  const handleParamChange = (paramName: string, value: string) => {
    const num = Number(value);
    setLocalParams({ ...localParams, [paramName]: isNaN(num) ? 0 : num });
  };

  const handleApply = () => {
    onChange(setArrivals(text, { distribution: dist, parameters: localParams }));
  };

  return (
    <div data-testid="sim-arrivals" style={{ padding: '10px', borderBottom: '1px solid #ccc' }}>
      <h3>Arrivals (Mode B)</h3>
      <div style={{ marginBottom: '8px' }}>
        <label style={{ display: 'block', marginBottom: '4px' }}>Distribution</label>
        <select value={dist} onChange={handleDistChange} style={{ width: '100%' }}>
          <option value="poisson">Poisson</option>
          <option value="constant">Constant</option>
          <option value="exponential">Exponential</option>
        </select>
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: '8px', marginBottom: '8px' }}>
        {(DISTRIBUTION_PARAMS[dist] || []).map(p => (
          <div key={p}>
            <label style={{ display: 'block', fontSize: '0.85rem' }}>{p}</label>
            <input
              type="number"
              step="any"
              value={localParams[p] !== undefined ? localParams[p] : ''}
              onChange={(e) => handleParamChange(p, e.target.value)}
              style={{ width: '100%', padding: '4px', boxSizing: 'border-box' }}
            />
          </div>
        ))}
      </div>
      <button onClick={handleApply}>Apply</button>
    </div>
  );
}
