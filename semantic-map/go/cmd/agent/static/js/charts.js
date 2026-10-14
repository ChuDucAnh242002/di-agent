// The small charts: a sparkline, a diverging contribution bar, a layered strength
// readout, a stacked census bar.
//
// All inline SVG, all built from the same tokens as the rest of the console, so a
// theme change is one variable swap and never a redraw. Two rules are enforced
// here rather than left to each call site:
//
//   * a value is never drawn without the thing that makes it readable — a bar
//     carries its own label, a line carries its axis extent;
//   * nothing is drawn for an absent measurement. An unobserved property has no
//     series, and the chart says so in words instead of drawing a flat line at
//     zero, which would look exactly like a measured zero.

import { fmt, escapeHTML, effective, recentOf } from './model.js';

const NS = 'http://www.w3.org/2000/svg';
const svgEl = (name, attrs = {}) => {
  const n = document.createElementNS(NS, name);
  for (const [k, v] of Object.entries(attrs)) if (v !== undefined && v !== null) n.setAttribute(k, v);
  return n;
};

/**
 * A sparkline of what this page has watched a property do.
 *
 * The series is the console's own record, not the agent's: the map holds a
 * current value per property and no history, so this is "what we saw while the
 * tab was open". The caption says that, because a chart that implies a longer
 * record than exists is the more expensive kind of wrong.
 */
export function sparkline(series, { width = 352, height = 54, unit = '' } = {}) {
  const wrap = document.createElement('div');

  if (series.length < 2) {
    wrap.innerHTML = `<p class="muted" style="font-size:11.5px;margin:2px 0 0">
      Not enough observed movement yet to plot — the console records a point each
      time the agent reports a change.</p>`;
    return wrap;
  }

  const vals = series.map((s) => s.v);
  let lo = Math.min(...vals);
  let hi = Math.max(...vals);
  if (hi - lo < 1e-9) { hi = lo + 1e-9; }          // a flat series still needs a band
  const pad = (hi - lo) * 0.14;
  lo -= pad; hi += pad;

  const t0 = series[0].t, t1 = series[series.length - 1].t;
  const span = Math.max(1, t1 - t0);
  const x = (t) => 2 + ((t - t0) / span) * (width - 4);
  const y = (v) => height - 6 - ((v - lo) / (hi - lo)) * (height - 12);

  const svg = svgEl('svg', {
    viewBox: `0 0 ${width} ${height}`, width: '100%', height,
    preserveAspectRatio: 'none', role: 'img',
    'aria-label': `recent values, ${fmt.num(Math.min(...vals))} to ${fmt.num(Math.max(...vals))}`,
  });
  svg.style.display = 'block';
  svg.style.overflow = 'visible';

  const d = series.map((s, i) => `${i ? 'L' : 'M'} ${x(s.t).toFixed(1)} ${y(s.v).toFixed(1)}`).join(' ');
  const area = `${d} L ${x(t1).toFixed(1)} ${height - 2} L ${x(t0).toFixed(1)} ${height - 2} Z`;

  svg.append(svgEl('path', { d: area, fill: 'var(--sign-pos)', opacity: 0.09 }));
  svg.append(svgEl('path', {
    d, fill: 'none', stroke: 'var(--sign-pos)', 'stroke-width': 2,
    'stroke-linejoin': 'round', 'stroke-linecap': 'round',
    'vector-effect': 'non-scaling-stroke',
  }));

  const last = series[series.length - 1];
  svg.append(svgEl('circle', {
    cx: x(last.t), cy: y(last.v), r: 3.2, fill: 'var(--sign-pos)',
    stroke: 'var(--surface-1)', 'stroke-width': 2,
  }));

  // Crosshair: an SVG chart is interactive by default, so it ships with a readout.
  const cross = svgEl('line', {
    y1: 0, y2: height, stroke: 'var(--baseline)', 'stroke-width': 1, opacity: 0,
  });
  const dot = svgEl('circle', { r: 3, fill: 'var(--ink-primary)', opacity: 0 });
  svg.append(cross, dot);

  const readout = document.createElement('div');
  readout.className = 'row-between';
  readout.style.cssText = 'font-size:11px;color:var(--ink-muted);margin-top:2px';
  const extent = `<span>${fmt.num(Math.min(...vals))} – ${fmt.num(Math.max(...vals))} ${escapeHTML(unit)}</span>`;
  const windowLabel = `<span>${fmt.duration((t1 - t0) / 1000)} of console history · ${series.length} pts</span>`;
  readout.innerHTML = extent + windowLabel;

  svg.addEventListener('pointermove', (e) => {
    const r = svg.getBoundingClientRect();
    const px = ((e.clientX - r.left) / r.width) * width;
    let best = series[0], bestD = Infinity;
    for (const s of series) {
      const dd = Math.abs(x(s.t) - px);
      if (dd < bestD) { bestD = dd; best = s; }
    }
    cross.setAttribute('x1', x(best.t)); cross.setAttribute('x2', x(best.t));
    cross.setAttribute('opacity', 1);
    dot.setAttribute('cx', x(best.t)); dot.setAttribute('cy', y(best.v));
    dot.setAttribute('opacity', 1);
    readout.innerHTML = `<span><b style="color:var(--ink-primary)">${fmt.num(best.v)}</b> ${escapeHTML(unit)}</span>`
      + `<span>${fmt.ago(new Date(best.t).toISOString())}</span>`;
  });
  svg.addEventListener('pointerleave', () => {
    cross.setAttribute('opacity', 0);
    dot.setAttribute('opacity', 0);
    readout.innerHTML = extent + windowLabel;
  });

  wrap.append(svg, readout);
  return wrap;
}

