// Source selector model. A separate axis from Milestone and Scope:
// those narrow within one tracker's hierarchy, this narrows to where the
// work came from.
//
// "Project" here means the thing an operator points at: a configured
// source (an org and its board) and, one level down, a repository. It is
// deliberately not the issue's status or its lifecycle stage, which are
// already the board's columns. A federated board mixes several orgs into
// one set of lanes, and without this the only way to look at one of them
// is to read every card's prefix.
//
// URL param: ?source=<id>. Default = SOURCE_ALL, and the param is
// dropped rather than written when nothing is selected, so a shared link
// carries a filter only when one was actually chosen.

import type { WorkItem } from '@/types/core.gen';

// SOURCE_ALL is the sentinel for "no narrowing". Default URL state.
export const SOURCE_ALL = 'all';

export type SourceID = string;

// A repository id is namespaced under its source so two orgs carrying a
// repository of the same name stay distinct, exactly as work item ids
// are. The separator is one this notation cannot otherwise contain.
const REPO_SEPARATOR = '::';

export function repoFilterID(source: string, repo: string): SourceID {
  return `${source}${REPO_SEPARATOR}${repo}`;
}

/** SourceOption is one button in the bar. */
export interface SourceOption {
  id: SourceID;
  /** label is what the button reads. */
  label: string;
  /** count is how many items the option would leave on the board. */
  count: number;
  /** kind separates the source rows from the repository rows under them. */
  kind: 'all' | 'source' | 'repo';
  /** parent is the source a repository row belongs to. */
  parent?: SourceID;
  /**
   * freshness is the source's own, so a bar showing counts can also say
   * the numbers behind one of them are old.
   */
  freshness?: string;
}

function custom(item: WorkItem, key: string): string {
  const value = (item.custom ?? {})[key];
  return typeof value === 'string' ? value : '';
}

/** sourceOf is the configured source an item came from. */
export function sourceOf(item: WorkItem): string {
  return custom(item, 'atab_source');
}

/** repoOf is the repository an item came from. */
export function repoOf(item: WorkItem): string {
  return custom(item, 'atab_repo');
}

// listSourceOptions builds the bar: All, then one row per source, then
// its repositories. Counts are of the whole set passed in, so they say
// what each button would show rather than what the current filter left.
//
// Sources sort by id and repositories by name, so the bar does not
// reorder itself when a refresh changes the counts underneath it.
export function listSourceOptions(items: WorkItem[]): SourceOption[] {
  const sources = new Map<string, { count: number; freshness: string; repos: Map<string, number> }>();

  for (const item of items) {
    const source = sourceOf(item);
    if (!source) continue;
    let entry = sources.get(source);
    if (!entry) {
      entry = { count: 0, freshness: '', repos: new Map() };
      sources.set(source, entry);
    }
    entry.count += 1;
    const freshness = custom(item, 'atab_freshness');
    if (freshness) entry.freshness = freshness;
    const repo = repoOf(item);
    if (repo) entry.repos.set(repo, (entry.repos.get(repo) ?? 0) + 1);
  }

  // A board with nothing that declares a source has no axis to offer,
  // and a bar with only an All button is chrome that does nothing.
  if (sources.size === 0) return [];

  const out: SourceOption[] = [
    { id: SOURCE_ALL, label: 'All', count: items.length, kind: 'all' },
  ];
  for (const source of [...sources.keys()].sort()) {
    const entry = sources.get(source)!;
    out.push({
      id: source,
      label: source,
      count: entry.count,
      kind: 'source',
      freshness: entry.freshness,
    });
    for (const repo of [...entry.repos.keys()].sort()) {
      out.push({
        id: repoFilterID(source, repo),
        label: shortRepo(repo),
        count: entry.repos.get(repo)!,
        kind: 'repo',
        parent: source,
      });
    }
  }
  return out;
}

// shortRepo drops the owner, which is already the source's own label
// and would otherwise repeat on every button in the row.
export function shortRepo(repo: string): string {
  const slash = repo.indexOf('/');
  return slash < 0 ? repo : repo.slice(slash + 1);
}

// filterBySource narrows items to the selection.
//
// A selection naming a source no item declares returns everything rather
// than nothing. That case is a shared link outliving the source it named,
// or a board whose adaptor does not label its items at all, and an empty
// board reads as "no work" rather than as "your filter matched nothing".
// A selection naming a real source with no matching items does return
// empty, because that is a true answer about a real filter.
export function filterBySource(items: WorkItem[], selection: SourceID): WorkItem[] {
  if (!selection || selection === SOURCE_ALL) return items;

  const [source, repo] = selection.split(REPO_SEPARATOR);
  if (!items.some((item) => sourceOf(item) === source)) return items;

  return items.filter((item) =>
    repo ? sourceOf(item) === source && repoOf(item) === repo : sourceOf(item) === source
  );
}
