import React, { useMemo } from 'react';
import { ReactFlow, Handle, Position, MarkerType, ReactFlowProvider } from '@xyflow/react';
import type { Node as ReactFlowNode, Edge as ReactFlowEdge } from '@xyflow/react';
import dagre from 'dagre';
import type { GraphNode, GraphEdge } from '../sim/graphDoc';

const nodeWidth = 150;
const nodeHeight = 60;
const direction = 'TB';

type PlainNodeData = {
  id: string;
  type: string;
  errors?: string[];
};

const PlainNode: React.FC<{ data: PlainNodeData }> = ({ data }) => {
  const hasErrors = data.errors && data.errors.length > 0;
  return (
    <div
      data-testid={`sim-node-${data.id}`}
      style={{
        padding: '10px',
        border: `2px solid ${hasErrors ? '#f44336' : '#ccc'}`,
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
      <div
        style={{
          position: 'absolute',
          top: 0,
          left: 0,
          right: 0,
          bottom: 0,
          background: '#fff',
          border: `2px solid ${hasErrors ? '#f44336' : '#ccc'}`,
          transform: 'rotate(45deg)',
          boxShadow: '0 4px 6px rgba(0,0,0,0.1)',
        }}
      />
      <div style={{ position: 'relative', zIndex: 1, textAlign: 'center', maxWidth: '80%' }}>
        <div style={{ fontWeight: 'bold', fontSize: '14px' }}>{data.id}</div>
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

interface SimGraphViewProps {
  nodes: GraphNode[];
  edges: GraphEdge[];
  nodeErrors: Record<string, string[]>;
  onNodeClick: (nodeId: string) => void;
}

export default function SimGraphView({ nodes, edges, nodeErrors, onNodeClick }: SimGraphViewProps) {
  const { flowNodes, flowEdges } = useMemo(() => {
    const dagreGraph = new dagre.graphlib.Graph();
    dagreGraph.setDefaultEdgeLabel(() => ({}));
    dagreGraph.setGraph({ rankdir: direction, nodesep: 150, ranksep: 100 });

    const rfNodes: ReactFlowNode[] = nodes.map((n) => ({
      id: n.id,
      type: n.type === 'decision' ? 'decision' : 'plain',
      position: { x: 0, y: 0 },
      data: { id: n.id, type: n.type, errors: nodeErrors[n.id] || [] },
    }));

    const rfEdges: ReactFlowEdge[] = edges.map((e, i) => ({
      id: `e${i}-${e.from}-${e.to}`,
      source: e.from,
      target: e.to,
      label: e.condition,
    }));

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
  }, [nodes, edges, nodeErrors]);

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
