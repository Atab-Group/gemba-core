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
 * layoutLayered places nodes row by row from their condensation depth.
 *
 * Only structural edges are passed in: `relates_to` and extension edges
 * do not imply ordering, and counting them would inflate depth without
 * representing a predecessor.
 */
export function layoutLayered(
  nodes: LayoutNodeInput[],
  structuralEdges: DirectedEdge[]
): LayoutResult {
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
  for (let row = 0; row < layers.length; row++) {
    const bucket = layers[row];
    if (bucket.length > maxCols) maxCols = bucket.length;
    for (let col = 0; col < bucket.length; col++) {
      positions.set(bucket[col], {
        x: col * COLUMN_WIDTH + LAYER_PADDING,
        y: row * ROW_HEIGHT + LAYER_PADDING,
      });
    }
  }

  return {
    positions,
    width: maxCols * COLUMN_WIDTH + LAYER_PADDING * 2,
    height: layers.length * ROW_HEIGHT + LAYER_PADDING * 2,
    layers: layers.length,
  };
}
