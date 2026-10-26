import React, { useState, useMemo } from 'react';
import { ReactFlow, Handle, Position, MarkerType, ReactFlowProvider } from '@xyflow/react';
import type { Node as ReactFlowNode, Edge as ReactFlowEdge } from '@xyflow/react';
import dagre from 'dagre';
import type { GraphNode, GraphEdge } from '../sim/graphDoc';
import type { DriftReport, DriftDetails } from '../sim/results';

const nodeWidth = 150;
const nodeHeight = 60;
const direction = 'TB';

type SimGraphResults = {
  choices?: Record<string, { infoSet: string; choice: string; reachProbability: number }[]>;
  edgeMass?: Record<string, number>;
};

type PlainNodeData = {
  id: string;
  type: string;
  errors?: string[];
  results?: SimGraphResults;
  nodeStatuses?: Record<string, string>;
  driftReport?: DriftReport;
};

const DriftBadge: React.FC<{ drift?: DriftDetails; nodeId: string; top?: string }> = ({ drift, nodeId, top = '-16px' }) => {
  const [showTooltip, setShowTooltip] = useState(false);
  if (!drift?.driftDetected) return null;

  const tooltipTitle = `Drift Detected!\nSamples: ${drift.observedSamples}\nFailure rate drift: ${drift.failureRateDrift?.isDrift ? 'Yes' : 'No'}\nDuration drift: ${drift.durationDrift?.isDrift ? 'Yes' : 'No'}`;

  return (
    <div
      style={{
        position: 'absolute',
        top,
        left: '50%',
        transform: 'translateX(-50%)',
        zIndex: 10,
      }}
      onMouseEnter={() => setShowTooltip(true)}
      onMouseLeave={() => setShowTooltip(false)}
      onClick={(e) => {
        e.stopPropagation();
        setShowTooltip((v) => !v);
      }}
    >
      <div
        data-testid={`sim-node-drift-${nodeId}`}
        title={tooltipTitle}
        style={{
          background: '#ff9800',
          color: 'white',
          borderRadius: '12px',
          padding: '2px 8px',
          fontSize: '10px',
          fontWeight: 'bold',
          cursor: 'pointer',
          zIndex: 10,
          boxShadow: '0 2px 4px rgba(0,0,0,0.2)',
          display: 'flex',
          alignItems: 'center',
          gap: '4px',
          whiteSpace: 'nowrap',
        }}
      >
        <span>DRIFT</span>
      </div>
      {showTooltip && (
        <div
          data-testid={`sim-node-drift-tooltip-${nodeId}`}
          style={{
            position: 'absolute',
            bottom: '22px',
            left: '50%',
            transform: 'translateX(-50%)',
            background: '#263238',
            color: '#eceff1',
            padding: '8px 12px',
            borderRadius: '6px',
            fontSize: '11px',
            lineHeight: '1.4',
            whiteSpace: 'nowrap',
            boxShadow: '0 4px 10px rgba(0,0,0,0.3)',
            zIndex: 100,
            textAlign: 'left',
          }}
        >
          <div style={{ fontWeight: 'bold', borderBottom: '1px solid #455a64', paddingBottom: '3px', marginBottom: '4px', color: '#ffb74d' }}>
            Drift Detected: {nodeId}
          </div>
          <div>Samples: {drift.observedSamples}</div>
          {drift.failureRateDrift && (
            <div style={{ color: drift.failureRateDrift.isDrift ? '#ff8a80' : '#b0bec5' }}>
              Failure rate: pred {(drift.failureRateDrift.predicted * 100).toFixed(1)}% vs obs {(drift.failureRateDrift.observed * 100).toFixed(1)}% (Δ {(drift.failureRateDrift.delta * 100).toFixed(1)}%)
            </div>
          )}
          {drift.durationDrift && (
            <div style={{ color: drift.durationDrift.isDrift ? '#ff8a80' : '#b0bec5' }}>
              Duration: pred {drift.durationDrift.predicted.toFixed(2)}s vs obs {drift.durationDrift.observed.toFixed(2)}s (Δ {drift.durationDrift.delta.toFixed(2)}s)
            </div>
          )}
        </div>
      )}
    </div>
  );
};

