// GraphPage (gm-e12.16). Replaces the placeholder Graph route with a
// real React Flow surface that renders the WorkItem dependency graph.
//
// Three core edge kinds (blocks, parent_child, relates_to) always
// draw. Extension edges only appear when the bound WorkPlane manifest
// declares them — the `buildGraph` helper enforces this so a stale
// manifest can't leak adaptor-internal edges.
//
// Liveness: SSE invalidates ['beads'] on workitem.* events (gm-e12.2),
// so the graph rebuilds within a frame of any mutation. We don't
// stream incremental graph patches — at 1000 nodes the recompute is
// well under a frame budget thanks to the O(V+E) analysis passes.

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  Check,
  ChevronDown,
  Filter,
  Layers,
  Minus,
  Network,
  Plus,
  RotateCcw,
  Route as RouteIcon,
  Target,
  X,
} from 'lucide-react';
import { STATE_CATEGORIES, type StateCategory, type WorkItem } from '@/types/core.gen';
import ReactFlow, {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  MiniMap,
  Panel,
  type Edge,
  type Node,
  type ReactFlowInstance,
} from 'reactflow';
import 'reactflow/dist/style.css';
import { useWorkItems } from '@/hooks/useWorkItems';
import { useCapabilities } from '@/capabilities/context-internal';
import { useRhp } from '@/components/rhp/RhpContext';
import {
  MILESTONE_ALL,
  buildMilestoneOptions,
  filterByMilestone,
  type MilestoneID,
} from '@/components/board/milestone';
import {
  SCOPE_ALL,
  buildScopeOptions,
  filterByScope,
  type ScopeID,
} from '@/components/board/scope';
import {
  PROJECT_ALL,
  filterByProject,
  listProjectOptions,
  type ProjectID,
} from '@/components/board/project';
import {
  SOURCE_ALL,
  filterBySource,
  repoFilterID,
  type SourceID,
} from '@/components/board/source';
import { WorkItemNode, type WorkItemNodeData } from '@/components/graph/WorkItemNode';
import { criticalPath, detectCycles, edgeKey } from '@/components/graph/graphAnalysis';
import { columnsFor, layoutLayered } from '@/components/graph/graphLayout';
import {
  DEFAULT_BUDGET,
  MAX_DEPTH,
  MIN_DEPTH,
  buildGraphModel,
  clampDepth,
  drawnSignature,
  parseMode,
  resolveMode,
  type GraphMode,
} from '@/components/graph/graphModel';
import { useHotkey, useHotkeyScope } from '@/hotkeys';
import { cn } from '@/lib/utils';

// HISTORY_CAP bounds the focused-node back-stack so a long-running
// session can't accumulate unbounded predecessor breadcrumbs. 32 is
// well past any reasonable Gemba-walk traversal length.
const HISTORY_CAP = 32;

const NODE_TYPES = { workItem: WorkItemNode };

// EDGE_STYLE encodes the core-edge visual identity. relates_to renders
// horizontal + dashed because it doesn't imply ordering; blocks /
// parent_child use solid arrows. Extension edges fall through to a
// dotted style so they're visually distinct from anything core
// without making the canvas noisy.
const EDGE_STYLE: Record<string, { stroke: string; strokeDasharray?: string }> = {
  blocks: { stroke: '#dc2626' },
  parent_child: { stroke: '#0284c7' },
  relates_to: { stroke: '#737373', strokeDasharray: '4 3' },
};

const EXTENSION_EDGE_STYLE = { stroke: '#8b5cf6', strokeDasharray: '2 4' };

const HIGHLIGHT_CYCLE = '#dc2626';
const HIGHLIGHT_CRITICAL = '#f59e0b';

const NARROW_CANVAS_WIDTH = 420;
// CLUSTER_PREFIX marks an overview node. It has to match the id
// aggregate.ts mints, and it is the one place the page distinguishes a
// tile from an item.
const CLUSTER_PREFIX = 'cluster:';
// A minimap of a dozen tiles is chrome that costs a render pass and
// tells nobody anything. It earns its place once the canvas is big
// enough to get lost in.
const MINIMAP_MIN_NODES = 40;
// FIT_MAX_FRAMES bounds the wait for React Flow to accept a new node
// set. Two or three frames is the normal case; the cap is what stops a
// canvas that never populates from scheduling frames forever.
const FIT_MAX_FRAMES = 20;
const GRAPH_SEARCH_PARAM = 'q';

function statesFromQuery(p: URLSearchParams): StateCategory[] {
  const all = p.getAll('state_category');
  return all.filter((s): s is StateCategory => (STATE_CATEGORIES as readonly string[]).includes(s));
}

function kindsFromQuery(p: URLSearchParams): string[] {
  return p.getAll('kind').filter((s) => s.length > 0);
}

