import { useState, useMemo } from 'react';
import { validateModel } from '../sim/validate';
import type { SimDocs, ValidationIssue } from '../sim/validate';
import { parseGraph, setDecision, addDecisionNode, nodeIdForPath } from '../sim/graphDoc';
import { parseSim } from '../sim/simDoc';
import SimGraphView from './SimGraphView';
import DecisionEditor from './DecisionEditor';
import WorldsPanel from './WorldsPanel';
import LatentsPanel from './LatentsPanel';
import ObjectivePanel from './ObjectivePanel';
import { SimulatePanel } from './SimulatePanel';
import RegretPanel from './RegretPanel';
import VoiPanel from './VoiPanel';
import FlipPointPanel from './FlipPointPanel';
import PolicyCompare from './PolicyCompare';
import RunReplay from './RunReplay';
import ResultsBar from './ResultsBar';
import { choicesByDecision, edgeMass } from '../sim/results';
import type { SimulationReport } from '../sim/results';

type DocName = keyof SimDocs;

const TABS: { name: DocName | 'experiment' | 'regret' | 'voi' | 'flip' | 'compare' | 'replay'; label: string }[] = [
  { name: 'graph', label: 'graph.yaml' },
  { name: 'sim', label: 'sim.yaml' },
  { name: 'experiment', label: 'Simulate' },
  { name: 'regret', label: 'Regret' },
  { name: 'voi', label: 'VOI' },
  { name: 'flip', label: 'Flip Points' },
  { name: 'compare', label: 'Compare' },
  { name: 'replay', label: 'Replay' },
];

type Props = {
  docs: SimDocs;
  onChange: (docs: SimDocs) => void;
};

