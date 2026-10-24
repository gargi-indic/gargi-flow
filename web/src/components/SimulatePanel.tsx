import { useState, useMemo } from 'react';
import * as yaml from 'js-yaml';
import { parseSim } from '../sim/simDoc';
import {
  saveModel,
  createExperiment,
  followProgress,
  getReport,
  type StreamExperimentProgressResponse,
} from '../sim/experiments';

interface SimulatePanelProps {
  simYaml: string;
  graphYaml: string;
  setSummaryJson: (data: unknown) => void;
  setExperimentId: (id: string) => void;
}

export function SimulatePanel({
  simYaml,
  graphYaml,
  setSummaryJson,
  setExperimentId,
}: SimulatePanelProps) {
  const availableWorlds = useMemo(() => {
    const parsed = parseSim(simYaml);
    if (!(parsed instanceof Error) && parsed.worlds) {
      return Object.keys(parsed.worlds);
    }
    return [];
  }, [simYaml]);

  // The actual selected worlds are availableWorlds minus excludedWorlds
  const [excludedWorlds, setExcludedWorlds] = useState<string[]>([]);
  const worlds = availableWorlds.filter(w => !excludedWorlds.includes(w));

  const toggleWorld = (worldName: string) => {
    setExcludedWorlds((prev) =>
      prev.includes(worldName)
        ? prev.filter((w) => w !== worldName)
        : [...prev, worldName]
    );
  };

  const [search, setSearch] = useState<'exact' | 'exhaustive'>('exact');
  const [evaluator, setEvaluator] = useState<'exact' | 'monte_carlo' | 'both'>('exact');
  const [runs, setRuns] = useState<number>(1000);

  const [progress, setProgress] = useState<StreamExperimentProgressResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [bestPerWorld, setBestPerWorld] = useState<{ world: string, policies?: { policyId: string, objective?: { mean: number } }[] }[]>([]);
  const [isRunning, setIsRunning] = useState(false);

  const handleRun = async () => {
    setError(null);
    setProgress(null);
    setBestPerWorld([]);
    setSummaryJson(null);
    setExperimentId('');
    setIsRunning(true);

    try {
      // 1. Save Model
      const modelRef = await saveModel({ sim: simYaml, graph: graphYaml });

      // 2. Build Experiment YAML
      const expData: Record<string, unknown> = {
        apiVersion: 'experiment/v1',
        worlds,
        policies: { search },
        evaluator,
        seed: 42,
        top: 5,
        reports: ['regret', 'policy_tree', 'path_heatmap'],
      };

      if (evaluator === 'exact') {
        expData.runs = 1;
      } else {
        expData.runs = runs;
      }

      const experimentYaml = yaml.dump(expData);

      // 3. Create Experiment
      const { id } = await createExperiment(modelRef, experimentYaml);
      setExperimentId(id);

      // 4. Follow Progress
      followProgress(
        id,
        (prog) => {
          setProgress(prog);
        },
        (doneEvent) => {
          setProgress(doneEvent);
          setIsRunning(false);

          if (doneEvent.status === 'FAILED') {
             setError(doneEvent.error || 'Experiment failed');
             return;
          }

          getReport<{ worlds?: { world: string, policies?: { policyId: string, objective?: { mean: number } }[] }[] }>(id, 'summary').then((summary) => {
            setSummaryJson(summary);
            if (summary && summary.worlds) {
              setBestPerWorld(summary.worlds as { world: string, policies?: { policyId: string, objective?: { mean: number } }[] }[]);
            }
          }).catch((e: Error) => {
             setError(e.message || 'Failed to fetch report');
          });
        }
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Unknown error');
      setIsRunning(false);
    }
  };

  return (
    <div data-testid="sim-simulate" className="p-4 flex flex-col gap-4">
      <h3 className="text-lg font-semibold">Simulate</h3>

      <div className="flex flex-col gap-2">
        <h4 className="font-medium">Worlds</h4>
        {availableWorlds.map((w) => (
          <label key={w} className="flex items-center gap-2">
            <input
              type="checkbox"
              data-testid={`simulate-world-${w}`}
              checked={worlds.includes(w)}
              onChange={() => toggleWorld(w)}
              disabled={isRunning}
            />
            {w}
          </label>
        ))}
      </div>

      <div className="flex flex-col gap-2">
        <label className="flex items-center gap-2">
          Search:
          <select
            value={search}
            onChange={(e) => setSearch(e.target.value as 'exact' | 'exhaustive')}
            disabled={isRunning}
            className="border p-1 rounded"
          >
            <option value="exact">exact</option>
            <option value="exhaustive">exhaustive</option>
          </select>
        </label>

        <label className="flex items-center gap-2">
          Evaluator:
          <select
            value={evaluator}
            onChange={(e) => setEvaluator(e.target.value as 'exact' | 'monte_carlo' | 'both')}
            disabled={isRunning}
            className="border p-1 rounded"
          >
            <option value="exact">exact</option>
            <option value="monte_carlo">monte_carlo</option>
            <option value="both">both</option>
          </select>
        </label>

        {evaluator !== 'exact' && (
          <label className="flex items-center gap-2">
            Runs:
            <input
              type="number"
              value={runs}
              onChange={(e) => setRuns(Number(e.target.value))}
              disabled={isRunning}
              className="border p-1 rounded w-24"
            />
          </label>
        )}
      </div>

      <button
        data-testid="simulate-run"
        onClick={handleRun}
        disabled={isRunning || worlds.length === 0}
        className="bg-blue-600 text-white px-4 py-2 rounded self-start disabled:opacity-50"
      >
        {isRunning ? 'Running...' : 'Run Experiment'}
      </button>

      {error && (
        <div data-testid="simulate-error" className="text-red-600 bg-red-50 p-2 rounded">
          {error}
        </div>
      )}

      {progress && progress.progress && (
        <div data-testid="simulate-progress" className="bg-gray-50 p-2 rounded text-sm">
          <div>
            done/total &middot; best {progress.progress.best} {progress.progress.bestMean} &plusmn; {progress.progress.ciWidth ? progress.progress.ciWidth / 2 : 0}
          </div>
          <div className="flex items-center gap-2 mt-1">
            <progress
              className="flex-1"
              value={progress.progress.done}
              max={progress.progress.total}
            />
            <span>{progress.progress.done} / {progress.progress.total}</span>
          </div>
        </div>
      )}

      {bestPerWorld.length > 0 && (
        <table className="w-full text-left border-collapse">
          <thead>
            <tr>
              <th className="border-b p-2">World</th>
              <th className="border-b p-2">Best Policy</th>
              <th className="border-b p-2">Mean</th>
            </tr>
          </thead>
          <tbody>
            {bestPerWorld.map((pw) => (
              <tr key={pw.world}>
                <td className="border-b p-2">{pw.world}</td>
                <td className="border-b p-2 text-xs truncate max-w-xs">{pw.policies?.[0]?.policyId}</td>
                <td className="border-b p-2" data-testid={`simulate-best-${pw.world}`}>
                  {pw.policies?.[0]?.objective?.mean}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
