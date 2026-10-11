// The canvas: properties as discs, relationships as arcs, subjects as enclosures.
//
// What each channel means, and why it was given that channel:
//
//   edge colour      the SIGN of the relationship. Diverging blue/red, because
//                    sign is the one genuinely bipolar quantity here.
//   edge width       effective strength — the number the agent reasons with.
//   edge opacity     confidence. Deliberately not a hue: confidence would
//                    otherwise compete with sign for the same channel.
//   edge dash        nothing observed yet. A seeded relationship with no pairs
//                    has no strength to show, and drawing it solid at zero would
//                    read as a measured zero.
//   midpoint dot     provenance (seeded / learned / discovered / asserted).
//   node shape       observed (filled) vs derived (ring). Shape, not hue, so the
//                    sign palette keeps its meaning everywhere on the canvas.
//   node ring        lifecycle: active, stale, retired.
//   node arc         the current value within its declared range, when one was
//                    declared. No range, no arc — an undeclared range cannot be
//                    normalised honestly.
//
// Properties scoped to a subject are enclosed and labelled with that subject, so
// the picture answers "which workloads are running" before anything is clicked.

import { effective, subjectOf, shortLabel, fmt, escapeHTML } from './model.js';
import { Force } from './force.js';

const NS = 'http://www.w3.org/2000/svg';
const el = (name, attrs = {}) => {
  const n = document.createElementNS(NS, name);
  for (const [k, v] of Object.entries(attrs)) {
    if (v !== undefined && v !== null) n.setAttribute(k, v);
  }
  return n;
};

const R_OBSERVED = 17;
const R_DERIVED = 21;

export class GraphView {
  constructor(svg, { onSelect, tooltip }) {
    this.svg = svg;
    this.onSelect = onSelect;
    this.tooltip = tooltip;
    this.force = new Force();
    this.selected = null;
    this.filter = { text: '' };
    this.transform = { k: 1, x: 0, y: 0 };
    this.raf = null;
    this.pulses = new Map();   // id -> ms when it last moved, for the ping animation
    this.lastModel = null;
    this.fittedOnce = false;

    this.defs = el('defs');
    this.gRoot = el('g');
    this.gHulls = el('g', { class: 'hulls' });
    this.gMembers = el('g', { class: 'members' });
    this.gEdges = el('g', { class: 'edges' });
    this.gNodes = el('g', { class: 'nodes' });
    this.gRoot.append(this.gHulls, this.gMembers, this.gEdges, this.gNodes);
    svg.append(this.defs, this.gRoot);
    this.buildDefs();
    this.wire();
  }

  buildDefs() {
    // One marker per sign, because a marker cannot inherit the path's stroke.
    for (const [name, color] of [
      ['pos', 'var(--sign-pos)'], ['neg', 'var(--sign-neg)'], ['none', 'var(--baseline)'],
    ]) {
      // userSpaceOnUse, so the head keeps a fixed size. The default scales the
      // marker by the path's stroke width, which made a strong relationship —
      // precisely the one worth reading — sprout an arrowhead larger than the
      // properties it joins.
      const m = el('marker', {
        id: `arrow-${name}`, viewBox: '0 0 10 10', refX: 8.5, refY: 5,
        markerWidth: 11, markerHeight: 11, markerUnits: 'userSpaceOnUse',
        orient: 'auto-start-reverse',
      });
      m.append(el('path', { d: 'M 0 1 L 9 5 L 0 9 z', fill: color }));
      this.defs.append(m);
    }
  }

