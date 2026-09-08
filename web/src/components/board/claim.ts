// Claim model for the board chrome.
//
// The claim is ATAB's answer to "is a worker holding this right now",
// and it is deliberately not the GitHub assignee. The assignee says who
// owns the issue; the claim says whether something is running against it
// this minute. A worker that dies leaves its assignee behind while its
// lease expires, and that difference is the only thing that separates a
// stalled claim from a live one, so the two are rendered as separate
// fields rather than one "who has this" column.

import type { WorkItem } from '@/types/core.gen';

export type ClaimState = 'active' | 'stale' | 'expired' | 'claiming' | 'none' | 'unknown';

export interface Claim {
  state: ClaimState;
  holder?: string;
  instance?: string;
  expires?: string;
  last_heartbeat?: string;
  released?: boolean;
  reason?: string;
}

const HELD: ReadonlySet<ClaimState> = new Set<ClaimState>(['active', 'stale', 'claiming']);

/** claimOf reads the projected claim off an item, if the adaptor set one. */
export function claimOf(item: WorkItem): Claim | null {
  const raw = (item.custom ?? {})['atab_claim'];
  if (!raw || typeof raw !== 'object') return null;
  const claim = raw as Claim;
  return claim.state ? claim : null;
}

/** claimState is the one-token form, for a chip or a table cell. */
export function claimState(item: WorkItem): ClaimState | null {
  const value = (item.custom ?? {})['atab_claim_state'];
  return typeof value === 'string' ? (value as ClaimState) : null;
}

/** claimedBy names the holder, or null when nothing holds the issue. */
export function claimedBy(item: WorkItem): string | null {
  const value = (item.custom ?? {})['atab_claimed_by'];
  return typeof value === 'string' && value !== '' ? value : null;
}

/** isHeld reports whether the claim should stop somebody else picking it up. */
export function isHeld(state: ClaimState | null): boolean {
  return state !== null && HELD.has(state);
}

// claimLabel is the short text a column or chip shows. "none" renders as
// nothing rather than as the word: an empty cell already says nobody has
// it, and filling every unclaimed row with "none" would bury the few
// rows that do carry a holder.
export function claimLabel(item: WorkItem): string {
  const state = claimState(item);
  if (state === null || state === 'none') return '';
  const holder = claimedBy(item);
  switch (state) {
    case 'active':
      return holder ?? 'held';
    case 'stale':
      return holder ? `${holder} (quiet)` : 'quiet';
    case 'expired':
      return holder ? `${holder} (expired)` : 'expired';
    case 'claiming':
      return holder ? `${holder} (claiming)` : 'claiming';
    case 'unknown':
      return 'unknown';
    default:
      return '';
  }
}

// claimTitle is the hover text: the whole protocol answer in a sentence,
// because the column has room for a name and the detail matters when
// somebody is deciding whether to take an issue off a dead worker.
export function claimTitle(item: WorkItem): string | undefined {
  const claim = claimOf(item);
  if (!claim || claim.state === 'none') return undefined;

  const parts: string[] = [];
  if (claim.holder) parts.push(`Holder: ${claim.holder}`);
  if (claim.instance) parts.push(`Instance: ${claim.instance}`);
  if (claim.expires) parts.push(`Lease expires: ${claim.expires}`);
  if (claim.last_heartbeat) parts.push(`Last heartbeat: ${claim.last_heartbeat}`);
  if (claim.released) parts.push('Previously released by the sweep');
  if (claim.reason) parts.push(claim.reason);
  return parts.length > 0 ? parts.join('\n') : undefined;
}

// claimTone maps a state onto the palette. Expired and stale are warned
// about rather than coloured like a healthy claim, because both mean the
// issue is probably not being worked despite looking taken.
export function claimTone(state: ClaimState | null): string {
  switch (state) {
    case 'active':
      return 'text-emerald-700 dark:text-emerald-400';
    case 'claiming':
      return 'text-cyan-700 dark:text-cyan-400';
    case 'stale':
      return 'text-amber-700 dark:text-amber-400';
    case 'expired':
      return 'text-rose-700 dark:text-rose-400';
    case 'unknown':
      return 'text-neutral-500 dark:text-neutral-400 italic';
    default:
      return 'text-neutral-500 dark:text-neutral-400';
  }
}
