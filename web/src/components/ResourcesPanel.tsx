import { useState } from 'react';
import { setResources } from '../sim/simDoc';

type Props = {
  text: string;
  resources: Record<string, number>;
  onChange: (newText: string) => void;
};

export default function ResourcesPanel({ text, resources, onChange }: Props) {
  const [newResourceName, setNewResourceName] = useState('');

  const handleAdd = () => {
    if (newResourceName.trim() && resources[newResourceName.trim()] === undefined) {
      const newResources = { ...resources, [newResourceName.trim()]: 1 };
      onChange(setResources(text, newResources));
      setNewResourceName('');
    }
  };

  const handleRemove = (name: string) => {
    const newResources = { ...resources };
    delete newResources[name];
    onChange(setResources(text, newResources));
  };

  const handleChangeCapacity = (name: string, value: string) => {
    const num = parseInt(value, 10);
    const newResources = { ...resources, [name]: isNaN(num) ? 0 : num };
    onChange(setResources(text, newResources));
  };

  return (
    <div data-testid="sim-resources" style={{ padding: '10px', borderBottom: '1px solid #ccc' }}>
      <h3>Resources</h3>
      {Object.entries(resources).map(([name, capacity]) => (
        <div key={name} style={{ display: 'flex', alignItems: 'center', marginBottom: '8px', gap: '8px' }}>
          <strong style={{ minWidth: '100px' }}>{name}</strong>
          <label>Capacity:</label>
          <input
            type="number"
            value={capacity}
            onChange={(e) => handleChangeCapacity(name, e.target.value)}
            style={{ width: '60px' }}
          />
          <button onClick={() => handleRemove(name)} style={{ marginLeft: 'auto' }}>Remove</button>
        </div>
      ))}
      <div style={{ marginTop: '10px', display: 'flex', gap: '4px' }}>
        <input
          value={newResourceName}
          onChange={(e) => setNewResourceName(e.target.value)}
          placeholder="New resource"
          style={{ flex: 1 }}
        />
        <button onClick={handleAdd}>Add</button>
      </div>
    </div>
  );
}
