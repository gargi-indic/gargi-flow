const baseUrl = import.meta.env.VITE_API_BASE_URL || 'http://api.gargi.test';

export interface ModelRef {
  id: string;
  version: string;
}

export async function saveModel(docs: { sim: string; graph: string }): Promise<ModelRef> {
  const response = await fetch(`${baseUrl}/v1/models`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      sim: docs.sim,
      graph: docs.graph,
      format: 'yaml',
    }),
  });

  if (!response.ok) {
    throw new Error(`Failed to save model: ${response.statusText}`);
  }

  const data = await response.json() as { model: ModelRef };
  return data.model;
}

export async function createExperiment(
  model: ModelRef,
  experimentYaml: string
): Promise<{ id: string }> {
  const response = await fetch(`${baseUrl}/v1/experiments`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      model: model,
      experiment: experimentYaml,
      format: 'yaml',
    }),
  });

  if (!response.ok) {
    throw new Error(`Failed to create experiment: ${response.statusText}`);
  }

  const data = await response.json() as { id: string };
  return { id: data.id };
}

export interface StreamExperimentProgressResponse {
  id: string;
  sequence: string | number;
  eventType: string;
  status: string;
  progress?: {
    done: string | number;
    total: string | number;
    best: string;
    bestMean: number;
    ciWidth?: number;
  };
  error?: string;
}

export function followProgress(
  id: string,
  onProgress: (progress: StreamExperimentProgressResponse) => void,
  onDone: (finalData: StreamExperimentProgressResponse) => void
): () => void {
  const source = new EventSource(`${baseUrl}/v1/experiments/${id}/events`);

  const handleMessage = (event: MessageEvent, isDone: boolean) => {
    try {
      const data = JSON.parse(event.data) as StreamExperimentProgressResponse;
      if (data.progress) {
        data.progress.done = Number(data.progress.done);
        data.progress.total = Number(data.progress.total);
      }

      if (isDone || data.eventType === 'done' || data.status === 'EXPERIMENT_STATUS_SUCCEEDED' || data.status === 'FAILED') {
        source.close();
        onDone(data);
      } else {
        onProgress(data);
      }
    } catch (err) {
      console.error('Error parsing SSE message', err);
    }
  };

  source.addEventListener('progress', (e) => handleMessage(e, false));
  source.addEventListener('done', (e) => handleMessage(e, true));

  source.onerror = (err) => {
    console.error('EventSource failed', err);
    source.close();
  };

  return () => {
    source.close();
  };
}

export async function getReport<T = unknown>(id: string, kind: string): Promise<T> {
  const response = await fetch(`${baseUrl}/v1/experiments/${id}/reports/${kind}`);

  if (!response.ok) {
    throw new Error(`Failed to get report ${kind}: ${response.statusText}`);
  }

  return await response.json() as T;
}
