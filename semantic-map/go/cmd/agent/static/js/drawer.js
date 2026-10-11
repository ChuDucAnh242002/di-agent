// The drawer: the record, the queue, and everything wider than one property.
//
// Journal, decisions, discovery, subjects, cluster, backbone — six panes over the
// endpoints the previous console never called. Two habits run through all of them:
//
//   * an empty list always says WHY it is empty. "No candidates" and "the proposer
//     needs 30 pairs and has 11" are different facts, and only the second one tells
//     an operator whether to wait or to go and look at something.
//   * a bounded window is stated. The journal drops its oldest entries, and a
//     reader who cannot see that will read an absence as evidence that nothing
//     happened.

import { api } from './api.js';
import { fmt, escapeHTML, effective, directionSign } from './model.js';

const JOURNAL_BUFFER = 400;

export class Drawer {
  constructor(root, ctx) {
    this.root = root;
    this.ctx = ctx;                 // { model, toast, selectProperty, refresh, ask }
    this.active = 'journal';
    this.events = [];
    this.journalWindow = null;
    this.sinceRevision = 0;
    this.decisions = [];
    this.openDecisionID = null;
    this.candidates = [];
    this.peers = null;
    this.backbone = null;
    this.panes = {};
    for (const name of ['journal', 'decisions', 'discovery', 'subjects', 'cluster', 'backbone']) {
      this.panes[name] = document.getElementById(`pane-${name}`);
    }
    this.wireTabs();
  }

  wireTabs() {
    this.root.querySelectorAll('.tab').forEach((tab) => {
      tab.addEventListener('click', () => this.activate(tab.dataset.pane));
    });
    document.getElementById('drawer-toggle').addEventListener('click', () => {
      const collapsed = this.root.classList.toggle('collapsed');
      document.getElementById('drawer-toggle').textContent = collapsed ? '▴' : '▾';
    });
  }

  /** Returns the poll it starts, so a caller can wait for it. */
  activate(name) {
    this.active = name;
    this.root.classList.remove('collapsed');
    document.getElementById('drawer-toggle').textContent = '▾';
    this.root.querySelectorAll('.tab').forEach((t) => {
      t.setAttribute('aria-selected', String(t.dataset.pane === name));
    });
    for (const [k, pane] of Object.entries(this.panes)) {
      pane.classList.toggle('active', k === name);
    }
    return this.poll({ force: true });
  }

  /** Called after every /state poll, plus on demand. */
  async poll({ force = false } = {}) {
    // The journal and the discovery queue are cheap and are what makes the page
    // feel live, so they are fetched every tick regardless of which pane is open;
    // the rest are fetched only when visible.
    await Promise.allSettled([
      this.pollJournal(),
      this.pollCandidates(),
      this.active === 'decisions' || force ? this.pollDecisions() : null,
      this.active === 'cluster' ? this.pollPeers() : null,
      this.active === 'backbone' && (!this.backbone || force) ? this.pollBackbone() : null,
    ]);
    this.renderActive();
    this.renderCounts();
  }

  renderActive() {
    switch (this.active) {
      case 'journal': return this.renderJournal();
      case 'decisions': return this.renderDecisions();
      case 'discovery': return this.renderDiscovery();
      case 'subjects': return this.renderSubjects();
      case 'cluster': return this.renderCluster();
      case 'backbone': return this.renderBackbone();
    }
  }

  renderCounts() {
    const model = this.ctx.model();
    set('c-journal', this.events.length);
    set('c-decisions', this.decisions.length);
    set('c-subjects', model?.counts?.subjects ?? 0);
    set('c-cluster', (this.peers?.peers?.length ?? 0)
      + Object.keys(this.peers?.silent || {}).length);
    const cEl = document.getElementById('c-discovery');
    cEl.textContent = this.candidates.length;
    // Pending discovery is the one count worth interrupting for: the map has
    // produced structure it was not told about and is waiting on a human.
    cEl.classList.toggle('hot', this.candidates.length > 0);
  }