const PlainNode: React.FC<{ data: PlainNodeData }> = ({ data }) => {

  const hasErrors = data.errors && data.errors.length > 0;
  const drift = data.driftReport?.nodes?.[data.id];
  const status = data.nodeStatuses?.[data.id];
  let statusColor = '#ccc';
  if (status === 'NODE_STATUS_SUCCEEDED') statusColor = '#4caf50';
  else if (status === 'NODE_STATUS_FAILED') statusColor = '#f44336';
  else if (status === 'NODE_STATUS_RUNNING') statusColor = '#2196f3';
  else if (status === 'NODE_STATUS_READY' || status === 'NODE_STATUS_PENDING') statusColor = '#ffeb3b';
  else if (status === 'NODE_STATUS_SKIPPED') statusColor = '#9e9e9e';

  return (
    <div
      data-testid={`sim-node-${data.id}`}
      style={{
        padding: '10px',
        border: `2px solid ${hasErrors ? '#f44336' : statusColor}`,
        borderRadius: '8px',

        background: '#fff',
        minWidth: '150px',
        textAlign: 'center',
        boxShadow: '0 4px 6px rgba(0,0,0,0.1)',
        position: 'relative',
      }}
    >
      <Handle type="target" position={Position.Top} />
      <div style={{ fontWeight: 'bold', fontSize: '14px' }}>{data.id}</div>
      <div style={{ fontSize: '12px', color: '#666' }}>Type: {data.type}</div>
      <Handle type="source" position={Position.Bottom} />
      <DriftBadge drift={drift} nodeId={data.id} top="-16px" />
      {hasErrors && (
        <div
          data-testid={`sim-node-error-${data.id}`}
          title={data.errors!.join('\n')}
          style={{
            position: 'absolute',
            top: '-10px',
            right: '-10px',
            background: '#f44336',
            color: 'white',
            borderRadius: '50%',
            width: '20px',
            height: '20px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            fontSize: '12px',
            fontWeight: 'bold',
            cursor: 'help',
          }}
        >
          !
        </div>
      )}
    </div>
  );
};

const DecisionNode: React.FC<{ data: PlainNodeData }> = ({ data }) => {

  const hasErrors = data.errors && data.errors.length > 0;
  const drift = data.driftReport?.nodes?.[data.id];
  const decisionChoices = data.results?.choices?.[data.id];
  const status = data.nodeStatuses?.[data.id];
  let statusColor = '#ccc';
  if (status === 'NODE_STATUS_SUCCEEDED') statusColor = '#4caf50';
  else if (status === 'NODE_STATUS_FAILED') statusColor = '#f44336';
  else if (status === 'NODE_STATUS_RUNNING') statusColor = '#2196f3';
  else if (status === 'NODE_STATUS_READY' || status === 'NODE_STATUS_PENDING') statusColor = '#ffeb3b';
  else if (status === 'NODE_STATUS_SKIPPED') statusColor = '#9e9e9e';

  return (
    <div
      data-testid={`sim-node-${data.id}`}
      style={{
        position: 'relative',
        width: '120px',
        height: '120px',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
      }}
    >
      <DriftBadge drift={drift} nodeId={data.id} top="-22px" />
      <div
        style={{
          position: 'absolute',
          top: 0,
          left: 0,
          right: 0,
          bottom: 0,
          background: '#fff',
          border: `2px solid ${hasErrors ? '#f44336' : statusColor}`,
          transform: 'rotate(45deg)',

          boxShadow: '0 4px 6px rgba(0,0,0,0.1)',
        }}
      />
      <div style={{ position: 'relative', zIndex: 1, textAlign: 'center', maxWidth: '90%' }}>
        <div style={{ fontWeight: 'bold', fontSize: '14px' }}>{data.id}</div>
        {decisionChoices && decisionChoices.length > 0 && (
          <div style={{ marginTop: '8px', fontSize: '10px', textAlign: 'left', background: 'rgba(255,255,255,0.8)', padding: '2px', borderRadius: '4px' }}>
            {decisionChoices.map((c: { infoSet: string; choice: string }, i: number) => (
              <div key={i} data-testid={`result-choice-${data.id}-${c.infoSet}`}>
                {c.infoSet} → {c.choice}
              </div>
            ))}
          </div>
        )}
      </div>
      <Handle type="target" position={Position.Top} style={{ zIndex: 2 }} />
      <Handle type="source" position={Position.Bottom} style={{ zIndex: 2 }} />
      {hasErrors && (
        <div
          data-testid={`sim-node-error-${data.id}`}
          title={data.errors!.join('\n')}
          style={{
            position: 'absolute',
            top: '0px',
            right: '0px',
            background: '#f44336',
            color: 'white',
            borderRadius: '50%',
            width: '20px',
            height: '20px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            fontSize: '12px',
            fontWeight: 'bold',
            cursor: 'help',
            zIndex: 3,
          }}
        >
          !
        </div>
      )}
    </div>
  );
};

