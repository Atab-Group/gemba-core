import { describe, expect, it } from 'vitest';

import { neighbourhood } from '../neighbourhood';

function edges(pairs: [string, string][]) {
  return pairs.map(([from, to]) => ({ from, to }));
}

// A small board: b blocks a, a blocks c, c blocks d, plus an island.
const board = edges([
  ['b', 'a'],
  ['a', 'c'],
  ['c', 'd'],
  ['x', 'y'],
]);

describe('neighbourhood', () => {
  it('walks both directions from the focus', () => {
    const hood = neighbourhood('a', board, { depth: 1, maxNodes: 100 });
    expect([...hood.ids].sort()).toEqual(['a', 'b', 'c']);
    expect(hood.directionOf.get('b')).toBe('upstream');
    expect(hood.directionOf.get('c')).toBe('downstream');
    expect(hood.directionOf.get('a')).toBe('focus');
  });

  it('reaches further as depth grows', () => {
    expect(neighbourhood('a', board, { depth: 2, maxNodes: 100 }).ids.sort()).toEqual([
      'a',
      'b',
      'c',
      'd',
    ]);
  });

  it('records the hop distance', () => {
    const hood = neighbourhood('a', board, { depth: 3, maxNodes: 100 });
    expect(hood.depthOf.get('a')).toBe(0);
    expect(hood.depthOf.get('c')).toBe(1);
    expect(hood.depthOf.get('d')).toBe(2);
  });

  // An island is not somebody else's business. A neighbourhood that
  // reached it would be answering a different question.
  it('never leaves the focus component', () => {
    const hood = neighbourhood('a', board, { depth: 5, maxNodes: 100 });
    expect(hood.ids).not.toContain('x');
    expect(hood.ids).not.toContain('y');
  });

  it('says when a deeper walk would reveal more', () => {
    expect(neighbourhood('a', board, { depth: 1, maxNodes: 100 }).hasMoreDepth).toBe(true);
    expect(neighbourhood('a', board, { depth: 4, maxNodes: 100 }).hasMoreDepth).toBe(false);
  });

  // Truncation drops the furthest first. Cutting an arbitrary slice
  // would leave holes in the middle of the answer, which reads as wrong
  // rather than partial.
  it('keeps the nearest nodes when the cap bites', () => {
    const hood = neighbourhood('a', board, { depth: 5, maxNodes: 2 });
    expect(hood.ids).toHaveLength(2);
    expect(hood.ids[0]).toBe('a');
    expect(hood.truncated).toBe(true);
    expect(hood.reachable).toBe(4);
    // Anything cut is cut from the maps too, so a renderer cannot read a
    // depth for a node it was never given.
    expect(hood.depthOf.size).toBe(2);
    expect(hood.directionOf.size).toBe(2);
  });

  it('reports the full reachable count even when it truncates', () => {
    const hood = neighbourhood('a', board, { depth: 5, maxNodes: 1 });
    expect(hood.ids).toEqual(['a']);
    expect(hood.reachable).toBe(4);
  });

  // A cut that moved between renders would look like data loss.
  it('truncates deterministically', () => {
    const wide = edges([
      ['a', 'z'],
      ['a', 'm'],
      ['a', 'b'],
      ['a', 'q'],
    ]);
    const first = neighbourhood('a', wide, { depth: 1, maxNodes: 3 });
    const second = neighbourhood('a', wide, { depth: 1, maxNodes: 3 });
    expect(first.ids).toEqual(second.ids);
    expect(first.ids).toEqual(['a', 'b', 'm']);
  });

  it('marks a node reachable from both sides', () => {
    const diamond = edges([
      ['a', 'b'],
      ['b', 'a'],
    ]);
    const hood = neighbourhood('a', diamond, { depth: 1, maxNodes: 10 });
    expect(hood.directionOf.get('b')).toBe('both');
  });

  it('handles a focus with no edges at all', () => {
    const hood = neighbourhood('lonely', board, { depth: 3, maxNodes: 10 });
    expect(hood.ids).toEqual(['lonely']);
    expect(hood.truncated).toBe(false);
    expect(hood.hasMoreDepth).toBe(false);
  });

  it('does not loop on a cycle', () => {
    const ring = edges([
      ['a', 'b'],
      ['b', 'c'],
      ['c', 'a'],
    ]);
    const hood = neighbourhood('a', ring, { depth: 5, maxNodes: 100 });
    expect(hood.ids.sort()).toEqual(['a', 'b', 'c']);
  });
});