/**
 * The three strength layers, with the one that answers marked.
 *
 * This is the shape of the agent's own precedence rule — an operator's assertion
 * outranks the long-run estimate, which outranks the most recent pairs — and it is
 * the thing a reader most often gets wrong when handed three numbers in a row.
 */
export function strengthLayers(rel) {
  const eff = effective(rel);
  const rows = [
    ['assertion', rel.assertion, 'asserted', 'an operator set this directly'],
    ['established', rel.established, 'established', 'the long-run estimate from this machine'],
    ['recent', rel.n_observations > 0 ? recentOf(rel) : null, 'recent', 'the last window of paired observations'],
  ];

  const wrap = document.createElement('div');
  wrap.className = 'layers';
  for (const [name, value, basis, why] of rows) {
    const present = value !== undefined && value !== null;
    const row = document.createElement('div');
    row.className = `layer${eff.basis === basis ? ' effective' : ''}${present ? '' : ' absent'}`;
    row.title = why;
    row.innerHTML = `
      <span class="name">${escapeHTML(name)}</span>
      <span class="meter"><i style="width:${present ? Math.max(2, value * 100) : 0}%"></i></span>
      <span class="n">${present ? fmt.num(value, 3) : '—'}</span>`;
    wrap.append(row);
  }

  const foot = document.createElement('p');
  foot.className = 'muted';
  foot.style.cssText = 'font-size:11px;margin:8px 0 0;line-height:1.5';
  foot.textContent = eff.known
    ? `The agent reasons with ${fmt.num(eff.value, 3)}, taken from the ${eff.basis} layer.`
    : 'Nothing has been observed about this relationship, so it offers no strength to reason with — not a strength of zero.';
  wrap.append(foot);
  return wrap;
}

/**
 * Contributions to an estimate, as a diverging bar chart.
 *
 * Blue pushes the target up, red pushes it down — the same meaning the sign
 * channel carries on the canvas. Each bar is labelled directly, so identity never
 * rests on the colour.
 */
