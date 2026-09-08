// Bounded neighbourhood extraction: the subgraph around one focused
// item, out to N hops, under a hard node cap.
//
// This is what makes the graph usable on a real board. The page used to
// hand React Flow every filtered item, which on the live company board
// is 2790 nodes and 2200 edges in one canvas: a hairball nobody can
// read and a mount nobody wants to wait for. A person opening the graph
// is almost always asking about one item, and the honest answer to
// "what is blocking this" is a small graph, not a large one.
//
// Direction matters and is kept. Following edges backwards reaches the
// blockers and parents that have to finish first; following them
// forwards reaches the dependents that finishing this would release.
// Both are walked, and each node records which side it came from and
// how far, so the renderer can shade upstream from downstream and the
// caller can expand one hop at a time.

import type { DirectedEdge } from './graphAnalysis';

/** Which side of the focus a node was reached from. */
export type NeighbourDirection = 'focus' | 'upstream' | 'downstream' | 'both';

export interface NeighbourhoodOptions {
  /** depth is the maximum hop distance from the focus, in each direction. */
  depth: number;
  /** maxNodes is the hard cap on the returned set, focus included. */
  maxNodes: number;
}

export interface NeighbourhoodResult {
  /** ids is the bounded node set, ordered nearest-first then by id. */
  ids: string[];
  /** depthOf is the hop distance from the focus for each returned id. */
  depthOf: Map<string, number>;
  /** directionOf says which side each returned id was reached from. */
  directionOf: Map<string, NeighbourDirection>;
  /**
   * reachable is how many nodes lie within `depth` of the focus,
   * counted before the cap. The difference between this and ids.length
   * is what the "showing X of Y" line reports.
   */
  reachable: number;
  /** truncated says the cap cut the set. */
  truncated: boolean;
  /**
   * hasMoreDepth says at least one returned node has a neighbour that
   * sits beyond `depth`, so expanding would actually show something.
   * Without it the expand control would offer a step that changes
   * nothing, which teaches an operator to distrust it.
   */
  hasMoreDepth: boolean;
}

interface Adjacency {
  out: Map<string, string[]>;
  in: Map<string, string[]>;
}

function buildAdjacency(edges: DirectedEdge[]): Adjacency {
  const out = new Map<string, string[]>();
  const inc = new Map<string, string[]>();
  const push = (m: Map<string, string[]>, key: string, value: string) => {
    const list = m.get(key);
    if (list) list.push(value);
    else m.set(key, [value]);
  };
  for (const e of edges) {
    // A self-loop adds nothing to a neighbourhood walk; it is a cycle
    // the cycle highlighter already reports.
    if (e.from === e.to) continue;
    push(out, e.from, e.to);
    push(inc, e.to, e.from);
  }
  // Sorted so a truncated expansion always cuts the same nodes. An
  // unstable order would reshuffle the canvas on every refresh and make
  // the cap look like data loss.
  for (const list of out.values()) list.sort();
  for (const list of inc.values()) list.sort();
  return { out, in: inc };
}

/**
 * neighbourhood returns the nodes within `depth` hops of `focusId`,
 * capped at `maxNodes`.
 *
 * Truncation drops the furthest nodes first, so what survives the cap is
 * the part of the graph nearest the thing being asked about. Dropping an
 * arbitrary slice instead would leave a set with holes in the middle,
 * which reads as a wrong answer rather than a partial one.
 */
export function neighbourhood(
  focusId: string,
  edges: DirectedEdge[],
  opts: NeighbourhoodOptions
): NeighbourhoodResult {
  const depthLimit = Math.max(0, opts.depth);
  const cap = Math.max(1, opts.maxNodes);
  const adj = buildAdjacency(edges);

  const depthOf = new Map<string, number>([[focusId, 0]]);
  const directionOf = new Map<string, NeighbourDirection>([[focusId, 'focus']]);

  const note = (id: string, dir: 'upstream' | 'downstream') => {
    const existing = directionOf.get(id);
    if (!existing) directionOf.set(id, dir);
    else if (existing !== dir && existing !== 'focus') directionOf.set(id, 'both');
  };

  let frontier = [focusId];
  let hasMoreDepth = false;
  for (let d = 1; d <= depthLimit; d++) {
    const next: string[] = [];
    for (const id of frontier) {
      for (const to of adj.out.get(id) ?? []) {
        note(to, 'downstream');
        if (depthOf.has(to)) continue;
        depthOf.set(to, d);
        next.push(to);
      }
      for (const from of adj.in.get(id) ?? []) {
        note(from, 'upstream');
        if (depthOf.has(from)) continue;
        depthOf.set(from, d);
        next.push(from);
      }
    }
    if (next.length === 0) break;
    next.sort();
    frontier = next;
  }

  // One probe past the limit, purely to answer whether expanding would
  // reveal anything. It walks the last frontier's neighbours only, so it
  // costs one level rather than another traversal.
  for (const id of frontier) {
    for (const to of adj.out.get(id) ?? []) {
      if (!depthOf.has(to)) hasMoreDepth = true;
    }
    for (const from of adj.in.get(id) ?? []) {
      if (!depthOf.has(from)) hasMoreDepth = true;
    }
    if (hasMoreDepth) break;
  }

  const reachable = depthOf.size;
  const ordered = [...depthOf.keys()].sort((a, b) => {
    const da = depthOf.get(a)!;
    const db = depthOf.get(b)!;
    return da !== db ? da - db : a < b ? -1 : a > b ? 1 : 0;
  });
  const ids = ordered.slice(0, cap);
  const kept = new Set(ids);
  for (const id of ordered) {
    if (!kept.has(id)) {
      depthOf.delete(id);
      directionOf.delete(id);
    }
  }

  return {
    ids,
    depthOf,
    directionOf,
    reachable,
    truncated: reachable > ids.length,
    hasMoreDepth,
  };
}
