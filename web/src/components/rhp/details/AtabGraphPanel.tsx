// AtabGraphPanel renders the issue's immediate graph as the adaptor
// resolved it.
//
// The generic Relationships list above it works from core relationship
// ids alone, so it can name a neighbour but cannot say whether that
// neighbour still holds, what it is called, or whether this deployment
// can read it at all. Those are the three things somebody deciding
// whether to pick up an issue actually needs, and the last of them has
// to be visible rather than inferred: an edge into a source the board
// cannot read is treated as still holding, and an operator who mistook
// it for a resolved one would start work that is still blocked.

import type { WorkItem } from '@/types/core.gen';
import { cn } from '@/lib/utils';

export type EdgeState = 'open' | 'resolved' | 'unknown';

export interface GraphEdge {
  kind: string;
  id?: string;
  ref: string;
  repo: string;
  number: number;
  title?: string;
  status?: string;
  state: EdgeState;
  cross_repo?: boolean;
  cross_source?: boolean;
  url: string;
}

export interface Neighbourhood {
  blocked_by: GraphEdge[];
  blocks: GraphEdge[];
  parent?: GraphEdge | null;
  children: GraphEdge[];
  discovered_from?: GraphEdge | null;
  unresolved: number;
}

/** graphOf reads the neighbourhood the adaptor attached to a detail read. */
export function graphOf(item: WorkItem): Neighbourhood | null {
  const raw = (item.custom ?? {})['atab_graph'];
  if (!raw || typeof raw !== 'object') return null;
  const graph = raw as Neighbourhood;
  return Array.isArray(graph.blocked_by) ? graph : null;
}

export interface AtabGraphPanelProps {
  item: WorkItem;
  onNavigate?: (id: string) => void;
}

export function AtabGraphPanel({ item, onNavigate }: AtabGraphPanelProps) {
  const graph = graphOf(item);
  if (!graph) return null;

  const groups: Array<{ label: string; hint: string; edges: GraphEdge[] }> = [
    {
      label: 'Blocked by',
      hint: 'Work that has to finish before this can start.',
      edges: graph.blocked_by,
    },
    {
      label: 'Blocks',
      hint: 'Work that finishing this would release.',
      edges: graph.blocks,
    },
    { label: 'Parent', hint: 'The epic this belongs to.', edges: graph.parent ? [graph.parent] : [] },
    { label: 'Children', hint: 'Where the work actually happens.', edges: graph.children },
    {
      label: 'Discovered from',
      hint: 'Provenance. This does not block anything.',
      edges: graph.discovered_from ? [graph.discovered_from] : [],
    },
  ];

  const total = groups.reduce((n, g) => n + g.edges.length, 0);

  return (
    <div data-testid="atab-graph" className="mt-4">
      <div className="mb-1 flex items-baseline gap-2">
        <h4 className="text-xs font-semibold uppercase tracking-wide text-neutral-500">
          Issue graph
        </h4>
        {graph.unresolved > 0 && (
          <span
            data-testid="atab-graph-unresolved"
            className="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-900 dark:bg-amber-950 dark:text-amber-200"
            title="These targets live somewhere this board cannot read. They are treated as still holding."
          >
            {graph.unresolved} unresolved
          </span>
        )}
      </div>

      {total === 0 ? (
        <p className="text-xs text-neutral-500 dark:text-neutral-400">
          Nothing blocks this, and nothing waits on it.
        </p>
      ) : (
        groups
          .filter((g) => g.edges.length > 0)
          .map((g) => (
            <div key={g.label} className="mb-2" data-testid={`atab-graph-${g.label.toLowerCase().replace(/ /g, '-')}`}>
              <div className="text-[11px] font-medium text-neutral-600 dark:text-neutral-300" title={g.hint}>
                {g.label}
              </div>
              <ul className="mt-0.5 flex flex-col gap-0.5">
                {g.edges.map((edge) => (
                  <EdgeRow key={`${edge.kind}:${edge.ref}`} edge={edge} onNavigate={onNavigate} />
                ))}
              </ul>
            </div>
          ))
      )}
    </div>
  );
}

function EdgeRow({ edge, onNavigate }: { edge: GraphEdge; onNavigate?: (id: string) => void }) {
  // An edge this board can read is navigable in place. One it cannot is
  // not: there is nothing here to open, and a control that looks
  // clickable and does nothing is worse than one that is plainly a link
  // out to GitHub.
  const navigable = Boolean(edge.id) && edge.state !== 'unknown';

  return (
    <li
      data-testid={`atab-edge-${edge.ref}`}
      data-edge-state={edge.state}
      className={cn(
        'flex items-center gap-1.5 rounded px-1 py-0.5 text-xs',
        edge.state === 'unknown' &&
          'border border-dashed border-amber-400/60 bg-amber-50/50 dark:bg-amber-950/20'
      )}
    >
      <StateDot state={edge.state} />
      {navigable ? (
        <button
          type="button"
          onClick={() => onNavigate?.(edge.id!)}
          className="truncate text-left text-cyan-700 hover:underline dark:text-cyan-400"
        >
          {edge.title || edge.ref}
        </button>
      ) : (
        <span className="truncate italic text-neutral-600 dark:text-neutral-400">
          {edge.title || edge.ref}
        </span>
      )}
      {edge.cross_repo && (
        <span
          className="shrink-0 rounded bg-neutral-200 px-1 text-[10px] text-neutral-700 dark:bg-neutral-800 dark:text-neutral-300"
          title={`Lives in ${edge.repo}`}
        >
          {edge.repo.split('/').pop()}
        </span>
      )}
      {edge.state === 'unknown' && (
        <span
          className="shrink-0 text-[10px] font-medium text-amber-700 dark:text-amber-400"
          title="This board cannot read that target, so the edge is treated as still holding rather than assumed clear."
        >
          unreadable
        </span>
      )}
      {/* Always reachable on GitHub, including the edges this board
          cannot follow. That is the point of keeping the link: an edge
          the board cannot resolve is one a person still can. */}
      <a
        href={edge.url}
        target="_blank"
        rel="noreferrer"
        className="ml-auto shrink-0 text-[10px] text-neutral-500 hover:underline dark:text-neutral-400"
        title={`Open ${edge.ref} on GitHub`}
      >
        {edge.ref}
      </a>
    </li>
  );
}

function StateDot({ state }: { state: EdgeState }) {
  const tone =
    state === 'resolved'
      ? 'bg-emerald-500'
      : state === 'open'
        ? 'bg-amber-500'
        : 'bg-neutral-400 ring-1 ring-amber-500';
  const label =
    state === 'resolved' ? 'resolved' : state === 'open' ? 'still open' : 'cannot be read';
  return <span aria-label={label} title={label} className={cn('h-1.5 w-1.5 shrink-0 rounded-full', tone)} />;
}
