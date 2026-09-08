// Pure graph algorithms used by GraphPage (gm-e12.16). No React, no
// React Flow — every function takes plain edges and returns plain
// data so we can unit-test without mounting the renderer. Two
// algorithms ship here:
//
//   * Cycle detection over the SCC condensation. Any component of size
//     >= 2 is a dependency cycle, and a self-loop qualifies too.
//   * Longest-path-by-hop on that same condensation, which drives the
//     critical-path mode. Edge weights are 1 because WorkItem carries
//     no effort estimate; the longest hop chain is the closest stand-in
//     for "longest critical sequence" available today.
//
// Both read the condensation from scc.ts rather than each running their
// own Tarjan, which is how the layout came to have a third and much
// worse implementation of the same idea. One shared condensation means
// one place where cycle handling can be right or wrong.
//
// Both are O(V + E). The page memoises them so an unrelated state
// change does not recompute a graph that has not moved.

import { condense } from './scc';

export interface DirectedEdge {
  from: string;
  to: string;
}

export interface CycleInfo {
  // nodeIds is the set of node ids that participate in any cycle —
  // either an SCC of size ≥ 2 or a self-loop.
  nodeIds: Set<string>;
  // edgeKeys is the set of "from→to" keys for edges whose endpoints
  // are both in the same cycle. A spanning edge between two SCCs is
  // not in this set; only intra-cycle edges are.
  edgeKeys: Set<string>;
  // sccs is the ordered list of cyclic SCCs (each is a list of node
  // ids). Useful for surfacing a "you have N cycles" badge.
  sccs: string[][];
}

// detectCycles runs Tarjan's SCC algorithm and reports which nodes /
// edges sit inside a cycle. Nodes referenced by edges but not present
// in the explicit nodes list are still considered (the algorithm
// builds its node universe from edges + nodes ∪) so a graph drawn
// from a partial WorkItem fetch doesn't silently miss cycles.
export function detectCycles(nodes: string[], edges: DirectedEdge[]): CycleInfo {
  const cond = condense(nodes, edges);

  const cycleNodes = new Set<string>();
  const cyclicSccs: string[][] = [];
  for (const comp of cond.cyclic) {
    cyclicSccs.push(cond.components[comp]);
    for (const n of cond.components[comp]) cycleNodes.add(n);
  }

  // Only intra-component edges of a cyclic component are cycle edges.
  // An edge spanning two components is what breaks the cycle, not part
  // of it, and painting it red would tell an operator to look at the
  // wrong dependency.
  const cycleEdges = new Set<string>();
  for (const e of edges) {
    const a = cond.compOf.get(e.from);
    const b = cond.compOf.get(e.to);
    if (a == null || a !== b || !cond.cyclic.has(a)) continue;
    cycleEdges.add(edgeKey(e));
  }

  return { nodeIds: cycleNodes, edgeKeys: cycleEdges, sccs: cyclicSccs };
}

export interface CriticalPath {
  nodeIds: Set<string>;
  edgeKeys: Set<string>;
  // length is the hop count along the longest chain. Surfaced in the
  // legend so an operator can tell whether the highlight reflects a
  // 30-link mountain or a 3-link shortcut.
  length: number;
}

// criticalPath returns the longest hop chain through the
// SCC-condensation of (nodes, edges). On a pure DAG this is simply
// the longest path; on a graph with cycles, each cycle is collapsed
// to a super-node so the highlight stays a single contiguous chain.
//
// We pick "longest by hop count" rather than "longest by some
// estimate" because WorkItem doesn't carry effort estimates today. If
// the manifest grows a `priority_weight` or similar later, swap the
// edge weight without touching the algorithm structure.
export function criticalPath(nodes: string[], edges: DirectedEdge[]): CriticalPath {
  const cond = condense(nodes, edges);
  if (cond.components.length === 0) {
    return { nodeIds: new Set(), edgeKeys: new Set(), length: 0 };
  }

  // Longest path on the condensation, one pass in topological order.
  const dist = new Array<number>(cond.components.length).fill(0);
  const pred = new Array<number>(cond.components.length).fill(-1);
  let bestEnd = -1;
  let bestDist = -1;
  for (const u of cond.order) {
    if (dist[u] > bestDist) {
      bestDist = dist[u];
      bestEnd = u;
    }
    for (const v of cond.outEdges.get(u) ?? []) {
      if (dist[u] + 1 > dist[v]) {
        dist[v] = dist[u] + 1;
        pred[v] = u;
      }
    }
  }
  if (bestEnd < 0) {
    return { nodeIds: new Set(), edgeKeys: new Set(), length: 0 };
  }

  const path: number[] = [];
  for (let cur = bestEnd; cur >= 0; cur = pred[cur]) path.push(cur);
  path.reverse();

  const pathSet = new Set(path);
  const pathNodeIds = new Set<string>();
  for (const compIdx of path) {
    for (const n of cond.components[compIdx]) pathNodeIds.add(n);
  }

  // Edges on the path: those spanning two adjacent components on the
  // chain, plus the internal edges of any cycle the chain runs through,
  // so the highlight stays connected rather than breaking at each cycle.
  const adjacentPairs = new Set<string>();
  for (let i = 0; i < path.length - 1; i++) {
    adjacentPairs.add(`${path[i]}->${path[i + 1]}`);
  }
  const pathEdgeKeys = new Set<string>();
  for (const e of edges) {
    const a = cond.compOf.get(e.from);
    const b = cond.compOf.get(e.to);
    if (a == null || b == null) continue;
    if (a === b && pathSet.has(a) && cond.cyclic.has(a)) {
      pathEdgeKeys.add(edgeKey(e));
    } else if (adjacentPairs.has(`${a}->${b}`)) {
      pathEdgeKeys.add(edgeKey(e));
    }
  }

  return { nodeIds: pathNodeIds, edgeKeys: pathEdgeKeys, length: bestDist };
}

export function edgeKey(e: DirectedEdge): string {
  return `${e.from}\u0000${e.to}`;
}