  // ── journal ───────────────────────────────────────────────────────────────

  async pollJournal() {
    const res = await api.journal({ since: this.sinceRevision || undefined, limit: 200 });
    this.journalWindow = {
      held: res.held, dropped: res.dropped, oldest: res.oldest_revision, revision: res.revision,
    };
    const fresh = res.events || [];
    if (fresh.length) {
      const known = new Set(this.events.map((e) => `${e.revision}:${e.kind}:${e.target}`));
      for (const e of fresh) {
        const key = `${e.revision}:${e.kind}:${e.target}`;
        if (known.has(key)) continue;
        e.__fresh = this.sinceRevision > 0;   // the first load is not "new"
        this.events.unshift(e);
      }
      this.sinceRevision = Math.max(this.sinceRevision, ...fresh.map((e) => e.revision));
      if (this.events.length > JOURNAL_BUFFER) this.events.length = JOURNAL_BUFFER;
    }
  }

  renderJournal() {
    const w = this.journalWindow;
    const note = w ? `
      <div class="window-note">
        <span><strong>${w.held}</strong> entries held</span>
        <span><strong>${w.dropped}</strong> dropped</span>
        <span>oldest revision <strong>${w.oldest}</strong></span>
        <span class="muted">The journal is bounded, so an absence here is not evidence that nothing happened.</span>
      </div>` : '';

    if (!this.events.length) {
      this.panes.journal.innerHTML = note + `<div class="empty">
        <strong>Nothing recorded yet</strong>
        <span class="why">Every mutation — a property admitted, gone stale, retired, a
        relationship asserted, a decision taken — advances the revision and lands here.</span>
      </div>`;
      return;
    }

    this.panes.journal.innerHTML = note + `<div class="journal">${this.events.map((e) => `
      <div class="row${e.__fresh ? ' fresh' : ''}">
        <span class="rev">r${e.revision}</span>
        <span class="kind ${kindClass(e.kind)}">${escapeHTML(shortKind(e.kind))}</span>
        <span class="target" title="${escapeHTML(e.target || '')}">${escapeHTML(e.target || '—')}</span>
        <span class="detail">${escapeHTML(detailLine(e))}</span>
        <span class="actor">${escapeHTML(e.actor || '')} · ${fmt.clock(e.at)}</span>
      </div>`).join('')}</div>`;
    // The highlight is a one-shot: a row stays new only until it has been seen.
    for (const e of this.events) e.__fresh = false;
  }

  // ── decisions ─────────────────────────────────────────────────────────────

  async pollDecisions() {
    const res = await api.decisions(40);
    const fresh = res.decisions || [];
    // The listing is capped, so an older trace the operator deliberately opened
    // is not in it. Dropping it here would make the row vanish from under them a
    // moment after they clicked "Open the trace".
    const held = this.openDecisionID
      && this.decisions.find((d) => d.id === this.openDecisionID);
    if (held && !fresh.some((d) => d.id === held.id)) fresh.unshift(held);
    this.decisions = fresh;
  }

  async openDecision(id) {
    // Awaited: activate() starts a poll that replaces this.decisions wholesale,
    // and if it lands after the fetch below it discards the record just injected.
    await this.activate('decisions');
    try {
      const d = await api.decision(id);
      this.openDecisionID = id;
      if (!this.decisions.some((x) => x.id === id)) this.decisions.unshift(d);
      else this.decisions = this.decisions.map((x) => (x.id === id ? d : x));
      this.renderDecisions();
    } catch (err) {
      this.ctx.toast(err.gone
        ? `${err.message} — the trace existed but has been evicted.`
        : err.message, 'err');
    }
  }

