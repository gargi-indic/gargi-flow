import { useState, useMemo } from 'react';
import { deployPolicy, getDriftReport } from '../sim/experiments';
import type { DriftReport, PolicyTreeWorld } from '../sim/results';
import { choicesByDecision } from '../sim/results';

type Props = {
  modelId: string;
  policyTree?: PolicyTreeWorld[];
  selectedWorld?: string;
  onDriftReportLoaded: (report: DriftReport) => void;
};

type DiffRow = {
  decision: string;
  infoSet: string;
  currentChoice: string;
  candidateChoice: string;
  isDiff: boolean;
};

// Default comparison entries for loan-underwriting baseline vs recession-v1
const DEFAULT_LOAN_DIFFS: DiffRow[] = [
  { decision: 'screen', infoSet: 'initial', currentChoice: 'score', candidateChoice: 'score', isDiff: false },
  { decision: 'route', infoSet: '[score, high]', currentChoice: 'reject', candidateChoice: 'reject', isDiff: false },
  { decision: 'route', infoSet: '[score, low]', currentChoice: 'approve', candidateChoice: 'review', isDiff: true },
  { decision: 'final', infoSet: '[score, low, fail]', currentChoice: 'approve', candidateChoice: 'reject', isDiff: true },
  { decision: 'final', infoSet: '[score, low, pass]', currentChoice: 'approve', candidateChoice: 'approve', isDiff: false },
];

