import { describe, expect, it } from 'vitest';

import type { WorkItem } from '@/types/core.gen';
import {
  DEFAULT_BUDGET,
  MAX_DEPTH,
  buildGraphModel,
  clampDepth,
  drawnSignature,
  parseMode,
  resolveMode,
} from '../graphModel';
import { aggregateClusters } from '../aggregate';

function item(
  id: string,
  over: { repo?: string; source?: string; project?: string; readiness?: string } = {},
  rels: { kind: 'blocks' | 'parent_child' | 'relates_to'; from: string; to: string }[] = []
): WorkItem {
  return {
    id,
    kind: 'task',
    title: `title ${id}`,
    status: 'Todo',
    state_category: 'backlog',
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    relationships: rels,
    custom: {
      atab_source: over.source ?? 'atab-group',
      atab_repo: over.repo ?? 'Atab-Group/One',
      atab_project_title: over.project ?? 'Atab Group Tasks',
      atab_readiness: over.readiness ?? 'ready',
    },
  } as unknown as WorkItem;
}

/** chain builds n items in a single blocks chain, plus m islands. */
function chain(n: number, islands = 0, repo = 'Atab-Group/One'): WorkItem[] {
  const ids = Array.from({ length: n }, (_, i) => `c${String(i).padStart(4, '0')}`);
  const out = ids.map((id, i) =>
    item(
      id,
      { repo },
      i === 0 ? [] : [{ kind: 'blocks', from: ids[i - 1], to: id }]
    )
  );
  for (let i = 0; i < islands; i++) {
    out.push(item(`island${String(i).padStart(4, '0')}`, { repo }));
  }
  return out;
}

describe('resolveMode', () => {
  // The default on an unfiltered company board is the overview, never
  // the hairball. That is the whole behavioural change.
  it('defaults a large unfocused board to the overview', () => {
    expect(resolveMode(null, null, 2790)).toBe('overview');
  });

  it('defaults a small board to scope', () => {
    expect(resolveMode(null, null, 42)).toBe('scope');
  });

  it('prefers focus whenever an item is focused', () => {
    expect(resolveMode(null, 'a', 2790)).toBe('focus');
  });

  it('honours an explicit mode over any default', () => {
    expect(resolveMode('scope', null, 999999)).toBe('scope');
    expect(resolveMode('overview', 'a', 3)).toBe('overview');
  });
});

describe('parseMode and clampDepth', () => {
  it('accepts only the three modes', () => {
    expect(parseMode('focus')).toBe('focus');
    expect(parseMode('overview')).toBe('overview');
    expect(parseMode('hairball')).toBeNull();
    expect(parseMode(null)).toBeNull();
  });

  it('clamps a depth from the URL into range', () => {
    expect(clampDepth('3')).toBe(3);
    expect(clampDepth('0')).toBe(1);
    expect(clampDepth('99')).toBe(MAX_DEPTH);
    expect(clampDepth('deep')).toBe(2);
    expect(clampDepth(null)).toBe(2);
  });
});

