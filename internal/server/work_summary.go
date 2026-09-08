// work_summary.go: GET /api/work-summary, the small stable read-only
// surface an external widget can poll.
//
// It exists because the alternative for a status widget is fetching the
// whole board and counting client-side. On this deployment that is
// several megabytes across three pages, every poll, to render a handful
// of numbers. This returns those numbers directly, in a shape that does
// not grow with the size of the board: the only unbounded thing on a
// board is the number of items, and nothing here is per-item except a
// deliberately capped list of what is being worked right now.
//
// It is composed from the WorkPlane interface rather than from any one
// adaptor, so it answers for whichever adaptor is bound. The atab_*
// fields it reads are absent on other adaptors and their groups simply
// come back empty rather than the handler failing.
package server

import (
	"net/http"
	"sort"
	"time"

	"github.com/GembaCore/gemba-core/core"
	"github.com/GembaCore/gemba-core/internal/server/httperr"
)

// summaryClaimCap bounds the held-work list. A widget shows what is
// running now, and a board with more than this many simultaneous claims
// has a problem the list would not help with anyway. Capping it is what
// keeps the response a fixed size rather than a second copy of the
// board.
const summaryClaimCap = 25

// summarySource is one configured source's contribution.
type summarySource struct {
	ID          string         `json:"id"`
	Items       int            `json:"items"`
	Freshness   string         `json:"freshness,omitempty"`
	ObservedAt  string         `json:"observed_at,omitempty"`
	ByStatus    map[string]int `json:"by_status"`
	ByReadiness map[string]int `json:"by_readiness,omitempty"`
	ByClaim     map[string]int `json:"by_claim,omitempty"`
	Repos       map[string]int `json:"repos,omitempty"`
}

// summaryProject is one project board's contribution.
//
// It is a separate axis from the source: a source is an org plus a
// credential and a repository allowlist, while a project is one board
// inside it. An org can run several boards, and work in a repository the
// source reads can sit on none of them. Collapsing the two would make
// "project" mean "org", which is what the board's own filter used to do.
type summaryProject struct {
	// ID is the source-qualified project identity, "<source>#<number>".
	// The empty string is the bucket of work on no board at all, which is
	// a real answer rather than a gap and so gets a row of its own.
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Source string `json:"source,omitempty"`
	Items  int    `json:"items"`
}

// summaryClaim is one held item, reduced to what a widget renders.
type summaryClaim struct {
	ID      core.WorkItemID `json:"id"`
	Title   string          `json:"title"`
	Source  string          `json:"source,omitempty"`
	Repo    string          `json:"repo,omitempty"`
	Holder  string          `json:"holder,omitempty"`
	State   string          `json:"state"`
	Expires string          `json:"expires,omitempty"`
}

type workSummary struct {
	InstanceID  string           `json:"instance_id"`
	GeneratedAt string           `json:"generated_at"`
	Total       int              `json:"total"`
	Adaptors    []adaptorSummary `json:"adaptors"`
	Sources     []summarySource  `json:"sources"`
	Projects    []summaryProject `json:"projects"`
	ByStatus    map[string]int   `json:"by_status"`
	ByReadiness map[string]int   `json:"by_readiness,omitempty"`
	ByClaim     map[string]int   `json:"by_claim,omitempty"`
	Claimed     []summaryClaim   `json:"claimed"`
	// ClaimedTruncated says the held list was cut, so a widget can say
	// "and N more" rather than implying it is showing everything.
	ClaimedTruncated int `json:"claimed_truncated,omitempty"`
}

type adaptorSummary struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Reason  string `json:"reason,omitempty"`
}

// custom reads one string off a work item's Custom map.
func customString(item core.WorkItem, key string) string {
	if item.Custom == nil {
		return ""
	}
	s, _ := item.Custom[key].(string)
	return s
}

func bump(m map[string]int, key string) {
	if key == "" {
		return
	}
	m[key]++
}

