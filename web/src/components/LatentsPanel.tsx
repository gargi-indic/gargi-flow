import { useState } from 'react';
import * as yaml from 'js-yaml';
import { setLatent, removeLatent } from '../sim/simDoc';

type Props = {
  text: string;
  latents: Record<string, unknown>;
  onChange: (newText: string) => void;
};

export default function LatentsPanel({ text, latents, onChange }: Props) {
  const [newLatentName, setNewLatentName] = useState('');

  const handleAddLatent = () => {
    if (newLatentName.trim()) {
      onChange(setLatent(text, newLatentName.trim(), '{}'));
      setNewLatentName('');
    }
  };

  return (
    <div data-testid="sim-latents" style={{ padding: '10px', borderBottom: '1px solid #ccc' }}>
      <h3>Latents</h3>
      {Object.entries(latents).map(([name, spec]) => (
        <LatentEditor
          key={name}
          text={text}
          name={name}
          spec={spec}
          onChange={onChange}
        />
      ))}
      <div style={{ marginTop: '10px' }}>
        <input
          value={newLatentName}
          onChange={(e) => setNewLatentName(e.target.value)}
          placeholder="New latent name"
        />
        <button onClick={handleAddLatent}>Add latent</button>
      </div>
    </div>
  );
}

function LatentEditor({ text, name, spec, onChange }: { text: string; name: string; spec: unknown; onChange: (text: string) => void }) {
  const [specText, setSpecText] = useState(() => {
    try {
      return yaml.dump(spec);
    } catch {
      return '{}';
    }
  });
  const [error, setError] = useState<string | null>(null);

  // Sync state if spec prop changes (from external text editor change)
  const [prevSpec, setPrevSpec] = useState(spec);
  if (spec !== prevSpec) {
    try {
      setSpecText(yaml.dump(spec));
      setError(null);
    } catch {
      // ignore
    }
    setPrevSpec(spec);
  }

  const handleApply = () => {
    try {
      yaml.load(specText); // validate
      setError(null);
      onChange(setLatent(text, name, specText));
    } catch (e) {
      setError(String(e));
    }
  };

  return (
    <div style={{ marginBottom: '10px', padding: '10px', border: '1px solid #ccc' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between' }}>
        <strong>{name}</strong>
        <button onClick={() => onChange(removeLatent(text, name))}>Remove</button>
      </div>
      <textarea
        data-testid={`latent-spec-${name}`}
        value={specText}
        onChange={(e) => {
          setSpecText(e.target.value);
          setError(null);
        }}
        style={{ width: '100%', height: '80px', fontFamily: 'monospace' }}
      />
      {error && <div style={{ color: 'red', fontSize: '12px' }}>{error}</div>}
      <button onClick={handleApply}>Apply</button>
    </div>
  );
}
