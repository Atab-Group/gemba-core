import { cn } from '@/lib/utils';
import type { WorkItem } from '@/types/core.gen';
import { listSourceOptions, SOURCE_ALL, type SourceID } from './source';

// SourceFilterBar is the prominent "which project am I looking at" row.
//
// It is buttons rather than a dropdown because on a federated board this
// is the first question rather than a refinement, and because the counts
// are the useful part: a dropdown hides them until it is opened, which
// is exactly when they stop being an overview. Every button carries the
// number of items it would leave on the board, counted before any
// narrowing, so the bar reads as a breakdown of the whole board rather
// than of the current view.
//
// Repository buttons sit under their source, indented, because a
// repository only means something inside the org that owns it.
export interface SourceFilterBarProps {
  items: WorkItem[];
  value: SourceID;
  onChange: (next: SourceID) => void;
}

export function SourceFilterBar({ items, value, onChange }: SourceFilterBarProps) {
  const options = listSourceOptions(items);
  // A board whose items declare no source has no axis to offer, and a
  // bar holding only an "All" button is chrome that does nothing.
  if (options.length === 0) return null;

  const sources = options.filter((o) => o.kind !== 'repo');
  const active = options.find((o) => o.id === value);
  // Repositories are shown for the selected source only. Showing every
  // repository of every source at once is the same wall of names the
  // bar exists to cut through.
  const selectedSource = active?.kind === 'repo' ? active.parent : active?.id;
  const repos = options.filter((o) => o.kind === 'repo' && o.parent === selectedSource);

  return (
    <div
      data-testid="source-filter-bar"
      className="flex flex-col gap-1 border-b border-neutral-200 bg-white/50 px-4 py-1.5 dark:border-neutral-800 dark:bg-neutral-950/50"
    >
      <div className="flex flex-wrap items-center gap-1">
        <span className="mr-1 text-[11px] font-medium uppercase tracking-wide text-neutral-500">
          Project
        </span>
        {sources.map((option) => (
          <SourceButton
            key={option.id}
            id={option.id}
            label={option.label}
            count={option.count}
            stale={option.freshness === 'stale' || option.freshness === 'unknown'}
            selected={option.id === value || (active?.kind === 'repo' && option.id === selectedSource)}
            exact={option.id === value}
            onClick={() => onChange(option.id)}
          />
        ))}
      </div>
      {repos.length > 0 && (
        <div className="flex flex-wrap items-center gap-1 pl-[52px]">
          {repos.map((option) => (
            <SourceButton
              key={option.id}
              id={option.id}
              label={option.label}
              count={option.count}
              selected={option.id === value}
              exact={option.id === value}
              onClick={() => onChange(option.id === value ? (selectedSource ?? SOURCE_ALL) : option.id)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

interface SourceButtonProps {
  id: string;
  label: string;
  count: number;
  selected: boolean;
  exact: boolean;
  stale?: boolean;
  onClick: () => void;
}

function SourceButton({ id, label, count, selected, exact, stale, onClick }: SourceButtonProps) {
  return (
    <button
      type="button"
      data-testid={`source-filter-${id}`}
      data-selected={exact ? 'true' : undefined}
      aria-pressed={exact}
      onClick={onClick}
      title={stale ? `${label}: the board behind this count is stale` : undefined}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors',
        exact
          ? 'border-cyan-400/70 bg-cyan-50 font-semibold text-cyan-950 dark:bg-cyan-950/60 dark:text-cyan-50'
          : selected
            ? 'border-cyan-300/50 bg-cyan-50/40 text-cyan-900 dark:bg-cyan-950/30 dark:text-cyan-100'
            : 'border-neutral-200 text-neutral-600 hover:bg-neutral-100 dark:border-neutral-800 dark:text-neutral-400 dark:hover:bg-neutral-900'
      )}
    >
      <span>{label}</span>
      {/* The count is the reason the bar is buttons rather than a menu,
          so it is always rendered, including when it is zero. */}
      <span
        data-testid={`source-filter-count-${id}`}
        className="rounded-full bg-neutral-200/70 px-1.5 text-[10px] font-medium tabular-nums text-neutral-700 dark:bg-neutral-800 dark:text-neutral-300"
      >
        {count}
      </span>
      {stale && (
        <span aria-label="stale" title="stale" className="text-amber-600 dark:text-amber-400">
          ●
        </span>
      )}
    </button>
  );
}
