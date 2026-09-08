// The graph model: what gets drawn, decided before React Flow exists.
//
// Every bound in this file is applied to plain data. That is the point.
// The page used to build a full canvas, mount it, fit the camera, read
// the resulting zoom, and only then decide the view was too dense to
// read and re-aggregate. So the expensive render always happened, once
// per load, at full company scale; and because the decision fed off the
// camera it fed back into itself. Deciding here means the oversized
// render never happens and no loop can form.
//
// Three modes, chosen explicitly and carried in the URL:
//
//   focus     one item plus its bounded N-hop neighbourhood. The
//             question a person almost always arrived with.
//   scope     the current filters, drawn as items, while they fit the
//             budget.
//   overview  the whole board as clusters with weighted edges. Never
//             individual items, at any board size.
//
// A budget is a hard cap, not a hint: whatever the mode, the returned
// node and edge counts are bounded, and the shortfall is reported so
// the page can say "showing X of Y" rather than quietly lying about
// how much work exists.

import type { CapabilityManifest, StateCategory, WorkItem } from '@/types/core.gen';
import { aggregateClusters, type ClusterEdge } from './aggregate';
import { buildGraph, type BuildGraphResult, type GraphEdge } from './buildGraph';
import type { DirectedEdge } from './graphAnalysis';
import { neighbourhood, type NeighbourDirection } from './neighbourhood';

export type GraphMode = 'focus' | 'scope' | 'overview';

export const GRAPH_MODES: readonly GraphMode[] = ['focus', 'scope', 'overview'];

/**
 * Render budgets.
 *
 * MAX_NODES is where a layered canvas stops being readable rather than
 * where the browser stops coping: at 300 nodes the widest layer is
 * already wider than any screen, and past that an operator is panning
 * blind. The edge cap is separate because density, not node count, is
 * what turns a canvas into a smear.
 */
export const DEFAULT_MAX_NODES = 300;
export const DEFAULT_MAX_EDGES = 600;

/** Depth bounds for the focus walk. */
export const MIN_DEPTH = 1;
export const MAX_DEPTH = 5;
export const DEFAULT_DEPTH = 2;

export interface GraphBudget {
  maxNodes: number;
  maxEdges: number;
}

export const DEFAULT_BUDGET: GraphBudget = {
  maxNodes: DEFAULT_MAX_NODES,
  maxEdges: DEFAULT_MAX_EDGES,
};

/** GraphNodeModel is one drawn node, item or cluster. */
export interface GraphNodeModel {
  id: string;
  title: string;
  /** shape decides the renderer: a work item card or a cluster tile. */
  shape: 'item' | 'cluster';
  stateCategory?: StateCategory;
  /** subtitle is the repository or project a node belongs to. */
  subtitle?: string;
  /** count is the member count on a cluster, and absent on an item. */
  count?: number;
  /** blocked is how many members are blocked, on a cluster. */
  blocked?: number;
  /** hops is the distance from the focus, in focus mode. */
  hops?: number;
  /** direction says which side of the focus a node sits on. */
  direction?: NeighbourDirection;
  isFocus?: boolean;
}

export interface GraphEdgeModel extends DirectedEdge {
  kind: string;
  isExtension: boolean;
  /** weight is the number of item edges a cluster edge stands for. */
  weight?: number;
}

export interface GraphModel {
  mode: GraphMode;
  nodes: GraphNodeModel[];
  edges: GraphEdgeModel[];
  /** structuralEdges drives layering, cycles and the critical path. */
  structuralEdges: DirectedEdge[];
  /** totalItems is how many items the current filters left. */
  totalItems: number;
  /**
   * availableNodes is how many nodes this mode would draw with no
   * budget. The gap against nodes.length is the "showing X of Y".
   */
  availableNodes: number;
  /** nodesTruncated / edgesTruncated say a cap actually cut something. */
  nodesTruncated: boolean;
  edgesTruncated: boolean;
  /** droppedEdges is how many edges the edge cap removed. */
  droppedEdges: number;
  /** focusId echoes the focus actually used, or null. */
  focusId: string | null;
  /** focusMissing says the requested focus is not in the filtered set. */
  focusMissing: boolean;
  /** canExpandDepth says a deeper walk would reveal more. */
  canExpandDepth: boolean;
  /** declaredExtensionEdgeKinds and droppedUndeclared pass through. */
  declaredExtensionEdgeKinds: string[];
  droppedUndeclared: number;
}

