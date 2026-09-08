import { describe, expect, it } from 'vitest';
import type { WorkItem } from '@/types/core.gen';
import {
  SOURCE_ALL,
  filterBySource,
  listSourceOptions,
  repoFilterID,
  shortRepo,
} from '../source';

function item(id: string, source: string, repo: string, freshness = 'fresh'): WorkItem {
  return {
    id,
    kind: 'task',
    title: id,
    status: 'Todo',
    state_category: 'backlog',
    updated_at: '2026-09-08T00:00:00Z',
    custom: { atab_source: source, atab_repo: repo, atab_freshness: freshness },
  } as unknown as WorkItem;
}

const board = [
  item('a/1', 'atab-group', 'Atab-Group/One'),
  item('a/2', 'atab-group', 'Atab-Group/One'),
  item('a/3', 'atab-group', 'Atab-Group/Two'),
  item('b/1', 'hadedahealth', 'hadedahealth/Health'),
];

describe('source filter model', () => {
  // The counts are the reason this is a bar of buttons rather than a
  // menu, so they have to describe the whole board rather than whatever
  // the current selection has already narrowed it to.
  it('counts every source and its repositories against the whole board', () => {
    const options = listSourceOptions(board);
    const all = options.find((o) => o.kind === 'all');
    expect(all?.count).toBe(4);

    const sources = options.filter((o) => o.kind === 'source');
    expect(sources.map((o) => o.id)).toEqual(['atab-group', 'hadedahealth']);
    expect(sources[0].count).toBe(3);
    expect(sources[1].count).toBe(1);

    const repos = options.filter((o) => o.kind === 'repo' && o.parent === 'atab-group');
    expect(repos.map((o) => [o.label, o.count])).toEqual([
      ['One', 2],
      ['Two', 1],
    ]);
  });

  // A board that reorders its own filter bar when a refresh changes the
  // counts is one an operator cannot build muscle memory against.
  it('orders sources and repositories by name, not by count', () => {
    const busiest = [...board, item('b/2', 'hadedahealth', 'hadedahealth/Health')];
    const ids = listSourceOptions(busiest)
      .filter((o) => o.kind === 'source')
      .map((o) => o.id);
    expect(ids).toEqual(['atab-group', 'hadedahealth']);
  });

  it('narrows to a source, and to a repository within it', () => {
    expect(filterBySource(board, 'atab-group').map((i) => i.id)).toEqual(['a/1', 'a/2', 'a/3']);
    expect(
      filterBySource(board, repoFilterID('atab-group', 'Atab-Group/Two')).map((i) => i.id)
    ).toEqual(['a/3']);
  });

  // Two orgs can carry a repository of the same name. Namespacing the
  // repository option under its source is what keeps them apart, exactly
  // as work item ids are kept apart.
  it('keeps same-named repositories in different sources distinct', () => {
    const collide = [
      item('a/1', 'atab-group', 'Atab-Group/Shared'),
      item('b/1', 'hadedahealth', 'hadedahealth/Shared'),
    ];
    expect(filterBySource(collide, repoFilterID('atab-group', 'Atab-Group/Shared')).map((i) => i.id)).toEqual([
      'a/1',
    ]);
    expect(filterBySource(collide, repoFilterID('hadedahealth', 'hadedahealth/Shared')).map((i) => i.id)).toEqual([
      'b/1',
    ]);
  });

  it('returns everything for the All sentinel', () => {
    expect(filterBySource(board, SOURCE_ALL)).toHaveLength(4);
    expect(filterBySource(board, '')).toHaveLength(4);
  });

  // A shared link can outlive the source it named. An empty board reads
  // as "there is no work", which is a different and worse claim than
  // "that filter no longer exists".
  it('shows the whole board when the selection names a source nothing declares', () => {
    expect(filterBySource(board, 'a-source-that-was-removed')).toHaveLength(4);
  });

  // A real source that genuinely has nothing in it is a true empty
  // answer, and must not be confused with the stale-link case above.
  it('returns empty for a real source whose repository filter matches nothing', () => {
    expect(filterBySource(board, repoFilterID('atab-group', 'Atab-Group/Absent'))).toHaveLength(0);
  });

  // An adaptor that does not label its items has no axis to offer, and a
  // bar holding only an All button is chrome that does nothing.
  it('offers no options when nothing declares a source', () => {
    const plain = [{ id: 'gm-1', kind: 'task', title: 'x', status: 'open' } as unknown as WorkItem];
    expect(listSourceOptions(plain)).toEqual([]);
  });

  it('drops the owner from a repository label, which the source already carries', () => {
    expect(shortRepo('Atab-Group/Product-Seela')).toBe('Product-Seela');
    expect(shortRepo('no-owner')).toBe('no-owner');
  });
});