  renderDecisions() {
    if (!this.decisions.length) {
      this.panes.decisions.innerHTML = `<div class="empty">
        <strong>No decisions recorded</strong>
        <span class="why">Ask the map something — select a property and run an estimate —
        and the answer is recorded here with the exact state that produced it.</span>
      </div>`;
      return;
    }

    this.panes.decisions.innerHTML = `<div class="pane-pad">
      <table class="grid">
        <thead><tr><th>decision</th><th>question</th><th class="num">revision</th><th>when</th><th></th></tr></thead>
        <tbody>${this.decisions.map((d) => `
          <tr data-decision="${escapeHTML(d.id)}">
            <td class="mono">${escapeHTML(d.id)}</td>
            <td>${escapeHTML(d.question || '—')}</td>
            <td class="num">${fmt.int(d.revision)}</td>
            <td class="muted nowrap">${fmt.ago(d.at)}</td>
            <td class="muted">${this.openDecisionID === d.id ? '▴' : '▾'}</td>
          </tr>
          ${this.openDecisionID === d.id ? `<tr><td colspan="5">${decisionTrace(d)}</td></tr>` : ''}
        `).join('')}</tbody>
      </table>
    </div>`;

    this.panes.decisions.querySelectorAll('[data-decision]').forEach((row) => {
      row.addEventListener('click', () => {
        const id = row.dataset.decision;
        this.openDecisionID = this.openDecisionID === id ? null : id;
        this.renderDecisions();
      });
    });
  }

  // ── discovery ─────────────────────────────────────────────────────────────

  async pollCandidates() {
    const res = await api.candidates();
    this.candidates = Array.isArray(res) ? res : (res?.candidates || []);
  }

  renderDiscovery() {
    if (!this.candidates.length) {
      this.panes.discovery.innerHTML = `<div class="empty">
        <strong>Nothing proposed right now</strong>
        <span class="why">The proposer pairs a subject's property with the node-level
        properties it co-varies with, and only emits a candidate once the correlation
        clears the configured threshold over the configured minimum number of pairs
        (<span class="mono">-proposer-threshold</span>, <span class="mono">-proposer-min-pairs</span>).
        An empty queue means no pair has cleared both yet — not that the proposer is off.</span>
      </div>`;
      return;
    }

    this.panes.discovery.innerHTML = `<div class="pane-pad">
      <p class="muted" style="font-size:11.5px;margin:0 0 9px;line-height:1.5">
        The map produced structure it was not told about. Confirming one admits it as a
        relationship with <span class="mono">discovered</span> provenance; rejecting tells
        the proposer to forget the pair.
      </p>
      <table class="grid">
        <thead><tr><th>pair</th><th>sign</th><th class="num">|r|</th><th class="num">p</th>
          <th class="num">pairs</th><th>status</th><th></th></tr></thead>
        <tbody>${this.candidates.map((c) => {
          const sign = directionSign(c.Direction);
          return `<tr>
            <td class="mono">${escapeHTML(c.FromID)} → ${escapeHTML(c.ToID)}</td>
            <td style="color:${sign < 0 ? 'var(--sign-neg)' : 'var(--sign-pos)'}">${sign < 0 ? '↓ negative' : '↑ positive'}</td>
            <td class="num">${fmt.num(c.MIScore, 3)}</td>
            <td class="num">${fmt.num(c.PValue, 4)}</td>
            <td class="num">${fmt.int(c.NObservations)}</td>
            <td class="muted">${escapeHTML(String(c.Status ?? ''))}</td>
            <td class="nowrap">
              <button class="btn small primary" data-cand="${escapeHTML(c.CandidateID)}" data-verdict="confirm">Confirm</button>
              <button class="btn small" data-cand="${escapeHTML(c.CandidateID)}" data-verdict="defer">Defer</button>
              <button class="btn small danger" data-cand="${escapeHTML(c.CandidateID)}" data-verdict="reject">Reject</button>
            </td>
          </tr>`;
        }).join('')}</tbody>
      </table>
    </div>`;

    this.panes.discovery.querySelectorAll('[data-cand]').forEach((b) => {
      b.addEventListener('click', async (e) => {
        e.stopPropagation();
        const { cand, verdict } = b.dataset;
        b.disabled = true;
        try {
          await api.reviewCandidate(cand, verdict);
          this.ctx.toast(`Candidate ${verdict}ed`, 'ok');
          await this.pollCandidates();
          this.renderDiscovery();
          this.renderCounts();
          this.ctx.refresh();
        } catch (err) {
          this.ctx.toast(err.message, 'err');
          b.disabled = false;
        }
      });
    });
  }

