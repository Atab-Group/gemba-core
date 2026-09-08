// GraphPage density tests: what the page hands React Flow at company
// scale.
//
// The bound that matters is the one applied before the canvas exists.
// The page used to render every filtered item, mount it, fit the camera,
// read the resulting zoom and only then decide the view was too dense —
// so the expensive render always happened, and the decision fed back
// into the thing that produced it. These tests assert on the counts the
// React Flow stub receives, which is the boundary the real renderer sits
// behind, and they assert them on every render rather than the settled
// one.
//
// The stub records what it was given and nothing else: node and edge
// counts, and a button per node so a click can be simulated. It renders
// no canvas, so a pass here says the page never asked for one.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import type { ReactNode } from 'react';
import type { WorkItem } from '@/types/core.gen';

let mountedNodeCounts: number[] = [];
let mountedEdgeCounts: number[] = [];
// fitViewCalls counts camera fits. The camera has to follow the picture,
// and the failure worth catching is the silent one: a view that changed
// without the camera noticing.
let fitViewCalls = 0;

vi.mock('reactflow', async () => {
  type StubNode = { id: string; data?: { id: string; title: string } };
  type StubInstance = {
    fitView: () => void;
    setCenter: (x: number, y: number, opts?: unknown) => void;
    getNode: (id: string) => StubNode | undefined;
    getViewport: () => { x: number; y: number; zoom: number };
  };
  type Props = {
    nodes: StubNode[];
    edges: { id: string; source: string; target: string }[];
    onNodeClick?: (e: unknown, node: StubNode) => void;
    onInit?: (instance: StubInstance) => void;
    children?: ReactNode;
  };
  function ReactFlow({ nodes, edges, onNodeClick, onInit, children }: Props) {
    mountedNodeCounts.push(nodes.length);
    mountedEdgeCounts.push(edges.length);
    const ref = (el: HTMLDivElement | null) => {
      if (el && onInit) {
        onInit({
          fitView: () => {
            fitViewCalls += 1;
          },
          setCenter: () => undefined,
          getNode: (id: string) => nodes.find((n) => n.id === id),
          getViewport: () => ({ x: 0, y: 0, zoom: 1 }),
        });
      }
    };
    return (
      <div data-testid="rf-stub" ref={ref}>
        <div data-testid="rf-stub-node-count">{nodes.length}</div>
        <div data-testid="rf-stub-edge-count">{edges.length}</div>
        {nodes.map((n) => (
          <button
            key={n.id}
            type="button"
            data-testid={`rf-stub-node-${n.id}`}
            onClick={(e) => onNodeClick?.(e, n)}
          >
            {n.data?.title ?? n.id}
          </button>
        ))}
        {children}
      </div>
    );
  }
  return {
    __esModule: true,
    default: ReactFlow,
    Background: () => null,
    BackgroundVariant: { Dots: 'dots' },
    Controls: () => null,
    MiniMap: () => null,
    Panel: ({ children }: { children: ReactNode }) => <>{children}</>,
    MarkerType: { ArrowClosed: 'arrowclosed' },
    Handle: () => null,
    Position: { Left: 'left', Right: 'right' },
  };
});

import { GraphPage } from '../GraphPage';
import { CapabilitiesProvider } from '@/capabilities';
import { HotkeysProvider } from '@/hotkeys';
import { RhpProvider } from '@/components/rhp/RhpContext';
import { RhpPinnedContentProvider } from '@/components/rhp/RhpPinnedContent';
import { DEFAULT_MAX_EDGES, DEFAULT_MAX_NODES } from '@/components/graph/graphModel';
import type { CapabilitiesResponse } from '@/capabilities';

function caps(): CapabilitiesResponse {
  return {
    work_plane: {
      adaptor_name: 'beads',
      adaptor_version: '0.1.0',
      protocol_version: '0.1.0',
      transport: 'api',
      state_map: { open: 'unstarted' },
      sprint_native: false,
      token_budget_enforced: false,
      evidence_synthesis_required: false,
    },
    orchestration_plane: null,
  };
}

function wrapper(entry = '/graph'): (p: { children: ReactNode }) => JSX.Element {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return function Wrapper({ children }: { children: ReactNode }): JSX.Element {
    return (
      <MemoryRouter initialEntries={[entry]}>
        <RhpProvider>
          <RhpPinnedContentProvider>
            <QueryClientProvider client={client}>
              <CapabilitiesProvider initial={caps()}>
                <HotkeysProvider>{children}</HotkeysProvider>
              </CapabilitiesProvider>
            </QueryClientProvider>
          </RhpPinnedContentProvider>
        </RhpProvider>
      </MemoryRouter>
    );
  };
}

