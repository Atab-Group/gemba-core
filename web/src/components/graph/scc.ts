// Strongly-connected-component condensation, shared by every graph
// algorithm on this page.
//
// It used to be inlined three times: once in detectCycles, once in
// criticalPath, and not at all in the layout, which instead relaxed
// layer numbers until nothing changed. That relaxation was the bug
// behind the unreadable canvas. It is O(V*E) in the worst case, and
// because it mutated depths in place while iterating it could raise a
// node by several layers per pass. On the live board of 2790 items it
// produced 4187 layers, more layers than there are nodes, and a canvas
// 416980 x 334960 pixels. Fitting that into a viewport puts every node
// below a pixel wide.
//
// Condensing first fixes it by construction. Every cycle collapses to
// one component, the result is a DAG, and a DAG's longest path is a
// single linear pass in topological order. Layers are then bounded by
// the real dependency depth, which on that same board is 25.

import type { DirectedEdge } from './graphAnalysis';

export interface Condensation {
  /** components lists each SCC's member ids, in reverse topological order. */
  components: string[][];
  /** compOf maps a node id to its component index. */
  compOf: Map<string, number>;
  /**
   * cyclic holds the component indices that are genuinely cycles: an
   * SCC with more than one member, or a single node with an edge to
   * itself. A component that is merely a node is not a cycle.
   */
  cyclic: Set<number>;
  /** order is a valid topological order of the condensation. */
  order: number[];
  /** adjacency between distinct components, deduplicated. */
  outEdges: Map<number, Set<number>>;
}

/**
 * condense runs Tarjan's algorithm over nodes ∪ edge endpoints and
 * returns the condensation.
 *
 * The node universe is the union rather than the explicit list, so a
 * graph drawn from a partial fetch still classifies an edge whose other
 * end was not fetched. Iteration is explicit-stack rather than
 * recursive: a thousand-node dependency chain would otherwise overflow
 * the JS stack, and dependency chains are exactly what this draws.
 *
 * Both the emission order and the ids within each component are
 * deterministic for a given input, because the layout above it has to
 * be stable across renders or the canvas jitters on every refresh.
 */
export function condense(nodes: string[], edges: DirectedEdge[]): Condensation {
  const adj = new Map<string, string[]>();
  const universe: string[] = [];
  const ensure = (id: string) => {
    if (adj.has(id)) return;
    adj.set(id, []);
    universe.push(id);
  };
  for (const n of nodes) ensure(n);
  for (const e of edges) {
    ensure(e.from);
    ensure(e.to);
    adj.get(e.from)!.push(e.to);
  }

  let index = 0;
  const idx = new Map<string, number>();
  const low = new Map<string, number>();
  const onStack = new Set<string>();
  const stack: string[] = [];
  const components: string[][] = [];
  const compOf = new Map<string, number>();

  type Frame = { node: string; iter: number };
  const callStack: Frame[] = [];

  const visit = (start: string) => {
    callStack.push({ node: start, iter: 0 });
    idx.set(start, index);
    low.set(start, index);
    index++;
    stack.push(start);
    onStack.add(start);

    while (callStack.length > 0) {
      const frame = callStack[callStack.length - 1];
      const neighbours = adj.get(frame.node) ?? [];
      if (frame.iter < neighbours.length) {
        const w = neighbours[frame.iter];
        frame.iter++;
        if (!idx.has(w)) {
          idx.set(w, index);
          low.set(w, index);
          index++;
          stack.push(w);
          onStack.add(w);
          callStack.push({ node: w, iter: 0 });
        } else if (onStack.has(w)) {
          low.set(frame.node, Math.min(low.get(frame.node)!, idx.get(w)!));
        }
        continue;
      }
      if (low.get(frame.node) === idx.get(frame.node)) {
        const comp: string[] = [];
        let popped: string;
        do {
          popped = stack.pop()!;
          onStack.delete(popped);
          comp.push(popped);
          compOf.set(popped, components.length);
        } while (popped !== frame.node);
        components.push(comp);
      }
      callStack.pop();
      if (callStack.length > 0) {
        const parent = callStack[callStack.length - 1];
        low.set(parent.node, Math.min(low.get(parent.node)!, low.get(frame.node)!));
      }
    }
  };

  for (const n of universe) if (!idx.has(n)) visit(n);

  const selfLoops = new Set<string>();
  for (const e of edges) if (e.from === e.to) selfLoops.add(e.from);

  const cyclic = new Set<number>();
  for (let i = 0; i < components.length; i++) {
    if (components[i].length > 1 || selfLoops.has(components[i][0])) cyclic.add(i);
  }

  const outEdges = new Map<number, Set<number>>();
  for (let i = 0; i < components.length; i++) outEdges.set(i, new Set());
  for (const e of edges) {
    const a = compOf.get(e.from);
    const b = compOf.get(e.to);
    if (a == null || b == null || a === b) continue;
    outEdges.get(a)!.add(b);
  }

  // Tarjan emits components in reverse topological order, so reversing
  // the emission order gives a valid topological order without a second
  // pass. Relying on that property is what keeps the whole condensation
  // one linear traversal.
  const order: number[] = [];
  for (let i = components.length - 1; i >= 0; i--) order.push(i);

  return { components, compOf, cyclic, order, outEdges };
}

/**
 * componentDepths returns each component's longest-path depth from any
 * root of the condensation, in one topological pass.
 *
 * This is the layer number the layout draws from. It is O(V + E) and it
 * terminates by construction, because the condensation has no cycles
 * left to relax around.
 */
export function componentDepths(cond: Condensation): number[] {
  const depth = new Array<number>(cond.components.length).fill(0);
  for (const u of cond.order) {
    const du = depth[u];
    for (const v of cond.outEdges.get(u) ?? []) {
      if (depth[v] < du + 1) depth[v] = du + 1;
    }
  }
  return depth;
}