export function GraphPage() {
  const { data: items = [], isLoading, error } = useWorkItems();
  const [params, setParams] = useSearchParams();
  const { workPlane } = useCapabilities();
  const { popDetail, tabs } = useRhp();
  const [highlightCycles, setHighlightCycles] = useState(true);
  const [criticalMode, setCriticalMode] = useState(false);
  // gm-sfbh: selection-aware viewport. Track the ReactFlow instance
  // via onInit so click-to-focus and clear-to-fit can drive the
  // camera. The id of the currently focused node is also surfaced
  // on the canvas host as data-focused-node so specs can assert
  // the selection without inspecting the React Flow viewport state.
  const instanceRef = useRef<ReactFlowInstance | null>(null);
  const canvasHostRef = useRef<HTMLDivElement | null>(null);
  const [canvasWidth, setCanvasWidth] = useState(Number.POSITIVE_INFINITY);
  // gm-sfbh (post-RHP): the legacy WorkItemDrawer's onClose used to
  // clear focusedId + re-fit the camera in one step (Escape, ×, click-
  // outside all funneled through it). The drawer is gone — its content
  // lives on an RHP detail tab now — so we mirror the behavior by
  // watching the RHP's open tabs: when no `workitem` detail tab is
  // open anymore we drop focus and re-fit. Keeps Escape (Radix-
  // independent: the RHP closes the focused detail tab on the
  // drawer-close hotkey) and the × button consistent with the prior
  // contract.
  const hasWorkItemDetailTab = tabs.some((t) => t.kind === 'workitem');
  // gm-qdqu: hovered node + its one-hop neighbours light up via
  // data-hover-related on each affected node and edge. Hover state
  // is ephemeral — leaves the DOM as soon as the pointer moves off.
  const [hoveredId, setHoveredId] = useState<string | null>(null);
  // The view is carried in the URL, so a graph is linkable: mode, the
  // focused item, the depth of the walk and every filter. It also means
  // there is exactly one source of truth for what is drawn, which is
  // what removes the old zoom-reads-camera-writes-granularity loop.
  const project: ProjectID = params.get('project') ?? PROJECT_ALL;
  const source: SourceID = params.get('source') ?? SOURCE_ALL;
  const urlFocus: string | null = params.get('focus');
  const depth = clampDepth(params.get('depth'));
  const requestedMode = parseMode(params.get('graph'));
  const milestone: MilestoneID = params.get('milestone') ?? MILESTONE_ALL;
  const scope: ScopeID = params.get('scope') ?? SCOPE_ALL;
  const stateFilters = useMemo(() => statesFromQuery(params), [params]);
  const kindFilters = useMemo(() => kindsFromQuery(params), [params]);
  const search = params.get(GRAPH_SEARCH_PARAM) ?? '';

  // The functional form matters here, and it is not a style choice.
  // The RHP owns query keys of its own and writes them from a snapshot
  // it captured at render. Two writes in one tick from two components
  // that both hold the query string means the second silently discards
  // the first, which is how clicking a node used to open the detail tab
  // and lose the focus it had just set. Composing against the latest
  // value instead of a captured one removes the lost update.
  const updateParams = useCallback(
    (mutate: (next: URLSearchParams) => void) => {
      setParams(
        (current) => {
          const next = new URLSearchParams(current);
          mutate(next);
          return next;
        },
        { replace: true }
      );
    },
    [setParams]
  );
  // Focus is held in state and mirrored to the URL, rather than read
  // straight back out of it.
  //
  // Two components write this query string: the graph and the RHP, and a
  // node click asks both to write in the same tick. React Router's
  // setter resolves against the value captured at the last render, even
  // in its functional form, so whichever write lands second discards the
  // other's key. Clicking a node either opened the detail tab or set the
  // focus, depending on the order.
  //
  // Publishing from an effect instead moves the graph's write after the
  // commit, so it composes onto whatever the RHP just wrote. The ref
  // records what this page published, which is what keeps the two
  // directions from chasing each other: a URL change the page did not
  // make is adopted, and one it did make is ignored.
  const [focusedId, setFocusedIdState] = useState<string | null>(urlFocus);
  const publishedFocusRef = useRef<string | null>(urlFocus);

  useEffect(() => {
    if (urlFocus === publishedFocusRef.current) return;
    publishedFocusRef.current = urlFocus;
    setFocusedIdState(urlFocus);
  }, [urlFocus]);

  useEffect(() => {
    if (focusedId === publishedFocusRef.current) return;
    publishedFocusRef.current = focusedId;
    updateParams((p) => {
      if (focusedId) {
        p.set('focus', focusedId);
        p.set('graph', 'focus');
      } else {
        p.delete('focus');
        if (parseMode(p.get('graph')) === 'focus') p.delete('graph');
      }
    });
  }, [focusedId, updateParams]);

  const setFocusedId = useCallback((id: string | null) => {
    setFocusedIdState(id);
  }, []);

  const setProject = useCallback(
    (next: ProjectID) => {
      updateParams((p) => {
        if (next === PROJECT_ALL) p.delete('project');
        else p.set('project', next);
      });
    },
    [updateParams]
  );
  const setMilestone = useCallback(
    (nextMilestone: MilestoneID) => {
      updateParams((next) => {
        if (nextMilestone === MILESTONE_ALL) next.delete('milestone');
        else next.set('milestone', nextMilestone);
      });
    },
    [updateParams]
  );
  const setScope = useCallback(
    (nextScope: ScopeID) => {
      updateParams((next) => {
        if (nextScope === SCOPE_ALL) next.delete('scope');
        else next.set('scope', nextScope);
      });
    },
    [updateParams]
  );
  const setStateFilters = useCallback(
    (states: StateCategory[]) => {
      updateParams((next) => {
        next.delete('state_category');
        for (const state of states) next.append('state_category', state);
      });
    },
    [updateParams]
  );
  const setKindFilters = useCallback(
    (kinds: string[]) => {
      updateParams((next) => {
        next.delete('kind');
        for (const kind of kinds) next.append('kind', kind);
      });
    },
    [updateParams]
  );
  const setSearch = useCallback(
    (nextSearch: string) => {
      updateParams((next) => {
        if (nextSearch.trim()) next.set(GRAPH_SEARCH_PARAM, nextSearch);
        else next.delete(GRAPH_SEARCH_PARAM);
      });
    },
    [updateParams]
  );
  const clearFilters = useCallback(() => {
    updateParams((next) => {
      next.delete('project');
      next.delete('source');
      next.delete('milestone');
      next.delete('scope');
      next.delete('state_category');
      next.delete('kind');
      next.delete(GRAPH_SEARCH_PARAM);
    });
  }, [updateParams]);

  const filteredItems = useMemo(() => {
    // Project and source narrow first, and they narrow before the graph
    // is built rather than after. They are the filters that actually cut
    // a company board down to something drawable, and applying them at
    // the end would mean paying for the whole graph to throw most of it
    // away.
    let out = filterBySource(filterByProject(items, project), source);
    out = filterByScope(filterByMilestone(out, milestone), scope);
    if (stateFilters.length > 0) {
      const allowed = new Set(stateFilters);
      out = out.filter((it) => allowed.has(it.state_category));
    }
    if (kindFilters.length > 0) {
      const allowed = new Set(kindFilters);
      out = out.filter((it) => allowed.has(it.kind));
    }
    const needle = search.trim().toLowerCase();
    if (needle) {
      out = out.filter(
        (it) => it.id.toLowerCase().includes(needle) || it.title.toLowerCase().includes(needle)
      );
    }
    return out;
  }, [items, project, source, milestone, scope, stateFilters, kindFilters, search]);
  const filtersActive =
    project !== PROJECT_ALL ||
    source !== SOURCE_ALL ||
    milestone !== MILESTONE_ALL ||
    scope !== SCOPE_ALL ||
    stateFilters.length > 0 ||
    kindFilters.length > 0 ||
    search.trim().length > 0;
  const narrowCanvas = canvasWidth < NARROW_CANVAS_WIDTH;

  // The mode decides what is drawn, and it is decided here on plain
  // data rather than after mounting a canvas and measuring its zoom.
  // That ordering is the fix: the old page rendered every filtered item,
  // fitted the camera, read the resulting zoom and only then concluded
  // the view was too dense, so the expensive render always happened and
  // the decision fed back into the thing that produced it.
  // resolveMode reads the focus state rather than the URL so the view
  // flips on the click, not one render later when the mirror lands.
  const mode = useMemo(
    () => resolveMode(focusedId ? null : requestedMode, focusedId, filteredItems.length),
    [requestedMode, focusedId, filteredItems.length]
  );

  const setMode = useCallback(
    (next: GraphMode) => {
      updateParams((p) => {
        p.set('graph', next);
        if (next !== 'focus') p.delete('focus');
      });
    },
    [updateParams]
  );

  const setDepth = useCallback(
    (next: number) => {
      updateParams((p) => {
        p.set('depth', String(Math.min(MAX_DEPTH, Math.max(MIN_DEPTH, next))));
      });
    },
    [updateParams]
  );

  // A narrow canvas gets a tighter budget for the same reason a wide one
  // gets a loose one: the cap exists to keep nodes legible, and fewer
  // pixels means fewer legible nodes.
  const budget = useMemo(
    () =>
      narrowCanvas
        ? { maxNodes: 60, maxEdges: 120 }
        : DEFAULT_BUDGET,
    [narrowCanvas]
  );

  // Manifest projection — the in-app type drops a couple of optional
  // fields the codegen carries, so we pass it through `as` instead of
  // exporting yet another shape. buildGraph only reads adaptor_name +
  // edge_extensions, both of which are present on both shapes.
  const manifest = useMemo(
    () =>
      workPlane
        ? // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (workPlane as any)
        : null,
    [workPlane]
  );

  // One memo produces everything drawn. Hover and pan do not touch its
  // inputs, so neither recomputes it.
  const model = useMemo(
    () =>
      buildGraphModel({
        items: filteredItems,
        manifest,
        mode,
        focusId: focusedId,
        depth,
        budget,
      }),
    [filteredItems, manifest, mode, focusedId, depth, budget]
  );

  // A cluster id is "cluster:<source>/<owner>/<repo>", which is exactly
  // the pair of filters that narrows the board to that tile's contents.
  // Drilling in sets them and drops to items, so the overview is a way
  // into the graph rather than a dead end.
  const drillIntoCluster = useCallback(
    (clusterId: string) => {
      const rest = clusterId.slice(CLUSTER_PREFIX.length);
      const slash = rest.indexOf('/');
      if (slash < 0) return;
      const clusterSource = rest.slice(0, slash);
      const repo = rest.slice(slash + 1);
      updateParams((p) => {
        p.set('graph', 'scope');
        p.delete('focus');
        if (clusterSource && clusterSource !== 'repo' && clusterSource !== 'source') {
          p.set('source', repoFilterID(clusterSource, repo));
        }
      });
    },
    [updateParams]
  );

  const nodeIds = useMemo(() => model.nodes.map((n) => n.id), [model.nodes]);

  const cycles = useMemo(
    () => detectCycles(nodeIds, model.structuralEdges),
    [nodeIds, model.structuralEdges]
  );

  const critical = useMemo(
    () => criticalPath(nodeIds, model.structuralEdges),
    [nodeIds, model.structuralEdges]
  );

  // The column cap comes from the canvas, not the graph: the right
  // number of nodes side by side is however many fit at a zoom somebody
  // can read at. Ten tiles in one line fitted to 0.27 on a half-width
  // panel, which is a picture of nothing.
  const maxColumns = useMemo(() => columnsFor(canvasWidth), [canvasWidth]);
  const layout = useMemo(
    () => layoutLayered(nodeIds.map((id) => ({ id })), model.structuralEdges, { maxColumns }),
    [nodeIds, model.structuralEdges, maxColumns]
  );

  const focusOnNode = useCallback((id: string) => {
    const inst = instanceRef.current;
    if (!inst) return;
    const node = inst.getNode(id);
    if (!node) return;
    // setCenter takes the node's position centre; node.position is
    // the top-left, so add half the rendered size when known.
    // Synchronous (no `duration`) — animations would keep React Flow
    // running into Playwright's teardown window and time out the
    // worker. Visual smoothness is a separate polish bead.
    const w = node.width ?? 200;
    const h = node.height ?? 60;
    const cx = node.position.x + w / 2;
    const cy = node.position.y + h / 2;
    void inst.setCenter(cx, cy, { zoom: 1.25 });
  }, []);

  const fitAll = useCallback(() => {
    const inst = instanceRef.current;
    if (!inst) return;
    void inst.fitView();
  }, []);

  useEffect(() => {
    const el = canvasHostRef.current;
    if (!el) return;
    const readWidth = () => {
      const next = el.clientWidth || el.getBoundingClientRect().width;
      setCanvasWidth(next || Number.POSITIVE_INFINITY);
    };
    readWidth();
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', readWidth);
      return () => window.removeEventListener('resize', readWidth);
    }
    const observer = new ResizeObserver(readWidth);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  // One fit, on one signal.
  //
  // There used to be four overlapping effects here, one of which read
  // the camera's zoom back into the granularity decision that changed
  // the node set that triggered the fit. Fitting is now keyed on a
  // signature of what is drawn, so it happens exactly when the drawn
  // set changes and never in response to the operator's own pan or
  // zoom. Nothing reads the camera at all.
  const fitOverview = useCallback(() => {
    const inst = instanceRef.current;
    if (!inst) return;
    void inst.fitView({ padding: 0.15, maxZoom: 1.2 });
  }, []);

  // The signature covers what is drawn, not how much of it. Keying on
  // the count alone missed two everyday cases: a filter change that
  // happens to leave the same number of items, and a resize, which
  // reflows the layout into a different number of columns without
  // changing a node. Both left the camera framing a picture that had
  // moved out from under it.
  const viewSignature = useMemo(
    () => `${mode}|${focusedId ?? ''}|${depth}|${drawnSignature(nodeIds, maxColumns)}`,
    [mode, focusedId, depth, nodeIds, maxColumns]
  );
  const lastFitSignature = useRef('');
  useEffect(() => {
    const expected = nodeIds.length;
    if (expected === 0) return;
    if (lastFitSignature.current === viewSignature) return;

    // The fit has to wait for React Flow to have the nodes in its own
    // store, and one frame is not reliably enough: fitView against an
    // empty store is a silent no-op, which is how the camera ended up
    // parked at scale 1 on a canvas ten screens wide. So the attempt
    // repeats until the store agrees with what was handed to it, and
    // gives up after a few frames rather than spinning.
    let frame = 0;
    let handle = 0;
    const attempt = () => {
      const inst = instanceRef.current;
      // A renderer that cannot report its nodes is taken at its word and
      // fitted straight away; only a store that answers and disagrees is
      // worth waiting on.
      const ready =
        inst && (typeof inst.getNodes !== 'function' || inst.getNodes().length >= expected);
      if (ready) {
        lastFitSignature.current = viewSignature;
        fitOverview();
        return;
      }
      if (frame++ < FIT_MAX_FRAMES) handle = requestAnimationFrame(attempt);
    };
    handle = requestAnimationFrame(attempt);
    return () => cancelAnimationFrame(handle);
  }, [fitOverview, nodeIds.length, viewSignature]);

  // gm-sfbh (post-RHP migration): mirror the legacy onClose behavior —
  // when the workitem detail tab transitions from open to closed
  // (via Escape, ×, or any other path) clear the focused-node marker
  // and re-fit the camera so the operator returns to the at-a-glance
  // view in one keystroke. Tracking the previous state (rather than
  // "absence == close") avoids racing with the registry: if the
  // 'workitem' kind hasn't been registered yet, `tabs` may briefly
  // omit the workitem entry on first paint even though `popDetail`
  // queued it; we only clear focus on the open → closed edge.
  const prevHasWorkItemDetailTabRef = useRef(false);
  useEffect(() => {
    const prev = prevHasWorkItemDetailTabRef.current;
    prevHasWorkItemDetailTabRef.current = hasWorkItemDetailTab;
    if (!prev) return;
    if (hasWorkItemDetailTab) return;
    if (!focusedId) return;
    setFocusedId(null);
    fitAll();
  }, [focusedId, hasWorkItemDetailTab, fitAll, setFocusedId]);

  // gm-e12.20: traversal indices. successors/predecessors are derived
  // once per render from the structural-edge slice (the same set the
  // layered layout uses) so non-ordering kinds like relates_to and
  // extension edges never participate in keyboard stepping. A
  // single-outgoing successor enables ArrowRight; multi-outgoing
  // disables it (the operator falls back to clicking).
  const successors = useMemo(() => {
    const m = new Map<string, string[]>();
    for (const e of model.structuralEdges) {
      const list = m.get(e.from) ?? [];
      list.push(e.to);
      m.set(e.from, list);
    }
    for (const list of m.values()) list.sort();
    return m;
  }, [model.structuralEdges]);
  const predecessors = useMemo(() => {
    const m = new Map<string, string[]>();
    for (const e of model.structuralEdges) {
      const list = m.get(e.to) ?? [];
      list.push(e.from);
      m.set(e.to, list);
    }
    for (const list of m.values()) list.sort();
    return m;
  }, [model.structuralEdges]);

  // History stack of focused-node ids. Used by the back hotkey to
  // prefer the most-recently-visited predecessor when the current
  // node has more than one. Bounded to HISTORY_CAP entries so a
  // long Gemba walk doesn't accumulate without bound.
  const historyRef = useRef<string[]>([]);
  const recordVisit = useCallback((id: string) => {
    const h = historyRef.current;
    if (h[h.length - 1] === id) return;
    h.push(id);
    if (h.length > HISTORY_CAP) h.shift();
  }, []);

  const moveFocus = useCallback(
    (id: string) => {
      recordVisit(id);
      setFocusedId(id);
      focusOnNode(id);
    },
    [focusOnNode, recordVisit, setFocusedId]
  );

  // canStepNext mirrors the next hotkey's enable rule for the
  // toolbar button — the two surfaces share a single source of
  // truth so a button click can never fire when the keystroke is
  // a no-op (or vice versa).
  const nextTarget = useMemo(() => {
    if (!focusedId) return null;
    const out = successors.get(focusedId) ?? [];
    return out.length === 1 ? out[0] : null;
  }, [focusedId, successors]);
  const canStepNext = nextTarget !== null;

  const backTarget = useMemo(() => {
    if (!focusedId) return null;
    const preds = predecessors.get(focusedId) ?? [];
    if (preds.length === 0) return null;
    if (preds.length === 1) return preds[0];
    // Ambiguous: prefer the most recently visited predecessor. The
    // history walks newest-first; the first match wins. Fall back
    // to the smallest-id predecessor when no history match exists.
    const h = historyRef.current;
    for (let i = h.length - 1; i >= 0; i--) {
      if (preds.includes(h[i])) return h[i];
    }
    return preds[0];
  }, [focusedId, predecessors]);
  const canStepBack = backTarget !== null;

  const stepNext = useCallback(() => {
    if (nextTarget) moveFocus(nextTarget);
  }, [moveFocus, nextTarget]);
  const stepBack = useCallback(() => {
    if (backTarget) moveFocus(backTarget);
  }, [moveFocus, backTarget]);
  const openFocused = useCallback(() => {
    if (focusedId) popDetail({ kind: 'workitem', id: focusedId });
  }, [focusedId, popDetail]);

  // Push the graph scope while this page is mounted so the
  // ArrowLeft / ArrowRight / Enter bindings don't leak into Board /
  // Grid / other pages where those keys carry different semantics.
  useHotkeyScope('graph');
  useHotkey('graph-next', stepNext);
  useHotkey('graph-back', stepBack);
  useHotkey('graph-open', openFocused);

  // gm-qdqu: precompute the hover-related set so node + edge passes
  // share the same answer. The set is the hovered node plus every
  // direct neighbour through any structural or extension edge.
  const hoverRelated = useMemo(() => {
    if (!hoveredId) return null;
    const set = new Set<string>([hoveredId]);
    for (const e of model.edges) {
      if (e.from === hoveredId) set.add(e.to);
      if (e.to === hoveredId) set.add(e.from);
    }
    return set;
  }, [hoveredId, model.edges]);

  const nodes = useMemo<Node<WorkItemNodeData>[]>(() => {
    return model.nodes.map((n) => {
      const pos = layout.positions.get(n.id) ?? { x: 0, y: 0 };
      return {
        id: n.id,
        type: 'workItem',
        position: pos,
        data: {
          id: n.id,
          title: n.title,
          stateCategory: n.stateCategory,
          inCycle: highlightCycles && cycles.nodeIds.has(n.id),
          onCriticalPath: criticalMode && critical.nodeIds.has(n.id),
          hoverRelated: hoverRelated?.has(n.id) ?? false,
          shape: n.shape,
          subtitle: n.subtitle,
          count: n.count,
          blocked: n.blocked,
          hops: n.hops,
          direction: n.direction,
          isFocus: n.isFocus,
        },
      };
    });
  }, [
    model.nodes,
    layout.positions,
    cycles.nodeIds,
    critical.nodeIds,
    highlightCycles,
    criticalMode,
    hoverRelated,
  ]);

  const edges = useMemo<Edge[]>(() => {
    return model.edges.map((e) => {
      const baseStyle = e.isExtension
        ? EXTENSION_EDGE_STYLE
        : EDGE_STYLE[e.kind] ?? EDGE_STYLE.relates_to;
      const k = edgeKey(e);
      const inCycle = highlightCycles && cycles.edgeKeys.has(k);
      const onCriticalPath = criticalMode && critical.edgeKeys.has(k);
      const stroke = inCycle
        ? HIGHLIGHT_CYCLE
        : onCriticalPath
          ? HIGHLIGHT_CRITICAL
          : baseStyle.stroke;
      return {
        id: `${e.from}->${e.to}:${e.kind}`,
        source: e.from,
        target: e.to,
        markerEnd: { type: MarkerType.ArrowClosed, color: stroke },
        style: {
          stroke,
          strokeWidth: inCycle || onCriticalPath ? 2.5 : 1.25,
          strokeDasharray: baseStyle.strokeDasharray,
        },
        data: {
          kind: e.kind,
          isExtension: e.isExtension,
          inCycle,
          onCriticalPath,
        },
      };
    });
  }, [model.edges, cycles.edgeKeys, critical.edgeKeys, highlightCycles, criticalMode]);

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="graph-page">
      {/* The header wraps rather than overflowing. Without it the title
          block and the toolbar overlap on a narrow canvas, and the
          controls underneath the description are unclickable: the depth
          buttons were sitting under the paragraph at 1280px wide with
          the side panel open. */}
      <header className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2 border-b border-neutral-200 px-8 py-4 dark:border-neutral-800">
        <div className="min-w-0">
          <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
            <Network className="h-5 w-5" aria-hidden />
            Graph
          </h1>
          <p className="text-xs text-neutral-500 dark:text-neutral-400">
            Dependency graph across visible work. Click a node to drill in.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <button
            type="button"
            onClick={stepBack}
            disabled={!canStepBack}
            data-testid="graph-step-back"
            title="Step up to predecessor (ArrowUp)"
            className={cn(
              'inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 transition-colors',
              'border-neutral-300 bg-white text-neutral-700 hover:bg-neutral-100 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-300 dark:hover:bg-neutral-800',
              'disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-white dark:disabled:hover:bg-neutral-900'
            )}
          >
            <ArrowUp className="h-3.5 w-3.5" />
            <span>Up</span>
          </button>
          <button
            type="button"
            onClick={stepNext}
            disabled={!canStepNext}
            data-testid="graph-step-next"
            title={
              canStepNext
                ? 'Step down to next node (ArrowDown)'
                : focusedId
                  ? 'Multiple successors — click one to choose'
                  : 'Click a node to start traversal'
            }
            className={cn(
              'inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 transition-colors',
              'border-neutral-300 bg-white text-neutral-700 hover:bg-neutral-100 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-300 dark:hover:bg-neutral-800',
              'disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-white dark:disabled:hover:bg-neutral-900'
            )}
          >
            <span>Down</span>
            <ArrowDown className="h-3.5 w-3.5" />
          </button>
          <div className="mx-1 h-4 w-px bg-neutral-200 dark:bg-neutral-800" />
          <GraphFilterMenu
            items={items}
            filteredCount={filteredItems.length}
            project={project}
            onChangeProject={setProject}
            milestone={milestone}
            onChangeMilestone={setMilestone}
            scope={scope}
            onChangeScope={setScope}
            stateFilters={stateFilters}
            onChangeStateFilters={setStateFilters}
            kindFilters={kindFilters}
            onChangeKindFilters={setKindFilters}
            search={search}
            onChangeSearch={setSearch}
            filtersActive={filtersActive}
            onClear={clearFilters}
          />
          <ModeButton
            mode="focus"
            current={mode}
            onSelect={setMode}
            disabled={!focusedId}
            icon={<Target className="h-3.5 w-3.5" />}
            title={focusedId ? 'One item and its neighbourhood' : 'Click a node to focus one'}
          >
            Focus
          </ModeButton>
          <ModeButton
            mode="scope"
            current={mode}
            onSelect={setMode}
            icon={<Filter className="h-3.5 w-3.5" />}
            title="The current filters, drawn as items"
          >
            Scope
          </ModeButton>
          <ModeButton
            mode="overview"
            current={mode}
            onSelect={setMode}
            icon={<Layers className="h-3.5 w-3.5" />}
            title="The whole board, grouped by repository"
          >
            Overview
          </ModeButton>
          {mode === 'focus' && focusedId ? (
            <div
              className="ml-1 inline-flex items-center gap-1 rounded-md border border-neutral-300 px-1.5 py-1 dark:border-neutral-700"
              data-testid="graph-depth-control"
            >
              <span className="text-[10px] uppercase tracking-wide text-neutral-500">Depth</span>
              <button
                type="button"
                data-testid="graph-depth-less"
                aria-label="Show fewer hops"
                disabled={depth <= MIN_DEPTH}
                onClick={() => setDepth(depth - 1)}
                className="rounded p-0.5 hover:bg-neutral-100 disabled:opacity-30 dark:hover:bg-neutral-800"
              >
                <Minus className="h-3 w-3" />
              </button>
              <span data-testid="graph-depth-value" className="w-3 text-center tabular-nums">
                {depth}
              </span>
              <button
                type="button"
                data-testid="graph-depth-more"
                aria-label="Show more hops"
                disabled={depth >= MAX_DEPTH}
                onClick={() => setDepth(depth + 1)}
                className="rounded p-0.5 hover:bg-neutral-100 disabled:opacity-30 dark:hover:bg-neutral-800"
              >
                <Plus className="h-3 w-3" />
              </button>
            </div>
          ) : null}
          {focusedId ? (
            <button
              type="button"
              data-testid="graph-clear-focus"
              onClick={() => setFocusedId(null)}
              title="Clear the focused item"
              className="inline-flex items-center gap-1 rounded-md border border-neutral-300 px-2 py-1.5 text-neutral-600 hover:bg-neutral-100 dark:border-neutral-700 dark:text-neutral-300 dark:hover:bg-neutral-800"
            >
              <X className="h-3.5 w-3.5" />
              <span>Clear focus</span>
            </button>
          ) : null}
          <ToggleButton
            active={highlightCycles}
            onClick={() => setHighlightCycles((v) => !v)}
            testid="graph-toggle-cycles"
            icon={<AlertTriangle className="h-3.5 w-3.5" />}
            badge={cycles.sccs.length > 0 ? cycles.sccs.length : undefined}
          >
            Cycles
          </ToggleButton>
          <ToggleButton
            active={criticalMode}
            onClick={() => setCriticalMode((v) => !v)}
            testid="graph-toggle-critical"
            icon={<RouteIcon className="h-3.5 w-3.5" />}
            badge={critical.length > 0 ? critical.length : undefined}
          >
            Critical path
          </ToggleButton>
        </div>
      </header>

      <div
        ref={canvasHostRef}
        className="relative min-h-0 flex-1"
        data-testid="graph-canvas-host"
        data-focused-node={focusedId ?? undefined}
        data-graph-mode={mode}
        data-node-count={model.nodes.length}
        data-edge-count={model.edges.length}
        data-available-nodes={model.availableNodes}
      >
        {error ? (
          <div className="m-8 rounded-md bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950 dark:text-red-300">
            {error.message}
          </div>
        ) : isLoading ? (
          <div className="p-8 text-sm text-neutral-500">Loading…</div>
        ) : items.length === 0 ? (
          <div className="m-8 rounded-md border border-dashed border-neutral-300 p-8 text-center text-sm text-neutral-500 dark:border-neutral-700">
            No work items. The graph populates once the bound WorkPlane has
            something to draw.
          </div>
        ) : model.focusMissing ? (
          <div
            className="m-8 rounded-md border border-dashed border-amber-400 p-8 text-center text-sm text-amber-800 dark:border-amber-700 dark:text-amber-200"
            data-testid="graph-focus-missing"
          >
            <p>
              The focused item is not in the current filters, so there is nothing
              to draw around it.
            </p>
            <div className="mt-3 flex justify-center gap-2">
              <button
                type="button"
                data-testid="graph-focus-missing-clear-focus"
                onClick={() => setFocusedId(null)}
                className="rounded border border-neutral-300 bg-white px-3 py-1 text-xs text-neutral-700 hover:bg-neutral-100 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-200"
              >
                Clear focus
              </button>
              <button
                type="button"
                data-testid="graph-focus-missing-clear-filters"
                onClick={clearFilters}
                className="rounded border border-neutral-300 bg-white px-3 py-1 text-xs text-neutral-700 hover:bg-neutral-100 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-200"
              >
                Clear filters
              </button>
            </div>
          </div>
        ) : filteredItems.length === 0 ? (
          <div
            className="m-8 rounded-md border border-dashed border-neutral-300 p-8 text-center text-sm text-neutral-500 dark:border-neutral-700"
            data-testid="graph-filtered-empty"
          >
            <p>No graph nodes match the current filters.</p>
            <button
              type="button"
              data-testid="graph-filtered-empty-clear"
              onClick={clearFilters}
              className="mt-3 rounded border border-neutral-300 bg-white px-3 py-1 text-xs text-neutral-700 hover:bg-neutral-100 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-200 dark:hover:bg-neutral-800"
            >
              Clear filters
            </button>
          </div>
        ) : (
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={NODE_TYPES}
            nodesDraggable={false}
            nodesConnectable={false}
            elementsSelectable
            proOptions={{ hideAttribution: true }}
            onInit={(instance) => {
              instanceRef.current = instance;
            }}
            onNodeClick={(_evt, node) => {
              // A cluster is not an item and has no detail to open.
              // Clicking one drills into it: the source and repository
              // it stands for become the filters, and the mode drops to
              // items. That is the progressive expansion path from the
              // overview down to a graph a person can read.
              if (node.id.startsWith(CLUSTER_PREFIX)) {
                drillIntoCluster(node.id);
                return;
              }
              // gm-sfbh: pop the RHP workitem detail tab, then focus the
              // clicked node. The two surfaces are independent — the tab
              // can be closed via the rail ×; the focus persists until
              // the operator clears it.
              // The order is deliberate. Both writes touch the query
              // string in the same tick, and the graph's is the one that
              // composes onto the latest value, so it goes last.
              // gm-e12.20: route through moveFocus so the click also
              // appends to the back-history that ArrowLeft consults.
              popDetail({ kind: 'workitem', id: node.id });
              moveFocus(node.id);
            }}
            onNodeMouseEnter={(_evt, node) => setHoveredId(node.id)}
            onNodeMouseLeave={() => setHoveredId(null)}
            // 1000-node DoD: panOnScroll keeps the canvas responsive
            // when the graph is bigger than the viewport, and the
            // minimap gives the operator something to navigate by
            // without paying for a full layout pass per render.
            panOnScroll
            // 0.02 used to be necessary because the canvas could be a
            // third of a million pixels across and fitView would
            // otherwise frame a slice with nothing in it. The drawn set
            // is bounded now, so the floor can be a zoom a person can
            // actually read at. Anything below 0.2 is a grey smear.
            minZoom={0.2}
            maxZoom={2}
          >
            <Background variant={BackgroundVariant.Dots} gap={16} size={1} />
            <Controls showInteractive={false} />
            {/* A minimap of a dozen tiles is chrome that costs a render
                pass and tells nobody anything. It appears once the
                canvas is big enough to get lost in. */}
            {model.nodes.length > MINIMAP_MIN_NODES ? (
              <MiniMap
                pannable
                zoomable
                ariaLabel="Graph minimap"
                nodeStrokeWidth={2}
                nodeColor={(node) => {
                  const data = node.data as WorkItemNodeData | undefined;
                  if (data?.inCycle) return HIGHLIGHT_CYCLE;
                  if (data?.onCriticalPath) return HIGHLIGHT_CRITICAL;
                  return '#d4d4d4';
                }}
              />
            ) : null}
            <Panel position="top-left">
              <GraphScopeBanner
                mode={mode}
                drawn={model.nodes.length}
                available={model.availableNodes}
                totalItems={model.totalItems}
                nodesTruncated={model.nodesTruncated}
                droppedEdges={model.droppedEdges}
                canExpandDepth={model.canExpandDepth}
                edgeCount={model.edges.length}
                onExpandDepth={() => setDepth(depth + 1)}
                onOverview={() => setMode('overview')}
              />
            </Panel>
            {/* Bottom-right, because the layout grows down and to the
                right from the top-left origin, so a bottom-left legend
                sits on top of the first nodes it is meant to explain. */}
            <Panel position="bottom-right">
              <Legend
                cycles={cycles.sccs.length}
                criticalLength={critical.length}
                extensionEdgeKinds={model.declaredExtensionEdgeKinds}
                droppedUndeclared={model.droppedUndeclared}
              />
            </Panel>
          </ReactFlow>
        )}
      </div>

    </div>
  );
}