  wire() {
    const svg = this.svg;
    let panning = null;
    let dragging = null;
    let press = null;

    // Selection is decided on pointerUP, from what was under the pointer when it
    // went DOWN — not from a `click` listener.
    //
    // Dragging a node and panning the canvas both need the pointer captured, and
    // a captured pointer retargets the subsequent `click` to the capture element.
    // So `click` always reported the <svg> as its target, `closest('[data-node]')`
    // always came back null, and clicking a node silently cleared the selection.
    const DRAG_SLOP = 4;

    const hitAt = (target) => {
      const nodeEl = target.closest?.('[data-node]');
      if (nodeEl) return { type: 'property', id: nodeEl.dataset.node };
      const edgeEl = target.closest?.('[data-edge]');
      if (edgeEl) return { type: 'relationship', id: edgeEl.dataset.edge };
      return null;
    };

    svg.addEventListener('pointerdown', (e) => {
      const hit = hitAt(e.target);
      press = { x: e.clientX, y: e.clientY, hit, moved: false };

      if (hit?.type === 'property') {
        const n = this.force.at(hit.id);
        if (n) {
          dragging = { n, wasPinned: n.pinned };
          n.pinned = true;
        }
      } else {
        panning = { x: e.clientX, y: e.clientY, tx: this.transform.x, ty: this.transform.y };
        svg.classList.add('dragging');
      }
      svg.setPointerCapture(e.pointerId);
      // A tooltip that outlives the pointer is worse than none: it keeps showing
      // a value the panel beside it has already moved past.
      this.tooltip.hide();
    });

    svg.addEventListener('pointermove', (e) => {
      if (press && !press.moved
          && Math.hypot(e.clientX - press.x, e.clientY - press.y) > DRAG_SLOP) {
        press.moved = true;
      }

      if (dragging) {
        if (!press?.moved) return;         // a tremor is not a drag
        const pt = this.toWorld(e.clientX, e.clientY);
        dragging.n.x = pt.x; dragging.n.y = pt.y;
        this.force.reheat(0.25);
        this.start();
        return;
      }
      if (panning) {
        this.transform.x = panning.tx + (e.clientX - panning.x);
        this.transform.y = panning.ty + (e.clientY - panning.y);
        this.applyTransform();
        return;
      }
      this.hover(e);
    });

    const end = (e) => {
      if (dragging) {
        // A node dragged deliberately stays put; one that was merely clicked keeps
        // whatever pinned state it already had.
        if (!press?.moved) dragging.n.pinned = dragging.wasPinned;
        dragging = null;
      }
      if (press && !press.moved) this.select(press.hit);
      press = null;
      panning = null;
      svg.classList.remove('dragging');
      try { svg.releasePointerCapture(e.pointerId); } catch (_) { /* already gone */ }
    };
    svg.addEventListener('pointerup', end);
    svg.addEventListener('pointercancel', end);
    svg.addEventListener('pointerleave', () => this.tooltip.hide());

    svg.addEventListener('dblclick', (e) => {
      const hit = hitAt(e.target);
      if (hit?.type !== 'property') return;
      const n = this.force.at(hit.id);
      if (n) { n.pinned = !n.pinned; this.render(this.lastModel); }
    });

    svg.addEventListener('wheel', (e) => {
      e.preventDefault();
      const factor = e.deltaY < 0 ? 1.12 : 1 / 1.12;
      const k = Math.min(3.5, Math.max(0.25, this.transform.k * factor));
      const rect = svg.getBoundingClientRect();
      const mx = e.clientX - rect.left, my = e.clientY - rect.top;
      // Zoom about the pointer, so the thing under the cursor stays under it.
      this.transform.x = mx - ((mx - this.transform.x) * k) / this.transform.k;
      this.transform.y = my - ((my - this.transform.y) * k) / this.transform.k;
      this.transform.k = k;
      this.applyTransform();
    }, { passive: false });

    // Guarded against a resize that is not one. The census bar re-rendering can
    // shift a stat tile by a sub-pixel, which cascades into a canvas resize; an
    // unguarded reheat on every poll keeps alpha above the settle threshold
    // forever, and anything waiting for the layout to settle waits forever.
    let lastW = 0, lastH = 0;
    const ro = new ResizeObserver(() => {
      const r = svg.getBoundingClientRect();
      if (Math.abs(r.width - lastW) < 1 && Math.abs(r.height - lastH) < 1) return;
      const first = lastW === 0;
      lastW = r.width; lastH = r.height;
      this.force.size(r.width, r.height);
      // The first observation arrives before any data; there is nothing to
      // re-frame yet, and the next render will lay the graph out and fit it.
      if (first) return;
      this.force.reheat(0.25);
      this.force.prewarm(60);
      this.position();
      this.fit();
      this.start();
    });
    ro.observe(svg.parentElement);
  }

