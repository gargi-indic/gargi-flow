export type PolicyTreeNode = {
  infoSet: string;
  decision: string;
  observed: Record<string, unknown>;
  reachProbability: number;
  onPath: boolean;
  choice: string;
  actions: {
    option: string;
    value: number;
    children: PolicyTreeNode[];
  }[];
};

export type PolicyTreeWorld = {
  world: string;
  policyId: string;
  ev: number;
  roots: PolicyTreeNode[];
};

export type PathHeatmapWorld = {
  world: string;
  policyId: string;
  nodes: { id: string; mass: number }[];
  edges: { from: string; to: string; index: number; mass: number }[];
};

export type SimulationReport = {
  reports?: {
    policyTree?: PolicyTreeWorld[];
    pathHeatmap?: PathHeatmapWorld[];
  };
};

export function choicesByDecision(
  policyTree: PolicyTreeWorld[] | undefined,
  world: string
): Record<string, { infoSet: string; choice: string; reachProbability: number }[]> {
  const result: Record<string, { infoSet: string; choice: string; reachProbability: number }[]> = {};
  
  if (!policyTree) return result;
  
  const worldTree = policyTree.find((w) => w.world === world);
  if (!worldTree) return result;

  const traverse = (node: PolicyTreeNode) => {
    if (node.onPath) {
      if (!result[node.decision]) {
        result[node.decision] = [];
      }
      result[node.decision].push({
        infoSet: node.infoSet,
        choice: node.choice,
        reachProbability: node.reachProbability,
      });
    }

    if (node.actions) {
      for (const action of node.actions) {
        if (action.children) {
          for (const child of action.children) {
            traverse(child);
          }
        }
      }
    }
  };

  for (const root of worldTree.roots || []) {
    traverse(root);
  }

  // Sort by infoSet
  for (const decision in result) {
    result[decision].sort((a, b) => a.infoSet.localeCompare(b.infoSet));
  }

  return result;
}

export function edgeMass(
  pathHeatmap: PathHeatmapWorld[] | undefined,
  world: string
): Record<string, number> {
  const result: Record<string, number> = {};
  
  if (!pathHeatmap) return result;
  
  const worldHeatmap = pathHeatmap.find((w) => w.world === world);
  if (!worldHeatmap) return result;

  for (const edge of worldHeatmap.edges || []) {
    const key = `${edge.from}->${edge.to}`;
    result[key] = (result[key] || 0) + edge.mass;
  }

  return result;
}
