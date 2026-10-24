import { useState, useMemo } from 'react';
import { addWorld, removeWorld, setWorldParam } from '../sim/simDoc';

type Props = {
  text: string;
  worlds: Record<string, Record<string, unknown>>;
  onChange: (newText: string) => void;
};

export default function WorldsPanel({ text, worlds, onChange }: Props) {
  const [newWorldName, setNewWorldName] = useState('');

  const worldNames = useMemo(() => Object.keys(worlds), [worlds]);

  const allParams = useMemo(() => {
    const params = new Set<string>();
    for (const world of Object.values(worlds)) {
      if (world && typeof world === 'object') {
        for (const p of Object.keys(world)) {
          params.add(p);
        }
      }
    }
    return Array.from(params);
  }, [worlds]);

  const handleAddWorld = () => {
    if (newWorldName.trim()) {
      onChange(addWorld(text, newWorldName.trim()));
      setNewWorldName('');
    }
  };

  const handleRemoveWorld = (world: string) => {
    onChange(removeWorld(text, world));
  };

  const handleParamChange = (world: string, param: string, value: string) => {
    onChange(setWorldParam(text, world, param, value));
  };

  return (
    <div data-testid="sim-worlds" style={{ padding: '10px', borderBottom: '1px solid #ccc' }}>
      <h3>Worlds</h3>
      <table style={{ borderCollapse: 'collapse', width: '100%', marginBottom: '10px' }}>
        <thead>
          <tr>
            <th style={{ border: '1px solid #ccc', padding: '4px' }}>Param</th>
            {worldNames.map((w) => (
              <th key={w} style={{ border: '1px solid #ccc', padding: '4px' }}>
                {w}
                <button onClick={() => handleRemoveWorld(w)} style={{ marginLeft: '8px' }}>x</button>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {allParams.map((param) => (
            <tr key={param}>
              <td style={{ border: '1px solid #ccc', padding: '4px' }}>{param}</td>
              {worldNames.map((w) => {
                const val = worlds[w]?.[param] ?? '';
                const strVal = typeof val === 'object' ? JSON.stringify(val) : String(val);
                return (
                  <td key={w} style={{ border: '1px solid #ccc', padding: '4px' }}>
                    <input
                      data-testid={`world-cell-${w}-${param}`}
                      key={`${w}-${strVal}`} defaultValue={strVal}
                      onBlur={(e) => {
                        if (e.target.value !== strVal) {
                          handleParamChange(w, param, e.target.value);
                        }
                      }}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.currentTarget.blur();
                        }
                      }}
                      style={{ width: '100%', boxSizing: 'border-box' }}
                    />
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      <div>
        <input
          value={newWorldName}
          onChange={(e) => setNewWorldName(e.target.value)}
          placeholder="New world name"
        />
        <button data-testid="world-add" onClick={handleAddWorld}>Add world</button>
      </div>
    </div>
  );
}
