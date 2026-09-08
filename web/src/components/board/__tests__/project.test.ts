import { describe, expect, it } from 'vitest';

import {
  PROJECT_ALL,
  PROJECT_NONE,
  filterByProject,
  listProjectOptions,
  projectOf,
  projectsOf,
} from '../project';
import type { WorkItem } from '@/types/core.gen';

function item(id: string, custom: Record<string, unknown>): WorkItem {
  return {
    id,
    kind: 'task',
    title: id,
    status: 'Todo',
    state_category: 'backlog',
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    custom,
  } as unknown as WorkItem;
}

function boarded(id: string, source: string, project: string, title: string): WorkItem {
  return item(id, {
    atab_source: source,
    atab_project: project,
    atab_project_title: title,
    atab_projects: [{ id: project, number: Number(project.split('#')[1] ?? 0), title }],
  });
}

function unfiled(id: string, source: string): WorkItem {
  return item(id, { atab_source: source });
}

describe('projectsOf', () => {
  it('reads the full list when the adaptor supplies one', () => {
    const wi = item('a/1', {
      atab_source: 'atab-group',
      atab_project: 'atab-group#1',
      atab_projects: [
        { id: 'atab-group#1', number: 1, title: 'Atab Group Tasks' },
        { id: 'atab-group#4', number: 4, title: 'Portfolio' },
      ],
    });
    expect(projectsOf(wi).map((p) => p.id)).toEqual(['atab-group#1', 'atab-group#4']);
    expect(projectOf(wi)).toBe('atab-group#1');
  });

  // An adaptor that only reports the primary board still yields one
  // entry. Returning none would render every one of its cards as unfiled.
  it('falls back to the primary field alone', () => {
    const wi = item('a/1', {
      atab_source: 'atab-group',
      atab_project: 'atab-group#1',
      atab_project_title: 'Atab Group Tasks',
    });
    expect(projectsOf(wi)).toEqual([{ id: 'atab-group#1', title: 'Atab Group Tasks' }]);
  });

  it('reports nothing for an item on no board', () => {
    expect(projectsOf(unfiled('a/1', 'atab-group'))).toEqual([]);
    expect(projectOf(unfiled('a/1', 'atab-group'))).toBe('');
  });
});

describe('listProjectOptions', () => {
  // The whole point of the axis: two boards inside one org are two
  // buttons, not one. The source axis cannot express this.
  it('offers one button per board, not per org', () => {
    const items = [
      boarded('a/1', 'atab-group', 'atab-group#1', 'Atab Group Tasks'),
      boarded('a/2', 'atab-group', 'atab-group#1', 'Atab Group Tasks'),
      boarded('a/3', 'atab-group', 'atab-group#4', 'Portfolio'),
      boarded('b/1', 'hadedahealth', 'hadedahealth#1', 'HadedaHealth Build'),
    ];
    const options = listProjectOptions(items);
    expect(options.map((o) => o.id)).toEqual([
      PROJECT_ALL,
      'atab-group#1',
      'atab-group#4',
      'hadedahealth#1',
    ]);
    expect(options.find((o) => o.id === 'atab-group#1')?.count).toBe(2);
    expect(options.find((o) => o.id === 'atab-group#1')?.label).toBe('Atab Group Tasks');
    expect(options.find((o) => o.id === 'hadedahealth#1')?.source).toBe('hadedahealth');
  });

  // Work on no board is the largest bucket on this deployment. A filter
  // that could not name it would leave a third of the board unreachable.
  it('gives unfiled work its own button, last', () => {
    const items = [
      boarded('a/1', 'atab-group', 'atab-group#1', 'Atab Group Tasks'),
      unfiled('a/2', 'atab-group'),
      unfiled('b/1', 'hadedahealth'),
    ];
    const options = listProjectOptions(items);
    expect(options[options.length - 1]).toMatchObject({
      id: PROJECT_NONE,
      count: 2,
      kind: 'none',
    });
  });

  it('counts against the whole set, so a button says what it would show', () => {
    const items = [
      boarded('a/1', 'atab-group', 'atab-group#1', 'Atab Group Tasks'),
      boarded('a/2', 'atab-group', 'atab-group#4', 'Portfolio'),
    ];
    expect(listProjectOptions(items).find((o) => o.id === PROJECT_ALL)?.count).toBe(2);
  });

  // An adaptor that labels nothing has no project axis, and a bar with
  // only an All button is chrome that does nothing.
  it('offers nothing when no item carries the axis', () => {
    expect(listProjectOptions([item('x/1', {})])).toEqual([]);
  });

  // An item on two boards counts once under each, so the buttons can sum
  // to more than the total. All is its own count rather than a sum.
  it('counts a multi-board item under each of its boards', () => {
    const items = [
      item('a/1', {
        atab_source: 'atab-group',
        atab_project: 'atab-group#1',
        atab_projects: [
          { id: 'atab-group#1', number: 1, title: 'Atab Group Tasks' },
          { id: 'atab-group#4', number: 4, title: 'Portfolio' },
        ],
      }),
    ];
    const options = listProjectOptions(items);
    expect(options.find((o) => o.id === 'atab-group#1')?.count).toBe(1);
    expect(options.find((o) => o.id === 'atab-group#4')?.count).toBe(1);
    expect(options.find((o) => o.id === PROJECT_ALL)?.count).toBe(1);
  });
});

describe('filterByProject', () => {
  const items = [
    boarded('a/1', 'atab-group', 'atab-group#1', 'Atab Group Tasks'),
    boarded('a/2', 'atab-group', 'atab-group#4', 'Portfolio'),
    unfiled('a/3', 'atab-group'),
    boarded('b/1', 'hadedahealth', 'hadedahealth#1', 'HadedaHealth Build'),
  ];

  it('narrows to one board', () => {
    expect(filterByProject(items, 'atab-group#1').map((i) => i.id)).toEqual(['a/1']);
  });

  it('narrows to unfiled work', () => {
    expect(filterByProject(items, PROJECT_NONE).map((i) => i.id)).toEqual(['a/3']);
  });

  it('leaves everything alone for the default', () => {
    expect(filterByProject(items, PROJECT_ALL)).toHaveLength(4);
    expect(filterByProject(items, '')).toHaveLength(4);
  });

  // A shared link outliving the board it named reads as "no work" rather
  // than as "your filter matched nothing", so it falls back to showing
  // everything, matching the source axis.
  it('falls back to everything for a board no item declares', () => {
    expect(filterByProject(items, 'atab-group#99')).toHaveLength(4);
  });

  // PROJECT_NONE is exempt from that fallback: "no item is unfiled" is a
  // true and useful answer.
  it('returns empty when nothing is unfiled', () => {
    const allBoarded = [boarded('a/1', 'atab-group', 'atab-group#1', 'Tasks')];
    expect(filterByProject(allBoarded, PROJECT_NONE)).toEqual([]);
  });

  // A multi-board item is reachable from either of its boards.
  it('matches an item on any of its boards', () => {
    const multi = item('a/9', {
      atab_source: 'atab-group',
      atab_project: 'atab-group#1',
      atab_projects: [
        { id: 'atab-group#1', number: 1, title: 'Atab Group Tasks' },
        { id: 'atab-group#4', number: 4, title: 'Portfolio' },
      ],
    });
    expect(filterByProject([multi], 'atab-group#4').map((i) => i.id)).toEqual(['a/9']);
  });
});
