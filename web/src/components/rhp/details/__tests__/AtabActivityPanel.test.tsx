import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';

import { ApiError } from '@/api/client';
import type { ActivityPage } from '@/types/core.gen';

const getWorkItemActivity = vi.fn();

vi.mock('@/api/workItems', () => ({
  getWorkItemActivity: (...args: unknown[]) => getWorkItemActivity(...args),
}));

import { AtabActivityPanel } from '../AtabActivityPanel';

function page(over: Partial<ActivityPage> = {}): ActivityPage {
  return { events: [], has_older: false, at_oldest: true, ...over } as ActivityPage;
}

beforeEach(() => {
  getWorkItemActivity.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('AtabActivityPanel', () => {
  it('renders the history newest first, with author and timestamp', async () => {
    getWorkItemActivity.mockResolvedValue(
      page({
        events: [
          { id: 'IC_1', kind: 'comment', actor: 'nic', at: '2026-09-01T09:00:00Z', body: 'the first word' },
          { id: 'CE_1', kind: 'closed', actor: 'atab-bot', at: '2026-09-01T10:00:00Z', summary: 'closed this' },
        ],
        source: 'atab-group',
        freshness: 'fresh',
      })
    );
    render(<AtabActivityPanel id="atab-group/Atab-Group~Repo/1" />);

    await screen.findByTestId('activity-feed');
    const rows = screen.getAllByTestId(/^activity-event-/);
    expect(rows.map((r) => r.getAttribute('data-testid'))).toEqual([
      'activity-event-CE_1',
      'activity-event-IC_1',
    ]);
    // getByText throws when absent, so reaching the assertion is the
    // assertion; the truthiness check keeps the intent readable.
    expect(screen.getByText('atab-bot')).toBeTruthy();
    expect(screen.getByText('closed this')).toBeTruthy();
    expect(screen.getByText('the first word')).toBeTruthy();
  });

  // The line that keeps a bounded read from being mistaken for a whole
  // history. Its absence is the failure this panel exists to prevent.
  it('says the history is complete only when the walk reached the beginning', async () => {
    getWorkItemActivity.mockResolvedValue(
      page({ events: [{ id: 'IC_1', kind: 'comment', actor: 'nic', at: '2026-09-01T09:00:00Z' }] })
    );
    render(<AtabActivityPanel id="a/1" />);
    await waitFor(() =>
      expect(screen.getByTestId('activity-completeness').textContent).toMatch(/whole history/)
    );
    expect(screen.queryByTestId('activity-load-older')).toBeNull();
  });

  it('offers older history and appends the next page on click', async () => {
    getWorkItemActivity
      .mockResolvedValueOnce(
        page({
          events: [{ id: 'IC_2', kind: 'comment', actor: 'nic', at: '2026-09-01T10:00:00Z' }],
          has_older: true,
          at_oldest: false,
          older_cursor: 'CUR',
          total: 2,
        })
      )
      .mockResolvedValueOnce(
        page({ events: [{ id: 'IC_1', kind: 'comment', actor: 'nic', at: '2026-09-01T09:00:00Z' }] })
      );

    render(<AtabActivityPanel id="a/1" />);
    const button = await screen.findByTestId('activity-load-older');
    expect(screen.getByTestId('activity-completeness').textContent).toMatch(/1 of 2/);

    fireEvent.click(button);

    await waitFor(() => expect(screen.getAllByTestId(/^activity-event-/)).toHaveLength(2));
    expect(getWorkItemActivity).toHaveBeenLastCalledWith('a/1', { before: 'CUR', limit: 30 });
    expect(screen.getByTestId('activity-completeness').textContent).toMatch(/whole history/);
    expect(screen.queryByTestId('activity-load-older')).toBeNull();
  });

  // A backend that keeps no history is neither an empty history nor a
  // fault, and it must not be rendered as either.
  it('says plainly when the adaptor keeps no history', async () => {
    getWorkItemActivity.mockRejectedValue(new ApiError(501, 'unsupported', 'no history'));
    render(<AtabActivityPanel id="a/1" />);
    const panel = await screen.findByTestId('activity-unsupported');
    expect(panel.textContent).toMatch(/does not fetch issue history/);
    expect(screen.queryByTestId('activity-empty')).toBeNull();
  });

  // A throttled read must never render as "nothing has happened".
  it('shows a throttle as a failure rather than as an empty history', async () => {
    getWorkItemActivity.mockRejectedValue(new ApiError(500, 'rate_limited', 'throttled'));
    render(<AtabActivityPanel id="a/1" />);
    const panel = await screen.findByTestId('activity-error');
    expect(panel.textContent).toMatch(/throttling/);
    expect(screen.queryByTestId('activity-empty')).toBeNull();
  });

  it('distinguishes an item nobody has touched', async () => {
    getWorkItemActivity.mockResolvedValue(page());
    render(<AtabActivityPanel id="a/1" />);
    await screen.findByTestId('activity-empty');
    expect(screen.queryByTestId('activity-error')).toBeNull();
  });

  // A live history beside a stale card is worth saying out loud: the two
  // disagree, and the reader has no other way to tell which is which.
  it('warns when the board beside the history is stale', async () => {
    getWorkItemActivity.mockResolvedValue(
      page({
        events: [{ id: 'IC_1', kind: 'comment', actor: 'nic', at: '2026-09-01T09:00:00Z' }],
        freshness: 'stale',
      })
    );
    render(<AtabActivityPanel id="a/1" />);
    const notice = await screen.findByTestId('activity-stale-board');
    expect(notice.textContent).toMatch(/stale board/);
  });

  // A reader clicking through cards fast would otherwise fold a late
  // response for the previous item into the feed now on screen.
  it('discards a response for an item that is no longer open', async () => {
    let resolveFirst: (p: ActivityPage) => void = () => {};
    getWorkItemActivity
      .mockImplementationOnce(
        () =>
          new Promise<ActivityPage>((resolve) => {
            resolveFirst = resolve;
          })
      )
      .mockResolvedValueOnce(
        page({ events: [{ id: 'IC_2', kind: 'comment', actor: 'nic', at: '2026-09-01T11:00:00Z' }] })
      );

    const { rerender } = render(<AtabActivityPanel id="a/1" />);
    rerender(<AtabActivityPanel id="a/2" />);
    resolveFirst(
      page({ events: [{ id: 'IC_1', kind: 'comment', actor: 'nic', at: '2026-09-01T09:00:00Z' }] })
    );

    await screen.findByTestId('activity-event-IC_2');
    expect(screen.queryByTestId('activity-event-IC_1')).toBeNull();
  });

  it('links each event to its record on GitHub', async () => {
    getWorkItemActivity.mockResolvedValue(
      page({
        events: [
          {
            id: 'IC_1',
            kind: 'comment',
            actor: 'nic',
            at: '2026-09-01T09:00:00Z',
            url: 'https://github.com/Atab-Group/Repo/issues/1#issuecomment-1',
          },
        ],
      })
    );
    render(<AtabActivityPanel id="a/1" />);
    const link = await screen.findByText('on GitHub');
    expect(link.getAttribute('href')).toBe(
      'https://github.com/Atab-Group/Repo/issues/1#issuecomment-1'
    );
    expect(link.getAttribute('rel')).toMatch(/noreferrer/);
  });
});
