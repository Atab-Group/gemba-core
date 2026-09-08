// Activity feed model: the pure half of the history panel.
//
// The panel's whole job is to be honest about what it is showing. Three
// facts have to survive from the server to the screen and none of them
// can be inferred from the events themselves:
//
//   - Whether the reader is looking at the whole history, or at the most
//     recent page of one. The card's own comment tail is a bounded read
//     for lease detection, and a reader who mistook either for a
//     complete record would draw conclusions from silence.
//   - Whether an empty feed means "nobody has touched this" or "the
//     backend would not answer". Those look identical if a failure is
//     rendered as emptiness.
//   - Whether the backend keeps a history at all. An adaptor with no
//     event log answers 501, which is neither of the above.

import { ApiError } from '@/api/client';
import type { ActivityEvent, ActivityPage } from '@/types/core.gen';

/** ActivityState is what the panel renders, and why. */
export type ActivityStatus =
  | 'loading'
  | 'ready'
  | 'loading-more'
  /** unsupported: the bound adaptor keeps no history. */
  | 'unsupported'
  | 'error';

export interface ActivityFeed {
  status: ActivityStatus;
  /** events are newest first, which is the end a history is read from. */
  events: ActivityEvent[];
  /** cursor pages one step further back. Null when there is no more. */
  cursor: string | null;
  /** atOldest is the only licence to say the reader has seen everything. */
  atOldest: boolean;
  /** total is the backend's own count, or null when it reports none. */
  total: number | null;
  /** source is the configured source that answered. */
  source: string | null;
  /**
   * freshness describes the board beside the history, not the history
   * itself: the page was read live. A stale board next to a live feed is
   * worth saying out loud.
   */
  freshness: string | null;
  /** reason is the human sentence for `unsupported` and `error`. */
  reason: string | null;
}

export const emptyFeed: ActivityFeed = {
  status: 'loading',
  events: [],
  cursor: null,
  atOldest: false,
  total: null,
  source: null,
  freshness: null,
  reason: null,
};

/**
 * mergePage folds one server page into the feed.
 *
 * The server returns a page oldest-first and pages backwards, so each
 * page is reversed and appended: the result stays newest-first however
 * many pages have been pulled. Ids already present are dropped, because
 * a refresh landing between two page reads can repeat an event across a
 * boundary and a duplicated comment reads as somebody saying it twice.
 */
export function mergePage(feed: ActivityFeed, page: ActivityPage): ActivityFeed {
  const seen = new Set(feed.events.map((e) => e.id));
  const older = [...(page.events ?? [])].reverse().filter((e) => {
    if (!e.id || seen.has(e.id)) return false;
    seen.add(e.id);
    return true;
  });
  return {
    status: 'ready',
    events: [...feed.events, ...older],
    // has_older and older_cursor are separate on the wire, and a cursor
    // is only followed when the server said there is something behind
    // it. Following a stale cursor would loop on the same page.
    cursor: page.has_older && page.older_cursor ? page.older_cursor : null,
    atOldest: page.at_oldest === true,
    total: typeof page.total === 'number' && page.total > 0 ? page.total : feed.total,
    source: page.source ?? feed.source,
    freshness: page.freshness ?? feed.freshness,
    reason: null,
  };
}

/**
 * failureOf turns a thrown error into a feed state.
 *
 * A 501 is its own status rather than an error, because "this adaptor
 * keeps no history" is a fact about the deployment and not a fault the
 * reader should retry.
 */
export function failureOf(feed: ActivityFeed, err: unknown): ActivityFeed {
  if (err instanceof ApiError && err.status === 501) {
    return {
      ...feed,
      status: 'unsupported',
      reason: 'This board reads GitHub through an adaptor that does not fetch issue history.',
    };
  }
  return { ...feed, status: 'error', reason: describeFailure(err) };
}

/**
 * describeFailure says what went wrong in the reader's terms, and never
 * echoes a backend body: a permission failure that quoted GitHub would
 * put the shape of a repository the reader cannot see into a page they
 * can.
 */
export function describeFailure(err: unknown): string {
  if (!(err instanceof ApiError)) {
    return 'The history could not be read.';
  }
  switch (err.code) {
    case 'rate_limited':
      return 'GitHub is throttling this credential, so the history could not be read. It will answer again once the budget resets.';
    case 'capability_denied':
      return 'This deployment is not authorised to read the history of this item.';
    case 'session_not_found':
      return 'This item is not on any source this board reads.';
    case 'adaptor_degraded':
    case 'adaptor_not_configured':
      return 'The source behind this item is unreadable right now, so its history is unavailable.';
    case 'network':
      return 'The dashboard could not reach its own API.';
    default:
      return 'The history could not be read.';
  }
}

/**
 * sentenceFor is the line rendered beside an actor's name.
 *
 * The adaptor supplies a summary for every non-comment event, including
 * ones this build has no icon for, so the fallback here is a last resort
 * rather than the normal path.
 */
export function sentenceFor(event: ActivityEvent): string {
  if (event.kind === 'comment') return 'commented';
  if (event.summary) return event.summary;
  return event.kind === 'other' ? 'did something this board does not name' : event.kind;
}

/** actorOf names who acted, or says plainly that the backend withheld it. */
export function actorOf(event: ActivityEvent): string {
  return event.actor || 'Someone';
}

/**
 * completenessOf is the one sentence that stops a bounded read being
 * mistaken for a complete one.
 */
export function completenessOf(feed: ActivityFeed): string {
  if (feed.atOldest) {
    return feed.events.length === 1
      ? 'This is the whole history: 1 event.'
      : `This is the whole history: ${feed.events.length} events.`;
  }
  if (feed.total && feed.total > feed.events.length) {
    return `Showing the most recent ${feed.events.length} of ${feed.total} events. Older history has not been loaded.`;
  }
  return `Showing the most recent ${feed.events.length} events. Older history has not been loaded.`;
}
