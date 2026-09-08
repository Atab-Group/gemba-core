// AtabActivityPanel renders the item's real backend history.
//
// It is deliberately not a React Query surface. Every other read here is
// a cached document the app re-renders from; this is a cursor walk whose
// state is "the pages this reader has pulled so far", which a cache
// keyed by id cannot express without either refetching the whole walk on
// every render or holding pages the reader never asked for. The walk is
// short-lived and belongs to the open panel, so it lives in the panel.

import { useCallback, useEffect, useRef, useState } from 'react';

import { getWorkItemActivity } from '@/api/workItems';
import { cn } from '@/lib/utils';
import type { ActivityEvent } from '@/types/core.gen';

import {
  actorOf,
  completenessOf,
  emptyFeed,
  failureOf,
  mergePage,
  sentenceFor,
  type ActivityFeed,
} from './activity';

// PAGE is one cursor step. Small enough that opening an item is cheap on
// a board whose backend charges a rate-limit budget per read, large
// enough that most items arrive whole in one page.
const PAGE = 30;

export interface AtabActivityPanelProps {
  id: string;
  /** onNavigate lets a cross-reference open the item it points at. */
  onNavigate?: (id: string) => void;
}

export function AtabActivityPanel({ id }: AtabActivityPanelProps) {
  const [feed, setFeed] = useState<ActivityFeed>(emptyFeed);
  // The walk belongs to one item. A reader clicking through cards fast
  // would otherwise fold a late response for the previous item into the
  // feed of the one now on screen.
  const requested = useRef(id);

  useEffect(() => {
    requested.current = id;
    setFeed(emptyFeed);
    let live = true;
    getWorkItemActivity(id, { limit: PAGE })
      .then((page) => {
        if (live && requested.current === id) setFeed((f) => mergePage(f, page));
      })
      .catch((err) => {
        if (live && requested.current === id) setFeed((f) => failureOf(f, err));
      });
    return () => {
      live = false;
    };
  }, [id]);

  const loadOlder = useCallback(() => {
    const cursor = feed.cursor;
    if (!cursor) return;
    setFeed((f) => ({ ...f, status: 'loading-more' }));
    getWorkItemActivity(id, { before: cursor, limit: PAGE })
      .then((page) => {
        if (requested.current === id) setFeed((f) => mergePage(f, page));
      })
      .catch((err) => {
        if (requested.current === id) setFeed((f) => failureOf(f, err));
      });
  }, [feed.cursor, id]);

  if (feed.status === 'loading') {
    return (
      <div data-testid="activity-loading" className="py-2 text-sm text-neutral-500">
        Reading the history from GitHub…
      </div>
    );
  }

  // "This adaptor keeps no history" is not a failure and not an empty
  // history. Rendering it as either would answer a question the board
  // cannot answer.
  if (feed.status === 'unsupported') {
    return (
      <div
        data-testid="activity-unsupported"
        className="rounded border border-neutral-200 bg-neutral-50 px-3 py-2 text-sm text-neutral-600 dark:border-neutral-800 dark:bg-neutral-900 dark:text-neutral-400"
      >
        {feed.reason}
      </div>
    );
  }

  if (feed.status === 'error' && feed.events.length === 0) {
    return (
      <div
        data-testid="activity-error"
        role="status"
        className="rounded border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-200"
      >
        {feed.reason}
      </div>
    );
  }

  return (
    <div data-testid="activity-feed" className="space-y-2">
      {feed.freshness && feed.freshness !== 'fresh' ? (
        <div
          data-testid="activity-stale-board"
          className="rounded border border-amber-300 bg-amber-50 px-2 py-1 text-xs text-amber-900 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-200"
        >
          The history below was read just now. The card beside it comes from a{' '}
          {feed.freshness} board.
        </div>
      ) : null}

      {feed.events.length === 0 ? (
        <div data-testid="activity-empty" className="py-2 text-sm text-neutral-500">
          Nothing has happened on this item since it was opened.
        </div>
      ) : (
        <ol className="space-y-2">
          {feed.events.map((event) => (
            <ActivityRow key={event.id} event={event} />
          ))}
        </ol>
      )}

      {/* The completeness line is the point of the panel. Without it a
          bounded page and a whole history look identical. */}
      <div data-testid="activity-completeness" className="pt-1 text-xs text-neutral-500">
        {completenessOf(feed)}
      </div>

      {feed.status === 'error' ? (
        <div
          data-testid="activity-page-error"
          role="status"
          className="text-xs text-amber-700 dark:text-amber-400"
        >
          {feed.reason}
        </div>
      ) : null}

      {feed.cursor ? (
        <button
          type="button"
          data-testid="activity-load-older"
          onClick={loadOlder}
          disabled={feed.status === 'loading-more'}
          className="rounded border border-neutral-300 px-2 py-1 text-xs text-neutral-700 hover:bg-neutral-100 disabled:opacity-60 dark:border-neutral-700 dark:text-neutral-300 dark:hover:bg-neutral-900"
        >
          {feed.status === 'loading-more' ? 'Loading…' : 'Load older history'}
        </button>
      ) : null}
    </div>
  );
}

function ActivityRow({ event }: { event: ActivityEvent }) {
  const comment = event.kind === 'comment';
  return (
    <li
      data-testid={`activity-event-${event.id}`}
      data-kind={event.kind}
      className={cn(
        'rounded border px-2 py-1.5 text-sm',
        comment
          ? 'border-neutral-200 bg-white dark:border-neutral-800 dark:bg-neutral-950'
          : 'border-transparent bg-neutral-50 dark:bg-neutral-900/50'
      )}
    >
      <div className="flex flex-wrap items-baseline gap-x-1.5 text-xs text-neutral-600 dark:text-neutral-400">
        <span className="font-medium text-neutral-800 dark:text-neutral-200">
          {actorOf(event)}
        </span>
        <span>{sentenceFor(event)}</span>
        <time dateTime={event.at} title={event.at} className="tabular-nums">
          {formatAt(event.at)}
        </time>
        {event.url ? (
          <a
            href={event.url}
            target="_blank"
            rel="noreferrer noopener"
            className="text-cyan-700 underline-offset-2 hover:underline dark:text-cyan-400"
          >
            on GitHub
          </a>
        ) : null}
      </div>
      {comment && event.body ? (
        // The body is rendered as text, not as markdown. It is arbitrary
        // content from an issue any org member can write, and this panel
        // is not the place to introduce a new HTML sink.
        <p className="mt-1 whitespace-pre-wrap break-words text-sm text-neutral-800 dark:text-neutral-200">
          {event.body}
        </p>
      ) : null}
    </li>
  );
}

// formatAt keeps the instant readable without pulling in a date library
// for one line. The full timestamp stays in the title attribute.
function formatAt(at: string): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return at;
  return d.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}