  toWorld(clientX, clientY) {
    const rect = this.svg.getBoundingClientRect();
    return {
      x: (clientX - rect.left - this.transform.x) / this.transform.k,
      y: (clientY - rect.top - this.transform.y) / this.transform.k,
    };
  }

  applyTransform() {
    const { k, x, y } = this.transform;
    this.gRoot.setAttribute('transform', `translate(${x},${y}) scale(${k})`);
  }

  select(sel) {
    this.tooltip.hide();
    this.selected = sel;
    this.onSelect(sel);
    if (this.lastModel) this.render(this.lastModel);
  }

  setFilter(patch) {
    Object.assign(this.filter, patch);
    if (this.lastModel) this.render(this.lastModel);
  }

  // -- which properties are on screen ---------------------------------------

  /**
   * Text search only. Lifecycle status is chosen in the REQUEST — see
   * STATUS_QUERY in app.js — because the agent decides which statuses to send
   * and filtering the response could only ever hide what arrived, never fetch
   * what did not.
   */
  visible(model) {
    const needle = this.filter.text.trim().toLowerCase();
    if (!needle) return model.properties;
    return model.properties.filter((p) => p.id.toLowerCase().includes(needle)
      || (p.subject || '').toLowerCase().includes(needle)
      || (p.unit || '').toLowerCase().includes(needle));
  }

  // -- render ---------------------------------------------------------------

  render(model, { refit = false } = {}) {
    this.lastModel = model;
    const props = this.visible(model);
    const ids = new Set(props.map((p) => p.id));

    for (const id of model.changed) this.pulses.set(id, Date.now());

    const nodes = props.map((p) => ({
      id: p.id,
      r: p.kind === 'derived' ? R_DERIVED : R_OBSERVED,
      group: subjectOf(p),
    }));

    const rels = model.relationships.filter((r) => ids.has(r.from) && ids.has(r.to));
    const members = model.memberships().filter((m) => ids.has(m.from) && ids.has(m.to));

    const links = [
      ...rels.map((r) => ({ source: r.from, target: r.to, distance: 210 })),
      ...members.map((m) => ({ source: m.from, target: m.to, distance: 155 })),
    ];

    const topologyChanged = this.force.sync(nodes, links);
    if (topologyChanged) {
      // Settle synchronously. The first layout is run to completion so the first
      // painted frame is the real structure; later admissions get a short warm so
      // the newcomer finds its place without rearranging everything around it.
      this.force.prewarm(this.fittedOnce ? 40 : 300);
    }
    this.draw(model, props, rels, members);
    this.position();
    if ((refit || (topologyChanged && !this.fittedOnce)) && props.length && this.fit()) {
      this.fittedOnce = true;
    }
    this.start();
  }

  start() {
    if (this.raf) return;
    const step = () => {
      const moving = this.force.tick();
      this.position();
      const pulsing = [...this.pulses.values()].some((t) => Date.now() - t < 1400);
      if (moving || pulsing) {
        this.raf = requestAnimationFrame(step);
      } else {
        this.raf = null;
      }
    };
    this.raf = requestAnimationFrame(step);
  }

