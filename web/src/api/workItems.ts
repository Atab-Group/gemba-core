// Typed data-access functions for the /api/work-items surface (gm-xgm / M1.6).
//
// These are thin wrappers over apiFetch — React Query hooks in
// src/hooks/useWorkItems.ts own caching, invalidation, and retry. Keep this
// module side-effect-free so it composes cleanly with any query client
// (including tests that mount the hooks with a fresh QueryClient per run).

import { apiFetch } from './client';
import type { ActivityPage, AgentRef, DefinitionOfDone, WorkItem } from '@/types/core.gen';

// ListWorkItemsEnvelope is the wire shape the gm-peg list handler emits.
// The server normalises nil slices so `items` is always a JSON array,
// never null.
//
// `total` is the length of THIS page, not the size of the filtered set.
// The handler asks the adaptor for one item more than the page needs and
// no more, which is what makes `has_more` exact, and it is also why the
// handler cannot know a pre-pagination count without an unbounded read
// on every request. Use `has_more` to decide whether to keep walking and
// `/api/work-summary` for a count of the whole board; a caller that
// treats `total` as the size of the set will read a page size instead.
export interface ListWorkItemsEnvelope {
  items: WorkItem[];
  // total is the length of this page. See the note above: it is not the
  // size of the filtered set.
  total: number;
  // offset is the index this page started at, echoed back so a caller
  // walking the list does not have to track it itself.
  offset?: number;
  // has_more says whether a page follows this one. The handler asks the
  // adaptor for one item more than the page needs to decide it, so it is
  // exact: a page that comes back exactly full is otherwise
  // indistinguishable from the last one.
  has_more?: boolean;
}

// WorkItemListFilter mirrors core.WorkItemFilter on the Go side.
// All fields optional; empty values are omitted from the query string
// so the server sees the canonical "no filter" shape.
export interface WorkItemListFilter {
  state_category?: WorkItem['state_category'][];
  kind?: string[];
  label?: string[];
  status?: string[];
  assignee_id?: string;
  sprint_id?: string;
  limit?: number;
  // offset is the index into the adaptor's ordering that a page starts
  // at. Set it only when walking pages by hand; listWorkItems() walks
  // them for you.
  offset?: number;
  // gm-e12.22.1: workflow-template + wisp opt-ins. Default behavior
  // hides templates and wisps from work surfaces (Plan / Backlog /
  // Sessions / Sprints). The Workflow Library opts in via
  // include_templates; the Active runs tab opts in via include_wisps.
  include_templates?: boolean;
  include_wisps?: boolean;
  // gm-g5xz.1: ISO8601 cutoff for the /recent view. Items created
  // before this timestamp are filtered out at the bd / dolt layer.
  // Accepts RFC3339 (Date.prototype.toISOString output) or YYYY-MM-DD.
  created_since?: string;
}

// buildListQuery turns the filter into a URLSearchParams. Multi-valued
// fields are emitted as repeated params (`?kind=task&kind=epic`) —
// the server accepts both repeated and CSV, but repeated is the
// canonical wire shape since it round-trips cleanly through URL
// builders without ambiguous comma handling.
function buildListQuery(filter?: WorkItemListFilter): string {
  if (!filter) return '';
  const p = new URLSearchParams();
  for (const v of filter.state_category ?? []) p.append('state_category', v);
  for (const v of filter.kind ?? []) p.append('kind', v);
  for (const v of filter.label ?? []) p.append('label', v);
  for (const v of filter.status ?? []) p.append('status', v);
  if (filter.assignee_id) p.set('assignee_id', filter.assignee_id);
  if (filter.sprint_id) p.set('sprint_id', filter.sprint_id);
  if (filter.limit != null) p.set('limit', String(filter.limit));
  // A zero offset is the default, so it is left off the wire: the first
  // page of a walk should look exactly like an unpaginated request.
  if (filter.offset) p.set('offset', String(filter.offset));
  if (filter.include_templates) p.set('include_templates', 'true');
  if (filter.include_wisps) p.set('include_wisps', 'true');
  if (filter.created_since) p.set('created_since', filter.created_since);
  const qs = p.toString();
  return qs ? `?${qs}` : '';
}

// listWorkItems — GET /api/work-items. The handler returns the {items,total}
// envelope (gm-peg); this helper unwraps to a bare array so callers
// can treat listWorkItems like a query. Accepts an optional filter
// (gm-e12.9.1); omit for the unfiltered list. Use
// listWorkItemsEnvelope() below when the caller also needs `total`.
// maxListPages bounds the walk. At the server's default page size this
// is far more work than any board can usefully render, and it is here so
// a server that answered has_more forever could not spin the tab
// indefinitely. Hitting it means something is wrong with paging, not
// that the workspace is large.
const maxListPages = 50;

