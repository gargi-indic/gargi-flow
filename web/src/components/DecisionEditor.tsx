import { useState } from 'react';

interface DecisionEditorProps {
  nodeId: string;
  initialOptions: string[];
  initialObserves: string[];
  onApply: (nodeId: string, payload: { options: string[]; observes: string[] }) => void;
  onClose: () => void;
}

export default function DecisionEditor({ nodeId, initialOptions, initialObserves, onApply, onClose }: DecisionEditorProps) {
  // Use a ref-like approach to reset state when the node changes without using an effect
  const [prevNodeId, setPrevNodeId] = useState(nodeId);
  const [options, setOptions] = useState(initialOptions.join(', '));
  const [observes, setObserves] = useState(initialObserves.join('\n'));

  if (nodeId !== prevNodeId) {
    setPrevNodeId(nodeId);
    setOptions(initialOptions.join(', '));
    setObserves(initialObserves.join('\n'));
  }

  const handleApply = () => {
    const opts = options.split(',').map((s) => s.trim()).filter(Boolean);
    const obs = observes.split('\n').map((s) => s.trim()).filter(Boolean);
    onApply(nodeId, { options: opts, observes: obs });
  };

  return (
    <div data-testid="decision-editor" style={{ width: '250px', padding: '16px', background: '#f5f5f5', borderLeft: '1px solid #ccc', display: 'flex', flexDirection: 'column', gap: '12px' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <strong style={{ fontSize: '14px' }}>Decision: {nodeId}</strong>
        <button onClick={onClose} style={{ cursor: 'pointer', background: 'none', border: 'none', fontSize: '16px' }}>×</button>
      </div>
      <label style={{ display: 'flex', flexDirection: 'column', fontSize: '12px', fontWeight: 'bold', gap: '4px' }}>
        Options (comma separated):
        <input
          data-testid="decision-options"
          value={options}
          onChange={(e) => setOptions(e.target.value)}
          style={{ padding: '6px', border: '1px solid #ccc', borderRadius: '4px', fontSize: '12px' }}
        />
      </label>
      <label style={{ display: 'flex', flexDirection: 'column', fontSize: '12px', fontWeight: 'bold', gap: '4px' }}>
        Observes (one per line):
        <textarea
          data-testid="decision-observes"
          value={observes}
          onChange={(e) => setObserves(e.target.value)}
          rows={5}
          style={{ padding: '6px', border: '1px solid #ccc', borderRadius: '4px', fontSize: '12px', resize: 'vertical' }}
        />
      </label>
      <button
        data-testid="decision-apply"
        onClick={handleApply}
        style={{
          marginTop: 'auto',
          padding: '8px',
          background: '#2196f3',
          color: 'white',
          border: 'none',
          borderRadius: '4px',
          cursor: 'pointer',
          fontWeight: 'bold',
        }}
      >
        Apply
      </button>
    </div>
  );
}