describe('render budgets', () => {
  // The hard cap. Whatever the mode, the counts handed onward are
  // bounded, because the render this page could not survive is the one
  // that happens before anyone gets to decide it was too big.
  it('bounds nodes and edges in scope mode', () => {
    const items = chain(3000);
    const model = buildGraphModel({
      items,
      manifest: null,
      mode: 'scope',
      focusId: null,
      depth: 2,
    });
    expect(model.nodes.length).toBe(DEFAULT_BUDGET.maxNodes);
    expect(model.edges.length).toBeLessThanOrEqual(DEFAULT_BUDGET.maxEdges);
    expect(model.availableNodes).toBe(3000);
    expect(model.nodesTruncated).toBe(true);
  });

  it('bounds edges when density is the problem rather than node count', () => {
    // 200 nodes, every one blocking every fifth other: 200 * 40 edges.
    const ids = Array.from({ length: 200 }, (_, i) => `d${String(i).padStart(3, '0')}`);
    const items = ids.map((id, i) =>
      item(
        id,
        {},
        ids
          .filter((_, j) => j % 5 === i % 5 && j !== i)
          .map((other) => ({ kind: 'blocks' as const, from: id, to: other }))
      )
    );
    const model = buildGraphModel({
      items,
      manifest: null,
      mode: 'scope',
      focusId: null,
      depth: 2,
    });
    expect(model.nodes.length).toBeLessThanOrEqual(DEFAULT_BUDGET.maxNodes);
    expect(model.edges.length).toBeLessThanOrEqual(DEFAULT_BUDGET.maxEdges);
    expect(model.edgesTruncated).toBe(true);
    expect(model.droppedEdges).toBeGreaterThan(0);
  });

  // A canvas whose budget went on disconnected cards shows a field of
  // dots and no dependencies, which is the least useful thing a
  // dependency graph can draw.
  it('spends the scope budget on connected items first', () => {
    const items = chain(50, 500);
    const model = buildGraphModel({
      items,
      manifest: null,
      mode: 'scope',
      focusId: null,
      depth: 2,
      budget: { maxNodes: 50, maxEdges: 600 },
    });
    expect(model.nodes).toHaveLength(50);
    expect(model.nodes.every((n) => n.id.startsWith('c'))).toBe(true);
  });

  it('never emits an edge whose endpoints are not both drawn', () => {
    const model = buildGraphModel({
      items: chain(3000),
      manifest: null,
      mode: 'scope',
      focusId: null,
      depth: 2,
    });
    const drawn = new Set(model.nodes.map((n) => n.id));
    for (const e of model.edges) {
      expect(drawn.has(e.from)).toBe(true);
      expect(drawn.has(e.to)).toBe(true);
    }
    for (const e of model.structuralEdges) {
      expect(drawn.has(e.from)).toBe(true);
      expect(drawn.has(e.to)).toBe(true);
    }
  });

  it('bounds the focus neighbourhood', () => {
    const items = chain(3000);
    const model = buildGraphModel({
      items,
      manifest: null,
      mode: 'focus',
      focusId: 'c1500',
      depth: MAX_DEPTH,
      budget: { maxNodes: 5, maxEdges: 600 },
    });
    expect(model.nodes.length).toBeLessThanOrEqual(5);
    expect(model.nodesTruncated).toBe(true);
    expect(model.nodes[0].id).toBe('c1500');
    expect(model.nodes[0].isFocus).toBe(true);
  });
});

describe('focus mode', () => {
  it('draws the focus and its bounded neighbourhood', () => {
    const model = buildGraphModel({
      items: chain(100),
      manifest: null,
      mode: 'focus',
      focusId: 'c0050',
      depth: 2,
    });
    expect(model.nodes.map((n) => n.id).sort()).toEqual([
      'c0048',
      'c0049',
      'c0050',
      'c0051',
      'c0052',
    ]);
    expect(model.canExpandDepth).toBe(true);
    expect(model.totalItems).toBe(100);
  });

  it('records which side of the focus each node sits on', () => {
    const model = buildGraphModel({
      items: chain(100),
      manifest: null,
      mode: 'focus',
      focusId: 'c0050',
      depth: 1,
    });
    const byId = new Map(model.nodes.map((n) => [n.id, n]));
    expect(byId.get('c0049')!.direction).toBe('upstream');
    expect(byId.get('c0051')!.direction).toBe('downstream');
    expect(byId.get('c0050')!.hops).toBe(0);
  });

  // A focus the filters exclude is reported, not silently reset.
  // Falling back to the whole board would answer a question nobody asked
  // and hide that the filter did it.
  it('reports a focus the filters removed', () => {
    const model = buildGraphModel({
      items: chain(10),
      manifest: null,
      mode: 'focus',
      focusId: 'not-here',
      depth: 2,
    });
    expect(model.focusMissing).toBe(true);
    expect(model.nodes).toHaveLength(0);
  });

  it('stops offering depth once the walk reaches the end', () => {
    const model = buildGraphModel({
      items: chain(3),
      manifest: null,
      mode: 'focus',
      focusId: 'c0001',
      depth: 3,
    });
    expect(model.canExpandDepth).toBe(false);
  });
});

describe('overview mode', () => {
  it('draws clusters rather than items, at any board size', () => {
    const items = [
      ...chain(1500, 0, 'Atab-Group/One'),
      ...chain(1500, 0, 'Atab-Group/Two').map((it) => ({ ...it, id: `t${it.id}` })),
    ];
    const model = buildGraphModel({
      items,
      manifest: null,
      mode: 'overview',
      focusId: null,
      depth: 2,
    });
    expect(model.nodes.every((n) => n.shape === 'cluster')).toBe(true);
    expect(model.nodes.length).toBeLessThanOrEqual(DEFAULT_BUDGET.maxNodes);
    expect(model.totalItems).toBe(3000);
    const total = model.nodes.reduce((sum, n) => sum + (n.count ?? 0), 0);
    expect(total).toBe(3000);
  });
});

