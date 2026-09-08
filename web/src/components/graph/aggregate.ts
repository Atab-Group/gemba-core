// Company-scale overview: clusters, never a canvas of issue cards.
//
// At 2790 items no arrangement of individual nodes is readable, so the
// overview does not attempt one. It groups work into the unit an
// operator already thinks in, draws one node per group with its size,
// and draws one weighted edge per pair of groups that depend on each
// other. On the live board that is roughly a dozen nodes, which is a
// picture rather than a hairball.
//
// Grouping key, in order of preference:
//
//   1. the repository, which is where work actually lives and which the
//      core WorkItem already carries as primary_repository_id;
//   2. the ATAB source, for an adaptor that names no repository;
//   3. one "All work" group, so the overview still draws something for
//      an adaptor that labels nothing at all.
//
// Cross-source safety: a group id is prefixed with its source, so two
// orgs owning a repository of the same name stay two groups. Nothing
// here reads a field it was not given, and a group carries counts
// rather than titles, so an overview cannot leak an item's contents
// across a source boundary.

import type { WorkItem } from '@/types/core.gen';
import type { DirectedEdge } from './graphAnalysis';

export interface ClusterNode {
  /** id is stable and source-qualified: "cluster:<source>/<key>". */
  id: string;
  /** label is what the node reads. */
  label: string;
  /** sublabel names the project or source the group belongs to. */
  sublabel?: string;
  /** count is how many work items the group holds. */
  count: number;
  /** blocked is how many of them are blocked, for a small warning pip. */
  blocked: number;
  /** internalEdges is how many dependencies stay inside the group. */
  internalEdges: number;
}

export interface ClusterEdge extends DirectedEdge {
  /** weight is how many item-level edges this one cluster edge stands for. */
  weight: number;
}

export interface AggregateResult {
  clusters: ClusterNode[];
  edges: ClusterEdge[];
  /** memberOf maps a work item id to its cluster id. */
  memberOf: Map<string, string>;
  /** itemCount is how many items the overview stands for. */
  itemCount: number;
}

function customString(item: WorkItem, key: string): string {
  const v = (item.custom ?? {})[key];
  return typeof v === 'string' ? v : '';
}

interface GroupKey {
  id: string;
  label: string;
  sublabel?: string;
}

function groupKeyFor(item: WorkItem): GroupKey {
  const source = customString(item, 'atab_source');
  const repo = customString(item, 'atab_repo') || item.primary_repository_id || '';
  const project = customString(item, 'atab_project_title');
  if (repo) {
    // Source-qualified so two orgs carrying a repository of the same
    // name never merge into one group.
    const scope = source || 'repo';
    return {
      id: `cluster:${scope}/${repo}`,
      label: shortRepoLabel(repo),
      sublabel: project || source || undefined,
    };
  }
  if (source) {
    return { id: `cluster:source/${source}`, label: source, sublabel: project || undefined };
  }
  return { id: 'cluster:all', label: 'All work' };
}

// shortRepoLabel drops the owner, which is already the sublabel and
// would otherwise repeat on every node in the picture.
function shortRepoLabel(repo: string): string {
  const slash = repo.indexOf('/');
  return slash < 0 ? repo : repo.slice(slash + 1);
}

/**
 * aggregateClusters folds items and their edges into groups.
 *
 * Every edge kind counts here, not just the structural ones. At item
 * level a `relates_to` is a hint and does not order anything, but at
 * group level "these two repositories reference each other forty times"
 * is exactly the signal the overview exists to show.
 *
 * Edges inside a group are counted rather than drawn: an overview
 * showing a group's internal dependencies would be the hairball again,
 * one level up. Edges between groups are merged and weighted, so forty
 * dependencies become one thick arrow rather than forty crossing lines.
 *
 * On the live board every one of the 2200 edges is same-repo, so the
 * overview draws ten tiles and no arrows. That is the board telling the
 * truth about itself, and the page says so in words rather than leaving
 * an empty canvas that reads as a failure.
 */
export function aggregateClusters(
  items: WorkItem[],
  allEdges: DirectedEdge[]
): AggregateResult {
  const memberOf = new Map<string, string>();
  const byId = new Map<string, ClusterNode>();

  for (const item of items) {
    const key = groupKeyFor(item);
    memberOf.set(item.id, key.id);
    let cluster = byId.get(key.id);
    if (!cluster) {
      cluster = {
        id: key.id,
        label: key.label,
        sublabel: key.sublabel,
        count: 0,
        blocked: 0,
        internalEdges: 0,
      };
      byId.set(key.id, cluster);
    }
    cluster.count += 1;
    if (customString(item, 'atab_readiness') === 'blocked') cluster.blocked += 1;
  }

  const weights = new Map<string, ClusterEdge>();
  for (const e of allEdges) {
    const a = memberOf.get(e.from);
    const b = memberOf.get(e.to);
    if (!a || !b) continue;
    if (a === b) {
      const cluster = byId.get(a);
      if (cluster) cluster.internalEdges += 1;
      continue;
    }
    // The pair key uses an escaped NUL rather than a literal one:
    // a raw NUL byte in the source makes the file binary to git and
    // to every diff tool. The separator itself cannot appear in a
    // cluster id.
    const key = `${a}\u0000${b}`;
    const existing = weights.get(key);
    if (existing) existing.weight += 1;
    else weights.set(key, { from: a, to: b, weight: 1 });
  }

  // Sorted so the overview is byte-identical between two renders of the
  // same board, which is what lets the layout below it stay still.
  const clusters = [...byId.values()].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
  const edges = [...weights.values()].sort((a, b) =>
    a.from !== b.from ? (a.from < b.from ? -1 : 1) : a.to < b.to ? -1 : a.to > b.to ? 1 : 0
  );

  return { clusters, edges, memberOf, itemCount: items.length };
}
