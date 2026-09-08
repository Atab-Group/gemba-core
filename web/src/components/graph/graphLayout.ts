// Layered DAG layout for the dependency graph (gm-e12.16, top-down
// re-orient gm-e12.20).
//
// No third-party layout dependency: the dependency graph is a sparse
// DAG with a handful of edge types, and a hand-rolled layered placer is
// easier to tune to the domain than a 200KB layout engine is to bend.
//
// Layering: collapse cycles to components, then each component's row is
// its longest hop chain from any root of the condensation. Roots anchor
// the top; descendants flow downward. Members of one cycle share a row,
// which reads correctly because the renderer paints them as a cycle
// anyway.
//
// The condensation is not an optimisation. The previous version relaxed
// layer numbers over the raw graph until nothing changed, bailing after
// V passes. Under cycles that never settles, and because it wrote
// depths in place while iterating it could lift a node several layers
// in a single pass. On the live 2790-item board it produced 4187
// layers, more layers than nodes, and a canvas 416980 x 334960 pixels;
// fitting that to a viewport puts every node below a pixel wide.
// Condensing first makes the input acyclic, so one topological pass is
// both correct and linear, and the same board lays out in 25 rows.
//
// Axes (gm-e12.20): layer → y (vertical), index-within-layer → x
// (horizontal). Within a layer, nodes sort by id so the layout is
// identical across renders and the canvas does not jitter on refresh.

import type { DirectedEdge } from './graphAnalysis';
import { componentDepths, condense } from './scc';

export interface LayoutNodeInput {
  id: string;
}

export interface LayoutResult {
  // positions maps each node id to its (x, y) in graph-space pixels.
  positions: Map<string, { x: number; y: number }>;
  // width / height are the layout's bounding box; the GraphPage uses
  // these to fit-view the React Flow canvas on initial mount.
  width: number;
  height: number;
  // layers is the number of rows used. Reported so a caller can assert
  // the depth is the real dependency depth rather than an artefact of
  // the layering, which is precisely what went wrong here before.
  layers: number;
}

const COLUMN_WIDTH = 220;
const ROW_HEIGHT = 80;
const LAYER_PADDING = 40;

/**
 * MAX_COLUMNS wraps a wide layer into several rows.
 *
 * A layer is a set of nodes at the same dependency depth, and nothing
 * says a hundred of them have to sit in one line. Laid out flat, the
 * live board's widest layer was 30880 pixels across, and the overview's
 * ten tiles were 2280, both of which fit to a viewport at a zoom where
 * the text is unreadable. Wrapping keeps the depth ordering, which is
 * the thing the layout is for, and bounds the width to something a
 * screen can hold at a legible zoom.
 */
const MAX_COLUMNS = 12;
const MIN_COLUMNS = 3;

export interface LayoutOptions {
  /**
   * maxColumns caps how many nodes sit side by side before a layer
   * wraps. The page derives it from the canvas width, because the right
   * number of columns is however many fit at a zoom somebody can read
   * at, and that is a property of the viewport rather than the graph.
   */
  maxColumns?: number;
}

/**
 * columnsFor picks the column cap from the canvas and the node count.
 *
 * Two pulls, and they point opposite ways. A narrow canvas wants few
 * columns, so what is drawn is legible at a zoom near 1. A large set
 * wants many, because the alternative is a ribbon: three hundred nodes
 * four wide is seventy-five rows, which fits to the zoom floor and
 * renders as a smear no matter how legible each row would have been.
 *
 * So the width sets the floor and a roughly square arrangement sets the
 * ceiling. A handful of nodes stays as wide as the canvas allows; a
 * board's worth spreads out until it is square-ish, capped so it never
 * becomes a single line again.
 */
export function columnsFor(canvasWidth: number, nodeCount = 0): number {
  const square = nodeCount > 0 ? Math.ceil(Math.sqrt(nodeCount)) : 0;
  const byWidth = !Number.isFinite(canvasWidth) || canvasWidth <= 0
    ? MAX_COLUMNS
    : Math.floor(canvasWidth / COLUMN_WIDTH);
  return Math.min(MAX_COLUMNS, Math.max(MIN_COLUMNS, byWidth, square));
}

/**
 * layoutLayered places nodes row by row from their condensation depth.
 *
 * Only structural edges are passed in: `relates_to` and extension edges
 * do not imply ordering, and counting them would inflate depth without
 * representing a predecessor.
 */
export function layoutLayered(
  nodes: LayoutNodeInput[],
  structuralEdges: DirectedEdge[],
  options: LayoutOptions = {}
): LayoutResult {
  const columnCap = Math.max(1, options.maxColumns ?? MAX_COLUMNS);
  const ids = nodes.map((n) => n.id);
  if (ids.length === 0) {
    return { positions: new Map(), width: 0, height: 0, layers: 0 };
  }

  const known = new Set(ids);
  // An edge naming a node outside this render is dropped rather than
  // pulling a phantom into the condensation. A bounded view is a real
  // subgraph, and laying out nodes that will not be drawn would leave
  // holes in every row.
  const scoped = structuralEdges.filter((e) => known.has(e.from) && known.has(e.to));

  const cond = condense(ids, scoped);
  const depths = componentDepths(cond);

  const layers: string[][] = [];
  for (const id of ids) {
    const comp = cond.compOf.get(id);
    const row = comp == null ? 0 : depths[comp];
    while (layers.length <= row) layers.push([]);
    layers[row].push(id);
  }
  for (const bucket of layers) bucket.sort();

  const positions = new Map<string, { x: number; y: number }>();
  let maxCols = 0;
  let visualRow = 0;
  for (const bucket of layers) {
    const cols = Math.min(columnCap, Math.max(1, bucket.length));
    if (cols > maxCols) maxCols = cols;
    for (let i = 0; i < bucket.length; i++) {
      positions.set(bucket[i], {
        x: (i % cols) * COLUMN_WIDTH + LAYER_PADDING,
        y: (visualRow + Math.floor(i / cols)) * ROW_HEIGHT + LAYER_PADDING,
      });
    }
    // A wrapped layer occupies as many visual rows as it needed, plus
    // one blank row so the next depth still reads as a separate band.
    visualRow += Math.max(1, Math.ceil(bucket.length / cols)) + (bucket.length > cols ? 1 : 0);
  }

  return {
    positions,
    width: maxCols * COLUMN_WIDTH + LAYER_PADDING * 2,
    height: visualRow * ROW_HEIGHT + LAYER_PADDING * 2,
    layers: layers.length,
  };
}

/**
 * positionsSignature identifies a laid-out arrangement.
 *
 * The camera fit has to happen after React Flow has taken the new
 * positions, and counting nodes does not tell you that. A resize
 * reflows the layout without changing a single node, so a count check
 * passes on the first frame, the fit runs against the positions still in
 * the store, and the camera ends up framing the arrangement that just
 * went away. Comparing the arrangement itself is the only check that
 * catches it.
 */
export function positionsSignature(
  ids: string[],
  positions: Map<string, { x: number; y: number }>
): string {
  let hash = 0x811c9dc5;
  const mix = (n: number) => {
    hash ^= n | 0;
    hash = Math.imul(hash, 0x01000193);
  };
  for (const id of ids) {
    const p = positions.get(id);
    if (!p) {
      mix(-1);
      continue;
    }
    mix(Math.round(p.x));
    mix(Math.round(p.y));
  }
  return `${ids.length}:${(hash >>> 0).toString(36)}`;
}
