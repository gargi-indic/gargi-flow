import * as yaml from 'js-yaml';

export type SimDoc = {
  worlds?: Record<string, Record<string, unknown>>;
  latents?: Record<string, unknown>;
  objective?: {
    name?: string;
    value?: string;
    maximize?: string;
    requirements?: string[];
  };
  resources?: Record<string, number>;
  arrivals?: {
    distribution?: string;
    parameters?: Record<string, number>;
  };
};

export function parseSim(text: string): {
  worlds: Record<string, Record<string, unknown>>;
  latents: Record<string, unknown>;
  objective: { name?: string; value?: string; maximize?: string; requirements?: string[] };
  resources: Record<string, number>;
  arrivals: { distribution?: string; parameters?: Record<string, number> };
} | Error {
  try {
    const doc = yaml.load(text) as SimDoc;
    if (doc && typeof doc !== 'object') {
      return new Error('Invalid YAML object');
    }
    const d = doc || {};
    return {
      worlds: d.worlds || {},
      latents: d.latents || {},
      objective: d.objective || {},
      resources: d.resources || {},
      arrivals: d.arrivals || {},
    };
  } catch (e) {
    return e instanceof Error ? e : new Error(String(e));
  }
}

export function setWorldParam(text: string, world: string, param: string, value: string): string {
  let doc: Record<string, unknown>;
  try {
    doc = (yaml.load(text) || {}) as Record<string, unknown>;
  } catch {
    doc = {};
  }
  if (!doc.worlds) doc.worlds = {};

  const worlds = doc.worlds as Record<string, Record<string, unknown>>;
  if (!worlds[world]) worlds[world] = {};

  const num = Number(value);
  worlds[world][param] = !isNaN(num) && value.trim() !== '' ? num : value;

  return yaml.dump(doc, { skipInvalid: true, noRefs: true });
}

export function addWorld(text: string, name: string): string {
  let doc: Record<string, unknown>;
  try {
    doc = (yaml.load(text) || {}) as Record<string, unknown>;
  } catch {
    doc = {};
  }
  if (!doc.worlds) doc.worlds = {};

  const worlds = doc.worlds as Record<string, Record<string, unknown>>;
  const newWorld: Record<string, unknown> = {};
  const existingWorlds = Object.keys(worlds);

  if (existingWorlds.length > 0) {
    const firstWorld = worlds[existingWorlds[0]];
    if (firstWorld && typeof firstWorld === 'object') {
      for (const param of Object.keys(firstWorld)) {
        newWorld[param] = '';
      }
    }
  }

  worlds[name] = newWorld;

  return yaml.dump(doc, { skipInvalid: true, noRefs: true });
}

export function removeWorld(text: string, name: string): string {
  let doc: Record<string, unknown>;
  try {
    doc = (yaml.load(text) || {}) as Record<string, unknown>;
  } catch {
    return text;
  }

  const worlds = doc.worlds as Record<string, Record<string, unknown>> | undefined;
  if (worlds && worlds[name]) {
    delete worlds[name];
  }
  return yaml.dump(doc, { skipInvalid: true, noRefs: true });
}

export function setLatent(text: string, name: string, specYamlText: string): string {
  let doc: Record<string, unknown>;
  try {
    doc = (yaml.load(text) || {}) as Record<string, unknown>;
  } catch {
    doc = {};
  }
  if (!doc.latents) doc.latents = {};

  let spec: unknown;
  try {
    spec = yaml.load(specYamlText);
  } catch {
    return text; // Invalid YAML, doesn't change document
  }

  const latents = doc.latents as Record<string, unknown>;
  latents[name] = spec;
  return yaml.dump(doc, { skipInvalid: true, noRefs: true });
}

export function removeLatent(text: string, name: string): string {
  let doc: Record<string, unknown>;
  try {
    doc = (yaml.load(text) || {}) as Record<string, unknown>;
  } catch {
    return text;
  }

  const latents = doc.latents as Record<string, unknown> | undefined;
  if (latents && latents[name]) {
    delete latents[name];
  }
  return yaml.dump(doc, { skipInvalid: true, noRefs: true });
}

export function setObjective(text: string, objective: { name?: string; value?: string; maximize?: string; requirements?: string[] }): string {
  let doc: Record<string, unknown>;
  try {
    doc = (yaml.load(text) || {}) as Record<string, unknown>;
  } catch {
    doc = {};
  }
  doc.objective = objective;
  return yaml.dump(doc, { skipInvalid: true, noRefs: true });
}

export function setResources(text: string, resources: Record<string, number>): string {
  let doc: Record<string, unknown>;
  try {
    doc = (yaml.load(text) || {}) as Record<string, unknown>;
  } catch {
    doc = {};
  }
  doc.resources = resources;
  return yaml.dump(doc, { skipInvalid: true, noRefs: true });
}

export function setArrivals(text: string, arrivals: { distribution?: string; parameters?: Record<string, number> }): string {
  let doc: Record<string, unknown>;
  try {
    doc = (yaml.load(text) || {}) as Record<string, unknown>;
  } catch {
    doc = {};
  }
  doc.arrivals = arrivals;
  return yaml.dump(doc, { skipInvalid: true, noRefs: true });
}
