import React from 'react';

const NODE_TYPES = [
  { type: 'passthrough', label: 'Passthrough', description: 'Passes input directly to output' },
  { type: 'transform', label: 'Transform', description: 'Transforms input using CEL expressions' },
  { type: 'wait_for_signal', label: 'Wait for Signal', description: 'Pauses execution until a signal is received' },
  { type: 'sub_graph', label: 'Sub Graph', description: 'Executes a child graph' },
  { type: 'http_call', label: 'HTTP Call', description: 'Makes an HTTP request' },
  { type: 'decision', label: 'Decision', description: 'Chooses one option; simulated by a policy' },
];

export const NodePalette: React.FC = () => {
  const onDragStart = (event: React.DragEvent, nodeType: string) => {
    event.dataTransfer.setData('application/reactflow', nodeType);
    event.dataTransfer.effectAllowed = 'move';
  };

  return (
    <div style={{
      width: '200px',
      padding: '10px',
      borderLeft: '1px solid #ccc',
      background: '#f9f9f9',
      display: 'flex',
      flexDirection: 'column',
      gap: '10px',
      height: '100%',
      boxSizing: 'border-box'
    }}>
      <div style={{ fontWeight: 'bold', marginBottom: '10px' }}>Node Palette</div>
      <div style={{ fontSize: '12px', color: '#666', marginBottom: '10px' }}>
        Drag to add
      </div>
      {NODE_TYPES.map((node) => (
        <div
          key={node.type}
          onDragStart={(event) => onDragStart(event, node.type)}
          draggable
          style={{
            padding: '10px',
            border: '1px solid #ddd',
            borderRadius: '4px',
            background: 'white',
            cursor: 'grab',
            display: 'flex',
            flexDirection: 'column',
            gap: '4px'
          }}
        >
          <div style={{ fontWeight: 'bold', fontSize: '14px' }}>{node.label}</div>
          <div style={{ fontSize: '10px', color: '#888' }}>{node.description}</div>
        </div>
      ))}
    </div>
  );
};
