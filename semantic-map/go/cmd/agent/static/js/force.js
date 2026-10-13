// A small force-directed layout.
//
// Written out rather than imported because the console must work on a node with
// no route to a CDN — which is the deployment this agent is built for. The map is
// tens of properties, not thousands, so the O(n²) repulsion is cheaper than the
// quadtree that would avoid it, and it is stable without tuning.
//
// Integration is explicit Euler with velocity damping and an annealing alpha:
// positions settle, then stop costing anything, and any topology change reheats.

const REPULSION = 9500;   // Coulomb constant between every pair
const SPRING = 0.035;     // Hooke constant along a link
const DAMPING = 0.82;
const CENTER = 0.012;     // pull toward the middle, so nothing drifts off-screen
const GROUP = 0.045;      // pull toward a subject's own centroid
const MIN_ALPHA = 0.004;

export class Force {
  constructor() {
    this.nodes = new Map();  // id → {id, x, y, vx, vy, r, group, pinned}
    this.links = [];         // {source, target, distance}
    this.alpha = 1;
    this.width = 800;
    this.height = 600;
  }

  size(w, h) { this.width = w; this.height = h; }

  /**
   * Reconcile the simulation with a new set of nodes and links.
   *
   * Surviving nodes keep their position: a poll that admits one property should
   * nudge the picture, not redraw it somewhere else. New nodes are seeded near
   * their group's centre of mass — or near the middle — so they arrive where they
   * belong instead of flying in from a corner.
   */
  sync(nodes, links) {
    const seen = new Set();
    let topologyChanged = false;

    for (const n of nodes) {
      seen.add(n.id);
      const existing = this.nodes.get(n.id);
      if (existing) {
        existing.r = n.r;
        existing.hw = n.hw ?? n.r;
        existing.below = n.below ?? 0;
        existing.group = n.group;
        continue;
      }
      topologyChanged = true;
      const seed = this.seedFor(n.group);
      this.nodes.set(n.id, {
        id: n.id, r: n.r, hw: n.hw ?? n.r, below: n.below ?? 0, group: n.group,
        x: seed.x, y: seed.y, vx: 0, vy: 0, pinned: false,
      });
    }
    for (const id of [...this.nodes.keys()]) {
      if (!seen.has(id)) { this.nodes.delete(id); topologyChanged = true; }
    }

    // Compared as a SET, not a count. One relationship retiring while another is
    // confirmed in the same poll leaves the count unchanged, and a layout that
    // never reheats draws the new edge across positions arranged for the old one.
    const next = links.filter((l) => this.nodes.has(l.source) && this.nodes.has(l.target));
    const key = (l) => `${l.source}\u0001${l.target}`;
    const before = new Set(this.links.map(key));
    if (next.length !== before.size || next.some((l) => !before.has(key(l)))) {
      topologyChanged = true;
    }
    this.links = next;

    if (topologyChanged) this.reheat(0.75);
    return topologyChanged;
  }

  seedFor(group) {
    const peers = [...this.nodes.values()].filter((n) => group && n.group === group);
    const jitter = () => (Math.random() - 0.5) * 60;
    if (peers.length) {
      const cx = peers.reduce((s, n) => s + n.x, 0) / peers.length;
      const cy = peers.reduce((s, n) => s + n.y, 0) / peers.length;
      return { x: cx + jitter(), y: cy + jitter() };
    }
    return { x: this.width / 2 + jitter() * 3, y: this.height / 2 + jitter() * 3 };
  }

  reheat(to = 0.6) { this.alpha = Math.max(this.alpha, to); }

  /**
   * Run the simulation synchronously until it settles, or for `max` steps.
   *
   * Animating a layout into place depends on requestAnimationFrame, and a browser
   * throttles that hard in a background tab — so a graph laid out only by the
   * animation loop can still be sitting on its seed positions when someone brings
   * the tab forward, or when a screenshot is taken. Settling here costs a few
   * milliseconds at this size and makes the first painted frame the right one.
   */
  prewarm(max = 300) {
    for (let i = 0; i < max && !this.settled; i++) this.tick();
  }

  get settled() { return this.alpha <= MIN_ALPHA; }