  // ── subjects ──────────────────────────────────────────────────────────────

  renderSubjects() {
    const model = this.ctx.model();
    const subjects = model.subjects();
    if (!subjects.length) {
      this.panes.subjects.innerHTML = `<div class="empty">
        <strong>No subject-scoped properties</strong>
        <span class="why">A sample carries a subject such as
        <span class="mono">pod:redis</span>, and a scoped sample becomes the property
        <span class="mono">metric@subject</span>. With none present, the map currently
        holds node-level properties only.</span>
      </div>`;
      return;
    }

    this.panes.subjects.innerHTML = `<div class="pane-pad">
      <table class="grid">
        <thead><tr><th>subject</th><th>kind</th><th class="num">properties</th>
          <th>status</th><th>properties held</th></tr></thead>
        <tbody>${subjects.map((s) => `
          <tr data-subject="${escapeHTML(s.id)}">
            <td class="mono">${escapeHTML(s.identity)}</td>
            <td class="muted">${escapeHTML(s.kind)}</td>
            <td class="num">${s.properties.length}</td>
            <td><span class="badge status-${s.status}">${s.status}</span></td>
            <td>${s.properties.map((p) => `
              <span class="badge mono" data-goto="${escapeHTML(p.id)}" style="cursor:pointer">
                ${escapeHTML(p.id.split('@')[0])} ${p.n_observations ? fmt.num(p.value, 2) : '—'}
              </span>`).join(' ')}</td>
          </tr>`).join('')}</tbody>
      </table>
      <p class="muted" style="font-size:11.5px;margin:9px 0 0;line-height:1.5">
        Each subject's properties are its own. They are never routed into a construct —
        the map holds what each workload is doing, not only what the node aggregates to.
      </p>
    </div>`;

    this.panes.subjects.querySelectorAll('[data-goto]').forEach((b) => {
      b.addEventListener('click', (e) => { e.stopPropagation(); this.ctx.selectProperty(b.dataset.goto); });
    });
    this.panes.subjects.querySelectorAll('[data-subject]').forEach((row) => {
      row.addEventListener('click', () => this.ctx.filterSubject(row.dataset.subject));
    });
  }

  // ── cluster ───────────────────────────────────────────────────────────────

  async pollPeers() {
    this.peers = await api.statePeers();
  }