export interface GraphModelInput {
  /** items are already narrowed by the page's filters. */
  items: WorkItem[];
  manifest: CapabilityManifest | null;
  mode: GraphMode;
  focusId: string | null;
  depth: number;
  budget?: GraphBudget;
}

function customString(item: WorkItem, key: string): string {
  const v = (item.custom ?? {})[key];
  return typeof v === 'string' ? v : '';
}

function subtitleOf(item: WorkItem): string | undefined {
  const repo = customString(item, 'atab_repo') || item.primary_repository_id || '';
  if (!repo) return undefined;
  const slash = repo.indexOf('/');
  return slash < 0 ? repo : repo.slice(slash + 1);
}

/**
 * resolveMode picks the mode when the URL does not name one.
 *
 * A focused item wins, because it is the most specific thing the
 * operator asked for. Otherwise the filtered set is drawn as items when
 * it fits the budget, and as an overview when it does not. The default
 * on an unfiltered company board is therefore the overview, never the
 * hairball.
 */
export function resolveMode(
  requested: GraphMode | null,
  focusId: string | null,
  itemCount: number,
  budget: GraphBudget = DEFAULT_BUDGET
): GraphMode {
  if (requested) return requested;
  if (focusId) return 'focus';
  return itemCount > budget.maxNodes ? 'overview' : 'scope';
}

/**
 * capEdges keeps only edges whose endpoints are both drawn, then bounds
 * the count.
 *
 * Structural edges are kept first. When a canvas has to lose edges, the
 * ones that carry ordering are the ones worth keeping: a `relates_to`
 * dropped costs a hint, and a `blocks` dropped costs the reason the
 * item is stuck.
 */
function capEdges(
  edges: GraphEdgeModel[],
  drawn: Set<string>,
  maxEdges: number
): { edges: GraphEdgeModel[]; dropped: number } {
  const usable = edges.filter((e) => drawn.has(e.from) && drawn.has(e.to));
  if (usable.length <= maxEdges) return { edges: usable, dropped: 0 };
  const structuralFirst = [...usable].sort((a, b) => {
    const rank = (e: GraphEdgeModel) =>
      e.kind === 'blocks' ? 0 : e.kind === 'parent_child' ? 1 : 2;
    const ra = rank(a);
    const rb = rank(b);
    if (ra !== rb) return ra - rb;
    if (a.from !== b.from) return a.from < b.from ? -1 : 1;
    return a.to < b.to ? -1 : a.to > b.to ? 1 : 0;
  });
  return { edges: structuralFirst.slice(0, maxEdges), dropped: usable.length - maxEdges };
}

/**
 * rankForScope orders items for the scope budget.
 *
 * Connected items come first. A canvas whose budget was spent on
 * disconnected cards shows a field of dots and no dependencies, which is
 * the least useful thing a dependency graph can draw. Within each group
 * the order is by id, so the cut is deterministic.
 */
