import * as yaml from 'js-yaml';

export type GraphNode = {
  id: string;
  type: string;
  config?: Record<string, unknown>;
  joinMode?: string;
};

export type GraphEdge = {
  from: string;
  to: string;
  condition?: string;
};

export type GraphDoc = {
  apiVersion?: string;
  id?: string;
  nodes?: GraphNode[];
  edges?: GraphEdge[];
};

export function parseGraph(text: string): { nodes: GraphNode[]; edges: GraphEdge[] } | Error {
  try {
    const doc = yaml.load(text) as GraphDoc;
    if (!doc || typeof doc !== 'object') {
      return new Error('Invalid YAML object');
    }
    return {
      nodes: doc.nodes || [],
      edges: doc.edges || [],
    };
  } catch (e) {
    return e instanceof Error ? e : new Error(String(e));
  }
}

export function setDecision(text: string, nodeId: string, payload: { options: string[]; observes: string[] }): string {
  let doc: GraphDoc;
  try {
    doc = (yaml.load(text) || {}) as GraphDoc;
  } catch {
    return text;
  }
  if (!doc.nodes) doc.nodes = [];
  const node = doc.nodes.find((n) => n.id === nodeId);
  if (node) {
    if (!node.config) node.config = {};
    node.config.options = payload.options;
    node.config.observes = payload.observes;
  }
  return yaml.dump(doc, { skipInvalid: true, noRefs: true });
}

export function addDecisionNode(text: string, id: string): string {
  let doc: GraphDoc;
  try {
    doc = (yaml.load(text) || {}) as GraphDoc;
  } catch {
    doc = {};
  }
  if (!doc.nodes) doc.nodes = [];
  doc.nodes.push({
    id,
    type: 'decision',
    config: { options: [], observes: [] },
  });
  return yaml.dump(doc, { skipInvalid: true, noRefs: true });
}

export function nodeIdForPath(text: string, path: string): string | null {
  const match = path.match(/^\$\.graph:\$\.nodes\[(\d+)\]/);
  if (!match) return null;
  const index = parseInt(match[1], 10);
  const parsed = parseGraph(text);
  if (parsed instanceof Error) return null;
  if (index >= 0 && index < parsed.nodes.length) {
    return parsed.nodes[index].id;
  }
  return null;
}