// workSummaryHandler serves GET /api/work-summary.
func (r *Router) workSummary(w http.ResponseWriter, req *http.Request) {
	if r.host == nil || r.host.WorkPlane() == nil {
		httperr.Write(w, http.StatusServiceUnavailable,
			"adaptor_not_configured", "no WorkPlane adaptor registered")
		return
	}
	wp := r.host.WorkPlane()

	// Limit 0 is "every item the adaptor will give", which is what a
	// summary has to count. The default list cap exists to bound a
	// response body; here nothing per-item is returned, so the cap would
	// only make the counts wrong.
	items, err := wp.ListWorkItems(req.Context(), core.WorkItemFilter{})
	if err != nil {
		httperr.WriteError(w, err)
		return
	}

	out := workSummary{
		InstanceID:  r.instanceID,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Total:       len(items),
		ByStatus:    map[string]int{},
		ByReadiness: map[string]int{},
		ByClaim:     map[string]int{},
		Claimed:     []summaryClaim{},
		Sources:     []summarySource{},
		Projects:    []summaryProject{},
		Adaptors:    []adaptorSummary{},
	}

	for _, st := range r.boundAdaptorStatuses() {
		out.Adaptors = append(out.Adaptors, adaptorSummary{
			Name: st.Name, Healthy: st.Healthy, Reason: st.Reason,
		})
	}

	bySource := map[string]*summarySource{}
	byProject := map[string]*summaryProject{}
	var held []summaryClaim

	for _, item := range items {
		bump(out.ByStatus, item.Status)
		bump(out.ByReadiness, customString(item, "atab_readiness"))
		claimState := customString(item, "atab_claim_state")
		bump(out.ByClaim, claimState)

		sourceID := customString(item, "atab_source")
		if sourceID == "" {
			sourceID = "default"
		}
		src, ok := bySource[sourceID]
		if !ok {
			src = &summarySource{
				ID:          sourceID,
				ByStatus:    map[string]int{},
				ByReadiness: map[string]int{},
				ByClaim:     map[string]int{},
				Repos:       map[string]int{},
			}
			bySource[sourceID] = src
		}
		src.Items++
		bump(src.ByStatus, item.Status)
		bump(src.ByReadiness, customString(item, "atab_readiness"))
		bump(src.ByClaim, claimState)
		bump(src.Repos, customString(item, "atab_repo"))
		if f := customString(item, "atab_freshness"); f != "" {
			src.Freshness = f
		}
		if o := customString(item, "atab_observed_at"); o != "" {
			src.ObservedAt = o
		}

		// The project axis. An item on no board lands in the "" bucket
		// rather than being left out: work nobody has filed is exactly
		// what a project filter has to be able to show.
		projectID := customString(item, "atab_project")
		proj, ok := byProject[projectID]
		if !ok {
			proj = &summaryProject{ID: projectID}
			if projectID != "" {
				// A project id is already source-qualified, so its source
				// is one value. The unfiled bucket spans every source and
				// is left unattributed rather than credited to whichever
				// one happened to be counted first.
				proj.Source = sourceID
				proj.Title = customString(item, "atab_project_title")
			}
			byProject[projectID] = proj
		}
		proj.Items++

		// Only work something is actually holding goes in the list.
		// Expired and released claims are counted in ByClaim, where the
		// number is the useful part; listing them would fill a widget
		// with work nobody is doing.
		switch claimState {
		case "active", "stale", "claiming":
			held = append(held, summaryClaim{
				ID:      item.ID,
				Title:   item.Title,
				Source:  sourceID,
				Repo:    customString(item, "atab_repo"),
				Holder:  customString(item, "atab_claimed_by"),
				State:   claimState,
				Expires: claimExpiry(item),
			})
		}
	}

	for _, src := range bySource {
		out.Sources = append(out.Sources, *src)
	}
	// Stable order, so a widget polling this does not reshuffle its rows
	// between two identical answers.
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].ID < out.Sources[j].ID })
	for _, proj := range byProject {
		out.Projects = append(out.Projects, *proj)
	}
	// Named boards first, in id order; the unfiled bucket last, because
	// it is the leftover rather than a board.
	sort.Slice(out.Projects, func(i, j int) bool {
		a, b := out.Projects[i].ID, out.Projects[j].ID
		if (a == "") != (b == "") {
			return b == ""
		}
		return a < b
	})
	sort.Slice(held, func(i, j int) bool { return held[i].ID < held[j].ID })

	if len(held) > summaryClaimCap {
		out.ClaimedTruncated = len(held) - summaryClaimCap
		held = held[:summaryClaimCap]
	}
	// A nil slice marshals to null, and a widget that has to handle both
	// null and [] for "nothing is running" will eventually handle only
	// one of them. The field is always an array.
	if held != nil {
		out.Claimed = held
	}

	writeJSON(w, http.StatusOK, out)
}

// claimExpiry digs the expiry out of the claim object when the adaptor
// supplies one.
func claimExpiry(item core.WorkItem) string {
	if item.Custom == nil {
		return ""
	}
	claim, ok := item.Custom["atab_claim"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := claim["expires"].(string)
	return s
}