  /** Rebuild the DOM for the current data. Positions are applied separately. */
  draw(model, props, rels, members) {
    this.gHulls.replaceChildren();
    this.gMembers.replaceChildren();
    this.gEdges.replaceChildren();
    this.gNodes.replaceChildren();
    this.nodeEls = new Map();
    this.edgeEls = [];
    this.hullEls = [];

    // Subject enclosures, drawn under everything.
    const bySubject = new Map();
    for (const p of props) {
      const s = subjectOf(p);
      if (!s) continue;
      if (!bySubject.has(s)) bySubject.set(s, []);
      bySubject.get(s).push(p.id);
    }
    for (const [subject, memberIDs] of bySubject) {
      const g = el('g', { class: 'hull' });
      const rect = el('rect', {
        rx: 16, fill: 'var(--surface-2)', 'fill-opacity': 0.55,
        stroke: 'var(--border-strong)', 'stroke-width': 1, 'stroke-dasharray': '4 3',
      });
      const label = el('text', {
        'font-size': 11, fill: 'var(--ink-muted)',
        'font-family': 'ui-monospace, monospace', 'letter-spacing': '.04em',
        stroke: 'var(--page)', 'stroke-width': 3, 'paint-order': 'stroke',
        'stroke-linejoin': 'round',
      });
      label.textContent = subject;
      g.append(rect, label);
      this.gHulls.append(g);
      this.hullEls.push({ subject, memberIDs, rect, label });
    }

    // Membership: a derived property is recomputed from these. Hairline and
    // arrowless, so it cannot be mistaken for an influence.
    for (const m of members) {
      const path = el('path', {
        fill: 'none', stroke: 'var(--grid)', 'stroke-width': 1.5,
        'stroke-dasharray': '1 4', 'stroke-linecap': 'round',
      });
      this.gMembers.append(path);
      this.edgeEls.push({ kind: 'member', from: m.from, to: m.to, path, curve: 0 });
    }

    // Relationships. Opposite-direction pairs are bowed apart so both are legible.
    const pairIndex = new Map();
    for (const r of rels) {
      const key = [r.from, r.to].sort().join('||');
      const n = pairIndex.get(key) || 0;
      pairIndex.set(key, n + 1);
      const curve = n === 0 ? 0.16 : -0.16 * n;

      const eff = effective(r);
      const sign = r.sign < 0 ? 'neg' : 'pos';
      const color = !eff.known ? 'var(--baseline)' : r.sign < 0 ? 'var(--sign-neg)' : 'var(--sign-pos)';
      const width = 1.5 + eff.value * 5.5;
      // A floor on opacity: an edge that exists must remain visible even at the
      // confidence it starts with, or the graph would appear to lose structure.
      const opacity = eff.known ? 0.35 + Math.min(0.65, r.confidence * 0.65) : 0.4;

      const g = el('g', { 'data-edge': r.id, class: 'edge', style: 'cursor:pointer' });
      // A transparent fat path under the visible one: a 2px line is not a hit target.
      const hit = el('path', { fill: 'none', stroke: 'transparent', 'stroke-width': 14 });
      const path = el('path', {
        fill: 'none', stroke: color, 'stroke-width': width,
        'stroke-opacity': opacity, 'stroke-linecap': 'round',
        'marker-end': `url(#arrow-${eff.known ? sign : 'none'})`,
        'stroke-dasharray': eff.known ? null : '5 5',
      });
      if (model.relChanged.has(r.id)) {
        path.setAttribute('class', 'flowing');
        path.setAttribute('stroke-dasharray', '7 7');
      }
      if (this.selected?.type === 'relationship' && this.selected.id === r.id) {
        path.setAttribute('stroke-width', width + 2.5);
        path.setAttribute('stroke-opacity', 1);
      }
      g.append(hit, path);

      // Provenance rides at the midpoint, with a ring so it reads on any backdrop.
      const dot = el('circle', {
        r: 4, fill: `var(--prov-${r.provenance})`,
        stroke: 'var(--surface-1)', 'stroke-width': 2,
      });
      g.append(dot);

      // A relationship the machine keeps contradicting is an alarm, not a weak
      // edge: it reports zero strength, which otherwise reads as a quiet system.
      let alarm = null;
      if (r.sign_suspect) {
        alarm = el('text', {
          'font-size': 13, 'text-anchor': 'middle', 'dominant-baseline': 'central',
          fill: 'var(--status-critical)', 'font-weight': '700',
        });
        alarm.textContent = '!';
        g.append(alarm);
      }

      this.gEdges.append(g);
      this.edgeEls.push({ kind: 'rel', rel: r, from: r.from, to: r.to, path, hit, dot, alarm, curve });
    }

    // Properties.
    for (const p of props) {
      const r = p.kind === 'derived' ? R_DERIVED : R_OBSERVED;
      const g = el('g', { 'data-node': p.id, class: 'node', style: 'cursor:pointer' });

      const statusStroke = p.status === 'stale' ? 'var(--status-serious)'
        : p.status === 'retired' ? 'var(--ink-muted)' : 'var(--baseline)';

      const halo = el('circle', {
        r: r + 6, fill: 'none', stroke: 'var(--sign-pos)', 'stroke-width': 2, opacity: 0,
      });
      g.append(halo);

      const disc = el('circle', {
        r, fill: 'var(--surface-1)', stroke: statusStroke,
        'stroke-width': p.status === 'retired' ? 1.2 : 1.6,
        'stroke-dasharray': p.status === 'retired' ? '3 3' : null,
        opacity: p.status === 'retired' ? 0.55 : 1,
      });
      g.append(disc);

      if (p.kind === 'derived') {
        // Ring plus core: a summary of other properties, never stored on its own.
        g.append(el('circle', { r: r - 6, fill: 'none', stroke: statusStroke, 'stroke-width': 1, opacity: 0.5 }));
        g.append(el('circle', { r: 3.5, fill: 'var(--ink-secondary)' }));
      } else {
        g.append(el('circle', { r: 4.5, fill: 'var(--ink-secondary)', opacity: p.n_observations ? 1 : 0.3 }));
      }

      // Value arc — only when a range was declared, and only once observed.
      const arc = el('path', {
        fill: 'none', stroke: 'var(--ink-primary)', 'stroke-width': 3,
        'stroke-linecap': 'round', opacity: 0.85,
      });
      g.append(arc);

      // Outlined in the surface colour and painted stroke-first, so a label that
      // lands over an edge or another label stays legible instead of blending.
      const label = el('text', {
        y: r + 14, 'text-anchor': 'middle', 'font-size': 11,
        fill: 'var(--ink-secondary)', 'font-family': 'ui-monospace, monospace',
        stroke: 'var(--page)', 'stroke-width': 3.5, 'paint-order': 'stroke',
        'stroke-linejoin': 'round',
      });
      label.textContent = shortLabel(p.id);
      g.append(label);

      const valueText = el('text', {
        y: r + 26, 'text-anchor': 'middle', 'font-size': 10.5,
        fill: 'var(--ink-muted)', 'font-variant-numeric': 'tabular-nums',
        stroke: 'var(--page)', 'stroke-width': 3, 'paint-order': 'stroke',
        'stroke-linejoin': 'round',
      });
      valueText.textContent = p.n_observations ? fmt.num(p.value, 3) : 'unobserved';
      g.append(valueText);

      if (this.force.at(p.id)?.pinned) {
        g.append(el('circle', { r: 2.5, cx: r - 3, cy: -(r - 3), fill: 'var(--ink-muted)' }));
      }

      this.gNodes.append(g);
      this.nodeEls.set(p.id, { g, disc, arc, halo, p, r });
    }

    this.applyTransform();
  }

