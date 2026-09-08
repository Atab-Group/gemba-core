import { describe, expect, it } from 'vitest';

import { componentDepths, condense } from '../scc';
import { columnsFor, layoutLayered, positionsSignature } from '../graphLayout';

function edges(pairs: [string, string][]) {
  return pairs.map(([from, to]) => ({ from, to }));
}

describe('condense', () => {
  it('gives every node of a cycle the same component', () => {
    const cond = condense(['a', 'b', 'c'], edges([['a', 'b'], ['b', 'c'], ['c', 'a']]));
    expect(cond.components).toHaveLength(1);
    expect(cond.cyclic.size).toBe(1);
    expect(cond.compOf.get('a')).toBe(cond.compOf.get('c'));
  });

  // A node is not a cycle. Reporting one would paint half the board red
  // on an ordinary DAG.
  it('does not call a single node a cycle', () => {
    const cond = condense(['a', 'b'], edges([['a', 'b']]));
    expect(cond.components).toHaveLength(2);
    expect(cond.cyclic.size).toBe(0);
  });

  it('treats a self-loop as a cycle', () => {
    const cond = condense(['a'], edges([['a', 'a']]));
    expect(cond.cyclic.size).toBe(1);
  });

  // Tarjan emits in reverse topological order, and the layout depends on
  // that rather than running a second sort. If it ever stopped holding,
  // every depth would be wrong and nothing else would say so.
  it('returns a topological order of the condensation', () => {
    const cond = condense(
      ['a', 'b', 'c', 'd'],
      edges([['a', 'b'], ['b', 'c'], ['c', 'd']])
    );
    const position = new Map(cond.order.map((comp, i) => [comp, i]));
    for (const [comp, targets] of cond.outEdges) {
      for (const t of targets) {
        expect(position.get(comp)!).toBeLessThan(position.get(t)!);
      }
    }
  });

  it('reaches nodes that only appear on an edge', () => {
    const cond = condense([], edges([['a', 'b']]));
    expect(cond.compOf.has('a')).toBe(true);
    expect(cond.compOf.has('b')).toBe(true);
  });
});

describe('componentDepths', () => {
  it('is the longest chain, not the shortest', () => {
    // a → b → c and a → c: c sits below b, not beside it.
    const cond = condense(['a', 'b', 'c'], edges([['a', 'b'], ['b', 'c'], ['a', 'c']]));
    const depth = componentDepths(cond);
    expect(depth[cond.compOf.get('c')!]).toBe(2);
  });

  it('collapses a cycle to one depth', () => {
    const cond = condense(
      ['a', 'b', 'c', 'd'],
      edges([['a', 'b'], ['b', 'c'], ['c', 'b'], ['c', 'd']])
    );
    const depth = componentDepths(cond);
    expect(depth[cond.compOf.get('b')!]).toBe(depth[cond.compOf.get('c')!]);
    expect(depth[cond.compOf.get('d')!]).toBe(2);
  });
});

describe('layoutLayered under cycles', () => {
  // The defect this replaced. The old relaxation raised depths in place
  // while iterating, so a cycle could lift nodes by several layers per
  // pass: on the live 2790-item board it produced 4187 layers and a
  // canvas 334960 pixels tall, which fits to a viewport at a zoom where
  // nothing is legible.
  it('never uses more layers than there are nodes', () => {
    const ids: string[] = [];
    const pairs: [string, string][] = [];
    for (let i = 0; i < 200; i++) ids.push(`n${String(i).padStart(3, '0')}`);
    for (let i = 0; i < 199; i++) pairs.push([ids[i], ids[i + 1]]);
    // Three back-edges, enough to make the old relaxation run away.
    pairs.push([ids[150], ids[10]]);
    pairs.push([ids[120], ids[40]]);
    pairs.push([ids[199], ids[0]]);

    const layout = layoutLayered(ids.map((id) => ({ id })), edges(pairs));
    expect(layout.layers).toBeLessThanOrEqual(ids.length);
    // Every node is in one cycle here, so there is one dependency layer.
    expect(layout.layers).toBe(1);
    // That layer wraps into a band rather than a 200-wide line, so the
    // canvas stays a shape a viewport can frame. The old relaxation put
    // this input in thousands of rows.
    expect(layout.width).toBeLessThan(3000);
    expect(layout.height).toBeLessThan(2000);
  });

  // Wrapping must not disturb the thing the layout is for: a node at a
  // greater dependency depth still sits below one at a lesser depth.
  it('keeps a wrapped layer above the layer that depends on it', () => {
    const wide = Array.from({ length: 40 }, (_, i) => `w${String(i).padStart(2, '0')}`);
    const pairs: [string, string][] = wide.map((id) => [id, 'sink']);
    const layout = layoutLayered(
      [...wide, 'sink'].map((id) => ({ id })),
      edges(pairs)
    );
    const sinkY = layout.positions.get('sink')!.y;
    for (const id of wide) {
      expect(layout.positions.get(id)!.y).toBeLessThan(sinkY);
    }
    // 40 nodes wrap rather than forming a 40-wide line.
    expect(layout.width).toBeLessThan(3000);
  });

  it('keeps a plain chain at its real depth', () => {
    const ids = ['a', 'b', 'c', 'd'];
    const layout = layoutLayered(
      ids.map((id) => ({ id })),
      edges([['a', 'b'], ['b', 'c'], ['c', 'd']])
    );
    expect(layout.layers).toBe(4);
    const y = (id: string) => layout.positions.get(id)!.y;
    expect(y('a')).toBeLessThan(y('b'));
    expect(y('b')).toBeLessThan(y('c'));
    expect(y('c')).toBeLessThan(y('d'));
  });

  // The canvas must not move between two renders of the same board.
  it('is deterministic for the same input', () => {
    const ids = ['c', 'a', 'b'];
    const pairs = edges([['a', 'b'], ['b', 'c']]);
    const first = layoutLayered(ids.map((id) => ({ id })), pairs);
    const second = layoutLayered(ids.map((id) => ({ id })), pairs);
    expect([...first.positions.entries()]).toEqual([...second.positions.entries()]);
  });

  // A bounded view is a real subgraph. Laying out a node that will not
  // be drawn would leave a hole in the row it was placed in.
  it('ignores an edge naming a node outside the render', () => {
    const layout = layoutLayered(
      [{ id: 'a' }, { id: 'b' }],
      edges([['a', 'b'], ['b', 'ghost'], ['ghost', 'a']])
    );
    expect(layout.layers).toBe(2);
    expect(layout.positions.has('ghost')).toBe(false);
  });
});

