import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';

import type { WorkItem } from '@/types/core.gen';
import { ProjectFilterBar } from '../ProjectFilterBar';
import { PROJECT_ALL, PROJECT_NONE } from '../project';

function boarded(id: string, source: string, project: string, title: string): WorkItem {
  return {
    id,
    kind: 'task',
    title: id,
    status: 'Todo',
    custom: {
      atab_source: source,
      atab_project: project,
      atab_project_title: title,
      atab_projects: [{ id: project, number: Number(project.split('#')[1] ?? 0), title }],
    },
  } as unknown as WorkItem;
}

function unfiled(id: string, source: string): WorkItem {
  return {
    id,
    kind: 'task',
    title: id,
    status: 'Todo',
    custom: { atab_source: source },
  } as unknown as WorkItem;
}

const items = [
  boarded('a/1', 'atab-group', 'atab-group#1', 'Atab Group Tasks'),
  boarded('a/2', 'atab-group', 'atab-group#1', 'Atab Group Tasks'),
  unfiled('a/3', 'atab-group'),
  boarded('b/1', 'hadedahealth', 'hadedahealth#1', 'HadedaHealth Build'),
];

describe('ProjectFilterBar', () => {
  it('renders a button per board with its count, labelled Project', () => {
    render(<ProjectFilterBar items={items} value={PROJECT_ALL} onChange={vi.fn()} />);

    expect(screen.getByTestId('project-filter-bar').textContent).toMatch(/Project/);
    expect(screen.getByTestId('project-filter-count-atab-group#1').textContent).toBe('2');
    expect(screen.getByTestId('project-filter-count-hadedahealth#1').textContent).toBe('1');
    expect(screen.getByTestId(`project-filter-count-${PROJECT_NONE}`).textContent).toBe('1');
    expect(screen.getByTestId(`project-filter-count-${PROJECT_ALL}`).textContent).toBe('4');
  });

  // Two boards that share a title stay distinguishable without putting an
  // org prefix on every button.
  it('carries the qualified identity in the tooltip', () => {
    render(<ProjectFilterBar items={items} value={PROJECT_ALL} onChange={vi.fn()} />);
    expect(screen.getByTestId('project-filter-atab-group#1').getAttribute('title')).toBe(
      'atab-group · project #1'
    );
  });

  it('selects a board on click', () => {
    const onChange = vi.fn();
    render(<ProjectFilterBar items={items} value={PROJECT_ALL} onChange={onChange} />);
    fireEvent.click(screen.getByTestId('project-filter-atab-group#1'));
    expect(onChange).toHaveBeenCalledWith('atab-group#1');
  });

  it('clears the selection when the selected board is clicked again', () => {
    const onChange = vi.fn();
    render(<ProjectFilterBar items={items} value="atab-group#1" onChange={onChange} />);
    fireEvent.click(screen.getByTestId('project-filter-atab-group#1'));
    expect(onChange).toHaveBeenCalledWith(PROJECT_ALL);
  });

  it('marks the selected button pressed', () => {
    render(<ProjectFilterBar items={items} value="hadedahealth#1" onChange={vi.fn()} />);
    expect(
      screen.getByTestId('project-filter-hadedahealth#1').getAttribute('aria-pressed')
    ).toBe('true');
    expect(screen.getByTestId('project-filter-atab-group#1').getAttribute('aria-pressed')).toBe(
      'false'
    );
  });

  // A board whose items carry no project axis has nothing to offer, and a
  // bar holding only an All button is chrome that does nothing.
  it('renders nothing when no item carries the axis', () => {
    const { container } = render(
      <ProjectFilterBar
        items={[{ id: 'x/1', kind: 'task', title: 'x', status: 'Todo', custom: {} } as unknown as WorkItem]}
        value={PROJECT_ALL}
        onChange={vi.fn()}
      />
    );
    expect(container.firstChild).toBeNull();
  });
});
