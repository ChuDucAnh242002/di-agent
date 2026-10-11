// The HTTP layer.
//
// One place that knows URLs, so a route change is a one-file edit, and one place
// that knows how this agent reports failure: every 4xx/5xx carries {"error": …},
// and mutations require an explicit JSON content type (the daemon's lightweight
// CSRF mitigation — see ARCHITECTURE.md §9). A caller gets either a parsed body
// or an Error whose message is the agent's own words, never a bare status code.

const JSON_CT = { 'Content-Type': 'application/json' };

async function send(method, path, { body, accept } = {}) {
  const init = { method, headers: {} };
  if (accept) init.headers['Accept'] = accept;
  if (body !== undefined) {
    Object.assign(init.headers, JSON_CT);
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);

  if (res.status === 204) return null;

  const text = await res.text();
  const isJSON = (res.headers.get('content-type') || '').includes('json');

  if (!res.ok) {
    let msg = text;
    if (isJSON) {
      try { msg = JSON.parse(text).error || text; } catch (_) { /* keep raw */ }
    }
    const err = new Error(msg || `${res.status} ${res.statusText}`);
    err.status = res.status;
    // A decision that was evicted from the bounded journal answers 410, which a
    // caller must be able to tell from one that never existed.
    err.gone = res.status === 410;
    throw err;
  }
  if (!isJSON) return text;
  return text ? JSON.parse(text) : null;
}

const q = (params) => {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) {
    if (v === undefined || v === null || v === '') continue;
    if (Array.isArray(v)) v.forEach((x) => u.append(k, x));
    else u.append(k, v);
  }
  const s = u.toString();
  return s ? `?${s}` : '';
};

export const api = {
  // ── the state model ──────────────────────────────────────────────────────
  state: (params) => send('GET', `/state${q(params)}`),
  property: (id) => send('GET', `/state/properties/${encodeURIComponent(id)}`),
  explainProperty: (id) => send('GET', `/state/properties/${encodeURIComponent(id)}`, { accept: 'text/plain' }),
  relationships: (params) => send('GET', `/state/relationships${q(params)}`),
  journal: (params) => send('GET', `/state/journal${q(params)}`),
  decisions: (limit) => send('GET', `/state/decisions${q({ limit })}`),
  decision: (id) => send('GET', `/state/decisions/${encodeURIComponent(id)}`),

  // assume and without are repeatable; the query builder above appends arrays.
  estimate: ({ target, assume, without, id }) =>
    send('GET', `/state/estimate${q({ target, assume, without, id })}`),

  sweep: () => send('POST', '/state/sweep', { body: {} }),

  // Retirement and assertion both demand a reason. That is the agent's rule, not
  // this page's: a claim with no stated basis cannot be audited later.
  retireProperty: (id, reason, actor) =>
    send('DELETE', `/state/properties/${encodeURIComponent(id)}${q({ reason, actor })}`),
  retireRelationship: (id, reason, actor) =>
    send('DELETE', `/state/relationships/${encodeURIComponent(id)}${q({ reason, actor })}`),
  assertStrength: (id, strength, reason, actor) =>
    send('POST', `/state/relationships/${encodeURIComponent(id)}/strength`,
      { body: { strength, reason, actor } }),

  // ── peers ────────────────────────────────────────────────────────────────
  statePeers: () => send('GET', '/state/peers'),
  refreshPeers: () => send('POST', '/state/peers/refresh', { body: {} }),
  where: (property) => send('GET', `/state/where${q({ property })}`),

  // ── discovery queue ──────────────────────────────────────────────────────
  candidates: () => send('GET', '/candidates'),
  reviewCandidate: (id, verdict) =>
    send('POST', `/candidates/${encodeURIComponent(id)}/${verdict}`, { body: {} }),

  // ── the declared backbone, kept readable beside what was learned ─────────
  graph: () => send('GET', '/graph'),
  propositions: () => send('GET', '/propositions'),
  deprecate: (propositionID, reason) =>
    send('POST', '/ontology/deprecate', { body: { proposition_id: propositionID, reason } }),
  tune: (intent, operator) => send('POST', '/agent/tune', { body: { intent, operator } }),

  // ── the natural-language surface ─────────────────────────────────────────
  //
  // Answers take seconds to minutes against a local model, so this one is left
  // without a client-side deadline: the agent's own budget is the limit that
  // matters, and cutting it short here would report a timeout for work that is
  // still running.
  explain: (question, opts = {}) =>
    send('POST', '/explain', { body: { question, ...opts } }),

  // ── meta ─────────────────────────────────────────────────────────────────
  health: () => send('GET', '/healthz'),
  version: () => send('GET', '/version'),
};
