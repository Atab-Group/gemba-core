// work_activity.go: GET /api/work-items/{id}/activity, one backwards
// page of a work item's real history.
//
// The route exists only when the bound WorkPlane implements the optional
// core.ActivityReader surface. A plane that cannot answer gets a 501
// saying so rather than an empty page, because an empty page and "this
// backend keeps no history" are different facts and a reader has to be
// able to tell them apart.
//
// Nothing here caches. A history is read on demand, for one item, when
// somebody opens it.
package server

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/GembaCore/gemba-core/core"
	"github.com/GembaCore/gemba-core/internal/server/httperr"
)

// maxActivityLimit bounds what a caller may ask for in one page. The
// adaptor clamps too; this stops an absurd limit reaching it at all.
const maxActivityLimit = 100

// workItemActivity serves GET /api/work-items/{id}/activity.
//
//	?before=<cursor>  page older than the cursor from the last response
//	?limit=<n>        page size, clamped to maxActivityLimit
//
// Error envelopes go through httperr, so a throttled backend surfaces as
// its tagged kind rather than as an empty history. That distinction is
// the point of the endpoint: a reader must never be shown "no activity"
// when the truth is "GitHub would not answer".
func (r *Router) workItemActivity(w http.ResponseWriter, req *http.Request) {
	raw := chi.URLParam(req, "id")
	if raw == "" {
		httperr.Write(w, http.StatusBadRequest, "bad_request", "missing work item id")
		return
	}
	id, err := url.PathUnescape(raw)
	if err != nil {
		httperr.Write(w, http.StatusBadRequest, "bad_request",
			"malformed work item id: "+err.Error())
		return
	}

	if r.host == nil || r.host.WorkPlane() == nil {
		httperr.Write(w, http.StatusServiceUnavailable,
			"adaptor_not_configured", "no WorkPlane adaptor registered")
		return
	}
	reader, ok := r.host.WorkPlane().(core.ActivityReader)
	if !ok {
		httperr.Write(w, http.StatusNotImplemented, "unsupported",
			"the bound adaptor does not read work item history")
		return
	}

	q := core.ActivityQuery{Before: req.URL.Query().Get("before")}
	if v := req.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			httperr.Write(w, http.StatusBadRequest, "bad_request",
				"limit must be a positive integer")
			return
		}
		if n > maxActivityLimit {
			n = maxActivityLimit
		}
		q.Limit = n
	}

	page, err := reader.ReadActivity(req.Context(), core.WorkItemID(id), q)
	if err != nil {
		httperr.WriteError(w, err)
		return
	}
	// Events is never null on the wire. A renderer branching on an empty
	// array is ordinary; one branching on null is a bug waiting for the
	// first item with no history.
	if page.Events == nil {
		page.Events = []core.ActivityEvent{}
	}
	writeJSON(w, http.StatusOK, page)
}
