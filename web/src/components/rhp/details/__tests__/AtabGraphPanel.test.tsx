import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import type { WorkItem } from '@/types/core.gen';
import { AtabGraphPanel, graphOf, type GraphEdge } from '../AtabGraphPanel';

function edge(over: Partial<GraphEdge> = {}): GraphEdge {
  return {
    kind: 'blocked_by',
    id: 'atab-group/Atab-Group~Repo/9',
    ref: 'Atab-Group/Repo#9',
    repo: 'Atab-Group/Repo',
    number: 9,
    title: 'the blocker',
    status: 'Todo',
    state: 'open',
    url: 'https://github.com/Atab-Group/Repo/issues/9',
    ...over,
  };
}

function itemWithGraph(graph: Record<string, unknown> | null): WorkItem {
  return {
    id: 'atab-group/Atab-Group~Repo/10',
    kind: 'task',
    title: 'subject',
    status: 'Todo',
    custom: graph ? { atab_graph: graph } : {},
  } as unknown as WorkItem;
}

const empty = { blocked_by: [], blocks: [], children: [], unresolved: 0 };

describe('AtabGraphPanel', () => {
  // Another adaptor supplies no graph at all, and the panel must simply
  // not render rather than showing an empty shell.
  it('renders nothing for an adaptor that supplies no graph', () => {
    const { container } = render(<AtabGraphPanel item={itemWithGraph(null)} />);
    expect(container.firstChild).toBeNull();
    expect(graphOf(itemWithGraph(null))).toBeNull();
  });

  it('says so plainly when an issue has no edges', () => {
    render(<AtabGraphPanel item={itemWithGraph(empty)} />);
    expect(screen.getByText(/Nothing blocks this/i)).toBeTruthy();
  });

  // Blockers and dependents are different questions and must not be
  // merged into one "related" list.
  it('separates what blocks this from what this blocks', () => {
    render(
      <AtabGraphPanel
        item={itemWithGraph({
          ...empty,
          blocked_by: [edge({ title: 'upstream' })],
          blocks: [edge({ kind: 'blocks', ref: 'Atab-Group/Repo#11', title: 'downstream' })],
        })}
      />
    );
    expect(screen.getByTestId('atab-graph-blocked-by')).toBeTruthy();
    expect(screen.getByTestId('atab-graph-blocks')).toBeTruthy();
    expect(screen.getByText('upstream')).toBeTruthy();
    expect(screen.getByText('downstream')).toBeTruthy();
  });

  // Provenance must be labelled as provenance. Rendered among the
  // blockers it would read as work that is holding this one up.
  it('labels provenance as provenance, not as a blocker', () => {
    render(
      <AtabGraphPanel
        item={itemWithGraph({
          ...empty,
          discovered_from: edge({ kind: 'discovered_from', title: 'where it came from' }),
        })}
      />
    );
    const group = screen.getByTestId('atab-graph-discovered-from');
    expect(group.textContent).toContain('where it came from');
    expect(screen.queryByTestId('atab-graph-blocked-by')).toBeNull();
  });

  // The fail-closed case, and the one that has to be visible: an edge
  // the board cannot read is treated as still holding, and an operator
  // who mistook it for resolved would start work that is still blocked.
  it('marks an unreadable edge and does not offer it as navigable', () => {
    const onNavigate = vi.fn();
    render(
      <AtabGraphPanel
        item={itemWithGraph({
          ...empty,
          blocked_by: [edge({ id: undefined, title: undefined, state: 'unknown', cross_source: true })],
          unresolved: 1,
        })}
        onNavigate={onNavigate}
      />
    );

    expect(screen.getByTestId('atab-graph-unresolved').textContent).toContain('1 unresolved');
    const row = screen.getByTestId('atab-edge-Atab-Group/Repo#9');
    expect(row.getAttribute('data-edge-state')).toBe('unknown');
    expect(row.textContent).toContain('unreadable');
    // Nothing to open in place, so no in-app control is offered.
    expect(row.querySelector('button')).toBeNull();
  });

  // Every edge keeps a working GitHub link, including the ones the
  // board cannot follow. That is the point of keeping it.
  it('always links out to GitHub, including for unreadable edges', () => {
    render(
      <AtabGraphPanel
        item={itemWithGraph({
          ...empty,
          blocked_by: [edge({ id: undefined, state: 'unknown' })],
          unresolved: 1,
        })}
      />
    );
    const link = screen.getByTitle('Open Atab-Group/Repo#9 on GitHub') as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('https://github.com/Atab-Group/Repo/issues/9');
  });

  it('navigates in place for an edge this board can read', () => {
    const onNavigate = vi.fn();
    render(
      <AtabGraphPanel
        item={itemWithGraph({ ...empty, blocked_by: [edge()] })}
        onNavigate={onNavigate}
      />
    );
    fireEvent.click(screen.getByText('the blocker'));
    expect(onNavigate).toHaveBeenCalledWith('atab-group/Atab-Group~Repo/9');
  });

  // A resolved blocker no longer holds, and the row has to read
  // differently from one that still does.
  it('distinguishes a resolved edge from one that still holds', () => {
    render(
      <AtabGraphPanel
        item={itemWithGraph({
          ...empty,
          blocked_by: [edge({ state: 'resolved', ref: 'Atab-Group/Repo#8' }), edge()],
        })}
      />
    );
    expect(screen.getByTestId('atab-edge-Atab-Group/Repo#8').getAttribute('data-edge-state')).toBe(
      'resolved'
    );
    expect(screen.getByTestId('atab-edge-Atab-Group/Repo#9').getAttribute('data-edge-state')).toBe(
      'open'
    );
  });
});
