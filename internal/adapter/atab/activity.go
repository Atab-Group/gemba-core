// activity.go: the work item's real GitHub history.
//
// This is a different read from everything else in the adaptor. The
// board is a snapshot: it is fetched on a timer, it holds a bounded tail
// of comments, and it exists to answer "what is the state of the work".
// A history answers "how did it get there", which is unbounded, and
// which nobody needs until they open one item. So it is fetched on
// demand, paged, and never cached into the snapshot.
//
// The bounded twenty-comment tail the projection carries is for lease
// and evidence detection. It is not a history and must never be
// presented as one, which is why [core.ActivityPage] reports AtOldest
// separately from HasOlder.
package atab

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/GembaCore/gemba-core/core"
)

// Activity page bounds. A page is capped because the caller is an HTTP
// request with a deadline and GitHub charges a GraphQL budget per node;
// an unbounded history read would spend the same budget the board needs.
const (
	defaultActivityPage = 30
	maxActivityPage     = 100
)

// ActivityClient is the optional history port. A [Client] that can read
// a work item's timeline implements it; one that cannot (a fixture
// replaying a recorded board, a backend with no event log) does not, and
// the source then reports history as unavailable rather than empty.
type ActivityClient interface {
	FetchActivity(ctx context.Context, ref IssueRef, q core.ActivityQuery) (core.ActivityPage, error)
}

var _ ActivityClient = (*GHClient)(nil)

// ReadActivity returns one page of a work item's history.
//
// Failing closed here matters more than anywhere else in the adaptor,
// because the caller supplies the id: an id naming a source this
// deployment does not carry answers not-found with nothing beyond the id
// they already had, exactly as GetWorkItem does. Saying "that source
// exists but you may not read it" would confirm the shape of a
// deployment the caller was never granted.
func (w *WorkPlane) ReadActivity(
	ctx context.Context, id core.WorkItemID, q core.ActivityQuery,
) (core.ActivityPage, error) {
	source, ref, err := ParseWorkItemID(id)
	if err != nil {
		return core.ActivityPage{}, err
	}
	e := w.registry.entry(source)
	if e == nil {
		return core.ActivityPage{}, core.WrapAdaptorError(core.KindSessionNotFound,
			core.ErrNotFound, "atab: no work item %q", id)
	}
	client, ok := e.cache.client.(ActivityClient)
	if !ok {
		return core.ActivityPage{}, core.NewAdaptorError(core.KindUnsupported,
			"atab: source %q is read through a client that cannot fetch history", source)
	}

	q.Limit = clampActivityLimit(q.Limit)
	page, err := client.FetchActivity(ctx, ref, q)
	if err != nil {
		return core.ActivityPage{}, err
	}
	page.Source = string(source)
	// Freshness describes the surrounding projection, not this page: the
	// page was fetched live just now. A reader seeing a stale board needs
	// to know the card beside the history is older than the history is.
	page.Freshness = string(e.cache.Freshness())
	return page, nil
}

func clampActivityLimit(n int) int {
	switch {
	case n <= 0:
		return defaultActivityPage
	case n > maxActivityPage:
		return maxActivityPage
	default:
		return n
	}
}

// --- GitHub timeline -------------------------------------------------

