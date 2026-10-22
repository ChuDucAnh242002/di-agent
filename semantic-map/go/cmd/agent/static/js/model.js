// Everything the page derives from a /state response, in one place.
//
// Two things live here that are easy to get wrong elsewhere.
//
// First, EFFECTIVE STRENGTH. A relationship's JSON carries up to four layers, and the
// agent decides which one answers by authority rather than arithmetic: an operator's
// assertion outranks what the agent's own interventions measured, which outranks the
// long-run estimate, which outranks the most recent few pairs,
// and a relationship nothing has been observed about has no strength to offer at all.
// The agent reports its own answer as `effective` and `basis`, and the page uses that
// rather than re-deriving it. `effective` is absent for an unknown relationship, which
// is why `known` is reported separately — rendering one as 0.00 would show a measured
// zero where there is no measurement.
//
// Second, HISTORY. The map holds a current value per property, not a series. A
// sparkline therefore has to be accumulated here, from successive polls, and it is
// honest only if it says so: these are samples of the agent's state as this page
// saw it, not the agent's own record.

export const HISTORY_LIMIT = 180;

/** Which layer answers, and with what, as the agent reported it. */
export function effective(rel) {
  const known = rel.effective !== undefined && rel.effective !== null;
  return { value: known ? clamp01(rel.effective) : 0, basis: rel.basis, known };
}

const clamp01 = (v) => (v < 0 ? 0 : v > 1 ? 1 : v);

/** A property's subject, or null for a node-level property. */
export const subjectOf = (p) => (p.subject && p.subject.length ? p.subject : null);

/**
 * What an operator calls a property's subject: a pod's namespace/name or a unit's
 * name, from the labels its collector stamped, or null when it stamped none. The
 * subject itself (pod:<uid>) stays the identity everywhere; this is only what is shown.
 */
export function subjectName(p) {
  const l = p?.labels || {};
  if (l.pod) return l.namespace ? `${l.namespace}/${l.pod}` : l.pod;
  if (l.unit) return l.unit;
  return null;
}

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
    // What the agent may change and what it did: GET /relief, /relief/actions and
    // /workloads. relief is null when the spec leaves the loop off.
    this.acting = { relief: null, actions: [], workloads: [] };
    this.workloadBy = new Map();
    // 'unknown' until /relief has answered; 'on', or 'off' once it said 404.
    this.actingState = 'unknown';
    this.actingError = '';
    this._names = null;         // names(), rebuilt only when /state or /workloads changes
  }

  /** Fold in the acting endpoints; a missing one is an empty one. */
  setActing({ relief, actions, workloads }) {
    this.acting = { relief: relief || null, actions: actions || [], workloads: workloads || [] };
    this.workloadBy = new Map(this.acting.workloads.map((w) => [w.subject, w]));
    this._names = null;
  }

  /** The runtime's workload for a subject, or null when it does not list one. */
  workloadOf(subject) { return this.workloadBy.get(subject) || null; }

  /** An action is in force from the act until it ends: in flight (outcome '') or kept. */
  throttled(subject) {
    return this.acting.actions.some((a) => a.subject === subject && (a.outcome === '' || a.outcome === 'kept'));
  }

  frozen(subject) { return !!this.workloadOf(subject)?.policy?.frozen; }

  /** Fold a fresh /state response in, recording what moved. */
  ingest(view) {
    const now = Date.now();
    this.prev = this.view;
    this.view = view;
    this._names = null;
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
  /** What the machine is — OS, CPU, memory, disk — as the agent read it at startup. */
  get facts() { return this.view?.facts || []; }
  fact(key) { return this.facts.find((f) => f.key === key) || null; }
  get revision() { return this.view?.revision || 0; }

  property(id) { return this.properties.find((p) => p.id === id) || null; }
  relationship(id) { return this.relationships.find((r) => r.id === id) || null; }

  /** Relationships pointing INTO a property — what moves it. */
  influencesOn(id) { return this.relationships.filter((r) => r.to === id); }
  /** Relationships pointing OUT of a property — what it moves. */
  influencesOf(id) { return this.relationships.filter((r) => r.from === id); }

  /** Each subject that has a name, by subject; see subjectName and /workloads. */
  names() {
    if (this._names) return this._names;
    const out = new Map();
    // The runtime's own name for the workload wins over what a collector stamped.
    for (const w of this.acting.workloads) if (w.name) out.set(w.subject, w.name);
    for (const p of this.properties) {
      const s = subjectOf(p), n = subjectName(p);
      if (s && n && !out.has(s)) out.set(s, n);
    }
    this._names = out;
    return out;
  }

  /** A subject's name, or the subject itself when it has none. */
  nameOf(subject) {
    const g = this.topology?.groups?.find((x) => x.id === subject);
    if (g) return `${g.name} · ${g.members.length}`;
    const name = this.names().get(subject) || subject;
    const in_ = this.topology?.groups?.find((x) => x.consumers && x.members.includes(subject));
    return in_ ? `${name} · ${in_.name}` : name;
  }

  /**
   * The enclosure a property is drawn in: its subject's, except that a container and a
   * virtual interface are drawn in their pod's, and a core, disk or interface in its
   * hardware or network group's — so the canvas shows workloads beside one box of
   * machine parts rather than a box per core. Null for the machine's own properties.
   * The topology is GET /groups; without it every subject has its own box.
   */
  boxOf(p) {
    const s = subjectOf(p);
    if (!s) return null;
    const parent = this.topology?.parents?.[s];
    if (parent) return parent;
    const structure = this.topology?.groups?.find((g) => !g.consumers && g.members.includes(s));
    return structure ? structure.id : s;
  }

  /** A property id as shown: `metric@<name>` for a named subject, else unchanged. */
  display(id) {
    const at = id.indexOf('@');
    if (at === -1) return id;
    const s = id.slice(at + 1), n = this.names().get(s);
    return n ? `${id.slice(0, at)}@${n}` : id;
  }

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
        name: props.map(subjectName).find(Boolean) || null,
        properties: props.sort((a, b) => a.id.localeCompare(b.id)),
        status: props.some((p) => p.status === 'active') ? 'active'
          : props.some((p) => p.status === 'stale') ? 'stale' : 'retired',
      }))
      // By what is shown: sorted by UID, named pods would come out in no visible order.
      .sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id));
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
  bytes(n) {
    if (n === null || n === undefined) return '—';
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return `${i ? n.toFixed(1) : n} ${units[i]}`;
  },
  /** A value shown with its unit, and normalised only when a range was declared. */
  value(p) {
    const v = fmt.num(p.value, 3);
    return p.unit ? `${v} ${p.unit}` : v;
  },
};

