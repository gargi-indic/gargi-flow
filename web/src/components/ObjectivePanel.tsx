import { useState } from 'react';
import { setObjective } from '../sim/simDoc';

type Props = {
  text: string;
  objective: { name?: string; value?: string; maximize?: string; requirements?: string[] };
  onChange: (newText: string) => void;
};

export default function ObjectivePanel({ text, objective, onChange }: Props) {
  const [name, setName] = useState(objective.name || '');
  const [value, setValue] = useState(objective.value || '');
  const [maximize, setMaximize] = useState(objective.maximize || 'mean');
  const [requirements, setRequirements] = useState((objective.requirements || []).join('\n'));

  // Sync state if objective props change - but avoid setting state synchronously in an effect
  // We can track the previous objective object and update state during render if it changed.
  const [prevObjective, setPrevObjective] = useState(objective);
  if (objective !== prevObjective) {
    setName(objective.name || '');
    setValue(objective.value || '');
    setMaximize(objective.maximize || 'mean');
    setRequirements((objective.requirements || []).join('\n'));
    setPrevObjective(objective);
  }

  const handleApply = () => {
    onChange(
      setObjective(text, {
        name: name || undefined,
        value: value || undefined,
        maximize: maximize || undefined,
        requirements: requirements.split('\n').filter((r) => r.trim() !== '').length > 0
          ? requirements.split('\n').filter((r) => r.trim() !== '')
          : undefined,
      })
    );
  };

  return (
    <div data-testid="sim-objective" style={{ padding: '10px' }}>
      <h3>Objective</h3>
      <div style={{ marginBottom: '8px' }}>
        <label style={{ display: 'block', marginBottom: '4px' }}>Name</label>
        <input value={name} onChange={(e) => setName(e.target.value)} style={{ width: '100%' }} />
      </div>
      <div style={{ marginBottom: '8px' }}>
        <label style={{ display: 'block', marginBottom: '4px' }}>Value (CEL)</label>
        <textarea
          value={value}
          onChange={(e) => setValue(e.target.value)}
          style={{ width: '100%', height: '60px', fontFamily: 'monospace' }}
        />
      </div>
      <div style={{ marginBottom: '8px' }}>
        <label style={{ display: 'block', marginBottom: '4px' }}>Maximize</label>
        <select value={maximize} onChange={(e) => setMaximize(e.target.value)} style={{ width: '100%' }}>
          <option value="mean">mean</option>
          <option value="min">min</option>
          <option value="p10">p10</option>
          <option value="p50">p50</option>
          <option value="p90">p90</option>
        </select>
      </div>
      <div style={{ marginBottom: '8px' }}>
        <label style={{ display: 'block', marginBottom: '4px' }}>Requirements (one per line)</label>
        <textarea
          value={requirements}
          onChange={(e) => setRequirements(e.target.value)}
          style={{ width: '100%', height: '60px', fontFamily: 'monospace' }}
        />
      </div>
      <button data-testid="objective-apply" onClick={handleApply}>Apply</button>
    </div>
  );
}
