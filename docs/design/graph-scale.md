# The graph at company scale

The dependency graph draws a bounded subgraph, chosen before React Flow
exists. This note records why, because both defects it fixes looked like
rendering problems and neither was.

## What was wrong

**The layout ran away under cycles.** Rows were assigned by relaxing
depths over the raw graph until nothing changed, bailing after V passes.
That never settles on a cyclic graph, and because it wrote depths in
place while iterating it could lift a node several rows in one pass. On
the live 2790-item board it produced 4187 rows, more rows than there are
nodes, and a canvas 416,980 x 334,960 pixels. Fitting that to a viewport
puts every node below a pixel wide. The board was not dense; it was
being drawn on a canvas a third of a million pixels tall.

**The page decided how much to draw after drawing it.** It built one
node per filtered item, mounted the canvas, fitted the camera, read the
resulting zoom, and only then concluded the view was too dense to read
and re-aggregated. So the company-scale render always happened, once per
load, and the decision fed off the thing it changed.

| | Before | After |
| --- | --- | --- |
| Layout rows, 2790 items | 4187 | 26 |
| Canvas | 416,980 x 334,960 px | fits the viewport |
| Layout time | 262 ms | 8.6 ms |
| Nodes handed to React Flow | 2790 | at most 300 |
| Edges handed to React Flow | 2200 | at most 600 |

## How it works now

**One condensation.** `scc.ts` runs Tarjan once and returns the
components, a topological order and which components are cycles.
Cycle detection, the critical path and the layout all read it. Collapsing
cycles first makes the input acyclic, so the longest path is one linear
pass rather than a relaxation that may not converge.

**Three modes, all in the URL.** `?graph=`, `?focus=` and `?depth=` make
a view linkable, and mean there is one source of truth for what is drawn.

- **Focus** draws one item and its N-hop neighbourhood, walked in both
  directions so blockers and dependents both appear, with a depth
  control and an expand-one-hop step. This is the question people
  actually arrive with.
- **Scope** draws the current filters as items, while they fit the
  budget. Connected items are drawn first: a canvas whose budget went on
  disconnected cards shows a field of dots and no dependencies.
- **Overview** groups the board and never draws items at any board size.

The default is Focus when an item is focused, Scope when the filters fit
the budget, and Overview otherwise. On an unfiltered company board that
is the Overview, never the hairball.

**Budgets are caps, not hints.** 300 nodes and 600 edges, applied to
plain data. Truncation drops the furthest nodes first in Focus and the
disconnected ones first in Scope, both deterministically, so the cut
does not move between two renders of the same board. Whatever was cut is
reported on screen: a budget that silently drops two thousand items is
indistinguishable from a board that only has three hundred.

**Layers wrap.** A layer is a set of nodes at one dependency depth, and
nothing says a hundred of them sit in one line. The column cap comes
from the canvas width, because the right number of nodes side by side is
however many fit at a zoom somebody can read at.

**One fit, on one signal.** Fitting is keyed on a signature of what is
drawn. Nothing reads the camera, so there is no loop. The fit retries
for a few frames until React Flow's store agrees with what it was handed,
because `fitView` against an empty store is a silent no-op and that is
how the camera ended up parked at scale 1 on a ten-screen canvas.

## What the live board actually looks like

Worth recording, because the design follows it rather than the other way
round. Of 2790 items:

- 2200 edges resolve, and **every one of them is same-repo**. A
  repository overview therefore draws ten tiles and no arrows. That is
  the board telling the truth about itself, so the tiles carry counts,
  blocked counts and internal dependency totals, and clicking one drills
  into it.
- 2069 items have no parent, and only 866 sit under an epic. The graph
  is a sparse forest with a few dependency clusters, not a web.
- 13 cycles across 27 items, and the longest chain is 24 hops.

## Limitations

- The overview groups by repository. An org that runs one repository
  gets one tile, which is honest but not useful; grouping by epic would
  suit that shape better and is not implemented.
- Truncation in Scope keeps connected items but does not try to keep
  them connected to each other, so a drawn item's neighbour may be
  outside the budget. The edge to it is dropped rather than drawn to
  nothing.
- Depth is capped at 5. A neighbourhood that large is past the node
  budget on any real board anyway.
- The layout is a layered placer with no edge-crossing minimisation, so
  a dense neighbourhood still crosses lines. Crossings, unlike the
  canvas size, are a readability cost rather than a correctness one.