function jsonResp(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * companyBoard builds a board past the live one: n items across eight
 * repositories, every item blocking three others and parented into an
 * epic, for roughly 4n relationship rows.
 */
function companyBoard(n = 3000): WorkItem[] {
  const ids = Array.from({ length: n }, (_, i) => `n${String(i).padStart(5, '0')}`);
  return ids.map((id, i) => {
    const relationships = [] as NonNullable<WorkItem['relationships']>;
    for (const step of [1, 11, 97]) {
      relationships.push({ kind: 'blocks', from: id, to: ids[(i + step) % n] });
    }
    if (i % 50 !== 0) {
      relationships.push({ kind: 'parent_child', from: ids[i - (i % 50)], to: id });
    }
    return {
      id,
      kind: 'task',
      title: `synthetic ${id}`,
      status: 'open',
      state_category: 'unstarted',
      created_at: '2026-04-25T00:00:00Z',
      updated_at: '2026-04-25T00:00:00Z',
      relationships,
      primary_repository_id: `Atab-Group/repo-${i % 8}`,
      custom: { atab_source: 'atab-group', atab_repo: `Atab-Group/repo-${i % 8}` },
    } as unknown as WorkItem;
  });
}

let fetchSpy: ReturnType<typeof vi.spyOn>;

function serve(items: WorkItem[]) {
  fetchSpy.mockImplementation(async (...args: unknown[]) => {
    const url = String(args[0]);
    if (url.startsWith('/api/work-items/')) return jsonResp(items[0]);
    return jsonResp({ items, total: items.length, has_more: false });
  });
}

beforeEach(() => {
  mountedNodeCounts = [];
  mountedEdgeCounts = [];
  fitViewCalls = 0;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  fetchSpy = vi.spyOn(globalThis, 'fetch' as any) as any;
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('GraphPage at company scale', () => {
  const board = companyBoard();

  it('never hands React Flow more than the budget, on any render', async () => {
    serve(board);
    render(<GraphPage />, { wrapper: wrapper() });
    await waitFor(() => expect(screen.getByTestId('rf-stub')).toBeTruthy());

    // Every render, not just the settled one. A page that draws the
    // whole board once and aggregates afterwards has already paid for
    // the render the budget exists to prevent.
    expect(mountedNodeCounts.length).toBeGreaterThan(0);
    for (const count of mountedNodeCounts) {
      expect(count).toBeLessThanOrEqual(DEFAULT_MAX_NODES);
    }
    for (const count of mountedEdgeCounts) {
      expect(count).toBeLessThanOrEqual(DEFAULT_MAX_EDGES);
    }
  });

  it('defaults an unfiltered company board to the grouped overview', async () => {
    serve(board);
    render(<GraphPage />, { wrapper: wrapper() });
    await waitFor(() => expect(screen.getByTestId('rf-stub')).toBeTruthy());

    const host = screen.getByTestId('graph-canvas-host');
    expect(host.getAttribute('data-graph-mode')).toBe('overview');
    // Eight repositories, so eight tiles rather than three thousand
    // cards.
    expect(Number(host.getAttribute('data-node-count'))).toBe(8);
  });

  it('says how much of the board it is standing for', async () => {
    serve(board);
    render(<GraphPage />, { wrapper: wrapper() });
    const banner = await screen.findByTestId('graph-scope-banner');
    expect(banner.textContent).toMatch(/grouping 3000 items/);
  });

  it('bounds scope mode and says what it cut', async () => {
    serve(board);
    render(<GraphPage />, { wrapper: wrapper('/graph?graph=scope') });
    await waitFor(() => expect(screen.getByTestId('rf-stub')).toBeTruthy());

    const host = screen.getByTestId('graph-canvas-host');
    expect(host.getAttribute('data-graph-mode')).toBe('scope');
    expect(Number(host.getAttribute('data-node-count'))).toBe(DEFAULT_MAX_NODES);
    expect(Number(host.getAttribute('data-available-nodes'))).toBe(3000);

    const banner = screen.getByTestId('graph-scope-banner');
    expect(banner.getAttribute('data-truncated')).toBe('true');
    expect(screen.getByTestId('graph-scope-count').textContent).toBe(
      `Showing ${DEFAULT_MAX_NODES} of 3000 items`
    );
    // The banner offers the way out rather than leaving the reader to
    // guess which control widens the view.
    expect(screen.getByTestId('graph-scope-overview')).toBeTruthy();
  });

  it('draws a bounded neighbourhood when an item is focused', async () => {
    serve(board);
    render(<GraphPage />, { wrapper: wrapper('/graph?focus=n01500&depth=1') });
    await waitFor(() => expect(screen.getByTestId('rf-stub')).toBeTruthy());

    const host = screen.getByTestId('graph-canvas-host');
    expect(host.getAttribute('data-graph-mode')).toBe('focus');
    const drawn = Number(host.getAttribute('data-node-count'));
    expect(drawn).toBeGreaterThan(1);
    expect(drawn).toBeLessThanOrEqual(DEFAULT_MAX_NODES);
    expect(screen.getByTestId('rf-stub-node-n01500')).toBeTruthy();
  });

  it('grows the neighbourhood one hop at a time', async () => {
    serve(board);
    render(<GraphPage />, { wrapper: wrapper('/graph?focus=n01500&depth=1') });
    await waitFor(() => expect(screen.getByTestId('rf-stub')).toBeTruthy());

    const before = Number(
      screen.getByTestId('graph-canvas-host').getAttribute('data-node-count')
    );
    act(() => {
      screen.getByTestId('graph-depth-more').click();
    });
    await waitFor(() => {
      const after = Number(
        screen.getByTestId('graph-canvas-host').getAttribute('data-node-count')
      );
      expect(after).toBeGreaterThan(before);
    });
    expect(screen.getByTestId('graph-depth-value').textContent).toBe('2');
  });

  it('drills from a cluster into the items behind it', async () => {
    serve(board);
    render(<GraphPage />, { wrapper: wrapper() });
    await waitFor(() =>
      expect(screen.getByTestId('graph-canvas-host').getAttribute('data-graph-mode')).toBe(
        'overview'
      )
    );
    act(() => {
      screen.getByTestId('rf-stub-node-cluster:atab-group/Atab-Group/repo-0').click();
    });
    await waitFor(() =>
      expect(screen.getByTestId('graph-canvas-host').getAttribute('data-graph-mode')).toBe(
        'scope'
      )
    );
    // One repository's worth, not the whole board.
    const available = Number(
      screen.getByTestId('graph-canvas-host').getAttribute('data-available-nodes')
    );
    expect(available).toBe(375);
  });

  it('switches mode from the toolbar', async () => {
    serve(board);
    render(<GraphPage />, { wrapper: wrapper() });
    await waitFor(() => expect(screen.getByTestId('rf-stub')).toBeTruthy());
    act(() => {
      screen.getByTestId('graph-mode-scope').click();
    });
    await waitFor(() =>
      expect(screen.getByTestId('graph-canvas-host').getAttribute('data-graph-mode')).toBe(
        'scope'
      )
    );
    expect(screen.getByTestId('graph-mode-scope').getAttribute('aria-checked')).toBe('true');
  });

  // A small board needs no aggregation, and forcing one on it would hide
  // the graph behind a single tile.
  it('draws a small board as items', async () => {
    serve(companyBoard(20));
    render(<GraphPage />, { wrapper: wrapper() });
    await waitFor(() => expect(screen.getByTestId('rf-stub')).toBeTruthy());
    const host = screen.getByTestId('graph-canvas-host');
    expect(host.getAttribute('data-graph-mode')).toBe('scope');
    expect(Number(host.getAttribute('data-node-count'))).toBe(20);
    expect(screen.getByTestId('graph-scope-banner').getAttribute('data-truncated')).toBeNull();
  });
});

describe('GraphPage camera', () => {
  // The refit used to key on the node count alone. Swapping between two
  // filters that leave the same number of items is an everyday move, and
  // it left the camera framing a picture that had moved out from under
  // it.
  it('refits when the drawn set changes but its size does not', async () => {
    const side = (name: string, project: string) =>
      companyBoard(6).map(
        (it) =>
          ({
            ...it,
            id: `${name}-${it.id}`,
            relationships: [],
            primary_repository_id: `Atab-Group/${name}`,
            custom: {
              atab_source: 'atab-group',
              atab_repo: `Atab-Group/${name}`,
              atab_project: project,
              atab_project_title: project,
              atab_projects: [{ id: project, number: 1, title: project }],
            },
          }) as unknown as WorkItem
      );
    serve([...side('left', 'Alpha'), ...side('right', 'Beta')]);

    render(<GraphPage />, { wrapper: wrapper('/graph?graph=scope&project=Alpha') });
    await waitFor(() =>
      expect(
        Number(screen.getByTestId('graph-canvas-host').getAttribute('data-node-count'))
      ).toBe(6)
    );
    await waitFor(() => expect(fitViewCalls).toBeGreaterThan(0));
    const before = fitViewCalls;
    expect(screen.getByTestId('rf-stub-node-left-n00000')).toBeTruthy();

    act(() => {
      screen.getByTestId('graph-filter-menu-button').click();
    });
    const select = (await screen.findByTestId('graph-filter-project')) as HTMLSelectElement;
    fireEvent.change(select, { target: { value: 'Beta' } });

    // Same six nodes' worth, entirely different six nodes.
    await waitFor(() => expect(screen.getByTestId('rf-stub-node-right-n00000')).toBeTruthy());
    expect(
      Number(screen.getByTestId('graph-canvas-host').getAttribute('data-node-count'))
    ).toBe(6);
    await waitFor(() => expect(fitViewCalls).toBeGreaterThan(before));
  });

  it('refits once per view change rather than on every render', async () => {
    serve(companyBoard(20));
    render(<GraphPage />, { wrapper: wrapper() });
    await waitFor(() => expect(fitViewCalls).toBeGreaterThan(0));
    const settled = fitViewCalls;
    // Nothing changed, so nothing should refit. A camera that refits on
    // every render is the loop this page used to have.
    await new Promise((r) => setTimeout(r, 120));
    expect(fitViewCalls).toBe(settled);
  });
});