  /** One integration step. Returns false once the layout has stopped moving. */
  tick() {
    if (this.settled) return false;
    const nodes = [...this.nodes.values()];
    const n = nodes.length;
    if (n === 0) return false;

    const cx = this.width / 2;
    const cy = this.height / 2;
    const ratio = Math.min(4, Math.max(0.25, this.width / Math.max(1, this.height)));
    const aspectX = ratio < 1 ? 1 / ratio : 1;
    const aspectY = ratio > 1 ? ratio : 1;

    // Group centroids, computed once per tick rather than per node.
    const groups = new Map();
    for (const a of nodes) {
      if (!a.group) continue;
      const g = groups.get(a.group) || { x: 0, y: 0, n: 0 };
      g.x += a.x; g.y += a.y; g.n++;
      groups.set(a.group, g);
    }
    for (const g of groups.values()) { g.x /= g.n; g.y /= g.n; }

    for (let i = 0; i < n; i++) {
      const a = nodes[i];
      let fx = 0, fy = 0;

      for (let j = 0; j < n; j++) {
        if (i === j) continue;
        const b = nodes[j];
        let dx = a.x - b.x, dy = a.y - b.y;
        let d2 = dx * dx + dy * dy;
        if (d2 < 1) { // coincident: push apart on a deterministic-enough jitter
          dx = (Math.random() - 0.5) * 2; dy = (Math.random() - 0.5) * 2; d2 = 1;
        }
        const d = Math.sqrt(d2);
        // Same-subject properties repel less, so a workload reads as one cluster.
        const scale = (a.group && a.group === b.group) ? 0.45 : 1;
        const f = (REPULSION * scale) / d2;
        fx += (dx / d) * f;
        fy += (dy / d) * f;
      }

      // The pull toward the middle follows the canvas's shape. Equal on both axes,
      // it settles the graph into a disc, and a disc fitted into a wide, short
      // canvas is drawn small in the middle third with edges threading through
      // nodes. Pulling harder along the short axis lays it out the canvas's way.
      fx += (cx - a.x) * CENTER * aspectX;
      fy += (cy - a.y) * CENTER * aspectY;

      if (a.group) {
        const g = groups.get(a.group);
        fx += (g.x - a.x) * GROUP;
        fy += (g.y - a.y) * GROUP;
      }

      a.fx = fx; a.fy = fy;
    }

    for (const l of this.links) {
      const a = this.nodes.get(l.source);
      const b = this.nodes.get(l.target);
      const dx = b.x - a.x, dy = b.y - a.y;
      const d = Math.sqrt(dx * dx + dy * dy) || 1;
      const f = (d - l.distance) * SPRING;
      const ux = (dx / d) * f, uy = (dy / d) * f;
      a.fx += ux; a.fy += uy;
      b.fx -= ux; b.fy -= uy;
    }

    for (const a of nodes) {
      if (a.pinned) { a.vx = 0; a.vy = 0; a.fx = a.fy = 0; continue; }
      a.vx = (a.vx + a.fx * this.alpha) * DAMPING;
      a.vy = (a.vy + a.fy * this.alpha) * DAMPING;
      // A speed cap keeps a reheat from throwing a node across the viewport.
      const sp = Math.hypot(a.vx, a.vy);
      if (sp > 28) { a.vx = (a.vx / sp) * 28; a.vy = (a.vy / sp) * 28; }
      a.x += a.vx;
      a.y += a.vy;
      a.fx = a.fy = 0;
    }

    // Separate overlapping footprints so a label always has somewhere to sit.
    //
    // A footprint is the disc AND its two label lines, as a box: a label such as
    // cpu_pressure_ratio is several discs wide, so separating circles left
    // neighbours whose discs cleared each other printing their names on top of
    // one another. Each overlapping pair is pushed apart along the axis it
    // overlaps least, which is the smallest move that clears it.
    for (let i = 0; i < n; i++) {
      for (let j = i + 1; j < n; j++) {
        const a = nodes[i], b = nodes[j];
        const gap = separation(a, b);
        const dx = b.x - a.x;
        const dy = (b.y + boxShift(b)) - (a.y + boxShift(a));
        const ox = a.hw + b.hw + gap - Math.abs(dx);
        const oy = boxHalfHeight(a) + boxHalfHeight(b) + gap - Math.abs(dy);
        if (ox <= 0 || oy <= 0) continue;
        let px = 0, py = 0;
        // Each overlap is weighted by the canvas's aspect, so collisions resolve
        // along its long axis — sideways on a wide canvas, downwards on a tall one —
        // and the graph spreads into the space it is drawn in rather than stacking.
        if (ox * aspectX < oy * aspectY) px = (dx === 0 ? (Math.random() < 0.5 ? -1 : 1) : Math.sign(dx)) * ox;
        else py = (dy === 0 ? (Math.random() < 0.5 ? -1 : 1) : Math.sign(dy)) * oy;
        // A pinned node does not move, so the other one takes the whole push.
        const share = a.pinned || b.pinned ? 1 : 0.5;
        if (!a.pinned) { a.x -= px * share; a.y -= py * share; }
        if (!b.pinned) { b.x += px * share; b.y += py * share; }
      }
    }

    this.alpha *= 0.975;
    return true;
  }

  at(id) { return this.nodes.get(id); }

  /** The bounding box of everything laid out, for a fit-to-viewport. */
  bounds() {
    const ns = [...this.nodes.values()];
    if (!ns.length) return null;
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const n of ns) {
      const f = footprint(n);
      x0 = Math.min(x0, f.x0); y0 = Math.min(y0, f.y0);
      x1 = Math.max(x1, f.x1); y1 = Math.max(y1, f.y1);
    }
    return { x0, y0, x1, y1, w: x1 - x0, h: y1 - y0 };
  }
}

/**
 * The box a node occupies on screen: its disc, widened to its label, and
 * extended below it by the label lines. Fitting, hulls and collision all use it,
 * so what is framed is what is drawn.
 */
export function footprint(n) {
  const hw = n.hw ?? n.r;
  return { x0: n.x - hw, x1: n.x + hw, y0: n.y - n.r, y1: n.y + n.r + (n.below ?? 0) };
}

const boxHalfHeight = (n) => (2 * n.r + (n.below ?? 0)) / 2;
// The box's centre sits below the disc's, by half the label block.
const boxShift = (n) => (n.below ?? 0) / 2;

// Clearance between two footprints. Properties of different subjects are kept
// further apart than the subject enclosure's padding, so a property does not sit
// inside the padding around a neighbour of another subject; inside one subject
// they sit close. This is pairwise only: an enclosure is the box around all its
// members, so a foreign property can still settle in the gap between two members
// of a spread-out subject and fall inside that box.
const HULL_PAD = 26;
const HULL_LABEL = 10;
function separation(a, b) {
  if (a.group && a.group === b.group) return 10;
  // HULL_LABEL is the strip above an enclosure that carries the subject's name.
  if (a.group && b.group) return 2 * HULL_PAD + HULL_LABEL + 6;   // two enclosures
  if (a.group || b.group) return HULL_PAD + HULL_LABEL + 6;
  return 14;
}

export { HULL_PAD, HULL_LABEL };