  /** Move the existing DOM to wherever the simulation currently has it. */
  position() {
    if (!this.nodeEls) return;
    const now = Date.now();

    for (const [id, nd] of this.nodeEls) {
      const n = this.force.at(id);
      if (!n) continue;
      nd.g.setAttribute('transform', `translate(${n.x.toFixed(1)},${n.y.toFixed(1)})`);

      const p = nd.p;
      if (p.range_declared && p.n_observations > 0) {
        const [lo, hi] = p.range || [0, 1];
        const span = hi - lo;
        const frac = span > 0 ? Math.min(1, Math.max(0, (p.value - lo) / span)) : 0;
        nd.arc.setAttribute('d', arcPath(nd.r - 4, frac));
      } else {
        nd.arc.setAttribute('d', '');
      }

      const pulsed = this.pulses.get(id);
      if (pulsed !== undefined) {
        const age = now - pulsed;
        if (age < 1400) {
          const t = age / 1400;
          nd.halo.setAttribute('opacity', (1 - t) * 0.7);
          nd.halo.setAttribute('r', nd.r + 6 + t * 14);
        } else {
          nd.halo.setAttribute('opacity', 0);
          this.pulses.delete(id);
        }
      }

      const isSel = this.selected?.type === 'property' && this.selected.id === id;
      nd.disc.setAttribute('stroke-width', isSel ? 3 : (p.status === 'retired' ? 1.2 : 1.6));
      if (isSel) nd.disc.setAttribute('stroke', 'var(--sign-pos)');
    }

    for (const e of this.edgeEls) {
      const a = this.force.at(e.from);
      const b = this.force.at(e.to);
      if (!a || !b) continue;
      const geom = curvePath(a, b, e.curve, nodeRadius(this.nodeEls, e.from), nodeRadius(this.nodeEls, e.to));
      e.path.setAttribute('d', geom.d);
      if (e.hit) e.hit.setAttribute('d', geom.d);
      if (e.dot) { e.dot.setAttribute('cx', geom.mx); e.dot.setAttribute('cy', geom.my); }
      if (e.alarm) { e.alarm.setAttribute('x', geom.mx); e.alarm.setAttribute('y', geom.my - 12); }
    }

    for (const h of this.hullEls) {
      let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
      for (const id of h.memberIDs) {
        const n = this.force.at(id);
        if (!n) continue;
        x0 = Math.min(x0, n.x - n.r); y0 = Math.min(y0, n.y - n.r);
        x1 = Math.max(x1, n.x + n.r); y1 = Math.max(y1, n.y + n.r);
      }
      if (!Number.isFinite(x0)) continue;
      const pad = 26;
      h.rect.setAttribute('x', x0 - pad);
      h.rect.setAttribute('y', y0 - pad - 10);
      h.rect.setAttribute('width', x1 - x0 + pad * 2);
      h.rect.setAttribute('height', y1 - y0 + pad * 2 + 10);
      h.label.setAttribute('x', x0 - pad + 10);
      h.label.setAttribute('y', y0 - pad + 4);
    }
  }