/** A fact's value as read: bytes in binary units, anything else as given. */
export function factValue(f) {
  if (f.number !== undefined && f.unit === 'bytes') return fmt.bytes(f.number);
  if (f.number !== undefined && f.unit) return `${f.value} ${f.unit}`;
  return f.value;
}

/** What each subject the map holds uses of the machine, from the map's own values. */
const ROLES = {
  'ollama.service': 'the agent\'s decision model',
  'di-agent.service': 'the agent',
  'k0scontroller.service': 'Kubernetes (k0s)',
  'containerd.service': 'container runtime',
  'netdata.service': 'telemetry (Netdata)',
};

/**
 * The machine's consumers: every pod and unit the map models, with its CPU in cores and
 * memory in bytes (both are fractions of the node in the map), largest CPU first, and the
 * remainder no subject accounts for — the kernel, sessions, every unit not modelled.
 * The remainder is computed here from the node's values and the subjects', never stored,
 * and is null where the subjects add up to more than the node: cgroup memory counts the
 * page cache a cgroup has read, the node's in-use figure does not.
 */
export function consumers(model) {
  const cores = model.fact('cpu_cores')?.number ?? null;
  const memTotal = model.fact('memory_total')?.number ?? null;
  const val = (id) => {
    const p = model.property(id);
    return p && p.n_observations && p.status !== 'retired' ? p.value : null;
  };
  const cpu = (f) => (f === null || cores === null ? null : f * cores);
  const mem = (f) => (f === null || memTotal === null ? null : f * memTotal);
  const rows = model.subjects()
    .filter((s) => (s.kind === 'pod' || s.kind === 'unit') && s.status !== 'retired')
    .map((s) => {
      const name = s.name || s.identity;
      return {
        subject: s.id, kind: s.kind, name, status: s.status,
        role: s.kind === 'unit' ? (ROLES[name] || 'system unit') : (name.startsWith('kube-system/') ? 'system pod' : 'workload'),
        cpu: cpu(val(`cpu_utilization@${s.id}`)),
        mem: mem(val(`memory_utilization@${s.id}`)),
      };
    })
    .sort((a, b) => (b.cpu ?? -1) - (a.cpu ?? -1));
  const node = { cpu: cpu(val('cpu_utilization')), mem: mem(val('memory_utilization')) };
  const sum = (k) => rows.reduce((t, r) => t + (r[k] ?? 0), 0);
  // The node's value and each subject's are separate averages over slightly different
  // windows, so a remainder within 2% of the machine below zero is none, not a contradiction.
  const total = { cpu: cores, mem: memTotal };
  const rest = (k) => {
    if (node[k] === null) return null;
    const r = node[k] - sum(k);
    return r >= 0 ? r : total[k] !== null && r > -0.02 * total[k] ? 0 : null;
  };
  return { cores, memTotal, node, rows, rest: { cpu: rest('cpu'), mem: rest('mem') } };
}

