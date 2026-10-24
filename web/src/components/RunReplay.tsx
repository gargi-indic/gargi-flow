import { useState, useEffect } from 'react';
import { getRun } from '../sim/experiments';

interface RunReplayProps {
  experimentId: string;
  selectedWorld: string;
  onNodeStatusesChange: (statuses: Record<string, string>) => void;
}

type ReplayEvent = {
  eventType: string;
  nodeId?: string;
  toStatus?: string;
  [key: string]: unknown;
};

export default function RunReplay({ experimentId, selectedWorld, onNodeStatusesChange }: RunReplayProps) {
  const [runIndex, setRunIndex] = useState<number>(0);
  const [events, setEvents] = useState<ReplayEvent[]>([]);
  const [currentStep, setCurrentStep] = useState<number>(0);
  const [error, setError] = useState<string>('');

  const fetchRun = async () => {
    try {
      setError('');
      const data = await getRun(experimentId, runIndex, selectedWorld) as { events?: ReplayEvent[] };
      if (data && data.events) {
        setEvents(data.events);
        setCurrentStep(0);
      } else {
        setEvents([]);
        setError('No events found for this run.');
      }
    } catch (err: unknown) {
      if (err instanceof Error) {
        setError(err.message);
      } else {
        setError(String(err));
      }
      setEvents([]);
    }
  };

  useEffect(() => {
    if (events.length === 0) {
      onNodeStatusesChange({});
      return;
    }

    const statuses: Record<string, string> = {};
    for (let i = 0; i < currentStep; i++) {
      const e = events[i];
      if (e.eventType === 'node_state_changed' && e.nodeId && e.toStatus) {
        statuses[e.nodeId] = e.toStatus;
      }
    }
    onNodeStatusesChange(statuses);
  }, [events, currentStep, onNodeStatusesChange]);

  return (
    <div data-testid="sim-replay" style={{ padding: '16px', overflowY: 'auto' }}>
      <h3>Run Replay</h3>
      
      <div style={{ display: 'flex', gap: '8px', marginBottom: '16px', alignItems: 'center' }}>
        <label>Run Index:</label>
        <input 
          type="number" 
          value={runIndex} 
          onChange={(e) => setRunIndex(parseInt(e.target.value) || 0)}
          min={0}
          style={{ width: '60px' }}
        />
        <button data-testid="replay-fetch" onClick={fetchRun} disabled={!experimentId}>
          Fetch Run
        </button>
      </div>

      {error && <div style={{ color: 'red', marginBottom: '16px' }}>{error}</div>}

      {events.length > 0 && (
        <div>
          <div style={{ display: 'flex', gap: '8px', marginBottom: '16px', alignItems: 'center' }}>
            <button 
              data-testid="replay-reset" 
              onClick={() => setCurrentStep(0)} 
              disabled={currentStep === 0}
            >
              Reset
            </button>
            <button 
              data-testid="replay-prev" 
              onClick={() => setCurrentStep(Math.max(0, currentStep - 1))} 
              disabled={currentStep === 0}
            >
              Prev
            </button>
            <span data-testid="replay-step">
              Step {currentStep} / {events.length}
            </span>
            <button 
              data-testid="replay-next" 
              onClick={() => setCurrentStep(Math.min(events.length, currentStep + 1))} 
              disabled={currentStep === events.length}
            >
              Next
            </button>
          </div>

          <div style={{ maxHeight: '300px', overflowY: 'auto', border: '1px solid #ccc', padding: '8px', fontFamily: 'monospace', fontSize: '12px' }}>
            {events.map((e, i) => (
              <div key={i} style={{ padding: '4px', background: i < currentStep ? '#e3f2fd' : 'transparent', color: i < currentStep ? 'black' : '#999' }}>
                {i + 1}. {e.eventType} {e.nodeId ? `(${e.nodeId})` : ''} {e.toStatus ? `-> ${e.toStatus}` : ''}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