// listWorkItems — GET /api/work-items, walking every page.
//
// The server caps a page at a default limit, and the board buckets items
// into columns, so it needs all of them: stopping at the first page
// silently drops whatever sorts last, which on a multi-repository source
// is entire repositories rather than a thin tail. A caller that wants
// one bounded page passes an explicit limit and gets exactly that.
export async function listWorkItems(filter?: WorkItemListFilter): Promise<WorkItem[]> {
  // An explicit limit means the caller asked for a bounded read, so it
  // is answered literally rather than walked.
  if (filter?.limit != null || filter?.offset) {
    const env = await apiFetch<ListWorkItemsEnvelope>(`/work-items${buildListQuery(filter)}`);
    return env.items ?? [];
  }

  const all: WorkItem[] = [];
  let offset = 0;
  for (let page = 0; page < maxListPages; page++) {
    const env = await apiFetch<ListWorkItemsEnvelope>(
      `/work-items${buildListQuery({ ...filter, offset })}`
    );
    const items = env.items ?? [];
    all.push(...items);
    // A server predating has_more omits it, and one page is what it
    // would have returned anyway.
    if (!env.has_more || items.length === 0) break;
    offset += items.length;
  }
  return all;
}

// listWorkItemsEnvelope — same fetch, but surfaces the full envelope for
// callers that need the raw envelope. Note that `total` is this page's
// length rather than the size of the set.
export async function listWorkItemsEnvelope(
  filter?: WorkItemListFilter
): Promise<ListWorkItemsEnvelope> {
  const env = await apiFetch<ListWorkItemsEnvelope>(`/work-items${buildListQuery(filter)}`);
  return { items: env.items ?? [], total: env.total ?? 0 };
}

// getWorkItem — GET /api/work-items/{id}. Returns one WorkItem with its full
// Relationship graph plus any adaptor-native edges under
// Custom["beads:dependencies"] (see internal/server/work_items.go, gm-kn2).
// Throws ApiError with status 404 / code "session_not_found" when the
// id is unknown.
export async function getWorkItem(id: string): Promise<WorkItem> {
  if (!id) {
    throw new Error('getWorkItem: id is required');
  }
  return apiFetch<WorkItem>(`/work-items/${encodeURIComponent(id)}`);
}

// getWorkItemActivity — GET /api/work-items/{id}/activity, one backwards
// page of the item's real history.
//
// This is not the same data as the bounded comment tail the projection
// carries on the card. That tail exists for lease and evidence
// detection; this is the backend's own timeline, read on demand. The
// page reports has_older and at_oldest separately, and a renderer must
// use at_oldest, not an empty next cursor, before telling anyone they
// have seen the whole history.
//
// Throws ApiError with status 501 / code "unsupported" when the bound
// adaptor keeps no history. That is a different answer from an item
// nobody has touched, and callers must render it differently.
export async function getWorkItemActivity(
  id: string,
  opts?: { before?: string; limit?: number }
): Promise<ActivityPage> {
  if (!id) {
    throw new Error('getWorkItemActivity: id is required');
  }
  const params = new URLSearchParams();
  if (opts?.before) params.set('before', opts.before);
  if (opts?.limit) params.set('limit', String(opts.limit));
  const qs = params.toString();
  const page = await apiFetch<ActivityPage>(
    `/work-items/${encodeURIComponent(id)}/activity${qs ? `?${qs}` : ''}`
  );
  return { ...page, events: page.events ?? [] };
}

// WorkItemPatch mirrors the Go shape (internal/core/workplane.go).
// Every field is optional; the wire encoding is `omitempty`-friendly,
// so undefined fields stay out of the JSON body and the adaptor
// treats unset == "no change".
export interface WorkItemPatch {
  title?: string;
  description?: string;
  status?: string;
  state_category?: WorkItem['state_category'];
  priority?: number | null;
  labels?: string[];
  owner?: AgentRef | null;
  assignee?: AgentRef | null;
  sprint_id?: string | null;
  // parent_id re-parents the work item via its parent_child edge.
  // Three-state sentinel (gm-gsbj) matching the Go *string on the
  // server: undefined / omitted = no change; "" (empty string) =
  // clear (orphan); non-empty string = reparent under that id.
  // JSON null is NOT used — it decodes to a nil Go pointer, which is
  // the "no change" sentinel, not "clear".
  parent_id?: string;
  dod?: DefinitionOfDone | null;
  // custom is sent as the full map the adaptor should persist. The
  // drawer's Extensions editor (gm-root.13) writes a single key by
  // spreading the existing item.custom and overwriting one entry, so
  // adaptors that treat the field as a replacement don't lose the
  // rest of the extension map.
  custom?: Record<string, unknown>;
}

