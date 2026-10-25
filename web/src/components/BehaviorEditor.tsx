import React, { useMemo } from 'react';

type DistributionType = 'constant' | 'exponential' | 'normal' | 'lognormal' | 'triangular' | 'bernoulli';

interface BehaviorConfig {
  distribution?: DistributionType;
  parameters?: Record<string, number>;
  resource?: string;
}

interface Props {
  config: BehaviorConfig;
  onChange: (config: BehaviorConfig) => void;
  availableResources?: string[];
}

const DISTRIBUTION_PARAMS: Record<DistributionType, string[]> = {
  constant: ['value'],
  exponential: ['rate'],
  normal: ['mean', 'sd'],
  lognormal: ['meanlog', 'sdlog'],
  triangular: ['min', 'mode', 'max'],
  bernoulli: ['p'],
};

export default function BehaviorEditor({ config, onChange, availableResources = [] }: Props) {
  const dist = config.distribution || 'constant';
  
  // useMemo for params to avoid recreating the object every render
  const params = useMemo(() => config.parameters || {}, [config.parameters]);

  const handleDistChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const newDist = e.target.value as DistributionType;
    const newParams: Record<string, number> = {};
    DISTRIBUTION_PARAMS[newDist].forEach(p => newParams[p] = 0);
    onChange({ ...config, distribution: newDist, parameters: newParams });
  };

  const handleParamChange = (paramName: string, value: string) => {
    const num = Number(value);
    onChange({
      ...config,
      parameters: {
        ...params,
        [paramName]: isNaN(num) ? 0 : num
      }
    });
  };

  const handleResourceChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    onChange({ ...config, resource: e.target.value || undefined });
  };

  const previewSvg = useMemo(() => {
    // Generate a simple SVG distribution preview based on current parameters
    const width = 200;
    const height = 100;
    
    // Very simple mock shapes for visual feedback
    let pathD = '';
    if (dist === 'constant') {
      pathD = `M ${width/2},${height} L ${width/2},10`;
    } else if (dist === 'normal' || dist === 'lognormal') {
      // bell curve
      pathD = `M 10,${height} Q ${width/2},-50 ${width-10},${height}`;
    } else if (dist === 'exponential') {
      pathD = `M 10,10 Q ${width/3},${height-10} ${width-10},${height}`;
    } else if (dist === 'triangular') {
      pathD = `M 10,${height} L ${width/2},10 L ${width-10},${height}`;
    } else if (dist === 'bernoulli') {
      const p = params.p || 0.5;
      const h1 = height - (p * height);
      const h2 = height - ((1-p) * height);
      pathD = `M 50,${height} L 50,${h1} M 150,${height} L 150,${h2}`;
    }

    return (
      <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} style={{ border: '1px solid #ddd', background: '#fafafa' }}>
        {dist !== 'bernoulli' && dist !== 'constant' ? (
          <path d={pathD} fill="none" stroke="#2196f3" strokeWidth="2" />
        ) : (
          <path d={pathD} fill="none" stroke="#2196f3" strokeWidth="10" strokeLinecap="square" />
        )}
      </svg>
    );
  }, [dist, params]);

  return (
    <div data-testid="behavior-editor" style={{ display: 'flex', flexDirection: 'column', gap: '10px' }}>
      <div>
        <label style={{ display: 'block', fontSize: '0.85rem' }}>Resource (Optional)</label>
        <select value={config.resource || ''} onChange={handleResourceChange} style={{ width: '100%', padding: '4px' }}>
          <option value="">None</option>
          {availableResources.map(r => (
            <option key={r} value={r}>{r}</option>
          ))}
          {!availableResources.includes(config.resource || '') && config.resource && (
            <option value={config.resource}>{config.resource}</option>
          )}
        </select>
      </div>

      <div>
        <label style={{ display: 'block', fontSize: '0.85rem' }}>Distribution</label>
        <select value={dist} onChange={handleDistChange} style={{ width: '100%', padding: '4px' }}>
          <option value="constant">Constant</option>
          <option value="exponential">Exponential</option>
          <option value="normal">Normal</option>
          <option value="lognormal">Log-Normal</option>
          <option value="triangular">Triangular</option>
          <option value="bernoulli">Bernoulli</option>
        </select>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
        {DISTRIBUTION_PARAMS[dist].map(p => (
          <div key={p}>
            <label style={{ display: 'block', fontSize: '0.75rem' }}>{p}</label>
            <input
              type="number"
              step="any"
              value={params[p] !== undefined ? params[p] : ''}
              onChange={(e) => handleParamChange(p, e.target.value)}
              style={{ width: '100%', padding: '4px', boxSizing: 'border-box' }}
            />
          </div>
        ))}
      </div>

      <div style={{ marginTop: '10px' }}>
        <div style={{ fontSize: '0.85rem', marginBottom: '4px' }}>Preview</div>
        {previewSvg}
      </div>
    </div>
  );
}
