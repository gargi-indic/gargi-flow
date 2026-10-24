import React, { useState, useMemo } from 'react';
import type { PolicyTreeWorld } from '../sim/results';
import { choicesByDecision } from '../sim/results';

interface PolicyCompareProps {
  policyTree?: PolicyTreeWorld[];
  selectedWorld: string;
}

export default function PolicyCompare({ policyTree, selectedWorld }: PolicyCompareProps) {
  const [policyA, setPolicyA] = useState<string>('');
  const [policyB, setPolicyB] = useState<string>('');

  const worldTrees = useMemo(() => {
    if (!policyTree) return [];
    return policyTree.filter((t) => t.world === selectedWorld);
  }, [policyTree, selectedWorld]);

  const availablePolicies = useMemo(() => {
    return Array.from(new Set(worldTrees.map((t) => t.policyId)));
  }, [worldTrees]);

  const effectivePolicyA = policyA || (availablePolicies.length > 0 ? availablePolicies[0] : '');
  const effectivePolicyB = policyB || (availablePolicies.length > 1 ? availablePolicies[1] : (availablePolicies.length > 0 ? availablePolicies[0] : ''));

  const choicesA = useMemo(() => {
    const pTree = policyTree?.filter(t => t.policyId === effectivePolicyA);
    if (!pTree) return {};
    return choicesByDecision(pTree, selectedWorld);
  }, [policyTree, effectivePolicyA, selectedWorld]);

  const choicesB = useMemo(() => {
    const pTree = policyTree?.filter(t => t.policyId === effectivePolicyB);
    if (!pTree) return {};
    return choicesByDecision(pTree, selectedWorld);
  }, [policyTree, effectivePolicyB, selectedWorld]);

  const allDecisions = useMemo(() => {
    return Array.from(new Set([...Object.keys(choicesA), ...Object.keys(choicesB)])).sort();
  }, [choicesA, choicesB]);

  if (!policyTree || policyTree.length === 0) {
    return <div style={{ padding: '16px' }}>No policy data available for comparison.</div>;
  }

  return (
    <div data-testid="sim-compare" style={{ padding: '16px', overflowY: 'auto', display: 'flex', flexDirection: 'column' }}>
      <h3>Policy Comparison (World: {selectedWorld})</h3>
      
      <div style={{ display: 'flex', gap: '16px', marginBottom: '16px' }}>
        <div>
          <label><strong>Policy A:</strong> </label>
          <select value={effectivePolicyA} onChange={(e) => setPolicyA(e.target.value)}>
            {availablePolicies.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </div>
        <div>
          <label><strong>Policy B:</strong> </label>
          <select value={effectivePolicyB} onChange={(e) => setPolicyB(e.target.value)}>
            {availablePolicies.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </div>
      </div>

      <table style={{ width: '100%', borderCollapse: 'collapse' }}>
        <thead>
          <tr>
            <th style={thStyle}>Decision Node</th>
            <th style={thStyle}>Information Set</th>
            <th style={thStyle}>Policy A Choice</th>
            <th style={thStyle}>Policy B Choice</th>
          </tr>
        </thead>
        <tbody>
          {allDecisions.map((decision) => {
            const aList = choicesA[decision] || [];
            const bList = choicesB[decision] || [];
            
            const infoSets = Array.from(new Set([
              ...aList.map(c => c.infoSet),
              ...bList.map(c => c.infoSet)
            ])).sort();

            return infoSets.map((infoSet, idx) => {
              const aChoice = aList.find(c => c.infoSet === infoSet)?.choice || '-';
              const bChoice = bList.find(c => c.infoSet === infoSet)?.choice || '-';
              const diff = aChoice !== bChoice && aChoice !== '-' && bChoice !== '-';

              return (
                <tr key={`${decision}-${infoSet}`} style={{ background: diff ? '#fff3e0' : 'transparent' }}>
                  {idx === 0 ? (
                    <td style={{ ...tdStyle, verticalAlign: 'top' }} rowSpan={infoSets.length}>
                      <strong>{decision}</strong>
                    </td>
                  ) : null}
                  <td style={tdStyle}><code>{infoSet}</code></td>
                  <td style={{ ...tdStyle, fontWeight: diff ? 'bold' : 'normal', color: diff ? '#d84315' : 'inherit' }}>
                    {aChoice}
                  </td>
                  <td style={{ ...tdStyle, fontWeight: diff ? 'bold' : 'normal', color: diff ? '#d84315' : 'inherit' }}>
                    {bChoice}
                  </td>
                </tr>
              );
            });
          })}
        </tbody>
      </table>
    </div>
  );
}

const thStyle: React.CSSProperties = {
  border: '1px solid #ddd',
  padding: '8px',
  textAlign: 'left',
  background: '#f5f5f5',
};

const tdStyle: React.CSSProperties = {
  border: '1px solid #ddd',
  padding: '8px',
  textAlign: 'left',
};
