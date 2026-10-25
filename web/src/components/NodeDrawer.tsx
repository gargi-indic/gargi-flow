import React, { useEffect, useState } from 'react';
import { apiClient } from '../api/client';
import type { definitions } from '../api/schema';
import BehaviorEditor from './BehaviorEditor';

interface NodeDrawerProps {
  executionId: string | null;
  nodeId: string | null;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  config?: any;
  onClose: () => void;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  onConfigChange?: (newConfig: any) => void;
}

const NodeDrawer: React.FC<NodeDrawerProps> = ({ executionId, nodeId, config, onClose, onConfigChange }) => {
  const [nodeDetail, setNodeDetail] = useState<definitions['v1NodeStateDetail'] | null>(null);
  const [editingConfig, setEditingConfig] = useState<string>(config ? JSON.stringify(config, null, 2) : '');
  const [configError, setConfigError] = useState<string | null>(null);

  const [prevConfig, setPrevConfig] = useState(config);
  if (config !== prevConfig) {
    setPrevConfig(config);
    setEditingConfig(config ? JSON.stringify(config, null, 2) : '');
  }

  const handleConfigSave = () => {
    try {
      const parsed = JSON.parse(editingConfig);
      setConfigError(null);
      if (onConfigChange) {
        onConfigChange(parsed);
      }
    } catch (e) {
      setConfigError('Invalid JSON');
    }
  };
  const [loading, setLoading] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);
  const [refreshNonce, setRefreshNonce] = useState<number>(0);

  const [signalName, setSignalName] = useState<string>(
    typeof config?.config?.signal === 'string' ? config.config.signal : ''
  );
  const [signalPayload, setSignalPayload] = useState<string>(JSON.stringify({ approved: true }, null, 2));
  const [signalSending, setSignalSending] = useState<boolean>(false);
  const [signalFeedback, setSignalFeedback] = useState<{ ok: boolean; message: string } | null>(null);

  const handleSendSignal = async () => {
    if (!executionId || !nodeId) return;
    if (!signalName.trim()) {
      setSignalFeedback({ ok: false, message: 'Signal name is required' });
      return;
    }
    let parsedPayload: unknown;
    try {
      parsedPayload = signalPayload.trim() === '' ? {} : JSON.parse(signalPayload);
    } catch {
      setSignalFeedback({ ok: false, message: 'Payload must be valid JSON' });
      return;
    }

    setSignalSending(true);
    setSignalFeedback(null);
    try {
      // Workaround for openapi-fetch types mismatch when generated with swagger 2.0
      const res = await apiClient.POST('/v1/executions/{executionId}/nodes/{nodeId}/signal', {
        params: { path: { executionId, nodeId } },
        body: { signalName: signalName.trim(), payload: parsedPayload }
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      const data = res.data as definitions['v1SignalNodeResponse'] | undefined;
      if (res.error) {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const errObj = res.error as any;
        setSignalFeedback({ ok: false, message: errObj?.message || 'Failed to send signal' });
      } else if (data?.accepted === false) {
        setSignalFeedback({ ok: false, message: data.message || 'Signal was not accepted' });
      } else {
        setSignalFeedback({ ok: true, message: data?.message || `Signal "${signalName.trim()}" sent` });
        // Re-fetch node state immediately so the drawer reflects the resumed graph
        setRefreshNonce((n) => n + 1);
      }
    } catch (err) {
      setSignalFeedback({ ok: false, message: err instanceof Error ? err.message : 'Failed to send signal' });
    } finally {
      setSignalSending(false);
    }
  };

  useEffect(() => {
    if (!executionId || !nodeId) {
      return;
    }

    const fetchNodeData = async (isInitial: boolean) => {
      if (isInitial) {
        setLoading(true);
      }
      setError(null);
      try {
        const res = await apiClient.GET('/v1/executions/{executionId}/nodes/{nodeId}', {
          params: { path: { executionId, nodeId } }
        });

        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const dataAny = res.data as any;
        if (dataAny?.nodeState) {
          setNodeDetail(dataAny.nodeState as definitions['v1NodeStateDetail']);
        } else if (res.error) {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          const errObj = res.error as any;
          setError(errObj.message || 'Failed to load node details');
        }
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to load node details');
      } finally {
        if (isInitial) {
          setLoading(false);
        }
      }
    };

    let interval: number;

    fetchNodeData(true).then(() => {
      // Poll the node details if it's not in a terminal state
      interval = window.setInterval(() => {
        setNodeDetail((prevDetail) => {
          if (
            prevDetail?.status === 'NODE_STATUS_SUCCEEDED' ||
            prevDetail?.status === 'NODE_STATUS_FAILED' ||
            prevDetail?.status === 'NODE_STATUS_COMPENSATED' ||
            prevDetail?.status === 'NODE_STATUS_SKIPPED'
          ) {
            clearInterval(interval);
            return prevDetail;
          }
          fetchNodeData(false);
          return prevDetail;
        });
      }, 2000);
    });

    return () => clearInterval(interval);
  }, [executionId, nodeId, refreshNonce]);

  if (!nodeId) return null;

  const handleCopy = (text: string) => {
    navigator.clipboard.writeText(text);
  };

  const isWaitNode = config?.type === 'wait_for_signal';
  const isBehaviorNode = config?.type === 'behavior' || (config && !config.type);
  const isNodeActive = nodeDetail?.status === 'NODE_STATUS_RUNNING';
  const showSignalForm = !!executionId && (isNodeActive || (isWaitNode && !nodeDetail));

  const calculateDuration = () => {
    if (!nodeDetail?.startedAt || !nodeDetail?.completedAt) return 'N/A';
    const start = new Date(nodeDetail.startedAt).getTime();
    const end = new Date(nodeDetail.completedAt).getTime();
    return `${end - start}ms`;
  };

  return (
    <div
      style={{
        position: 'absolute',
        top: 0,
        right: 0,
        width: '400px',
        height: '100%',
        backgroundColor: '#fff',
        boxShadow: '-4px 0 15px rgba(0,0,0,0.1)',
        display: 'flex',
        flexDirection: 'column',
        zIndex: 1000,
      }}
    >
      <div style={{ padding: '16px', borderBottom: '1px solid #ccc', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h2 style={{ margin: 0, fontSize: '1.2rem' }}>Node: {nodeId}</h2>
        <button onClick={onClose} style={{ background: 'none', border: 'none', fontSize: '1.2rem', cursor: 'pointer' }}>✖</button>
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: '16px' }}>
        {!executionId && (
          <div style={{ marginBottom: '16px', padding: '8px', backgroundColor: '#fff3e0', border: '1px solid #ffcc80', borderRadius: '4px', fontSize: '0.9rem', color: '#e65100' }}>
            Not executed yet.
          </div>
        )}

        {loading && executionId && <p>Loading execution data...</p>}
        {error && <p style={{ color: 'red' }}>{error}</p>}

        <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>

          {/* Signal Inspector */}
          {showSignalForm && (
            <div style={{ padding: '12px', backgroundColor: '#fff3e0', border: '1px solid #ffcc80', borderRadius: '4px' }}>
              <strong style={{ display: 'block', marginBottom: '8px', color: '#e65100' }}>
                {isWaitNode ? '⏳ Awaiting signal' : 'Send Signal'}
              </strong>
              <label style={{ display: 'block', fontSize: '0.85rem', marginBottom: '4px' }} htmlFor="signal-name">
                Signal name
              </label>
              <input
                id="signal-name"
                type="text"
                value={signalName}
                onChange={(e) => setSignalName(e.target.value)}
                placeholder="e.g. manager_approval"
                style={{ width: '100%', padding: '6px 8px', marginBottom: '8px', fontSize: '0.85rem', border: '1px solid #ddd', borderRadius: '4px', boxSizing: 'border-box' }}
              />
              <label style={{ display: 'block', fontSize: '0.85rem', marginBottom: '4px' }} htmlFor="signal-payload">
                Payload (JSON)
              </label>
              <textarea
                id="signal-payload"
                value={signalPayload}
                onChange={(e) => setSignalPayload(e.target.value)}
                style={{ width: '100%', minHeight: '80px', padding: '8px', marginBottom: '8px', backgroundColor: '#fff', borderRadius: '4px', fontSize: '0.85rem', border: '1px solid #ddd', fontFamily: 'monospace', boxSizing: 'border-box' }}
              />
              <button
                onClick={handleSendSignal}
                disabled={signalSending}
                style={{ padding: '6px 12px', fontSize: '0.85rem', cursor: signalSending ? 'not-allowed' : 'pointer', backgroundColor: '#ff9800', color: 'white', border: 'none', borderRadius: '4px', opacity: signalSending ? 0.7 : 1 }}
              >
                {signalSending ? 'Sending...' : 'Send Signal'}
              </button>
              {signalFeedback && (
                <div style={{ marginTop: '8px', fontSize: '0.85rem', color: signalFeedback.ok ? '#2e7d32' : '#c62828' }}>
                  {signalFeedback.ok ? '✔ ' : '✖ '}{signalFeedback.message}
                </div>
              )}
            </div>
          )}

          {/* Behavior Config Editor */}
          {isBehaviorNode && (
            <div style={{ marginBottom: '16px' }}>
              <strong style={{ display: 'block', marginBottom: '8px' }}>Behavior Editor</strong>
              <BehaviorEditor 
                config={config} 
                onChange={(newConf) => {
                  if (onConfigChange) onConfigChange({ ...config, ...newConf });
                  setEditingConfig(JSON.stringify({ ...config, ...newConf }, null, 2));
                }} 
              />
            </div>
          )}

          {/* Config Data */}
          {config && (
            <div>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '4px' }}>
                <strong style={{ display: 'block' }}>Raw Config (Edit as JSON)</strong>
                <div>
                  <button
                    onClick={handleConfigSave}
                    style={{ fontSize: '0.8rem', padding: '2px 6px', cursor: 'pointer', marginRight: '4px', backgroundColor: '#4caf50', color: 'white', border: 'none', borderRadius: '4px' }}
                  >
                    Save
                  </button>
                  <button
                    onClick={() => handleCopy(JSON.stringify(config, null, 2))}
                    style={{ fontSize: '0.8rem', padding: '2px 6px', cursor: 'pointer' }}
                  >
                    Copy
                  </button>
                </div>
              </div>
              <textarea
                value={editingConfig}
                onChange={(e) => setEditingConfig(e.target.value)}
                style={{ width: '100%', minHeight: '150px', padding: '8px', backgroundColor: '#f5f5f5', borderRadius: '4px', fontSize: '0.85rem', border: '1px solid #ddd', fontFamily: 'monospace', boxSizing: 'border-box' }}
              />
              {configError && <div style={{ color: 'red', fontSize: '0.8rem', marginTop: '4px' }}>{configError}</div>}
            </div>
          )}

          {/* Node Execution Details */}
          {nodeDetail && (
            <>
              {/* Status */}
              <div>
                <strong style={{ display: 'block', marginBottom: '4px' }}>Status</strong>
                <span style={{ padding: '4px 8px', backgroundColor: '#eee', borderRadius: '4px', fontSize: '0.9rem' }}>
                  {nodeDetail.status?.replace('NODE_STATUS_', '') || 'UNKNOWN'}
                </span>
              </div>

              {/* Timing */}
              <div>
                <strong style={{ display: 'block', marginBottom: '4px' }}>Timing</strong>
                <div style={{ fontSize: '0.9rem', color: '#555', display: 'grid', gridTemplateColumns: '80px 1fr', gap: '4px' }}>
                  <span>Start:</span>
                  <span>{nodeDetail.startedAt ? new Date(nodeDetail.startedAt).toLocaleTimeString() : 'N/A'}</span>
                  <span>End:</span>
                  <span>{nodeDetail.completedAt ? new Date(nodeDetail.completedAt).toLocaleTimeString() : 'N/A'}</span>
                  <span>Duration:</span>
                  <span>{calculateDuration()}</span>
                </div>
              </div>

              {/* Error Message */}
              {typeof nodeDetail.errorMessage === 'string' && nodeDetail.errorMessage !== '' ? (
                <div>
                  <strong style={{ display: 'block', marginBottom: '4px', color: 'red' }}>Error</strong>
                  <div style={{ padding: '8px', backgroundColor: '#ffebee', border: '1px solid #ef9a9a', borderRadius: '4px', fontSize: '0.9rem', color: '#c62828', wordBreak: 'break-all' }}>
                    {String(nodeDetail.errorMessage)}
                  </div>
                </div>
              ) : null}

              {/* Attempts */}
              {nodeDetail.attempt !== undefined ? (
                <div>
                  <strong style={{ display: 'block', marginBottom: '4px' }}>Attempts</strong>
                  <span style={{ fontSize: '0.9rem' }}>{String(nodeDetail.attempt)}</span>
                </div>
              ) : null}

              {/* Input */}
              {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
              {(nodeDetail as any).input !== undefined && (nodeDetail as any).input !== null ? (
                <div>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '4px' }}>
                    <strong style={{ display: 'block' }}>Input</strong>
                    <button
                      // eslint-disable-next-line @typescript-eslint/no-explicit-any
                      onClick={() => handleCopy(JSON.stringify((nodeDetail as any).input, null, 2))}
                      style={{ fontSize: '0.8rem', padding: '2px 6px', cursor: 'pointer' }}
                    >
                      Copy
                    </button>
                  </div>
                  <pre style={{ margin: 0, padding: '8px', backgroundColor: '#f5f5f5', borderRadius: '4px', fontSize: '0.85rem', overflowX: 'auto', maxHeight: '300px', border: '1px solid #ddd' }}>
                    {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
                    {String(JSON.stringify((nodeDetail as any).input, null, 2))}
                  </pre>
                </div>
              ) : null}

              {/* Deep Link to Temporal */}
              {executionId && (
                <div>
                  <strong style={{ display: 'block', marginBottom: '4px' }}>Temporal Link</strong>
                  <a href={`${import.meta.env.VITE_TEMPORAL_UI_URL || 'http://localhost:8233'}/namespaces/default/workflows/${executionId}`} target="_blank" rel="noopener noreferrer" style={{ fontSize: '0.9rem', color: '#0066cc', textDecoration: 'none' }}>
                    View in Temporal UI ↗
                  </a>
                </div>
              )}

              {/* Output */}
              {nodeDetail.output !== undefined && nodeDetail.output !== null ? (
                <div>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '4px' }}>
                    <strong style={{ display: 'block' }}>Output</strong>
                    <button
                      onClick={() => handleCopy(JSON.stringify(nodeDetail.output, null, 2))}
                      style={{ fontSize: '0.8rem', padding: '2px 6px', cursor: 'pointer' }}
                    >
                      Copy
                    </button>
                  </div>
                  <pre style={{ margin: 0, padding: '8px', backgroundColor: '#f5f5f5', borderRadius: '4px', fontSize: '0.85rem', overflowX: 'auto', maxHeight: '300px', border: '1px solid #ddd' }}>
                    {String(JSON.stringify(nodeDetail.output, null, 2))}
                  </pre>
                </div>
              ) : null}
            </>
          )}
        </div>
      </div>
    </div>
  );
};

export default NodeDrawer;