  renderCluster() {
    const p = this.peers;
    if (!p) { this.panes.cluster.innerHTML = `<div class="empty">Loading…</div>`; return; }

    // `silent` maps a URL to the reason that peer has never answered. It is an
    // object, not an array, and a peer in it is deliberately absent from `peers`
    // — the store has no owner id to key it under. Both of those bit the first
    // version: the length check never fired, and a configured-but-unreachable
    // peer was reported as "none configured", which is the opposite diagnosis.
    const silent = Object.entries(p.silent || {});

    if (!p.peers?.length && !silent.length) {
      this.panes.cluster.innerHTML = `<div class="empty">
        <strong>No peers registered</strong>
        <span class="why">This agent is node-local by design: one agent per machine,
        its map holding that machine's state. Cluster-level questions go to peers, and
        none are configured — start the daemon with
        <span class="mono">-peers http://node_1:8080,…</span> or POST to
        <span class="mono">/peers</span>.</span>
      </div>`;
      return;
    }

    if (!p.peers?.length) {
      this.panes.cluster.innerHTML = `<div class="pane-pad">
        <p style="font-size:12.5px;color:var(--status-serious);margin:0 0 9px;line-height:1.55">
          <strong>${silent.length} peer${silent.length === 1 ? ' is' : 's are'} configured but
          ${silent.length === 1 ? 'has' : 'have'} never answered</strong>, so this agent holds no
          state for ${silent.length === 1 ? 'it' : 'them'} and cannot name
          ${silent.length === 1 ? 'it' : 'them'}. That is a different situation from a known peer
          that is currently unreachable.
        </p>
        <table class="grid">
          <thead><tr><th>url</th><th>why it has never answered</th></tr></thead>
          <tbody>${silent.map(([url, why]) => `<tr>
            <td class="mono">${escapeHTML(url)}</td>
            <td><span class="badge alarm">! ${escapeHTML(why)}</span></td>
          </tr>`).join('')}</tbody>
        </table>
        <div class="row-between" style="margin-top:10px">
          <span class="muted" style="font-size:11.5px">Local node
            <span class="mono">${escapeHTML(p.self || '')}</span>.</span>
          <button class="btn small" data-act="refresh-peers">Poll peers now</button>
        </div>
      </div>`;
      this.wireRefreshPeers();
      return;
    }

    this.panes.cluster.innerHTML = `<div class="pane-pad">
      <div class="row-between" style="margin-bottom:9px">
        <span class="muted" style="font-size:11.5px">
          Local node <span class="mono">${escapeHTML(p.self || '')}</span>. Peer state is
          reported as received and never merged — a property with the same id on two nodes
          describes two systems.
        </span>
        <button class="btn small" data-act="refresh-peers">Poll peers now</button>
      </div>
      <table class="grid">
        <thead><tr><th>peer</th><th>url</th><th class="num">revision</th><th class="num">age</th>
          <th class="num">properties</th><th class="num">subjects</th><th>last attempt</th></tr></thead>
        <tbody>${p.peers.map((x) => `
          <tr>
            <td class="mono">${escapeHTML(x.peer_id || '—')}</td>
            <td class="mono muted">${escapeHTML(x.url)}</td>
            <td class="num">${fmt.int(x.revision)}</td>
            <td class="num">${fmt.duration(x.age_seconds)}</td>
            <td class="num">${fmt.int(x.counts?.properties_total)}</td>
            <td class="num">${fmt.int(x.counts?.subjects)}</td>
            <td>${x.error
              ? `<span class="badge alarm">! ${escapeHTML(x.error)}</span>`
              : `<span class="badge status-active">● answered</span>`}</td>
          </tr>`).join('')}</tbody>
      </table>
      ${p.stale?.length ? `<p style="font-size:11.5px;color:var(--status-serious);margin:9px 0 0">
        Stale snapshots: ${p.stale.map(escapeHTML).join(', ')} — a snapshot is only as good as its age.</p>` : ''}
      ${silent.length ? `<p style="font-size:11.5px;color:var(--status-serious);margin:5px 0 0;line-height:1.5">
        Never answered, so no state is held for ${silent.length === 1 ? 'it' : 'them'}:
        ${silent.map(([url, why]) => `${escapeHTML(url)} (${escapeHTML(why)})`).join('; ')}</p>` : ''}
    </div>`;

    this.wireRefreshPeers();
  }

  wireRefreshPeers() {
    this.panes.cluster.querySelector('[data-act="refresh-peers"]')?.addEventListener('click', async (e) => {
      e.currentTarget.disabled = true;
      try {
        const res = await api.refreshPeers();
        this.ctx.toast(`${res.answered} answered, ${res.failed} failed`, res.failed ? 'err' : 'ok');
        await this.pollPeers();
        this.renderCluster();
      } catch (err) { this.ctx.toast(err.message, 'err'); }
    });
  }

  // ── backbone ──────────────────────────────────────────────────────────────

  async pollBackbone() {
    const [graph, propositions] = await Promise.all([api.graph(), api.propositions()]);
    this.backbone = { graph, propositions: propositions?.propositions || propositions || [] };
  }