  hover(e) {
    const nodeEl = e.target.closest('[data-node]');
    const edgeEl = e.target.closest('[data-edge]');
    if (nodeEl) {
      const p = this.lastModel?.property(nodeEl.dataset.node);
      if (p) return this.tooltip.show(e, propertyTip(p, this.lastModel));
    }
    if (edgeEl) {
      const r = this.lastModel?.relationship(edgeEl.dataset.edge);
      if (r) return this.tooltip.show(e, relationshipTip(r));
    }
    this.tooltip.hide();
  }

  /**
   * Frame the whole graph, inside the area the overlays leave free.
   *
   * The controls and the legend float over the canvas, so fitting to the raw
   * viewport puts nodes underneath them. The insets are measured from the
   * overlays themselves rather than hard-coded, so hiding the legend genuinely
   * gives the graph the space back.
   */
  /** Returns whether there was anything to frame. */
  fit() {
    const b = this.force.bounds();
    const rect = this.svg.getBoundingClientRect();
    if (!b || !rect.width || !rect.height) return false;

    // Only the floating controls are inset against; the legend is a sibling of
    // the canvas, so the measured rect already excludes it.
    const topInset = 56;      // the filter box and the canvas buttons
    const bottomInset = 20;
    const sideInset = 28;

    const availW = Math.max(120, rect.width - sideInset * 2);
    const availH = Math.max(120, rect.height - topInset - bottomInset);

    const k = Math.min(1.5, Math.max(0.2,
      Math.min(availW / Math.max(b.w, 1), availH / Math.max(b.h, 1))));
    this.transform.k = k;
    this.transform.x = sideInset + availW / 2 - ((b.x0 + b.x1) / 2) * k;
    this.transform.y = topInset + availH / 2 - ((b.y0 + b.y1) / 2) * k;
    this.applyTransform();
    return true;
  }