// FetchActivity reads one backwards page of the issue's timeline.
//
// GitHub's timelineItems connection pages forward from the beginning,
// which is the wrong end: a reader opens a history at the newest entry.
// `last:` plus `before:` walks it backwards instead, so the first page
// is the most recent events and each subsequent page is older. Within a
// page the events stay in GitHub's chronological order.
func (c *GHClient) FetchActivity(
	ctx context.Context, ref IssueRef, q core.ActivityQuery,
) (core.ActivityPage, error) {
	if err := ref.Validate(); err != nil {
		return core.ActivityPage{}, err
	}
	vars := map[string]string{
		"owner":  ref.Owner,
		"repo":   ref.Repo,
		"number": strconv.Itoa(ref.Number),
		"n":      strconv.Itoa(clampActivityLimit(q.Limit)),
	}
	if q.Before != "" {
		vars["before"] = q.Before
	}

	var resp struct {
		Data struct {
			Repository *struct {
				Issue *struct {
					TimelineItems struct {
						TotalCount int `json:"totalCount"`
						PageInfo   struct {
							HasPreviousPage bool   `json:"hasPreviousPage"`
							StartCursor     string `json:"startCursor"`
						} `json:"pageInfo"`
						Nodes []json.RawMessage `json:"nodes"`
					} `json:"timelineItems"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := c.graphql(ctx, activityQuery, vars, &resp); err != nil {
		return core.ActivityPage{}, err
	}
	if resp.Data.Repository == nil || resp.Data.Repository.Issue == nil {
		return core.ActivityPage{}, core.WrapAdaptorError(core.KindSessionNotFound,
			core.ErrNotFound, "atab: issue %s not found", ref)
	}

	tl := resp.Data.Repository.Issue.TimelineItems
	page := core.ActivityPage{
		Events:   make([]core.ActivityEvent, 0, len(tl.Nodes)),
		Total:    tl.TotalCount,
		HasOlder: tl.PageInfo.HasPreviousPage,
		AtOldest: !tl.PageInfo.HasPreviousPage,
	}
	if tl.PageInfo.HasPreviousPage {
		page.OlderCursor = tl.PageInfo.StartCursor
	}
	for _, raw := range tl.Nodes {
		if ev, ok := convertTimelineNode(raw); ok {
			page.Events = append(page.Events, ev)
		}
	}
	return page, nil
}

// ghTimelineNode is the union of every field the requested item types
// carry. Decoding into one struct rather than a type switch keeps the
// mapping in one readable table below, and an unrequested type simply
// leaves every field zero.
type ghTimelineNode struct {
	Typename  string   `json:"__typename"`
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	CreatedAt string   `json:"createdAt"`
	Body      string   `json:"body"`
	Author    *ghActor `json:"author"`
	Actor     *ghActor `json:"actor"`
	Label     *struct {
		Name string `json:"name"`
	} `json:"label"`
	Assignee *struct {
		Login string `json:"login"`
	} `json:"assignee"`
	PreviousTitle  string `json:"previousTitle"`
	CurrentTitle   string `json:"currentTitle"`
	MilestoneTitle string `json:"milestoneTitle"`
	Source         *struct {
		Typename   string    `json:"__typename"`
		Number     int       `json:"number"`
		Title      string    `json:"title"`
		URL        string    `json:"url"`
		Repository ghRepoRef `json:"repository"`
	} `json:"source"`
}

// convertTimelineNode maps one GraphQL union member onto a core event.
//
// A node with no createdAt is dropped rather than rendered at the zero
// time: an event whose position in the history is unknown is worse than
// no event, because it sorts to the top of a chronological list and
// reads as the most recent thing that happened.
func convertTimelineNode(raw json.RawMessage) (core.ActivityEvent, bool) {
	var n ghTimelineNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return core.ActivityEvent{}, false
	}
	at := parseTime(n.CreatedAt)
	if at.IsZero() {
		return core.ActivityEvent{}, false
	}
	ev := core.ActivityEvent{
		ID:   n.ID,
		At:   at,
		URL:  n.URL,
		Kind: core.ActivityOther,
	}
	if n.Actor != nil {
		ev.Actor = n.Actor.Login
	}
	if n.Author != nil {
		ev.Actor = n.Author.Login
	}

	switch n.Typename {
	case "IssueComment":
		ev.Kind = core.ActivityComment
		ev.Body = n.Body
	case "ClosedEvent":
		ev.Kind = core.ActivityClosed
		ev.Summary = "closed this"
	case "ReopenedEvent":
		ev.Kind = core.ActivityReopened
		ev.Summary = "reopened this"
	case "LabeledEvent", "UnlabeledEvent":
		ev.Kind = core.ActivityLabeled
		verb := "added"
		if n.Typename == "UnlabeledEvent" {
			ev.Kind = core.ActivityUnlabeled
			verb = "removed"
		}
		name := ""
		if n.Label != nil {
			name = n.Label.Name
		}
		ev.Summary = fmt.Sprintf("%s the %s label", verb, quoteOrUnnamed(name))
		ev.Detail = map[string]string{"label": name}
	case "AssignedEvent", "UnassignedEvent":
		ev.Kind = core.ActivityAssigned
		verb := "assigned"
		if n.Typename == "UnassignedEvent" {
			ev.Kind = core.ActivityUnassigned
			verb = "unassigned"
		}
		who := ""
		if n.Assignee != nil {
			who = n.Assignee.Login
		}
		ev.Summary = fmt.Sprintf("%s %s", verb, quoteOrUnnamed(who))
		ev.Detail = map[string]string{"assignee": who}
	case "RenamedTitleEvent":
		ev.Kind = core.ActivityRenamed
		ev.Summary = fmt.Sprintf("renamed this from %q to %q", n.PreviousTitle, n.CurrentTitle)
		ev.Detail = map[string]string{
			"previous_title": n.PreviousTitle,
			"current_title":  n.CurrentTitle,
		}
	case "CrossReferencedEvent":
		ev.Kind = core.ActivityReferenced
		target, title, url := crossReference(n)
		ev.Summary = "referenced from " + target
		if title != "" {
			ev.Summary += " (" + title + ")"
		}
		ev.Detail = map[string]string{"target": target}
		if url != "" {
			ev.URL = url
		}
	case "MilestonedEvent", "DemilestonedEvent":
		ev.Kind = core.ActivityMilestoned
		verb := "added this to the"
		if n.Typename == "DemilestonedEvent" {
			ev.Kind = core.ActivityDemilestoned
			verb = "removed this from the"
		}
		ev.Summary = fmt.Sprintf("%s %s milestone", verb, quoteOrUnnamed(n.MilestoneTitle))
		ev.Detail = map[string]string{"milestone": n.MilestoneTitle}
	case "MarkedAsDuplicateEvent":
		ev.Kind = core.ActivityDuplicate
		ev.Summary = "marked this as a duplicate"
	default:
		// A type this build did not ask for still renders, saying what it
		// was. Silently dropping it would put a gap in a history whose
		// whole value is that it has none.
		ev.Summary = humanizeTypename(n.Typename)
	}
	return ev, true
}

func crossReference(n ghTimelineNode) (target, title, url string) {
	if n.Source == nil {
		return "another item", "", ""
	}
	owner := n.Source.Repository.Owner.Login
	repo := n.Source.Repository.Name
	switch {
	case owner != "" && repo != "":
		target = fmt.Sprintf("%s/%s#%d", owner, repo, n.Source.Number)
	case n.Source.Number > 0:
		target = fmt.Sprintf("#%d", n.Source.Number)
	default:
		target = "another item"
	}
	return target, n.Source.Title, n.Source.URL
}

func quoteOrUnnamed(s string) string {
	if s == "" {
		return "an unnamed one"
	}
	return strconv.Quote(s)
}

// humanizeTypename turns "SubIssueAddedEvent" into "sub issue added".
func humanizeTypename(s string) string {
	s = strings.TrimSuffix(s, "Event")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// Freshness reports the cache's current freshness without forcing a
// read. The activity surface needs it to say how old the card beside the
// history is, and must not trigger a refresh to find out: a reader
// opening a history on a stale board would otherwise pay for a full
// crawl before seeing a single comment.
func (c *Cache) Freshness() Freshness {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.freshnessLocked(c.now().UTC())
}

// activityQuery reads the issue timeline backwards.
//
// The item-type list is explicit rather than "everything": an unfiltered
// timeline includes high-volume machine events (subscriptions, mentions,
// deployment references) that would fill every page before a human
// comment appeared. The types below are the ones that change what the
// work item is.
var activityQuery = `query($owner:String!,$repo:String!,$number:Int!,$n:Int!,$before:String){
  repository(owner:$owner, name:$repo){
    issue(number:$number){
      timelineItems(last:$n, before:$before, itemTypes:[
        ISSUE_COMMENT, CLOSED_EVENT, REOPENED_EVENT,
        LABELED_EVENT, UNLABELED_EVENT,
        ASSIGNED_EVENT, UNASSIGNED_EVENT,
        RENAMED_TITLE_EVENT, CROSS_REFERENCED_EVENT,
        MILESTONED_EVENT, DEMILESTONED_EVENT,
        MARKED_AS_DUPLICATE_EVENT
      ]){
        totalCount
        pageInfo { hasPreviousPage startCursor }
        nodes {
          __typename
          ... on IssueComment { id url createdAt body author { login } }
          ... on ClosedEvent { id url createdAt actor { login } }
          ... on ReopenedEvent { id createdAt actor { login } }
          ... on LabeledEvent { id createdAt actor { login } label { name } }
          ... on UnlabeledEvent { id createdAt actor { login } label { name } }
          ... on AssignedEvent { id createdAt actor { login } assignee { ... on User { login } } }
          ... on UnassignedEvent { id createdAt actor { login } assignee { ... on User { login } } }
          ... on RenamedTitleEvent { id createdAt actor { login } previousTitle currentTitle }
          ... on CrossReferencedEvent { id url createdAt actor { login }
            source {
              __typename
              ... on Issue { number title url repository { name owner { login } } }
              ... on PullRequest { number title url repository { name owner { login } } }
            }
          }
          ... on MilestonedEvent { id createdAt actor { login } milestoneTitle }
          ... on DemilestonedEvent { id createdAt actor { login } milestoneTitle }
          ... on MarkedAsDuplicateEvent { id createdAt actor { login } }
        }
      }
    }
  }
}`