  renderBackbone() {
    const b = this.backbone;
    if (!b) { this.panes.backbone.innerHTML = `<div class="empty">Loading…</div>`; return; }
    const props = Array.isArray(b.propositions) ? b.propositions : [];

    this.panes.backbone.innerHTML = `<div class="pane-pad">
      <p class="muted" style="font-size:11.5px;margin:0 0 9px;line-height:1.5">
        The declared backbone the map was seeded from. Its constructs appear on the canvas
        as <strong>derived</strong> properties — this pane is where the declaration itself,
        and its deprecations, stay readable.
      </p>
      <table class="grid">
        <thead><tr><th>proposition</th><th>from</th><th>to</th><th>direction</th>
          <th class="num">prior</th><th>evidence</th><th></th></tr></thead>
        <tbody>${props.map((p) => `
          <tr${p.deprecated ? ' style="opacity:.5"' : ''}>
            <td class="mono">${escapeHTML(p.proposition_id)}</td>
            <td class="mono">${escapeHTML(p.from)}</td>
            <td class="mono">${escapeHTML(p.to)}</td>
            <td>${escapeHTML(p.direction)}</td>
            <td class="num">${fmt.num(p.prior_strength, 3)}</td>
            <td class="muted">${escapeHTML((p.evidence_sources || []).join(', ') || '—')}</td>
            <td>${p.deprecated
              ? `<span class="badge" title="${escapeHTML(p.deprecated_reason || '')}">deprecated</span>`
              : `<button class="btn small danger" data-deprecate="${escapeHTML(p.proposition_id)}">Deprecate…</button>`}</td>
          </tr>`).join('')}</tbody>
      </table>
      <div class="row-between" style="margin-top:12px">
        <span class="muted" style="font-size:11.5px">
          ${(b.graph?.constructs || []).length} constructs · ${props.length} propositions
        </span>
        <button class="btn small" data-act="tune">Apply an operator intent…</button>
      </div>
    </div>`;

    this.panes.backbone.querySelectorAll('[data-deprecate]').forEach((btn) => {
      btn.addEventListener('click', async () => {
        const got = await this.ctx.ask({
          title: `Deprecate ${btn.dataset.deprecate}`,
          fields: [{ name: 'reason', label: 'Reason (required)' }],
          ok: 'Deprecate',
        });
        if (!got?.reason?.trim()) return;
        try {
          await api.deprecate(btn.dataset.deprecate, got.reason.trim());
          this.ctx.toast('Proposition deprecated', 'ok');
          await this.pollBackbone();
          this.renderBackbone();
        } catch (err) { this.ctx.toast(err.message, 'err'); }
      });
    });

    this.panes.backbone.querySelector('[data-act="tune"]')?.addEventListener('click', async () => {
      const got = await this.ctx.ask({
        title: 'Operator intent',
        fields: [
          { name: 'intent', label: 'Intent', placeholder: 'e.g. prefer_low_latency' },
          { name: 'operator', label: 'Operator', value: 'operator' },
        ],
        hint: 'Tuning records the intent and the adjustments it implied, so the change stays attributable.',
        ok: 'Apply',
      });
      if (!got?.intent?.trim()) return;
      try {
        const res = await api.tune(got.intent.trim(), got.operator?.trim() || 'operator');
        this.ctx.toast(`${(res.applied || []).length} adjustment(s) applied`, 'ok');
        await this.pollBackbone();
        this.renderBackbone();
      } catch (err) { this.ctx.toast(err.message, 'err'); }
    });
  }
}

// ── helpers ──────────────────────────────────────────────────────────────────

const set = (id, v) => { const el = document.getElementById(id); if (el) el.textContent = v; };

function shortKind(kind) {
  const k = String(kind || '');
  const dot = k.indexOf('.');
  return dot === -1 ? k : k.slice(dot + 1);
}

function kindClass(kind) {
  const k = String(kind || '');
  if (k.includes('admitted') || k.includes('declared')) return 'admitted';
  if (k.includes('stale')) return 'stale';
  if (k.includes('retired')) return 'retired';
  if (k.includes('asserted')) return 'asserted';
  if (k.includes('conflict') || k.includes('failed')) return 'conflict';
  if (k === 'decision') return 'decision';
  return '';
}

