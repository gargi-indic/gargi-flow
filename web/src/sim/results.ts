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

export type RegretCell = {
  world: string;
  policyId: string;
  value: number;
  regret: number;
  feasible: boolean;
};

export type RegretRow = {
  tunedFor: string;
  policyId: string;
  cells: RegretCell[];
  maxRegret: number;
};

export type RegretData = {
  worlds: string[];
  rows: RegretRow[];
};

export type VoiReportEntry = {
  world: string;
  node: string;
  withObservation: { policyId: string; ev: number };
  withoutObservation: { policyId: string; ev: number };
  voi: number;
};

export type FlipPointSweep = {
  value: number;
  policyId: string;
  ev: number;
};

export type FlipPointTransition = {
  from: string;
  to: string;
  left: { value: number; policyId: string; ev: number };
  right: { value: number; policyId: string; ev: number };
};

export type FlipPointReportEntry = {
  param: string;
  baseWorld: string;
  lo: number;
  hi: number;
  sweep: FlipPointSweep[];
  flips: FlipPointTransition[];
};

export type SimulationReport = {
  reports?: {
    policyTree?: PolicyTreeWorld[];
    regret?: RegretData;
    voi?: VoiReportEntry[];
    flipPoints?: FlipPointReportEntry[];
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

export type DriftMetric = {
  predicted: number;
  observed: number;
  delta: number;
  isDrift: boolean;
};

export type DriftDetails = {
  observedSamples: number;
  driftDetected: boolean;
  failureRateDrift?: DriftMetric;
  durationDrift?: DriftMetric;
  outputDrift?: Record<string, DriftMetric>;
};

export type DriftReport = {
  policyVersion: string;
  driftDetected: boolean;
  nodes: Record<string, DriftDetails>;
  latents?: Record<string, DriftMetric>;
};