function rankForScope(items: WorkItem[], edges: DirectedEdge[]): WorkItem[] {
  const connected = new Set<string>();
  for (const e of edges) {
    connected.add(e.from);
    connected.add(e.to);
  }
  return [...items].sort((a, b) => {
    const ca = connected.has(a.id) ? 0 : 1;
    const cb = connected.has(b.id) ? 0 : 1;
    if (ca !== cb) return ca - cb;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}

function itemNode(item: WorkItem): GraphNodeModel {
  return {
    id: item.id,
    title: item.title,
    shape: 'item',
    stateCategory: item.state_category,
    subtitle: subtitleOf(item),
  };
}

function clusterEdgeModel(e: ClusterEdge): GraphEdgeModel {
  return { from: e.from, to: e.to, kind: 'blocks', isExtension: false, weight: e.weight };
}

/**
 * buildGraphModel produces the bounded, drawable graph for one mode.
 *
 * Pure and memoisable: the same input returns the same shape, so the
 * page recomputes only when the filters, mode, focus or depth move, and
 * never on hover or pan.
 */
export function buildGraphModel(input: GraphModelInput): GraphModel {
  const budget = input.budget ?? DEFAULT_BUDGET;
  const { items, manifest } = input;
  const built: BuildGraphResult = buildGraph(items, manifest);

  const base = {
    totalItems: items.length,
    declaredExtensionEdgeKinds: built.declaredExtensionEdgeKinds,
    droppedUndeclared: built.droppedUndeclared,
  };

  if (input.mode === 'overview') {
    const agg = aggregateClusters(items, built.edges);
    const clusterNodes: GraphNodeModel[] = agg.clusters.map((c) => ({
      id: c.id,
      title: c.label,
      shape: 'cluster',
      subtitle: c.sublabel,
      count: c.count,
      blocked: c.blocked,
    }));
    const drawn = new Set(clusterNodes.map((n) => n.id));
    const nodes = clusterNodes.slice(0, budget.maxNodes);
    const kept = new Set(nodes.map((n) => n.id));
    const { edges, dropped } = capEdges(
      agg.edges.map(clusterEdgeModel).filter((e) => kept.has(e.from) && kept.has(e.to)),
      kept,
      budget.maxEdges
    );
    return {
      ...base,
      mode: 'overview',
      nodes,
      edges,
      structuralEdges: edges.map((e) => ({ from: e.from, to: e.to })),
      availableNodes: drawn.size,
      nodesTruncated: clusterNodes.length > nodes.length,
      edgesTruncated: dropped > 0,
      droppedEdges: dropped,
      focusId: null,
      focusMissing: false,
      canExpandDepth: false,
    };
  }

  const byId = new Map(items.map((it) => [it.id, it]));

  if (input.mode === 'focus') {
    const focusId = input.focusId;
    if (!focusId || !byId.has(focusId)) {
      return {
        ...base,
        mode: 'focus',
        nodes: [],
        edges: [],
        structuralEdges: [],
        availableNodes: 0,
        nodesTruncated: false,
        edgesTruncated: false,
        droppedEdges: 0,
        focusId: focusId ?? null,
        // A focus the filters exclude is reported rather than silently
        // reset. Dropping back to the whole board would answer a
        // question nobody asked and hide that the filter did it.
        focusMissing: Boolean(focusId),
        canExpandDepth: false,
      };
    }
    const hood = neighbourhood(focusId, built.structuralEdges, {
      depth: input.depth,
      maxNodes: budget.maxNodes,
    });
    const nodes: GraphNodeModel[] = hood.ids
      .map((id) => byId.get(id))
      .filter((it): it is WorkItem => Boolean(it))
      .map((it) => ({
        ...itemNode(it),
        hops: hood.depthOf.get(it.id),
        direction: hood.directionOf.get(it.id),
        isFocus: it.id === focusId,
      }));
    const kept = new Set(nodes.map((n) => n.id));
    const { edges, dropped } = capEdges(built.edges as GraphEdgeModel[], kept, budget.maxEdges);
    return {
      ...base,
      mode: 'focus',
      nodes,
      edges,
      structuralEdges: built.structuralEdges.filter(
        (e) => kept.has(e.from) && kept.has(e.to)
      ),
      availableNodes: hood.reachable,
      nodesTruncated: hood.truncated,
      edgesTruncated: dropped > 0,
      droppedEdges: dropped,
      focusId,
      focusMissing: false,
      canExpandDepth: hood.hasMoreDepth && input.depth < MAX_DEPTH,
    };
  }

  // scope
  const ranked = rankForScope(items, built.structuralEdges);
  const chosen = ranked.slice(0, budget.maxNodes);
  const kept = new Set(chosen.map((it) => it.id));
  const nodes = chosen.map(itemNode);
  const { edges, dropped } = capEdges(built.edges as GraphEdgeModel[], kept, budget.maxEdges);
  return {
    ...base,
    mode: 'scope',
    nodes,
    edges,
    structuralEdges: built.structuralEdges.filter((e) => kept.has(e.from) && kept.has(e.to)),
    availableNodes: items.length,
    nodesTruncated: items.length > chosen.length,
    edgesTruncated: dropped > 0,
    droppedEdges: dropped,
    focusId: input.focusId,
    focusMissing: false,
    canExpandDepth: false,
  };
}

/** clampDepth keeps a URL-supplied depth inside the supported range. */
export function clampDepth(raw: string | null): number {
  const n = Number.parseInt(raw ?? '', 10);
  if (!Number.isFinite(n)) return DEFAULT_DEPTH;
  return Math.min(MAX_DEPTH, Math.max(MIN_DEPTH, n));
}

/** parseMode reads a mode from the URL, or null when it names none. */
export function parseMode(raw: string | null): GraphMode | null {
  return GRAPH_MODES.includes(raw as GraphMode) ? (raw as GraphMode) : null;
}

export type { GraphEdge };