/**
 * Render one journal-detail or decision-answer value as text.
 *
 * Both of those arrive as free-form `map[string]any`, so a value can be a nested
 * object — a decision's `hypothetical` is one — and the default stringification
 * of those is "[object Object]", which tells a reader nothing at all.
 */
function plain(v) {
  if (v === null || v === undefined) return '—';
  if (typeof v === 'number') return String(Number(v.toFixed(4)));
  if (Array.isArray(v)) return v.map(plain).join(', ');
  if (typeof v === 'object') {
    return Object.entries(v).map(([k, x]) => `${k} ${plain(x)}`).join(' · ');
  }
  return String(v);
}

function detailLine(e) {
  if (e.kind === 'decision' && e.decision) return e.decision.question || '';
  const d = e.detail;
  if (!d) return '';
  return Object.entries(d).map(([k, v]) => `${k}=${plain(v)}`).join('  ');
}

/** The full trace: what was read, what was supposed, and the agent's own words. */
function decisionTrace(d) {
  const props = d.properties_read || [];
  const rels = d.relationships_read || [];
  return `<div style="padding:10px 2px 14px;display:flex;flex-direction:column;gap:12px">
    ${d.answer ? `<div>
      <h3 style="margin:0 0 6px;font-size:10.5px;text-transform:uppercase;letter-spacing:.07em;color:var(--ink-muted)">Answer</h3>
      <dl class="kv" style="max-width:460px">${Object.entries(d.answer).map(([k, v]) =>
        `<dt>${escapeHTML(k)}</dt><dd>${escapeHTML(plain(v))}</dd>`).join('')}</dl>
    </div>` : ''}

    ${(d.assumptions && Object.keys(d.assumptions).length) || (d.excluded || []).length ? `<div>
      <h3 style="margin:0 0 6px;font-size:10.5px;text-transform:uppercase;letter-spacing:.07em;color:var(--ink-muted)">Supposed</h3>
      <div class="chips">
        ${Object.entries(d.assumptions || {}).map(([k, v]) =>
          `<span class="badge mono">${escapeHTML(k)} = ${escapeHTML(String(v))}</span>`).join('')}
        ${(d.excluded || []).map((x) => `<span class="badge mono">without ${escapeHTML(x)}</span>`).join('')}
      </div>
    </div>` : ''}

    <div>
      <h3 style="margin:0 0 6px;font-size:10.5px;text-transform:uppercase;letter-spacing:.07em;color:var(--ink-muted)">
        Read ${props.length} propert${props.length === 1 ? 'y' : 'ies'}, ${rels.length} relationship${rels.length === 1 ? '' : 's'}
        <span style="text-transform:none;letter-spacing:0;font-weight:400"> — copied at the time, so this record cannot become a description of a later system</span>
      </h3>
      <div class="chips">
        ${props.map((p) => `<span class="badge mono" title="confidence ${fmt.num(p.confidence, 3)}">
          ${escapeHTML(p.id)} = ${fmt.num(p.value, 3)}</span>`).join('')}
      </div>
      ${rels.length ? `<div class="chips" style="margin-top:6px">
        ${rels.map((r) => {
          const eff = effective(r);
          return `<span class="badge mono">
            <span class="swatch" style="background:var(--prov-${escapeHTML(r.provenance)})"></span>
            ${escapeHTML(r.from)} → ${escapeHTML(r.to)} ${eff.known ? fmt.num(eff.value, 3) : 'unknown'}</span>`;
        }).join('')}</div>` : ''}
    </div>

    ${d.rationale ? `<pre class="rationale">${escapeHTML(d.rationale)}</pre>` : ''}

    ${(d.caveats || []).length ? `<ul class="caveats">
      ${d.caveats.map((c) => `<li>${escapeHTML(c)}</li>`).join('')}</ul>` : ''}
  </div>`;
}
