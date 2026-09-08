// Custom React Flow node for the dependency graph (gm-e12.16). One per
// drawn node, which is either a work item or, in the overview, a cluster
// standing for a repository's worth of them. Click is owned by GraphPage
// via React Flow's onNodeClick — the node itself stays stateless so a
// full canvas does not pay for a hook per card.

import { Handle, Position } from 'reactflow';
import type { NodeProps } from 'reactflow';
import type { StateCategory } from '@/types/core.gen';
import { cn } from '@/lib/utils';

export interface WorkItemNodeData {
  id: string;
  title: string;
  stateCategory?: StateCategory;
  // inCycle / onCriticalPath are pre-computed by GraphPage and pushed
  // onto each node's data. Keeping them in `data` (vs deriving in the
  // node component) lets React Flow's diff skip nodes whose flags
  // haven't changed when the cycle/critical-path mode toggles.
  inCycle?: boolean;
  onCriticalPath?: boolean;
  // hoverRelated is true for the hovered node + every direct
  // neighbour through any edge (gm-qdqu). Pure passthrough — the
  // node renders a data-hover-related attribute that specs / styling
  // can hook into.
  hoverRelated?: boolean;
  /** shape decides whether this draws as one item or as a cluster. */
  shape?: 'item' | 'cluster';
  /** subtitle names the repository the item or cluster belongs to. */
  subtitle?: string;
  /** count is the member count on a cluster. */
  count?: number;
  /** blocked is how many members of a cluster are blocked. */
  blocked?: number;
  /** hops is the distance from the focused item, in focus mode. */
  hops?: number;
  /** direction says which side of the focus this node sits on. */
  direction?: 'focus' | 'upstream' | 'downstream' | 'both';
  isFocus?: boolean;
}

const STATE_PIP: Record<StateCategory, string> = {
  backlog: 'bg-neutral-400',
  unstarted: 'bg-sky-400',
  staged: 'bg-violet-400',
  started: 'bg-amber-400',
  completed: 'bg-emerald-500',
  canceled: 'bg-neutral-300',
};

export function WorkItemNode({ data }: NodeProps<WorkItemNodeData>) {
  if (data.shape === 'cluster') return <ClusterCard data={data} />;
  return <ItemCard data={data} />;
}

function ItemCard({ data }: { data: WorkItemNodeData }) {
  return (
    <div
      className={cn(
        'flex items-center gap-2 rounded-md border bg-white px-2 py-1.5 text-xs shadow-sm dark:bg-neutral-900',
        data.isFocus
          ? 'border-cyan-500 ring-2 ring-cyan-300 dark:ring-cyan-800'
          : data.inCycle
            ? 'border-rose-500 ring-2 ring-rose-300 dark:ring-rose-800'
            : data.onCriticalPath
              ? 'border-amber-500 ring-2 ring-amber-300 dark:ring-amber-800'
              : 'border-neutral-300 dark:border-neutral-700'
      )}
      style={{ width: 200 }}
      data-testid={`graph-node-${data.id}`}
      data-in-cycle={data.inCycle || undefined}
      data-on-critical-path={data.onCriticalPath || undefined}
      data-hover-related={data.hoverRelated || undefined}
      data-focus={data.isFocus || undefined}
      data-direction={data.direction}
      data-hops={data.hops}
    >
      <Handle type="target" position={Position.Left} className="!bg-neutral-400" />
      {data.stateCategory ? (
        <span
          className={cn('h-2 w-2 shrink-0 rounded-full', STATE_PIP[data.stateCategory])}
          aria-hidden
        />
      ) : null}
      <div className="min-w-0 flex-1">
        <div className="truncate font-mono text-[10px] text-neutral-500">{data.id}</div>
        <div className="truncate font-medium text-neutral-800 dark:text-neutral-200">
          {data.title}
        </div>
      </div>
      <Handle type="source" position={Position.Right} className="!bg-neutral-400" />
    </div>
  );
}

// ClusterCard is the overview tile. It carries counts rather than item
// titles, which is both what makes the overview readable and what keeps
// one source's work out of another source's tile.
function ClusterCard({ data }: { data: WorkItemNodeData }) {
  return (
    <div
      className="rounded-lg border-2 border-neutral-400 bg-neutral-50 px-3 py-2 text-xs shadow-sm dark:border-neutral-600 dark:bg-neutral-900"
      style={{ width: 200 }}
      data-testid={`graph-cluster-${data.id}`}
      data-cluster="true"
      data-count={data.count}
      data-hover-related={data.hoverRelated || undefined}
    >
      <Handle type="target" position={Position.Left} className="!bg-neutral-400" />
      <div className="truncate font-semibold text-neutral-800 dark:text-neutral-100">
        {data.title}
      </div>
      {data.subtitle ? (
        <div className="truncate text-[10px] text-neutral-500">{data.subtitle}</div>
      ) : null}
      <div className="mt-1 flex items-baseline gap-2">
        <span className="text-lg font-semibold tabular-nums text-neutral-900 dark:text-neutral-50">
          {data.count ?? 0}
        </span>
        <span className="text-[10px] text-neutral-500">items</span>
        {data.blocked ? (
          <span className="ml-auto rounded-full bg-rose-100 px-1.5 text-[10px] font-medium text-rose-800 dark:bg-rose-950 dark:text-rose-200">
            {data.blocked} blocked
          </span>
        ) : null}
      </div>
      <Handle type="source" position={Position.Right} className="!bg-neutral-400" />
    </div>
  );
}
