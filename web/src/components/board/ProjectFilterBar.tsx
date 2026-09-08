// ProjectFilterBar is the "which board am I looking at" row.
//
// It sits above the source bar because it is the narrower and more
// useful question: a source says which org the work came from, a project
// says which board it is being run on, and an operator planning a week
// cares about the second. The two are separate axes and compose.
//
// Buttons rather than a dropdown, for the same reason as the source bar:
// the counts are the useful part and a menu hides them until it is
// opened, which is exactly when they stop being an overview.

import { cn } from '@/lib/utils';
import type { WorkItem } from '@/types/core.gen';
import { listProjectOptions, PROJECT_ALL, type ProjectID } from './project';

export interface ProjectFilterBarProps {
  items: WorkItem[];
  value: ProjectID;
  onChange: (next: ProjectID) => void;
}

export function ProjectFilterBar({ items, value, onChange }: ProjectFilterBarProps) {
  const options = listProjectOptions(items);
  if (options.length === 0) return null;

  return (
    <div
      data-testid="project-filter-bar"
      className="flex flex-wrap items-center gap-1 border-b border-neutral-200 bg-white/50 px-4 py-1.5 dark:border-neutral-800 dark:bg-neutral-950/50"
    >
      <span className="mr-1 text-[11px] font-medium uppercase tracking-wide text-neutral-500">
        Project
      </span>
      {options.map((option) => {
        const selected = option.id === value || (value === '' && option.id === PROJECT_ALL);
        // The tooltip carries the qualified identity, so two boards that
        // happen to share a title are still distinguishable without
        // putting an org prefix on every button.
        const title =
          option.kind === 'project'
            ? `${option.source ?? ''} · project #${option.number ?? '?'}`
            : option.kind === 'none'
              ? 'Work in a read repository that is on no project board'
              : undefined;
        return (
          <button
            key={option.id}
            type="button"
            data-testid={`project-filter-${option.id}`}
            data-selected={selected ? 'true' : undefined}
            aria-pressed={selected}
            title={title}
            onClick={() => onChange(selected ? PROJECT_ALL : option.id)}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors',
              selected
                ? 'border-violet-400/70 bg-violet-50 font-semibold text-violet-950 dark:bg-violet-950/60 dark:text-violet-50'
                : option.kind === 'none'
                  ? 'border-dashed border-neutral-300 text-neutral-500 hover:bg-neutral-100 dark:border-neutral-700 dark:hover:bg-neutral-900'
                  : 'border-neutral-200 text-neutral-600 hover:bg-neutral-100 dark:border-neutral-800 dark:text-neutral-400 dark:hover:bg-neutral-900'
            )}
          >
            <span>{option.label}</span>
            <span
              data-testid={`project-filter-count-${option.id}`}
              className="rounded-full bg-neutral-200/70 px-1.5 text-[10px] font-medium tabular-nums text-neutral-700 dark:bg-neutral-800 dark:text-neutral-300"
            >
              {option.count}
            </span>
          </button>
        );
      })}
    </div>
  );
}
