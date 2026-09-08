import { describe, expect, it } from 'vitest';
import type { WorkItem } from '@/types/core.gen';
import { claimLabel, claimOf, claimState, claimedBy, claimTitle, isHeld } from '../claim';

function withClaim(claim: Record<string, unknown> | null, state?: string, holder?: string): WorkItem {
  const custom: Record<string, unknown> = {};
  if (claim) custom.atab_claim = claim;
  if (state) custom.atab_claim_state = state;
  if (holder) custom.atab_claimed_by = holder;
  return { id: 'a/1', kind: 'task', title: 'x', status: 'Todo', custom } as unknown as WorkItem;
}

describe('claim model', () => {
  it('reads the projected claim, state and holder', () => {
    const item = withClaim(
      { state: 'active', holder: 'bot-a', instance: 'rig-1', expires: '2026-09-08T12:00:00Z' },
      'active',
      'bot-a'
    );
    expect(claimOf(item)?.instance).toBe('rig-1');
    expect(claimState(item)).toBe('active');
    expect(claimedBy(item)).toBe('bot-a');
  });

  // An empty cell already says nobody has it. Filling every unclaimed
  // row with the word "none" would bury the few rows that do carry one.
  it('renders nothing for an unclaimed item', () => {
    expect(claimLabel(withClaim({ state: 'none' }, 'none'))).toBe('');
    expect(claimTitle(withClaim({ state: 'none' }, 'none'))).toBeUndefined();
  });

  // Held and not-held are the distinction that decides whether somebody
  // else may pick the issue up, so each state has to land on the right
  // side of it. Expired is the one that looks taken and is not.
  it('separates held states from the rest', () => {
    expect(isHeld('active')).toBe(true);
    expect(isHeld('stale')).toBe(true);
    expect(isHeld('claiming')).toBe(true);
    expect(isHeld('expired')).toBe(false);
    expect(isHeld('none')).toBe(false);
    expect(isHeld('unknown')).toBe(false);
    expect(isHeld(null)).toBe(false);
  });

  // A holder whose lease still stands but who has gone quiet is not the
  // same as one actively working, and the label has to say which.
  it('distinguishes a quiet holder from a working one', () => {
    expect(claimLabel(withClaim({ state: 'active' }, 'active', 'bot-a'))).toBe('bot-a');
    expect(claimLabel(withClaim({ state: 'stale' }, 'stale', 'bot-a'))).toBe('bot-a (quiet)');
    expect(claimLabel(withClaim({ state: 'expired' }, 'expired', 'bot-a'))).toBe('bot-a (expired)');
    expect(claimLabel(withClaim({ state: 'claiming' }, 'claiming', 'bot-a'))).toBe('bot-a (claiming)');
  });

  // A board too old to vouch for cannot name a holder, and saying so is
  // different from saying nobody has it.
  it('renders unknown for a board outside its freshness budget', () => {
    expect(claimLabel(withClaim({ state: 'unknown' }, 'unknown'))).toBe('unknown');
  });

  it('puts the whole protocol answer in the hover text', () => {
    const title = claimTitle(
      withClaim(
        {
          state: 'stale',
          holder: 'bot-a',
          instance: 'rig-1',
          expires: '2026-09-08T12:00:00Z',
          last_heartbeat: '2026-09-08T09:00:00Z',
          released: true,
          reason: 'the holder has stopped reporting',
        },
        'stale',
        'bot-a'
      )
    );
    expect(title).toContain('bot-a');
    expect(title).toContain('rig-1');
    expect(title).toContain('Last heartbeat');
    expect(title).toContain('Previously released');
  });

  // Another adaptor supplies none of these fields, and the chrome must
  // simply not render rather than break.
  it('is inert for an adaptor that reports no claim', () => {
    const plain = { id: 'gm-1', kind: 'task', title: 'x', status: 'open' } as unknown as WorkItem;
    expect(claimOf(plain)).toBeNull();
    expect(claimState(plain)).toBeNull();
    expect(claimLabel(plain)).toBe('');
  });
});
