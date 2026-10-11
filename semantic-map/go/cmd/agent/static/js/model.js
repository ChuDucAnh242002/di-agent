// Everything the page derives from a /state response, in one place.
//
// Two things live here that are easy to get wrong elsewhere.
//
// First, EFFECTIVE STRENGTH. A relationship's JSON carries three layers and no
// effective value, because the agent decides which one answers by authority rather
// than arithmetic: an operator's assertion outranks the long-run estimate, which
// outranks the most recent few pairs, and a relationship nothing has been observed
// about has no strength to offer at all. That last case is why `known` is reported
// separately — rendering an unobserved relationship as 0.00 would show a measured
// zero where there is no measurement.
//
// Second, HISTORY. The map holds a current value per property, not a series. A
// sparkline therefore has to be accumulated here, from successive polls, and it is
// honest only if it says so: these are samples of the agent's state as this page
// saw it, not the agent's own record.

export const HISTORY_LIMIT = 180;

/** Which layer answers, and with what. Mirrors Relationship.Effective in Go. */
export function effective(rel) {
  if (rel.assertion !== undefined && rel.assertion !== null) {
    return { value: clamp01(rel.assertion), basis: 'asserted', known: true };
  }
  if (rel.established !== undefined && rel.established !== null) {
    return { value: clamp01(rel.established), basis: 'established', known: true };
  }
  if (rel.n_observations > 0) {
    return { value: clamp01(rel.strength), basis: 'recent', known: true };
  }
  return { value: 0, basis: 'unknown', known: false };
}

const clamp01 = (v) => (v < 0 ? 0 : v > 1 ? 1 : v);

/** A property's subject, or null for a node-level property. */
export const subjectOf = (p) => (p.subject && p.subject.length ? p.subject : null);

/** The scoped half of `metric@pod:a`, for a label that is not mostly subject. */
export function shortLabel(id) {
  const at = id.indexOf('@');
  return at === -1 ? id : id.slice(0, at);
}

export class Model {
  constructor() {
    this.view = null;           // the last /state response
    this.prev = null;           // the one before it, for diffing
    this.history = new Map();   // property id → [{t, v}]
    this.changed = new Set();   // property ids whose value moved on the last poll
    this.relChanged = new Set();// relationship ids that gained observations
    this.seenRevision = 0;
    this.firstSeen = new Map(); // property id → ms when this page first saw it
  }

  /** Fold a fresh /state response in, recording what moved. */
  ingest(view) {
    const now = Date.now();
    this.prev = this.view;
    this.view = view;
    this.changed.clear();
    this.relChanged.clear();

    const before = new Map((this.prev?.properties || []).map((p) => [p.id, p]));
    for (const p of view.properties || []) {
      const was = before.get(p.id);
      if (!was) this.firstSeen.set(p.id, now);
      if (was && (was.value !== p.value || was.n_observations !== p.n_observations)) {
        this.changed.add(p.id);
      }
      // Only observed movement enters the series. Re-recording an unchanged value
      // every poll would draw a flat line that looks like a measurement and is not.
      if (!was || was.n_observations !== p.n_observations || was.value !== p.value) {
        this.push(p.id, now, p.value);
      }
    }

    const relBefore = new Map((this.prev?.relationships || []).map((r) => [r.id, r]));
    for (const r of view.relationships || []) {
      const was = relBefore.get(r.id);
      if (was && was.n_observations !== r.n_observations) this.relChanged.add(r.id);
    }

    this.seenRevision = view.revision;
    return this;
  }

  push(id, t, v) {
    let s = this.history.get(id);
    if (!s) { s = []; this.history.set(id, s); }
    s.push({ t, v });
    if (s.length > HISTORY_LIMIT) s.shift();
  }

  series(id) { return this.history.get(id) || []; }

  get properties() { return this.view?.properties || []; }
  get relationships() { return this.view?.relationships || []; }
  get counts() { return this.view?.counts || {}; }
  get owner() { return this.view?.owner || ''; }
  get revision() { return this.view?.revision || 0; }

  property(id) { return this.properties.find((p) => p.id === id) || null; }
  relationship(id) { return this.relationships.find((r) => r.id === id) || null; }

  /** Relationships pointing INTO a property — what moves it. */
  influencesOn(id) { return this.relationships.filter((r) => r.to === id); }
  /** Relationships pointing OUT of a property — what it moves. */
  influencesOf(id) { return this.relationships.filter((r) => r.from === id); }

  /** Subjects the map currently models, each with the properties scoped to it. */
  subjects() {
    const by = new Map();
    for (const p of this.properties) {
      const s = subjectOf(p);
      if (!s) continue;
      if (!by.has(s)) by.set(s, []);
      by.get(s).push(p);
    }
    return [...by.entries()]
      .map(([id, props]) => ({
        id,
        kind: id.includes(':') ? id.slice(0, id.indexOf(':')) : 'subject',
        identity: id.includes(':') ? id.slice(id.indexOf(':') + 1) : id,
        properties: props.sort((a, b) => a.id.localeCompare(b.id)),
        status: props.some((p) => p.status === 'active') ? 'active'
          : props.some((p) => p.status === 'stale') ? 'stale' : 'retired',
      }))
      .sort((a, b) => a.id.localeCompare(b.id));
  }

  /** Membership edges: a derived property is recomputed from these, never stored. */
  memberships() {
    const out = [];
    const ids = new Set(this.properties.map((p) => p.id));
    for (const p of this.properties) {
      for (const m of p.members || []) {
        if (ids.has(m)) out.push({ from: m, to: p.id });
      }
    }
    return out;
  }
}

// ── formatting ──────────────────────────────────────────────────────────────

export const fmt = {
  num: (v, d = 3) => (v === null || v === undefined || Number.isNaN(v) ? '—' : Number(v).toFixed(d)),
  pct: (v) => (v === null || v === undefined ? '—' : `${(v * 100).toFixed(1)}%`),
  int: (v) => (v === null || v === undefined ? '—' : String(v)),
  signed: (v, d = 3) => (v > 0 ? '+' : v < 0 ? '−' : '') + Math.abs(v).toFixed(d),

  /** Short, absolute-free age — the thing an operator actually reads. */
  ago(iso) {
    if (!iso || iso.startsWith('0001')) return '—';
    const ms = Date.now() - new Date(iso).getTime();
    if (!Number.isFinite(ms)) return '—';
    return fmt.duration(ms / 1000) + ' ago';
  },
  duration(sec) {
    sec = Math.max(0, Math.round(sec));
    if (sec < 60) return `${sec}s`;
    if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`;
    if (sec < 86400) return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`;
    return `${Math.floor(sec / 86400)}d ${Math.floor((sec % 86400) / 3600)}h`;
  },
  clock(iso) {
    if (!iso) return '—';
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? '—'
      : d.toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' });
  },
  /** A value shown with its unit, and normalised only when a range was declared. */
  value(p) {
    const v = fmt.num(p.value, 3);
    return p.unit ? `${v} ${p.unit}` : v;
  },
};

export function escapeHTML(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

/** Direction arrives as a Go iota on the candidate wire: 0 positive, 1 negative. */
export const directionSign = (d) => (d === 1 || d === '1' || d === 'negative' ? -1 : 1);
