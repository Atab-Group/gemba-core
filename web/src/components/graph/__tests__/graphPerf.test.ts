// Performance guards for the graph pipeline.
//
// These exist because the previous version shipped a comment claiming a
// thousand nodes laid out inside a frame, and nothing checked it. On the
// live board that claim was wrong by two orders of magnitude: the layout
// alone took 262ms and produced 4187 rows for 2790 nodes.
//
// The budgets below are deliberately loose. A tight number would fail on
// a loaded CI box and teach everyone to skip the file; these are set
// where a regression means an algorithm changed complexity class, not
// where a machine had a slow second.

import { describe, expect, it } from 'vitest';

import type { WorkItem } from '@/types/core.gen';
import { criticalPath, detectCycles } from '../graphAnalysis';
import { layoutLayered } from '../graphLayout';
import { buildGraphModel, DEFAULT_BUDGET } from '../graphModel';
import { condense } from '../scc';

// PIPELINE_BUDGET_MS covers model, cycles, critical path and layout for
// a graph larger than the live board.
const PIPELINE_BUDGET_MS = 400;
// LAYOUT_BUDGET_MS covers the layout alone on a heavily cyclic graph,
// which is the input that used to be quadratic.
const LAYOUT_BUDGET_MS = 150;

function synthItem(id: string, repo: string, rels: WorkItem['relationships']): WorkItem {
  return {
    id,
    kind: 'task',
    title: `synthetic ${id}`,
    status: 'Todo',
    state_category: 'backlog',
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    relationships: rels,
    custom: { atab_source: 'atab-group', atab_repo: repo, atab_readiness: 'ready' },
  } as unknown as WorkItem;
}

/**
 * denseBoard builds a graph past company scale: 3,200 items across 8
 * repositories, each item blocking three others and parented into an
 * epic, for roughly 12,000 edges.
 */
function denseBoard(n = 3200): WorkItem[] {
  const ids = Array.from({ length: n }, (_, i) => `n${String(i).padStart(5, '0')}`);
  return ids.map((id, i) => {
    const rels: NonNullable<WorkItem['relationships']> = [];
    for (const step of [1, 7, 53]) {
      const j = (i + step) % n;
      rels.push({ kind: 'blocks', from: id, to: ids[j] });
    }
    if (i % 40 !== 0) {
      rels.push({ kind: 'parent_child', from: ids[i - (i % 40)], to: id });
    }
    return synthItem(id, `Atab-Group/repo-${i % 8}`, rels);
  });
}

describe('graph pipeline at company scale', () => {
  const items = denseBoard();

  it('bounds every mode well under the budget', () => {
    for (const mode of ['overview', 'scope', 'focus'] as const) {
      const started = performance.now();
      const model = buildGraphModel({
        items,
        manifest: null,
        mode,
        focusId: 'n01600',
        depth: 2,
      });
      const ids = model.nodes.map((n) => n.id);
      detectCycles(ids, model.structuralEdges);
      criticalPath(ids, model.structuralEdges);
      const layout = layoutLayered(ids.map((id) => ({ id })), model.structuralEdges);
      const elapsed = performance.now() - started;

      expect(model.nodes.length).toBeLessThanOrEqual(DEFAULT_BUDGET.maxNodes);
      expect(model.edges.length).toBeLessThanOrEqual(DEFAULT_BUDGET.maxEdges);
      expect(layout.layers).toBeLessThanOrEqual(model.nodes.length + 1);
      expect(elapsed).toBeLessThan(PIPELINE_BUDGET_MS);
    }
  });

  // The specific hazard. The old layer assignment relaxed depths over
  // the raw graph and bailed only after V passes, so a cyclic graph cost
  // O(V*E). Doubling the input here roughly doubles the work when the
  // algorithm is linear and roughly quadruples it when it is not, so the
  // ratio is the assertion rather than any absolute number.
  it('lays out a heavily cyclic graph in linear-ish time', () => {
    const build = (n: number) => {
      const ids = Array.from({ length: n }, (_, i) => `c${String(i).padStart(5, '0')}`);
      const edges = ids.map((id, i) => ({ from: id, to: ids[(i + 1) % n] }));
      // Chords, so the whole thing is one enormous strongly-connected
      // component rather than a simple ring.
      for (let i = 0; i < n; i += 3) {
        edges.push({ from: ids[i], to: ids[(i + Math.floor(n / 2)) % n] });
      }
      return { nodes: ids.map((id) => ({ id })), edges };
    };

    const small = build(2000);
    const large = build(4000);

    const timeOf = (g: ReturnType<typeof build>) => {
      const started = performance.now();
      const layout = layoutLayered(g.nodes, g.edges);
      const elapsed = performance.now() - started;
      // One component, so one row. The old code produced thousands.
      expect(layout.layers).toBe(1);
      return elapsed;
    };

    // Warm the JIT so the ratio measures the algorithm and not the
    // first-call compile.
    timeOf(small);
    const tSmall = Math.max(timeOf(small), 0.5);
    const tLarge = timeOf(large);

    expect(tLarge).toBeLessThan(LAYOUT_BUDGET_MS);
    // Linear would be ~2x. Quadratic would be ~4x and would have been
    // ~40x on the real shape. 8x leaves generous headroom for a noisy
    // machine while still failing a complexity regression.
    expect(tLarge / tSmall).toBeLessThan(8);
  });

  it('condenses a large cyclic graph in one pass', () => {
    const n = 4000;
    const ids = Array.from({ length: n }, (_, i) => `s${String(i).padStart(5, '0')}`);
    const edges = ids.map((id, i) => ({ from: id, to: ids[(i + 1) % n] }));
    const started = performance.now();
    const cond = condense(ids, edges);
    const elapsed = performance.now() - started;
    expect(cond.components).toHaveLength(1);
    expect(cond.cyclic.size).toBe(1);
    expect(elapsed).toBeLessThan(LAYOUT_BUDGET_MS);
  });

  // A deep chain must not blow the JS stack, because a dependency chain
  // is exactly the shape this page draws.
  it('handles a 5,000 deep chain without recursing', () => {
    const n = 5000;
    const ids = Array.from({ length: n }, (_, i) => `d${String(i).padStart(5, '0')}`);
    const edges = ids.slice(0, -1).map((id, i) => ({ from: id, to: ids[i + 1] }));
    const layout = layoutLayered(ids.map((id) => ({ id })), edges);
    expect(layout.layers).toBe(n);
  });
});
