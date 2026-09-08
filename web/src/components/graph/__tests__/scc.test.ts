import { describe, expect, it } from 'vitest';

import { componentDepths, condense } from '../scc';
import { layoutLayered } from '../graphLayout';

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
    // Every node is in one cycle here, so they share a single row.
    expect(layout.layers).toBe(1);
    expect(layout.height).toBeLessThan(200);
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