// Confirm header MUST match internal/server/nonce.go ConfirmHeader.
// Kept on the api/ side so every mutation route uses the same token
// without duplicating the literal across hooks.
export const CONFIRM_HEADER = 'X-GEMBA-Confirm';

// CreateWorkItemInput is the caller-facing shape for createWorkItem.
// Mirrors the fields the boundary decoder (transport.DecodeCreateWorkItem)
// accepts: required kind/title/status/state_category + optional
// description, priority, labels, assignee, relationships. Server-assigned
// fields (id, timestamps) are not accepted here — the decoder rejects
// them if sent.
export interface CreateWorkItemInput {
  kind: string;
  title: string;
  status: string;
  state_category: WorkItem['state_category'];
  description?: string;
  priority?: number | null;
  labels?: string[];
  assignee?: AgentRef | null;
  // Parent is encoded as a parent_child Relationship with To="" because
  // the new bead's id isn't known until the adaptor assigns it. The bd
  // adaptor translates this to `bd create --parent <id>`; adaptors that
  // set parent differently will receive the same relationship and can
  // translate as they see fit.
  relationships?: { kind: string; from: string; to: string }[];
}

// createWorkItem — POST /api/work-items (gm-e12.10). Nonce-gated like
// updateWorkItem; a caller passing the same nonce twice gets the same
// cached 201 envelope back, not a duplicate bead.
//
// Server response is the materialized WorkItem with its backend-assigned
// id and timestamps. Callers should pipe the return value into the
// react-query cache (the useCreateWorkItem hook does this).
export async function createWorkItem(
  input: CreateWorkItemInput,
  opts: { nonce?: string } = {}
): Promise<WorkItem> {
  if (!input.title) {
    throw new Error('createWorkItem: title is required');
  }
  return apiFetch<WorkItem>('/work-items', {
    method: 'POST',
    body: JSON.stringify({ item: input }),
    headers: {
      'Content-Type': 'application/json',
      [CONFIRM_HEADER]: opts.nonce ?? freshNonce(),
    },
  });
}

// updateWorkItem — PATCH /api/work-items/{id}. Generates a fresh UUID nonce
// per call so a SPA double-click can't double-apply (the server
// caches replays per nonce and returns the cached envelope verbatim).
// Caller can pass an explicit `nonce` for retry semantics — useful
// when a request is in-flight and the network drops; resending with
// the same nonce is idempotent.
export async function updateWorkItem(
  id: string,
  patch: WorkItemPatch,
  opts: { nonce?: string } = {}
): Promise<WorkItem> {
  if (!id) {
    throw new Error('updateWorkItem: id is required');
  }
  return apiFetch<WorkItem>(`/work-items/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
    headers: {
      'Content-Type': 'application/json',
      [CONFIRM_HEADER]: opts.nonce ?? freshNonce(),
    },
  });
}

// deleteWorkItem — DELETE /api/work-items/{id}. This is a hard-delete
// administration action for Beads-capable modes. Closing a completed
// work item remains a PATCH state transition.
export async function deleteWorkItem(
  id: string,
  opts: { nonce?: string } = {}
): Promise<WorkItem> {
  if (!id) {
    throw new Error('deleteWorkItem: id is required');
  }
  return apiFetch<WorkItem>(`/work-items/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: {
      [CONFIRM_HEADER]: opts.nonce ?? freshNonce(),
    },
  });
}

export interface CascadeDispatchRequest {
  agent_type: string;
  limit?: number;
}

export interface CascadeDispatchResponse {
  wrapper_id: string;
  staged?: string[];
  dispatched: Array<{ work_item_id: string; session_id?: string }>;
  blocked?: string[];
  skipped?: string[];
  errors?: Array<{ work_item_id: string; message: string }>;
  limit?: number;
}

export async function cascadeDispatchWorkItem(
  id: string,
  body: CascadeDispatchRequest,
  opts: { nonce?: string } = {}
): Promise<CascadeDispatchResponse> {
  if (!id) {
    throw new Error('cascadeDispatchWorkItem: id is required');
  }
  return apiFetch<CascadeDispatchResponse>(
    `/work-items/${encodeURIComponent(id)}/cascade-dispatch`,
    {
      method: 'POST',
      body: JSON.stringify(body),
      headers: {
        'Content-Type': 'application/json',
        [CONFIRM_HEADER]: opts.nonce ?? freshNonce(),
      },
    }
  );
}

// freshNonce returns a UUID-like opaque token. crypto.randomUUID is
// available in every browser the SPA targets and in jsdom; the
// fallback covers older test environments without crypto wired up.
function freshNonce(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  // Math.random fallback — only the test environment hits this.
  return `nonce-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}
