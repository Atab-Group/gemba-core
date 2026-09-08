// Project selector model.
//
// A project here is a real project board: a GitHub Projects v2 board
// with its own number and title, qualified by the source that carries it
// because two orgs both run a project #1. It is not the source, and the
// difference is the reason this module exists. A source is an org plus a
// credential plus a repository allowlist; a board is one view inside it,
// an org can run several, and work in a repository the source reads can
// sit on none of them.
//
// The board's earlier filter labelled the source axis "Project", which
// answered a question nobody asked: on this deployment it offered two
// buttons, one per org, while the org's actual board held 1599 of its
// 2530 issues and the rest were on no board at all. That third fact is
// the one an operator most wants to see, and an org filter cannot show
// it.
//
// This axis is independent of the source axis and composes with it. URL
// param: ?project=<id>. Default = PROJECT_ALL, and the param is dropped
// rather than written when nothing is selected.

import type { WorkItem } from '@/types/core.gen';

/** PROJECT_ALL is the sentinel for "no narrowing". Default URL state. */
export const PROJECT_ALL = 'all';

/**
 * PROJECT_NONE selects the work that is on no board.
 *
 * It is a real selection rather than the absence of one. Work nobody has
 * filed is the largest single bucket on this deployment, and a filter
 * that could not name it would leave a third of the board unreachable
 * from the project axis.
 */
export const PROJECT_NONE = 'none';

export type ProjectID = string;

/** ProjectOption is one button in the bar. */
export interface ProjectOption {
  id: ProjectID;
  /** label is what the button reads: the board's own title. */
  label: string;
  /** count is how many items the option would leave on the board. */
  count: number;
  /** source is the configured source carrying the board. */
  source?: string;
  /** number is the board's Projects v2 number. */
  number?: number;
  kind: 'all' | 'project' | 'none';
}

interface ProjectEntry {
  id: string;
  number?: number;
  title?: string;
}

function custom(item: WorkItem, key: string): unknown {
  return (item.custom ?? {})[key];
}

function customString(item: WorkItem, key: string): string {
  const value = custom(item, key);
  return typeof value === 'string' ? value : '';
}

/**
 * projectsOf lists every board an item sits on.
 *
 * atab_projects is the full list and atab_project is the primary one.
 * An older snapshot, or an adaptor that only reports the primary, still
 * yields one entry rather than none.
 */
export function projectsOf(item: WorkItem): ProjectEntry[] {
  const raw = custom(item, 'atab_projects');
  if (Array.isArray(raw)) {
    const out: ProjectEntry[] = [];
    for (const entry of raw) {
      if (!entry || typeof entry !== 'object') continue;
      const rec = entry as Record<string, unknown>;
      const id = typeof rec.id === 'string' ? rec.id : '';
      if (!id) continue;
      out.push({
        id,
        number: typeof rec.number === 'number' ? rec.number : undefined,
        title: typeof rec.title === 'string' ? rec.title : undefined,
      });
    }
    if (out.length > 0) return out;
  }
  const primary = customString(item, 'atab_project');
  if (!primary) return [];
  return [{ id: primary, title: customString(item, 'atab_project_title') || undefined }];
}

/** projectOf is the item's primary board id, or "" when it is on none. */
export function projectOf(item: WorkItem): string {
  return projectsOf(item)[0]?.id ?? '';
}

/**
 * listProjectOptions builds the bar: All, then one button per board, then
 * the unfiled bucket.
 *
 * Counts are of the whole set passed in, so each button says what it
 * would show rather than what the current filter has left. An item on two
 * boards counts once under each, which is why the counts can sum to more
 * than the total and why the total is its own button rather than a sum.
 */
export function listProjectOptions(items: WorkItem[]): ProjectOption[] {
  const boards = new Map<string, { count: number; title: string; number?: number; source: string }>();
  let unfiled = 0;
  let labelled = 0;

  for (const item of items) {
    const projects = projectsOf(item);
    if (projects.length === 0) {
      // Only an item that declares a source is known to be unfiled. An
      // adaptor that labels nothing has no project axis at all, and
      // counting its items as unfiled would invent a bucket.
      if (customString(item, 'atab_source')) {
        unfiled += 1;
        labelled += 1;
      }
      continue;
    }
    labelled += 1;
    for (const project of projects) {
      const entry = boards.get(project.id) ?? {
        count: 0,
        title: '',
        number: project.number,
        source: project.id.split('#')[0] ?? '',
      };
      entry.count += 1;
      if (project.title) entry.title = project.title;
      if (project.number !== undefined) entry.number = project.number;
      boards.set(project.id, entry);
    }
  }

  // A board whose items carry no project axis has nothing to offer, and
  // a bar holding only an "All" button is chrome that does nothing.
  if (labelled === 0) return [];

  const out: ProjectOption[] = [
    { id: PROJECT_ALL, label: 'All', count: items.length, kind: 'all' },
  ];
  // Sorted by id, so the bar does not reorder itself when a refresh
  // changes the counts underneath it.
  for (const id of [...boards.keys()].sort()) {
    const entry = boards.get(id)!;
    out.push({
      id,
      label: entry.title || id,
      count: entry.count,
      source: entry.source,
      number: entry.number,
      kind: 'project',
    });
  }
  if (unfiled > 0) {
    out.push({ id: PROJECT_NONE, label: 'No project', count: unfiled, kind: 'none' });
  }
  return out;
}

/**
 * filterByProject narrows items to the selection.
 *
 * A selection naming a board no item declares returns everything rather
 * than nothing, matching the source axis: that case is a shared link
 * outliving the board it named, and an empty board reads as "no work"
 * rather than as "your filter matched nothing". PROJECT_NONE is exempt,
 * because "no item is unfiled" is a true and useful answer.
 */
export function filterByProject(items: WorkItem[], selection: ProjectID): WorkItem[] {
  if (!selection || selection === PROJECT_ALL) return items;
  if (selection === PROJECT_NONE) {
    return items.filter((item) => projectsOf(item).length === 0 && hasSource(item));
  }
  if (!items.some((item) => projectsOf(item).some((p) => p.id === selection))) return items;
  return items.filter((item) => projectsOf(item).some((p) => p.id === selection));
}

function hasSource(item: WorkItem): boolean {
  return customString(item, 'atab_source') !== '';
}