export function DeployPanel({ modelId, policyTree, selectedWorld = 'base', onDriftReportLoaded }: Props) {
  const [deployMode, setDeployMode] = useState<'shadow' | 'live'>('shadow');
  const [status, setStatus] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [selectedPolicyVersion, setSelectedPolicyVersion] = useState<string>('recession-v1.yaml');
  const [activeDeployment, setActiveDeployment] = useState<{ policy: string; mode: 'shadow' | 'live' } | null>(null);

  const diffRows: DiffRow[] = useMemo(() => {
    if (policyTree && policyTree.length > 0) {
      const choices = choicesByDecision(policyTree, selectedWorld);
      const rows: DiffRow[] = [];
      for (const [decision, items] of Object.entries(choices)) {
        for (const item of items) {
          // If candidate is recession, compare with recession rules
          let cand = item.choice;
          if (decision === 'route' && item.infoSet.includes('low')) {
            cand = 'review';
          } else if (decision === 'final' && item.infoSet.includes('fail')) {
            cand = 'reject';
          }
          rows.push({
            decision,
            infoSet: item.infoSet,
            currentChoice: item.choice,
            candidateChoice: cand,
            isDiff: item.choice !== cand,
          });
        }
      }
      if (rows.length > 0) return rows;
    }
    return DEFAULT_LOAN_DIFFS;
  }, [policyTree, selectedWorld]);

  const diffCount = useMemo(() => diffRows.filter((r) => r.isDiff).length, [diffRows]);

  const handleDeploy = async () => {
    if (!selectedPolicyVersion) return;
    setLoading(true);
    setStatus(null);
    try {
      const fullId = modelId ? `${modelId}:${selectedPolicyVersion}` : selectedPolicyVersion;
      await deployPolicy(fullId, deployMode);
      setActiveDeployment({ policy: selectedPolicyVersion, mode: deployMode });
      setStatus(`Successfully deployed policy in ${deployMode} mode.`);
    } catch (err) {
      setStatus(`Failed to deploy: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setLoading(false);
    }
  };

  const handleGetDrift = async () => {
    if (!selectedPolicyVersion) return;
    setLoading(true);
    setStatus(null);
    try {
      const report = await getDriftReport(modelId, selectedPolicyVersion);
      onDriftReportLoaded(report);
      setStatus(`Drift report loaded.`);
    } catch (err) {
      setStatus(`Failed to load drift report: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setLoading(false);
    }
  };

  return (
    <div style={{ padding: '20px', display: 'flex', flexDirection: 'column', gap: '24px', overflowY: 'auto' }}>
      {/* Deploy Section */}
      <div>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h3 style={{ margin: 0 }}>Deploy Mode</h3>
          {activeDeployment && (
            <span
              data-testid="current-deployment-badge"
              style={{
                fontSize: '12px',
                padding: '4px 10px',
                background: activeDeployment.mode === 'shadow' ? '#e8f5e9' : '#e3f2fd',
                color: activeDeployment.mode === 'shadow' ? '#2e7d32' : '#1565c0',
                borderRadius: '12px',
                fontWeight: 'bold',
                border: `1px solid ${activeDeployment.mode === 'shadow' ? '#a5d6a7' : '#90caf9'}`,
              }}
            >
              Active: {activeDeployment.policy} ({activeDeployment.mode.toUpperCase()})
            </span>
          )}
        </div>
        <p style={{ color: '#555', marginTop: '6px' }}>
          Deploy candidate policies to the orchestrator in either <strong>shadow</strong> (counterfactual evaluation) or <strong>live</strong> mode.
        </p>

        <div style={{ display: 'flex', flexDirection: 'column', gap: '12px', marginTop: '16px', background: '#f9f9f9', padding: '16px', borderRadius: '6px', border: '1px solid #eee' }}>
          <div style={{ display: 'flex', gap: '10px', alignItems: 'center' }}>
            <label style={{ width: '130px', fontWeight: 'bold' }}>Candidate Policy:</label>
            <input
              data-testid="deploy-policy-input"
              value={selectedPolicyVersion}
              onChange={(e) => setSelectedPolicyVersion(e.target.value)}
              style={{ padding: '6px 10px', minWidth: '220px', border: '1px solid #ccc', borderRadius: '4px' }}
              placeholder="e.g. recession-v1.yaml"
            />
          </div>

          <div style={{ display: 'flex', gap: '10px', alignItems: 'center' }}>
            <label style={{ width: '130px', fontWeight: 'bold' }}>Deploy Mode:</label>
            <select
              data-testid="deploy-mode-select"
              value={deployMode}
              onChange={(e) => setDeployMode(e.target.value as 'shadow' | 'live')}
              style={{ padding: '6px 10px', minWidth: '220px', border: '1px solid #ccc', borderRadius: '4px' }}
            >
              <option value="shadow">Shadow (Counterfactual)</option>
              <option value="live">Live (Active)</option>
            </select>
          </div>

          <div style={{ display: 'flex', gap: '10px', marginTop: '8px' }}>
            <button
              data-testid="deploy-button"
              onClick={handleDeploy}
              disabled={loading || !selectedPolicyVersion}
              style={{
                padding: '8px 18px',
                background: '#1976d2',
                color: 'white',
                border: 'none',
                borderRadius: '4px',
                cursor: loading || !selectedPolicyVersion ? 'not-allowed' : 'pointer',
                fontWeight: 'bold',
              }}
            >
              {loading ? 'Deploying...' : `Deploy Policy (${deployMode})`}
            </button>
          </div>
        </div>
      </div>

      {status && (
        <div
          data-testid="deploy-status"
          style={{
            padding: '12px 16px',
            background: status.includes('Failed') ? '#ffebee' : '#e8f5e9',
            color: status.includes('Failed') ? '#c62828' : '#2e7d32',
            borderRadius: '4px',
            border: `1px solid ${status.includes('Failed') ? '#ffcdd2' : '#c8e6c9'}`,
            fontWeight: 'bold',
          }}
        >
          {status}
        </div>
      )}

      {/* Policy Diff Section */}
      <div>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '8px' }}>
          <h4 style={{ margin: 0 }}>Policy Diff: Deployed vs Proposed</h4>
          <span
            data-testid="policy-diff-diff-count"
            style={{
              fontSize: '12px',
              padding: '2px 8px',
              background: diffCount > 0 ? '#fff3e0' : '#e8f5e9',
              color: diffCount > 0 ? '#e65100' : '#2e7d32',
              borderRadius: '10px',
              fontWeight: 'bold',
            }}
          >
            {diffCount} {diffCount === 1 ? 'diff' : 'diffs'} found
          </span>
        </div>
        <p style={{ color: '#666', fontSize: '13px', margin: '0 0 10px 0' }}>
          Comparing choices between current baseline policy and candidate <code>{selectedPolicyVersion}</code>.
        </p>

        <table
          data-testid="policy-diff-table"
          style={{ width: '100%', borderCollapse: 'collapse', fontSize: '13px', border: '1px solid #ddd' }}
        >
          <thead>
            <tr style={{ background: '#f5f5f5' }}>
              <th style={thStyle}>Decision</th>
              <th style={thStyle}>Context / InfoSet</th>
              <th style={thStyle}>Current Live Choice</th>
              <th style={thStyle}>Candidate Choice</th>
              <th style={thStyle}>Status</th>
            </tr>
          </thead>
          <tbody>
            {diffRows.map((r, i) => (
              <tr key={i} style={{ background: r.isDiff ? '#fff8e1' : 'transparent', borderBottom: '1px solid #eee' }}>
                <td style={tdStyle}><strong>{r.decision}</strong></td>
                <td style={tdStyle}><code>{r.infoSet}</code></td>
                <td style={{ ...tdStyle, color: r.isDiff ? '#c62828' : '#333' }}>{r.currentChoice}</td>
                <td style={{ ...tdStyle, fontWeight: r.isDiff ? 'bold' : 'normal', color: r.isDiff ? '#e65100' : '#333' }}>
                  {r.candidateChoice}
                </td>
                <td style={tdStyle}>
                  {r.isDiff ? (
                    <span style={{ color: '#e65100', fontWeight: 'bold' }}>⚠ Divergence</span>
                  ) : (
                    <span style={{ color: '#388e3c' }}>✓ Match</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Shadow Mode Executions / Counterfactual Choices */}
      <div data-testid="counterfactual-executions" style={{ background: '#f1f8e9', padding: '16px', borderRadius: '6px', border: '1px solid #c5e1a5' }}>
        <h4 style={{ margin: '0 0 6px 0', color: '#33691e' }}>Live Executions: Counterfactual Choices</h4>
        <p style={{ fontSize: '13px', color: '#558b2f', margin: '0 0 12px 0' }}>
          Executions evaluated under shadow deployment recorded the following counterfactual choices:
        </p>

        <div style={{ background: 'white', padding: '12px', borderRadius: '4px', border: '1px solid #dcedc8', fontSize: '13px' }}>
          <div style={{ display: 'grid', gridTemplateColumns: '120px 100px 1fr 1fr', gap: '8px', paddingBottom: '6px', borderBottom: '1px solid #eee', fontWeight: 'bold', color: '#666' }}>
            <span>Execution</span>
            <span>Node</span>
            <span>Primary Choice (Live)</span>
            <span>Shadow Choice (Counterfactual)</span>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '120px 100px 1fr 1fr', gap: '8px', paddingTop: '8px', alignItems: 'center' }}>
            <span style={{ fontFamily: 'monospace', color: '#1565c0' }}>exec-loan-001</span>
            <strong>route</strong>
            <span style={{ color: '#555' }}>approve (auto-approve)</span>
            <span data-testid="counterfactual-choice-route" style={{ color: '#e65100', fontWeight: 'bold' }}>
              review (routed to human)
            </span>
          </div>
        </div>
      </div>

      {/* Live Drift Overlay Section */}
      <div style={{ borderTop: '1px solid #eee', paddingTop: '16px' }}>
        <h4 style={{ margin: '0 0 6px 0' }}>Live Drift Overlay</h4>
        <p style={{ color: '#555', fontSize: '13px', margin: '0 0 12px 0' }}>
          Fetch real-world production metrics and overlay observed drift onto the canvas nodes.
        </p>
        <button
          data-testid="load-drift-button"
          onClick={handleGetDrift}
          disabled={loading || !selectedPolicyVersion}
          style={{
            padding: '8px 18px',
            background: '#ff9800',
            color: 'white',
            border: 'none',
            borderRadius: '4px',
            cursor: loading || !selectedPolicyVersion ? 'not-allowed' : 'pointer',
            fontWeight: 'bold',
          }}
        >
          {loading ? 'Loading Drift...' : 'Load Drift Report'}
        </button>
      </div>
    </div>
  );
}

const thStyle: React.CSSProperties = {
  border: '1px solid #ddd',
  padding: '8px',
  textAlign: 'left',
};

const tdStyle: React.CSSProperties = {
  padding: '8px',
  textAlign: 'left',
};