describe('columnsFor', () => {
  // The column cap is a property of the viewport, not the graph: the
  // right number of nodes side by side is however many fit at a zoom
  // somebody can read at.
  it('scales the column cap with the canvas', () => {
    expect(columnsFor(2600)).toBe(11);
    expect(columnsFor(1200)).toBe(5);
  });

  // A large set spreads out rather than becoming a ribbon: three hundred
  // nodes four wide is seventy-five rows, which fits to the zoom floor
  // however legible each row would have been.
  it('widens for a large set even on a narrow canvas', () => {
    expect(columnsFor(900, 300)).toBe(12);
    expect(columnsFor(900, 36)).toBe(6);
  });

  // A handful of nodes stays as wide as the canvas allows rather than
  // being squeezed into a square for no reason.
  it('leaves a small set to the canvas', () => {
    expect(columnsFor(1200, 4)).toBe(5);
  });

  // Below three columns a wrapped layer becomes a column, which is a
  // worse shape than a slightly-too-wide row.
  it('never goes below three columns', () => {
    expect(columnsFor(300)).toBe(3);
    expect(columnsFor(0)).toBe(12);
  });

  it('falls back to the default when the canvas is unmeasured', () => {
    expect(columnsFor(Number.POSITIVE_INFINITY)).toBe(12);
  });

  it('wraps to the cap it is given', () => {
    const ids = Array.from({ length: 12 }, (_, i) => `g${i}`);
    const layout = layoutLayered(ids.map((id) => ({ id })), [], { maxColumns: 3 });
    expect(layout.width).toBeLessThanOrEqual(3 * 220 + 80);
    const rows = new Set([...layout.positions.values()].map((p) => p.y));
    expect(rows.size).toBe(4);
  });
});

describe('positionsSignature', () => {
  // The camera fit waits on this. A resize reflows the layout without
  // changing a single node, so a count check passes on the first frame
  // and the fit frames the arrangement that just went away.
  it('differs when the same nodes are arranged differently', () => {
    const ids = Array.from({ length: 8 }, (_, i) => `p${i}`);
    const wide = layoutLayered(ids.map((id) => ({ id })), [], { maxColumns: 4 });
    const narrow = layoutLayered(ids.map((id) => ({ id })), [], { maxColumns: 3 });
    expect(positionsSignature(ids, wide.positions)).not.toBe(
      positionsSignature(ids, narrow.positions)
    );
  });

  it('is stable for the same arrangement', () => {
    const ids = ['a', 'b'];
    const layout = layoutLayered(ids.map((id) => ({ id })), []);
    expect(positionsSignature(ids, layout.positions)).toBe(
      positionsSignature(ids, layout.positions)
    );
  });

  // A node the store has not placed yet must not read as settled.
  it('differs when a node is missing a position', () => {
    const ids = ['a', 'b'];
    const layout = layoutLayered(ids.map((id) => ({ id })), []);
    const partial = new Map(layout.positions);
    partial.delete('b');
    expect(positionsSignature(ids, partial)).not.toBe(
      positionsSignature(ids, layout.positions)
    );
  });
});