export function contributionBars(influences, { hypothetical = false } = {}) {
  const wrap = document.createElement('div');
  if (!influences || !influences.length) {
    wrap.innerHTML = `<p class="muted" style="font-size:11.5px;margin:0">
      No relationship points into this property, so its level is whatever was
      observed and nothing else contributes to it.</p>`;
    return wrap;
  }

  const val = (i) => (hypothetical && i.hypothetical_contribution !== undefined && i.hypothetical_contribution !== null
    ? i.hypothetical_contribution : i.contribution);

  const max = Math.max(...influences.map((i) => Math.abs(val(i))), 1e-6);
  const table = document.createElement('div');
  table.style.cssText = 'display:flex;flex-direction:column;gap:7px';

  for (const inf of influences) {
    const v = val(inf);
    const pct = (Math.abs(v) / max) * 50; // half-width per arm
    const row = document.createElement('div');
    row.style.cssText = 'display:grid;grid-template-columns:1fr 54px;gap:8px;align-items:center';
    const colour = v < 0 ? 'var(--sign-neg)' : 'var(--sign-pos)';

    row.innerHTML = `
      <div>
        <div style="font-family:var(--mono);font-size:11px;color:var(--ink-secondary);
                    overflow:hidden;text-overflow:ellipsis;white-space:nowrap"
             title="${escapeHTML(inf.source)} · ${escapeHTML(inf.basis)} · ${escapeHTML(inf.provenance)}">
          ${escapeHTML(inf.source)}
        </div>
        <div style="position:relative;height:9px;margin-top:2px;background:var(--surface-sunken);border-radius:3px">
          <span style="position:absolute;left:50%;top:0;bottom:0;width:1px;background:var(--baseline)"></span>
          <span style="position:absolute;top:0;height:9px;border-radius:3px;background:${colour};
                       ${v < 0 ? `right:50%;width:${pct}%` : `left:50%;width:${pct}%`}"></span>
        </div>
      </div>
      <div class="num mono" style="text-align:right;font-size:11px;font-variant-numeric:tabular-nums">
        ${fmt.signed(v, 3)}
      </div>`;
    table.append(row);
  }

  const axis = document.createElement('div');
  axis.style.cssText = 'display:flex;justify-content:space-between;font-size:10px;color:var(--ink-muted);margin-top:6px';
  axis.innerHTML = `<span>lowers</span><span>no effect</span><span>raises</span>`;

  wrap.append(table, axis);
  return wrap;
}

/** A stacked magnitude bar: the census, with 2px gaps between fills. */
export function censusBar(segments) {
  const total = segments.reduce((s, x) => s + x.n, 0);
  const bar = document.createElement('div');
  bar.className = 'censusbar';
  if (!total) return bar;
  for (const s of segments) {
    if (!s.n) continue;
    const i = document.createElement('span');
    i.style.cssText = `flex:${s.n};background:${s.color}`;
    i.title = `${s.label}: ${s.n}`;
    bar.append(i);
  }
  return bar;
}

/** A labelled 0–1 meter, used for confidence wherever it is reported. */
export function meter(value, label, { of = 1 } = {}) {
  const row = document.createElement('div');
  row.className = 'meter-row';
  const pct = Math.max(0, Math.min(1, value / of)) * 100;
  row.innerHTML = `
    <span class="meter"><i style="width:${pct.toFixed(1)}%"></i></span>
    <span class="n">${escapeHTML(label)}</span>`;
  return row;
}

/**
 * Sign agreements against sign conflicts.
 *
 * Reported as a pair because the ratio separates two states that a strength near
 * zero renders identical: a quiet system, and a declared direction the machine
 * keeps refuting.
 */
export function signSplit(rel) {
  const a = rel.sign_agreements || 0;
  const c = rel.sign_conflicts || 0;
  const wrap = document.createElement('div');
  if (!a && !c) {
    wrap.innerHTML = `<p class="muted" style="font-size:11.5px;margin:0">No paired observations yet.</p>`;
    return wrap;
  }
  const total = a + c;
  wrap.innerHTML = `
    <div style="display:flex;height:9px;gap:2px;margin-bottom:5px">
      <span style="flex:${a || 0.0001};background:var(--sign-pos);border-radius:3px"></span>
      <span style="flex:${c || 0.0001};background:var(--status-critical);border-radius:3px"></span>
    </div>
    <div class="row-between" style="font-size:11px;color:var(--ink-secondary)">
      <span>${a} agreed with the declared sign</span>
      <span>${c} contradicted it</span>
    </div>
    ${rel.sign_suspect ? `<p style="font-size:11.5px;color:var(--status-critical);margin:7px 0 0;line-height:1.5">
      <strong>Sign suspect.</strong> ${Math.round((c / total) * 100)}% of pairs contradict the declared
      direction. This relationship reports a strength near zero, which reads as a quiet
      system — the real message is that the claim is backwards.</p>` : ''}`;
  return wrap;
}