const nodeTypes = {
  plain: PlainNode,
  decision: DecisionNode,
};

export interface SimGraphViewProps {
  nodes: GraphNode[];
  edges: GraphEdge[];
  nodeErrors: Record<string, string[]>;
  onNodeClick: (nodeId: string) => void;
  results?: SimGraphResults;
  nodeStatuses?: Record<string, string>;
  driftReport?: DriftReport | null;
}

export default function SimGraphView({ nodes, edges, nodeErrors, onNodeClick, results, nodeStatuses, driftReport }: SimGraphViewProps) {

  const { flowNodes, flowEdges } = useMemo(() => {
    const dagreGraph = new dagre.graphlib.Graph();
    dagreGraph.setDefaultEdgeLabel(() => ({}));
    dagreGraph.setGraph({ rankdir: direction, nodesep: 150, ranksep: 100 });

    const rfNodes: ReactFlowNode[] = nodes.map((n) => ({
      id: n.id,
      type: n.type === 'decision' ? 'decision' : 'plain',
      position: { x: 0, y: 0 },
      data: { id: n.id, type: n.type, errors: nodeErrors[n.id] || [], results, nodeStatuses, driftReport },
    }));

    const rfEdges: ReactFlowEdge[] = edges.map((e, i) => {
      const edgeKey = `${e.from}->${e.to}`;
      let style: React.CSSProperties | undefined = undefined;
      
      if (results?.edgeMass) {
        const mass = results.edgeMass[edgeKey] || 0;
        style = {
          strokeWidth: 1 + 6 * mass,
          opacity: mass === 0 ? 0.3 : 1,
          strokeDasharray: mass === 0 ? '5,5' : 'none',
        };
      }
      
      return {
        id: `e${i}-${e.from}-${e.to}`,
        source: e.from,
        target: e.to,
        label: e.condition,
        style,
        data: results?.edgeMass ? { mass: results.edgeMass[edgeKey] || 0 } : undefined,
      };
    });

    rfNodes.forEach((node) => {
      dagreGraph.setNode(node.id, { width: nodeWidth, height: nodeHeight });
    });

    rfEdges.forEach((edge) => {
      if (dagreGraph.hasNode(edge.source) && dagreGraph.hasNode(edge.target)) {
        dagreGraph.setEdge(edge.source, edge.target);
      }
    });

    dagre.layout(dagreGraph);

    const layoutedNodes = rfNodes.map((node) => {
      const nodeWithPosition = dagreGraph.node(node.id);
      return {
        ...node,
        position: {
          x: nodeWithPosition.x - nodeWidth / 2,
          y: nodeWithPosition.y - nodeHeight / 2,
        },
      };
    });

    return { flowNodes: layoutedNodes, flowEdges: rfEdges };
  }, [nodes, edges, nodeErrors, results, nodeStatuses, driftReport]);

  return (
    <div style={{ flex: 1, width: '100%', height: '100%', background: '#fafafa' }}>
      <ReactFlowProvider>
        <ReactFlow
          nodes={flowNodes}
          edges={flowEdges}
          nodeTypes={nodeTypes}
          fitView
          onNodeClick={(_, node) => onNodeClick(node.id)}
          defaultEdgeOptions={{
            markerEnd: {
              type: MarkerType.ArrowClosed,
            },
          }}
        />
      </ReactFlowProvider>
    </div>
  );
}