interface GraphFilterMenuProps {
  items: WorkItem[];
  filteredCount: number;
  project: ProjectID;
  onChangeProject: (next: ProjectID) => void;
  milestone: MilestoneID;
  onChangeMilestone: (next: MilestoneID) => void;
  scope: ScopeID;
  onChangeScope: (next: ScopeID) => void;
  stateFilters: StateCategory[];
  onChangeStateFilters: (next: StateCategory[]) => void;
  kindFilters: string[];
  onChangeKindFilters: (next: string[]) => void;
  search: string;
  onChangeSearch: (next: string) => void;
  filtersActive: boolean;
  onClear: () => void;
}

function GraphFilterMenu({
  items,
  filteredCount,
  project,
  onChangeProject,
  milestone,
  onChangeMilestone,
  scope,
  onChangeScope,
  stateFilters,
  onChangeStateFilters,
  kindFilters,
  onChangeKindFilters,
  search,
  onChangeSearch,
  filtersActive,
  onClear,
}: GraphFilterMenuProps) {
  const [open, setOpen] = useState(false);
  const milestones = buildMilestoneOptions(items);
  const scopes = buildScopeOptions(items);
  // The project axis matters more here than anywhere: it is the filter
  // that turns a company board into a graph small enough to draw.
  const projects = useMemo(() => listProjectOptions(items), [items]);
  const kinds = useMemo(() => {
    const set = new Set<string>();
    for (const item of items) set.add(item.kind);
    return Array.from(set).sort((a, b) => a.localeCompare(b));
  }, [items]);
  const activeCount =
    (project !== PROJECT_ALL ? 1 : 0) +
    (milestone !== MILESTONE_ALL ? 1 : 0) +
    (scope !== SCOPE_ALL ? 1 : 0) +
    stateFilters.length +
    kindFilters.length +
    (search.trim() ? 1 : 0);
  const toggleState = (state: StateCategory) => {
    const next = stateFilters.includes(state)
      ? stateFilters.filter((s) => s !== state)
      : [...stateFilters, state];
    onChangeStateFilters(next);
  };
  const toggleKind = (kind: string) => {
    const next = kindFilters.includes(kind)
      ? kindFilters.filter((k) => k !== kind)
      : [...kindFilters, kind];
    onChangeKindFilters(next);
  };

  return (
    <div className="relative">
      <button
        type="button"
        data-testid="graph-filter-menu-button"
        aria-haspopup="menu"
        aria-expanded={open}
        title="Filters"
        onClick={() => setOpen((cur) => !cur)}
        className={cn(
          'inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 transition-colors',
          open || filtersActive
            ? 'border-sky-700 bg-sky-50 text-sky-800 dark:border-sky-600 dark:bg-sky-950/50 dark:text-sky-100'
            : 'border-neutral-300 bg-white text-neutral-700 hover:bg-neutral-100 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-300 dark:hover:bg-neutral-800'
        )}
      >
        <Filter className="h-3.5 w-3.5" aria-hidden />
        <span>Filters</span>
        {activeCount > 0 ? (
          <span
            data-testid="graph-filter-menu-count"
            className="rounded-full bg-sky-700 px-1.5 text-[10px] font-medium leading-4 text-white dark:bg-sky-500 dark:text-sky-950"
          >
            {activeCount}
          </span>
        ) : null}
        <ChevronDown className="h-3 w-3" aria-hidden />
      </button>
      {open ? (
        <div
          data-testid="graph-filter-menu"
          role="menu"
          className={cn(
            'absolute right-0 z-30 mt-1 w-80 rounded-md border p-2 shadow-lg',
            'border-neutral-200 bg-white text-xs text-neutral-700',
            'dark:border-neutral-800 dark:bg-neutral-950 dark:text-neutral-200'
          )}
        >
          <GraphMenuSection title="Scope">
            {projects.length > 0 ? (
              <GraphSelect
                label="Project"
                testid="graph-filter-project"
                value={project}
                onChange={onChangeProject}
                options={projects.map((o) => ({
                  value: o.id,
                  label: o.kind === 'all' ? 'All projects' : `${o.label} (${o.count})`,
                }))}
              />
            ) : null}
            <GraphSelect
              label="Milestone"
              testid="graph-filter-milestone"
              value={milestone}
              onChange={onChangeMilestone}
              options={milestones.map((m) => ({ value: m.id, label: m.label }))}
            />
            <GraphSelect
              label="Epic"
              testid="graph-filter-scope"
              value={scope}
              onChange={onChangeScope}
              options={scopes.map((s) => ({
                value: s.id,
                label: `${s.depth === 1 ? '  ' : ''}${s.label}`,
              }))}
            />
          </GraphMenuSection>

          <GraphMenuSection title="Search">
            <input
              data-testid="graph-filter-search"
              value={search}
              onChange={(e) => onChangeSearch(e.target.value)}
              placeholder="Title or id"
              className="h-7 w-full rounded border border-neutral-300 bg-white px-2 text-xs text-neutral-700 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-200"
            />
          </GraphMenuSection>

          <GraphMenuSection title="State">
            <div className="grid grid-cols-2 gap-1">
              {STATE_CATEGORIES.map((state) => (
                <GraphCheckOption
                  key={state}
                  active={stateFilters.includes(state)}
                  onClick={() => toggleState(state)}
                  label={state}
                  testid={`graph-filter-state-${state}`}
                />
              ))}
            </div>
          </GraphMenuSection>

          <GraphMenuSection title="Kind">
            {kinds.length > 0 ? (
              <div className="grid grid-cols-2 gap-1">
                {kinds.map((kind) => (
                  <GraphCheckOption
                    key={kind}
                    active={kindFilters.includes(kind)}
                    onClick={() => toggleKind(kind)}
                    label={kind}
                    testid={`graph-filter-kind-${kind}`}
                  />
                ))}
              </div>
            ) : (
              <p className="px-2 py-1 text-[11px] text-neutral-500">No kinds available.</p>
            )}
          </GraphMenuSection>

          <div className="flex items-center justify-between px-2 pt-2 text-[11px] text-neutral-500">
            <span data-testid="graph-filter-visible-count">{filteredCount} visible</span>
            <button
              type="button"
              data-testid="graph-filter-clear"
              disabled={!filtersActive}
              onClick={onClear}
              className="inline-flex items-center gap-1 rounded px-1.5 py-1 text-neutral-600 hover:bg-neutral-100 disabled:cursor-not-allowed disabled:opacity-40 dark:text-neutral-300 dark:hover:bg-neutral-900"
            >
              <RotateCcw className="h-3 w-3" aria-hidden />
              Clear
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function GraphMenuSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="border-b border-neutral-100 py-2 last:border-0 dark:border-neutral-800">
      <h3 className="px-2 pb-1 text-[10px] font-semibold uppercase tracking-wide text-neutral-500">
        {title}
      </h3>
      <div className="space-y-1">{children}</div>
    </section>
  );
}

function GraphSelect({
  label,
  testid,
  value,
  onChange,
  options,
}: {
  label: string;
  testid: string;
  value: string;
  onChange: (next: string) => void;
  options: { value: string; label: string }[];
}) {
  return (
    <label className="flex items-center gap-2 px-2 text-neutral-500">
      <span className="w-16 shrink-0">{label}</span>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="h-7 min-w-0 flex-1 rounded border border-neutral-300 bg-white px-1.5 text-xs text-neutral-700 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-200"
        data-testid={testid}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    </label>
  );
}

function GraphCheckOption({
  active,
  onClick,
  label,
  testid,
}: {
  active: boolean;
  onClick: () => void;
  label: string;
  testid: string;
}) {
  return (
    <button
      type="button"
      role="menuitemcheckbox"
      aria-checked={active}
      data-testid={testid}
      data-active={active || undefined}
      onClick={onClick}
      className={cn(
        'flex items-center gap-1 rounded border px-2 py-1 text-left text-xs',
        active
          ? 'border-sky-700 bg-sky-700 text-white'
          : 'border-neutral-300 bg-white text-neutral-700 hover:bg-neutral-100 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-300 dark:hover:bg-neutral-800'
      )}
    >
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {active ? <Check className="h-3 w-3 shrink-0" aria-hidden /> : null}
    </button>
  );
}

interface GraphScopeBannerProps {
  mode: GraphMode;
  drawn: number;
  available: number;
  totalItems: number;
  edgeCount: number;
  nodesTruncated: boolean;
  droppedEdges: number;
  canExpandDepth: boolean;
  onExpandDepth: () => void;
  onOverview: () => void;
}

// GraphScopeBanner is the page saying what it is showing and what it is
// not.
//
// A budget that silently drops two thousand items is indistinguishable
// from a board that only has three hundred, and an operator who cannot
// tell those apart will make a decision on the wrong number. So the
// count is always on screen, and when something was cut the banner says
// so and offers the next move rather than leaving the reader to guess
// which control widens the view.
function GraphScopeBanner({
  mode,
  drawn,
  available,
  totalItems,
  edgeCount,
  nodesTruncated,
  droppedEdges,
  canExpandDepth,
  onExpandDepth,
  onOverview,
}: GraphScopeBannerProps) {
  const noun = mode === 'overview' ? 'groups' : 'items';
  return (
    <div
      data-testid="graph-scope-banner"
      data-drawn={drawn}
      data-available={available}
      data-truncated={nodesTruncated ? 'true' : undefined}
      className="rounded-md border border-neutral-300 bg-white/90 px-2.5 py-1.5 text-[11px] text-neutral-700 shadow-sm backdrop-blur dark:border-neutral-700 dark:bg-neutral-900/90 dark:text-neutral-300"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span data-testid="graph-scope-count" className="font-medium tabular-nums">
          {nodesTruncated
            ? `Showing ${drawn} of ${available} ${noun}`
            : `${drawn} ${noun}`}
        </span>
        <span className="text-neutral-500 tabular-nums">{edgeCount} edges</span>
        {mode === 'overview' ? (
          <span className="text-neutral-500 tabular-nums">
            grouping {totalItems} items
          </span>
        ) : null}
        {droppedEdges > 0 ? (
          <span
            data-testid="graph-scope-dropped-edges"
            className="text-amber-700 tabular-nums dark:text-amber-400"
          >
            {droppedEdges} edges hidden
          </span>
        ) : null}
      </div>
      {nodesTruncated || canExpandDepth ? (
        <div className="mt-1 flex flex-wrap items-center gap-2">
          {canExpandDepth ? (
            <button
              type="button"
              data-testid="graph-scope-expand"
              onClick={onExpandDepth}
              className="rounded border border-neutral-300 px-1.5 py-0.5 hover:bg-neutral-100 dark:border-neutral-700 dark:hover:bg-neutral-800"
            >
              Expand one hop
            </button>
          ) : null}
          {nodesTruncated && mode !== 'overview' ? (
            <button
              type="button"
              data-testid="graph-scope-overview"
              onClick={onOverview}
              className="rounded border border-neutral-300 px-1.5 py-0.5 hover:bg-neutral-100 dark:border-neutral-700 dark:hover:bg-neutral-800"
            >
              See all as groups
            </button>
          ) : null}
          {nodesTruncated ? (
            <span className="text-neutral-500">or narrow the filters</span>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

interface ModeButtonProps {
  mode: GraphMode;
  current: GraphMode;
  onSelect: (mode: GraphMode) => void;
  icon: ReactNode;
  title: string;
  disabled?: boolean;
  children: ReactNode;
}

// ModeButton is a radio, not a toggle. The three modes are exclusive
// and one is always on, so a button that could turn itself off would
// leave the canvas with nothing to draw.
function ModeButton({
  mode,
  current,
  onSelect,
  icon,
  title,
  disabled,
  children,
}: ModeButtonProps) {
  const active = mode === current;
  return (
    <button
      type="button"
      role="radio"
      aria-checked={active}
      disabled={disabled}
      title={title}
      data-testid={`graph-mode-${mode}`}
      data-active={active ? 'true' : undefined}
      onClick={() => onSelect(mode)}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 transition-colors',
        active
          ? 'border-cyan-500 bg-cyan-50 font-medium text-cyan-900 dark:bg-cyan-950 dark:text-cyan-100'
          : 'border-neutral-300 bg-white text-neutral-700 hover:bg-neutral-100 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-300 dark:hover:bg-neutral-800',
        'disabled:cursor-not-allowed disabled:opacity-40'
      )}
    >
      {icon}
      <span>{children}</span>
    </button>
  );
}

interface ToggleButtonProps {
  active: boolean;
  onClick: () => void;
  testid: string;
  icon: React.ReactNode;
  badge?: number;
  children: React.ReactNode;
}

function ToggleButton({
  active,
  onClick,
  testid,
  icon,
  badge,
  children,
}: ToggleButtonProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      data-testid={testid}
      data-active={active || undefined}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 transition-colors',
        active
          ? 'border-sky-700 bg-sky-700 text-white'
          : 'border-neutral-300 bg-white text-neutral-700 hover:bg-neutral-100 dark:border-neutral-700 dark:bg-neutral-900 dark:text-neutral-300 dark:hover:bg-neutral-800'
      )}
    >
      {icon}
      <span>{children}</span>
      {badge != null ? (
        <span
          className={cn(
            'rounded-full px-1.5 py-px font-mono text-[10px]',
            active ? 'bg-white/20' : 'bg-neutral-200 dark:bg-neutral-800'
          )}
        >
          {badge}
        </span>
      ) : null}
    </button>
  );
}

interface LegendProps {
  cycles: number;
  criticalLength: number;
  extensionEdgeKinds: string[];
  droppedUndeclared: number;
}

function Legend({ cycles, criticalLength, extensionEdgeKinds, droppedUndeclared }: LegendProps) {
  return (
    <div
      className="rounded-md border border-neutral-200 bg-white/95 px-3 py-2 text-[11px] shadow-sm backdrop-blur dark:border-neutral-700 dark:bg-neutral-900/95"
      data-testid="graph-legend"
    >
      <div className="mb-1 font-semibold uppercase tracking-wide text-neutral-500">
        Edges
      </div>
      <LegendRow color="#dc2626">blocks</LegendRow>
      <LegendRow color="#0284c7">parent_child</LegendRow>
      <LegendRow color="#737373" dashed>
        relates_to
      </LegendRow>
      {extensionEdgeKinds.length > 0 ? (
        <>
          <div className="mt-1 font-semibold uppercase tracking-wide text-neutral-500">
            Extension
          </div>
          {extensionEdgeKinds.map((k) => (
            <LegendRow key={k} color="#8b5cf6" dashed>
              {k}
            </LegendRow>
          ))}
        </>
      ) : null}
      {(cycles > 0 || criticalLength > 0 || droppedUndeclared > 0) && (
        <div className="mt-2 border-t border-neutral-200 pt-1 dark:border-neutral-700">
          {cycles > 0 ? (
            <div className="text-rose-700 dark:text-rose-400" data-testid="graph-legend-cycles">
              {cycles} cycle{cycles === 1 ? '' : 's'}
            </div>
          ) : null}
          {criticalLength > 0 ? (
            <div
              className="text-amber-700 dark:text-amber-400"
              data-testid="graph-legend-critical"
            >
              critical chain: {criticalLength} hops
            </div>
          ) : null}
          {droppedUndeclared > 0 ? (
            <div
              className="text-neutral-500"
              title="Adaptor surfaced edges its manifest does not declare; not drawn."
              data-testid="graph-legend-dropped"
            >
              {droppedUndeclared} edge{droppedUndeclared === 1 ? '' : 's'} hidden
            </div>
          ) : null}
        </div>
      )}
    </div>
  );
}

function LegendRow({
  color,
  dashed,
  children,
}: {
  color: string;
  dashed?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-2">
      <span
        className="inline-block h-0.5 w-6 shrink-0"
        style={{
          backgroundColor: dashed ? 'transparent' : color,
          borderTop: dashed ? `1.5px dashed ${color}` : undefined,
        }}
      />
      <span>{children}</span>
    </div>
  );
}