describe('aggregateClusters', () => {
  it('groups by repository and counts members', () => {
    const items = [
      item('a', { repo: 'Atab-Group/One' }),
      item('b', { repo: 'Atab-Group/One' }),
      item('c', { repo: 'Atab-Group/Two', readiness: 'blocked' }),
    ];
    const agg = aggregateClusters(items, []);
    expect(agg.clusters).toHaveLength(2);
    expect(agg.clusters[0].count).toBe(2);
    expect(agg.clusters[1].blocked).toBe(1);
    expect(agg.clusters[0].label).toBe('One');
  });

  // Two orgs owning a repository of the same name are two groups. This
  // is the same collision-safety the work item ids carry, and merging
  // them would put one source's counts inside another's tile.
  it('keeps same-named repositories in different sources apart', () => {
    const agg = aggregateClusters(
      [
        item('a', { repo: 'x/platform', source: 'atab-group' }),
        item('b', { repo: 'y/platform', source: 'hadedahealth' }),
      ],
      []
    );
    expect(agg.clusters).toHaveLength(2);
    expect(new Set(agg.clusters.map((c) => c.id)).size).toBe(2);
  });

  it('merges cross-group edges into one weighted arrow', () => {
    const items = [
      item('a1', { repo: 'Atab-Group/One' }, [{ kind: 'blocks', from: 'a1', to: 'b1' }]),
      item('a2', { repo: 'Atab-Group/One' }, [{ kind: 'blocks', from: 'a2', to: 'b1' }]),
      item('b1', { repo: 'Atab-Group/Two' }),
    ];
    const agg = aggregateClusters(items, [
      { from: 'a1', to: 'b1' },
      { from: 'a2', to: 'b1' },
    ]);
    expect(agg.edges).toHaveLength(1);
    expect(agg.edges[0].weight).toBe(2);
  });

  // An overview showing a group's internal dependencies would be the
  // hairball again, one level up, so they are counted instead.
  it('counts rather than draws an edge inside a group', () => {
    const agg = aggregateClusters(
      [item('a', { repo: 'Atab-Group/One' }), item('b', { repo: 'Atab-Group/One' })],
      [{ from: 'a', to: 'b' }]
    );
    expect(agg.edges).toHaveLength(0);
    expect(agg.clusters[0].internalEdges).toBe(1);
  });

  it('is deterministic', () => {
    const items = [
      item('a', { repo: 'Atab-Group/Zed' }),
      item('b', { repo: 'Atab-Group/Alpha' }),
    ];
    expect(aggregateClusters(items, []).clusters.map((c) => c.id)).toEqual(
      aggregateClusters(items, []).clusters.map((c) => c.id)
    );
    expect(aggregateClusters(items, []).clusters[0].label).toBe('Alpha');
  });

  it('still draws one group for an adaptor that labels nothing', () => {
    const bare = {
      id: 'x',
      kind: 'task',
      title: 'x',
      status: 'open',
      state_category: 'backlog',
    } as unknown as WorkItem;
    const agg = aggregateClusters([bare], []);
    expect(agg.clusters).toHaveLength(1);
    expect(agg.clusters[0].label).toBe('All work');
  });
});

describe('drawnSignature', () => {
  // The camera refits when this changes, so it has to change whenever
  // the picture does. Counting alone missed a filter swap that left the
  // same number of items, which is an everyday move.
  it('differs for two different sets of the same size', () => {
    expect(drawnSignature(['a', 'b', 'c'], 5)).not.toBe(drawnSignature(['a', 'b', 'd'], 5));
  });

  // A resize reflows the layout without changing a node, and the camera
  // has to follow it.
  it('differs when only the column count changed', () => {
    expect(drawnSignature(['a', 'b'], 3)).not.toBe(drawnSignature(['a', 'b'], 8));
  });

  it('is stable for the same input', () => {
    expect(drawnSignature(['a', 'b'], 4)).toBe(drawnSignature(['a', 'b'], 4));
  });

  // Order is part of the picture: the layout sorts within a layer, so a
  // different order is a different arrangement.
  it('differs when the order changed', () => {
    expect(drawnSignature(['a', 'b'], 4)).not.toBe(drawnSignature(['b', 'a'], 4));
  });

  // Without a separator the hash would collide across a boundary, and a
  // collision here is a canvas that silently stops refitting.
  it('does not collide across an id boundary', () => {
    expect(drawnSignature(['ab', 'c'], 4)).not.toBe(drawnSignature(['a', 'bc'], 4));
  });

  it('handles an empty set', () => {
    expect(drawnSignature([], 4)).toBe(drawnSignature([], 4));
    expect(drawnSignature([], 4)).not.toBe(drawnSignature(['a'], 4));
  });
});