export default function SimModelEditor({ docs, onChange }: Props) {
  const [active, setActive] = useState<DocName | 'experiment' | 'regret' | 'voi' | 'flip' | 'compare' | 'replay'>('sim');
  const [issues, setIssues] = useState<ValidationIssue[] | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [newDecisionId, setNewDecisionId] = useState('');
  const [panelsCollapsed, setPanelsCollapsed] = useState(false);
const [summaryJson, setSummaryJson] = useState<SimulationReport | null>(null);
  const [selectedWorld, setSelectedWorld] = useState<string>('base');
  const [experimentId, setExperimentId] = useState<string>('');
  const [nodeStatuses, setNodeStatuses] = useState<Record<string, string>>({});

  const handleValidate = async () => {
    setBusy(true);
    setFailure(null);
    try {
      setIssues(await validateModel(docs));
    } catch (err) {
      setIssues(null);
      setFailure(`Validation failed: ${err instanceof Error ? err.message : 'unknown error'}`);
    } finally {
      setBusy(false);
    }
  };

  const handleAddDecision = () => {
    if (!newDecisionId.trim()) return;
    const newText = addDecisionNode(docs.graph, newDecisionId.trim());
    onChange({ ...docs, graph: newText });
    setNewDecisionId('');
  };

  const handleDecisionApply = (nodeId: string, payload: { options: string[]; observes: string[] }) => {
    const newText = setDecision(docs.graph, nodeId, payload);
    onChange({ ...docs, graph: newText });
  };

  const parsedGraph = useMemo(() => parseGraph(docs.graph), [docs.graph]);
  const parsedSim = useMemo(() => parseSim(docs.sim), [docs.sim]);

  const { nodeErrors, globalIssues } = useMemo(() => {
    const nodeMap: Record<string, string[]> = {};
    const globalList: ValidationIssue[] = [];
    if (issues) {
      for (const issue of issues) {
        if (!issue.path) {
          globalList.push(issue);
          continue;
        }
        const mappedNodeId = nodeIdForPath(docs.graph, issue.path);
        if (mappedNodeId) {
          if (!nodeMap[mappedNodeId]) nodeMap[mappedNodeId] = [];
          nodeMap[mappedNodeId].push(issue.message || 'unknown error');
        } else {
          globalList.push(issue);
        }
      }
    }
    return { nodeErrors: nodeMap, globalIssues: globalList };
  }, [docs.graph, issues]);

  const selectedNode = parsedGraph instanceof Error ? undefined : parsedGraph.nodes.find((n) => n.id === selectedNodeId);

  return (
    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      <div style={{ display: 'flex', gap: '4px', padding: '8px', background: '#eee', alignItems: 'center' }}>
        <strong style={{ marginRight: '12px' }}>Simulation model</strong>
        {TABS.map((t) => (
          <button
            key={t.name}
            data-testid={`sim-tab-${t.name}`}
            onClick={() => setActive(t.name)}
            style={{
              padding: '6px 12px',
              border: '1px solid #ccc',
              borderRadius: '4px',
              cursor: 'pointer',
              background: active === t.name ? '#2196f3' : 'white',
              color: active === t.name ? 'white' : 'black',
            }}
          >
            {t.label}
          </button>
        ))}

        <div style={{ display: 'flex', gap: '4px', alignItems: 'center', marginLeft: '20px' }}>
          <input
            value={newDecisionId}
            onChange={(e) => setNewDecisionId(e.target.value)}
            placeholder="Decision ID"
            style={{ padding: '6px', border: '1px solid #ccc', borderRadius: '4px' }}
          />
          <button
            data-testid="sim-add-decision"
            onClick={handleAddDecision}
            disabled={!newDecisionId.trim()}
            style={{
              padding: '6px 12px',
              background: newDecisionId.trim() ? '#2196f3' : '#ccc',
              color: 'white',
              border: 'none',
              borderRadius: '4px',
              cursor: newDecisionId.trim() ? 'pointer' : 'not-allowed',
            }}
          >
            Add decision
          </button>
        </div>

        <button
          data-testid="sim-validate"
          onClick={handleValidate}
          disabled={busy}
          style={{
            marginLeft: 'auto',
            padding: '6px 16px',
            border: 'none',
            borderRadius: '4px',
            cursor: busy ? 'not-allowed' : 'pointer',
            background: busy ? '#ccc' : '#4caf50',
            color: 'white',
            fontWeight: 'bold',
          }}
        >
          {busy ? 'Validating...' : 'Validate'}
        </button>
      </div>

      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        {!(parsedSim instanceof Error) && (
          <div style={{ width: panelsCollapsed ? '40px' : '400px', borderRight: '1px solid #ccc', display: 'flex', flexDirection: 'column', transition: 'width 0.2s', overflowY: 'auto', overflowX: 'hidden' }}>
            <div style={{ padding: '8px', background: '#f5f5f5', borderBottom: '1px solid #ccc', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              {!panelsCollapsed && <strong>Model Panels</strong>}
              <button onClick={() => setPanelsCollapsed(!panelsCollapsed)}>{panelsCollapsed ? '>>' : '<<'}</button>
            </div>
            {!panelsCollapsed && (
              <>
                <WorldsPanel
                  text={docs.sim}
                  worlds={parsedSim.worlds}
                  onChange={(newSimText) => onChange({ ...docs, sim: newSimText })}
                />
                <LatentsPanel
                  text={docs.sim}
                  latents={parsedSim.latents}
                  onChange={(newSimText) => onChange({ ...docs, sim: newSimText })}
                />
                <ObjectivePanel
                  text={docs.sim}
                  objective={parsedSim.objective}
                  onChange={(newSimText) => onChange({ ...docs, sim: newSimText })}
                />
              </>
            )}
          </div>
        )}

        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
          {summaryJson && (
            <ResultsBar 
              summary={summaryJson}
              world={selectedWorld}
              onWorldChange={setSelectedWorld}
            />
          )}
<div style={{ flex: 1, display: 'flex', borderBottom: '1px solid #ccc' }}>
            {active === 'experiment' ? (
              <SimulatePanel
                simYaml={docs.sim}
                graphYaml={docs.graph}
                setSummaryJson={(data) => {
                  const report = data as SimulationReport;
                  setSummaryJson(report);
                  if (report && report.reports && report.reports.policyTree && report.reports.policyTree.length > 0) {
                    const firstWorld = report.reports.policyTree[0].world;
                    setSelectedWorld((prev) => {
                      if (report.reports?.policyTree?.some(t => t.world === prev)) return prev;
                      return firstWorld;
                    });
                  }
                }}
                setExperimentId={setExperimentId}
              />
            ) : active === 'regret' ? (
              <RegretPanel regret={summaryJson?.reports?.regret} />
            ) : active === 'voi' ? (
              <VoiPanel voiData={summaryJson?.reports?.voi} />
            ) : active === 'flip' ? (
              <FlipPointPanel flipData={summaryJson?.reports?.flipPoints} />
            ) : active === 'compare' ? (
              <PolicyCompare policyTree={summaryJson?.reports?.policyTree} selectedWorld={selectedWorld} />
            ) : active === 'replay' ? (
              <RunReplay experimentId={experimentId} selectedWorld={selectedWorld} onNodeStatusesChange={setNodeStatuses} />
            ) : (
              <textarea
                key={active}
                data-testid={`sim-editor-${active}`}
                value={docs[active]}
                onChange={(e) => onChange({ ...docs, [active]: e.target.value })}
                spellCheck={false}
                style={{ flex: 1, padding: '10px', boxSizing: 'border-box', fontFamily: 'monospace', resize: 'none', border: 'none' }}
              />
            )}
          </div>
          <div style={{ flex: 1, position: 'relative' }}>
            {!(parsedGraph instanceof Error) && (
              <SimGraphView
                nodes={parsedGraph.nodes}
                edges={parsedGraph.edges}
                nodeErrors={nodeErrors}
                onNodeClick={setSelectedNodeId}
                results={summaryJson ? {
                  choices: choicesByDecision(summaryJson.reports?.policyTree, selectedWorld),
                  edgeMass: edgeMass(summaryJson.reports?.pathHeatmap, selectedWorld),
                } : undefined}
                nodeStatuses={nodeStatuses}
              />
            )}
            {parsedGraph instanceof Error && (
              <div style={{ padding: '20px', color: 'red' }}>Error parsing graph: {parsedGraph.message}</div>
            )}
          </div>
        </div>

        {selectedNode && selectedNode.type === 'decision' && (
          <DecisionEditor
            nodeId={selectedNode.id}
            initialOptions={(selectedNode.config?.options as string[]) || []}
            initialObserves={(selectedNode.config?.observes as string[]) || []}
            onApply={handleDecisionApply}
            onClose={() => setSelectedNodeId(null)}
          />
        )}
      </div>

      {failure && (
        <div data-testid="sim-validate-failure" style={{ padding: '10px', color: 'red', background: '#ffebee', borderTop: '1px solid #ffcdd2' }}>
          {failure}
        </div>
      )}
      {issues && issues.length === 0 && (
        <div data-testid="sim-validate-ok" style={{ padding: '10px', color: '#1b5e20', background: '#e8f5e9', borderTop: '1px solid #c8e6c9' }}>
          No errors
        </div>
      )}
      {issues && issues.length > 0 && globalIssues.length > 0 && (
        <ul data-testid="sim-validate-errors" style={{ margin: 0, padding: '10px 10px 10px 30px', color: '#b71c1c', background: '#ffebee', borderTop: '1px solid #ffcdd2', maxHeight: '30%', overflowY: 'auto' }}>
          {globalIssues.map((e, i) => (
            <li key={i}>
              {e.path} · {e.code} · {e.message}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