/**
 * The consumers as a tree an operator can open: each group the topology names (GET
 * /groups) with its members, each pod with its containers and virtual interfaces, the
 * consumers no group holds under "ungrouped", the hardware and network as structure,
 * and the rest no subject accounts for. A consumer group's numbers are the map's own sums
 * (cpu_utilization@group:<name>); every other number is what consumers() reads.
 */
export function resourceTree(model, topology) {
  const c = consumers(model);
  const groups = topology?.groups || [];
  const parents = topology?.parents || {};
  const children = {};
  for (const [child, parent] of Object.entries(parents)) (children[parent] ||= []).push(child);
  const val = (id) => {
    const p = model.property(id);
    return p && p.n_observations && p.status !== 'retired' ? p.value : null;
  };
  const scale = (f, total) => (f === null || total === null ? null : f * total);
  const name = (s) => model.subjects().find((x) => x.id === s)?.name || s.slice(s.indexOf(':') + 1);
  const part = (s) => {
    const kind = s.slice(0, s.indexOf(':'));
    const label = kind === 'container' ? (model.subjects().find((x) => x.id === s)?.properties[0]?.labels?.container || name(s)) : name(s);
    return { id: s, label, role: kind === 'container' ? 'container' : kind === 'net' ? 'network interface' : kind,
      cpu: scale(val(`cpu_utilization@${s}`), c.cores), mem: scale(val(`memory_utilization@${s}`), c.memTotal), children: [] };
  };
  const row = (r) => ({ id: r.subject, label: r.name, role: r.role, cpu: r.cpu, mem: r.mem, status: r.status,
    children: (children[r.subject] || []).sort().map(part) });
  const byCPU = (a, b) => (b.cpu ?? -1) - (a.cpu ?? -1);
  const held = new Set();
  const items = [];
  for (const g of groups) {
    if (g.consumers) {
      const members = c.rows.filter((r) => g.members.includes(r.subject));
      members.forEach((r) => held.add(r.subject));
      items.push({ id: g.id, label: g.name, role: `group · ${g.source}`, group: true,
        cpu: scale(val(`cpu_utilization@${g.id}`), c.cores), mem: scale(val(`memory_utilization@${g.id}`), c.memTotal),
        children: members.map(row).sort(byCPU) });
    }
  }
  const loose = c.rows.filter((r) => !held.has(r.subject)).map(row);
  if (loose.length) {
    const sum = (k) => loose.reduce((t, r) => (r[k] === null ? t : (t ?? 0) + r[k]), null);
    items.push({ id: 'ungrouped', label: 'ungrouped', role: 'no group label', group: true,
      cpu: sum('cpu'), mem: sum('mem'), children: loose.sort(byCPU) });
  }
  items.sort(byCPU);
  for (const g of groups) {
    if (!g.consumers) {
      items.push({ id: g.id, label: g.name, role: g.source, group: true, structure: true, cpu: null, mem: null,
        children: g.members.map(part) });
    }
  }
  return { cores: c.cores, memTotal: c.memTotal, node: c.node, rest: c.rest, items };
}

/** The machine in one line, for its enclosure: name, then how much of it there is. */
export function machineSummary(model) {
  const f = (k) => model.fact(k);
  const parts = [model.owner || f('hostname')?.value || 'this machine'];
  if (f('cpu_cores')) parts.push(`${f('cpu_cores').value} cores`);
  if (f('memory_total')) parts.push(fmt.bytes(f('memory_total').number));
  if (f('disk_total')) parts.push(`${fmt.bytes(f('disk_total').number)} disk`);
  if (f('os')) parts.push(f('os').value);
  return parts.join(' · ');
}

export function escapeHTML(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

/** Direction arrives as a Go iota on the candidate wire: 0 positive, 1 negative. */
export const directionSign = (d) => (d === 1 || d === '1' || d === 'negative' ? -1 : 1);

/**
 * Whether the operator is working a control inside root that a rebuild would
 * destroy: typing, dragging a slider, or choosing from a dropdown. Buttons do not
 * count — they keep focus after a click and after a dialog closes, and treating
 * that as "busy" would freeze a live pane until the operator clicked elsewhere.
 */
export function isEditing(el, root) {
  if (!el || el === document.body || !root.contains(el)) return false;
  if (el.isContentEditable || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT') return true;
  if (el.tagName !== 'INPUT') return false;
  return !['button', 'submit', 'reset', 'checkbox', 'radio'].includes(el.type);
}