  relayout() {
    for (const n of this.force.nodes.values()) {
      n.pinned = false;
      n.x = this.force.width / 2 + (Math.random() - 0.5) * 240;
      n.y = this.force.height / 2 + (Math.random() - 0.5) * 240;
      n.vx = n.vy = 0;
    }
    this.force.reheat(1);
    this.force.prewarm(300);
    this.position();
    this.fit();
    this.start();
  }
}

const nodeRadius = (map, id) => map.get(id)?.r ?? R_OBSERVED;

/** A quadratic bow from a to b, trimmed to each disc's edge so arrows land clean. */
function curvePath(a, b, curve, ra, rb) {
  const dx = b.x - a.x, dy = b.y - a.y;
  const d = Math.hypot(dx, dy) || 1;
  const ux = dx / d, uy = dy / d;
  const sx = a.x + ux * (ra + 2), sy = a.y + uy * (ra + 2);
  const ex = b.x - ux * (rb + 11), ey = b.y - uy * (rb + 11);
  const mx = (sx + ex) / 2 - uy * d * curve;
  const my = (sy + ey) / 2 + ux * d * curve;
  return {
    d: `M ${sx.toFixed(1)} ${sy.toFixed(1)} Q ${mx.toFixed(1)} ${my.toFixed(1)} ${ex.toFixed(1)} ${ey.toFixed(1)}`,
    mx, my,
  };
}

/** A clockwise arc from 12 o'clock covering `frac` of the circle. */
function arcPath(r, frac) {
  if (frac <= 0) return '';
  const a = Math.min(frac, 0.9999) * Math.PI * 2 - Math.PI / 2;
  const x = r * Math.cos(a), y = r * Math.sin(a);
  return `M 0 ${-r} A ${r} ${r} 0 ${frac > 0.5 ? 1 : 0} 1 ${x.toFixed(2)} ${y.toFixed(2)}`;
}

function propertyTip(p, model) {
  const rows = [
    ['value', p.n_observations ? fmt.value(p) : 'unobserved'],
    ['confidence', fmt.num(p.confidence, 3)],
    ['observations', fmt.int(p.n_observations)],
    ['status', p.status],
  ];
  if (p.subject) rows.unshift(['subject', p.subject]);
  if (p.kind === 'derived' && p.members?.length) rows.push(['members', p.members.length]);
  const inbound = model.influencesOn(p.id).length;
  if (inbound) rows.push(['influenced by', inbound]);
  return { title: p.id, kind: p.kind, rows };
}

function relationshipTip(r) {
  const eff = effective(r);
  return {
    title: `${r.from} -> ${r.to}`,
    kind: `${r.provenance}${r.label ? ' · ' + r.label : ''}`,
    rows: [
      ['sign', r.sign < 0 ? 'negative' : 'positive'],
      ['effective', eff.known ? `${fmt.num(eff.value)} (${eff.basis})` : 'not yet known'],
      ['confidence', fmt.num(r.confidence, 3)],
      ['pairs', fmt.int(r.n_observations)],
      ...(r.sign_suspect ? [['sign status', 'contradicted by observation']] : []),
    ],
  };
}

export function tooltipHTML({ title, kind, rows }) {
  return `<div class="t-title">${escapeHTML(title)}</div>`
    + (kind ? `<div class="t-row muted" style="margin-bottom:4px">${escapeHTML(kind)}</div>` : '')
    + rows.map(([k, v]) => `<div class="t-row"><span>${escapeHTML(k)}</span><b>${escapeHTML(v)}</b></div>`).join('');
}
