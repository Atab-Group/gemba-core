import { describe, expect, it } from 'vitest';

import { ApiError } from '@/api/client';
import type { ActivityEvent, ActivityPage } from '@/types/core.gen';

import {
  completenessOf,
  describeFailure,
  emptyFeed,
  failureOf,
  mergePage,
  sentenceFor,
} from '../activity';

function event(id: string, at: string, over: Partial<ActivityEvent> = {}): ActivityEvent {
  return { id, kind: 'comment', at, ...over } as ActivityEvent;
}

function page(over: Partial<ActivityPage> = {}): ActivityPage {
  return {
    events: [],
    has_older: false,
    at_oldest: true,
    ...over,
  } as ActivityPage;
}

describe('mergePage', () => {
  // The server pages backwards and returns each page oldest-first, so
  // the feed has to reverse and append to stay newest-first however many
  // pages have been pulled.
  it('keeps the feed newest first across pages', () => {
    const first = mergePage(
      emptyFeed,
      page({
        events: [event('c', '2026-09-01T09:00:00Z'), event('d', '2026-09-01T10:00:00Z')],
        has_older: true,
        at_oldest: false,
        older_cursor: 'CUR',
      })
    );
    expect(first.events.map((e) => e.id)).toEqual(['d', 'c']);

    const second = mergePage(
      first,
      page({
        events: [event('a', '2026-09-01T07:00:00Z'), event('b', '2026-09-01T08:00:00Z')],
        at_oldest: true,
      })
    );
    expect(second.events.map((e) => e.id)).toEqual(['d', 'c', 'b', 'a']);
  });

  // A refresh landing between two page reads can repeat an event across
  // a boundary, and a duplicated comment reads as somebody saying it
  // twice.
  it('drops an event already in the feed', () => {
    const first = mergePage(emptyFeed, page({ events: [event('a', '2026-09-01T09:00:00Z')] }));
    const second = mergePage(
      first,
      page({ events: [event('a', '2026-09-01T09:00:00Z'), event('b', '2026-09-01T08:00:00Z')] })
    );
    expect(second.events.map((e) => e.id)).toEqual(['a', 'b']);
  });

  // A cursor is only followed when the server said there is something
  // behind it. Following a stale cursor would loop on the same page.
  it('only carries a cursor the server backed with has_older', () => {
    const withOlder = mergePage(emptyFeed, page({ has_older: true, older_cursor: 'CUR' }));
    expect(withOlder.cursor).toBe('CUR');

    const stale = mergePage(emptyFeed, page({ has_older: false, older_cursor: 'CUR' }));
    expect(stale.cursor).toBeNull();
  });

  it('carries the source and the board freshness', () => {
    const feed = mergePage(emptyFeed, page({ source: 'atab-group', freshness: 'stale' }));
    expect(feed.source).toBe('atab-group');
    expect(feed.freshness).toBe('stale');
  });

  // A page length is never a total, so a page reporting none must not
  // overwrite a total an earlier page gave.
  it('keeps a total a later page does not report', () => {
    const first = mergePage(emptyFeed, page({ total: 12, has_older: true, older_cursor: 'C' }));
    const second = mergePage(first, page({ total: 0 }));
    expect(second.total).toBe(12);
  });
});

describe('failureOf', () => {
  // "This adaptor keeps no history" is a fact about the deployment, not
  // a fault to retry, so it is its own status.
  it('treats a 501 as unsupported rather than an error', () => {
    const feed = failureOf(emptyFeed, new ApiError(501, 'unsupported', 'no history'));
    expect(feed.status).toBe('unsupported');
    expect(feed.reason).toMatch(/does not fetch issue history/);
  });

  it('treats every other failure as an error', () => {
    const feed = failureOf(emptyFeed, new ApiError(500, 'rate_limited', 'throttled'));
    expect(feed.status).toBe('error');
  });

  it('keeps the events already loaded when a later page fails', () => {
    const loaded = mergePage(
      emptyFeed,
      page({ events: [event('a', '2026-09-01T09:00:00Z')], has_older: true, older_cursor: 'C' })
    );
    const failed = failureOf(loaded, new ApiError(500, 'rate_limited', 'throttled'));
    expect(failed.events).toHaveLength(1);
    expect(failed.status).toBe('error');
  });
});

describe('describeFailure', () => {
  it('names a throttle as a throttle', () => {
    expect(describeFailure(new ApiError(500, 'rate_limited', 'x'))).toMatch(/throttling/);
  });

  it('names a permission failure without echoing the backend', () => {
    const msg = describeFailure(
      new ApiError(500, 'capability_denied', 'repository Atab-Group/Secret-Client is private')
    );
    expect(msg).not.toMatch(/Secret-Client/);
    expect(msg).toMatch(/not authorised/);
  });

  it('has an answer for an unrecognised failure', () => {
    expect(describeFailure(new Error('boom'))).toMatch(/could not be read/);
  });
});

describe('completenessOf', () => {
  // The line that stops a bounded page being mistaken for a whole
  // history. Only at_oldest licenses the first form.
  it('claims completeness only when the walk reached the beginning', () => {
    const whole = mergePage(emptyFeed, page({ events: [event('a', '2026-09-01T09:00:00Z')] }));
    expect(completenessOf(whole)).toMatch(/whole history/);

    const partial = mergePage(
      emptyFeed,
      page({
        events: [event('a', '2026-09-01T09:00:00Z')],
        has_older: true,
        at_oldest: false,
        older_cursor: 'C',
        total: 40,
      })
    );
    expect(completenessOf(partial)).not.toMatch(/whole history/);
    expect(completenessOf(partial)).toMatch(/1 of 40/);
    expect(completenessOf(partial)).toMatch(/has not been loaded/);
  });
});

describe('sentenceFor', () => {
  it('uses the adaptor summary for a state event', () => {
    expect(sentenceFor(event('a', '2026-09-01T09:00:00Z', { kind: 'closed', summary: 'closed this' })))
      .toBe('closed this');
  });

  it('says "commented" for a comment', () => {
    expect(sentenceFor(event('a', '2026-09-01T09:00:00Z'))).toBe('commented');
  });

  it('is honest about an event this build does not name', () => {
    expect(sentenceFor(event('a', '2026-09-01T09:00:00Z', { kind: 'other', summary: '' })))
      .toMatch(/does not name/);
  });
});
