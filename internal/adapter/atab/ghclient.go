package atab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// GHClient reads GitHub through the authenticated `gh` CLI.
//
// The CLI is the port on purpose. It already holds the operator's
// credentials in the system keyring, so this adaptor never handles a
// token, never reads one from the environment, and cannot log one. Every
// call it makes is a GraphQL query; nothing here can mutate anything on
// GitHub even if it were asked to.
type GHClient struct {
	cfg SourceConfig
	// bin is the gh executable. Overridable so tests can point at a shim.
	bin string
	// pageSize bounds one GraphQL page. GitHub caps issue connections at
	// 100.
	pageSize int
	// maxPages bounds a single fetch so an unexpectedly large repo cannot
	// spend the whole rate-limit budget in one refresh.
	maxPages int
}

var _ Client = (*GHClient)(nil)

// NewGHClient returns a client for cfg. cfg must already be normalised.
func NewGHClient(cfg SourceConfig) *GHClient {
	return &GHClient{cfg: cfg, bin: "gh", pageSize: 50, maxPages: 20}
}

// WithBinary points the client at a different gh executable.
func (c *GHClient) WithBinary(bin string) *GHClient { c.bin = bin; return c }

// WithPaging overrides the page size and page budget.
func (c *GHClient) WithPaging(pageSize, maxPages int) *GHClient {
	if pageSize > 0 {
		c.pageSize = pageSize
	}
	if maxPages > 0 {
		c.maxPages = maxPages
	}
	return c
}

// SourceID names the source this client reads.
func (c *GHClient) SourceID() SourceID { return c.cfg.ID }

// Ping runs the cheapest query that proves both the credential and the
// org are reachable. Its cost does not grow with the size of the source,
// so the health surface can call it on a timer.
func (c *GHClient) Ping(ctx context.Context) error {
	const q = `query($org:String!){ organization(login:$org){ login } }`
	var out struct {
		Data struct {
			Organization *struct {
				Login string `json:"login"`
			} `json:"organization"`
		} `json:"data"`
	}
	if err := c.graphql(ctx, q, map[string]string{"org": c.cfg.Org}, &out); err != nil {
		return err
	}
	if out.Data.Organization == nil {
		return core.NewAdaptorError(core.KindCapabilityDenied,
			"atab: source %q cannot see organisation %q", c.cfg.ID, c.cfg.Org)
	}
	return nil
}

// FetchIssue reads one issue by ref.
func (c *GHClient) FetchIssue(ctx context.Context, ref IssueRef) (Issue, error) {
	if err := ref.Validate(); err != nil {
		return Issue{}, err
	}
	var out struct {
		Data struct {
			Repository *struct {
				Issue *ghIssue `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	err := c.graphql(ctx, singleIssueQuery, map[string]string{
		"owner":  ref.Owner,
		"repo":   ref.Repo,
		"number": strconv.Itoa(ref.Number),
	}, &out)
	if err != nil {
		return Issue{}, err
	}
	if out.Data.Repository == nil || out.Data.Repository.Issue == nil {
		return Issue{}, core.WrapAdaptorError(core.KindSessionNotFound, core.ErrNotFound,
			"atab: issue %s not found", ref)
	}
	return c.convert(ref.Owner, ref.Repo, *out.Data.Repository.Issue), nil
}

// FetchIssues reads every configured repository, honouring the
// incremental watermark when opts.UpdatedSince is set.
//
// One repository failing does not abandon the rest: its error is
// recorded and the loop continues, so a repo the token has lost access
// to degrades that repo rather than the whole source. The error is
// returned only when every repository failed, because a partial result
// presented as complete would silently shrink the board.
func (c *GHClient) FetchIssues(ctx context.Context, opts FetchOptions) ([]Issue, error) {
	repos := opts.Repos
	if len(repos) == 0 {
		repos = c.cfg.Repos
	}
	if len(repos) == 0 {
		discovered, err := c.listOrgRepos(ctx)
		if err != nil {
			return nil, err
		}
		repos = discovered
	}

	var out []Issue
	var failures []string
	for _, repo := range repos {
		issues, err := c.fetchRepoIssues(ctx, repo, opts)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", repo, err))
			continue
		}
		out = append(out, issues...)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			return out[:opts.Limit], nil
		}
	}
	if len(failures) > 0 && len(out) == 0 {
		return nil, core.NewAdaptorError(core.KindRequestFailed,
			"atab: every repository in source %q failed: %s",
			c.cfg.ID, strings.Join(failures, "; "))
	}
	return out, nil
}

func (c *GHClient) fetchRepoIssues(ctx context.Context, repo string, opts FetchOptions) ([]Issue, error) {
	var out []Issue
	cursor := ""
	for page := 0; page < c.maxPages; page++ {
		vars := map[string]string{
			"owner": c.cfg.Org,
			"repo":  repo,
			"n":     strconv.Itoa(c.pageSize),
		}
		if cursor != "" {
			vars["after"] = cursor
		}
		if !opts.UpdatedSince.IsZero() {
			vars["since"] = opts.UpdatedSince.UTC().Format(time.RFC3339)
		}

		var resp struct {
			Data struct {
				Repository *struct {
					Issues struct {
						Nodes    []ghIssue `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"issues"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := c.graphql(ctx, listIssuesQueryFor(opts.IncludeClosed), vars, &resp); err != nil {
			return nil, err
		}
		if resp.Data.Repository == nil {
			return nil, core.WrapAdaptorError(core.KindCapabilityDenied, core.ErrNotFound,
				"atab: repository %s/%s is not visible to this credential", c.cfg.Org, repo)
		}
		for _, n := range resp.Data.Repository.Issues.Nodes {
			out = append(out, c.convert(c.cfg.Org, repo, n))
		}
		if !resp.Data.Repository.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Repository.Issues.PageInfo.EndCursor
		// An incremental fetch is ordered newest-first, so once a page
		// falls entirely before the watermark there is nothing older to
		// find and the walk stops early.
		if !opts.UpdatedSince.IsZero() && len(out) > 0 &&
			out[len(out)-1].UpdatedAt.Before(opts.UpdatedSince) {
			break
		}
	}
	return out, nil
}

func (c *GHClient) listOrgRepos(ctx context.Context) ([]string, error) {
	const q = `query($org:String!,$n:Int!){
  organization(login:$org){
    repositories(first:$n, orderBy:{field:PUSHED_AT, direction:DESC}){
      nodes { name isArchived }
    }
  }
}`
	var resp struct {
		Data struct {
			Organization *struct {
				Repositories struct {
					Nodes []struct {
						Name       string `json:"name"`
						IsArchived bool   `json:"isArchived"`
					} `json:"nodes"`
				} `json:"repositories"`
			} `json:"organization"`
		} `json:"data"`
	}
	if err := c.graphql(ctx, q, map[string]string{"org": c.cfg.Org, "n": "100"}, &resp); err != nil {
		return nil, err
	}
	if resp.Data.Organization == nil {
		return nil, core.NewAdaptorError(core.KindCapabilityDenied,
			"atab: source %q cannot list repositories in %q", c.cfg.ID, c.cfg.Org)
	}
	var out []string
	for _, r := range resp.Data.Organization.Repositories.Nodes {
		if !r.IsArchived {
			out = append(out, r.Name)
		}
	}
	return out, nil
}

// graphql runs one query through `gh api graphql` and decodes the
// response into out.
func (c *GHClient) graphql(ctx context.Context, query string, vars map[string]string, out any) error {
	args := []string{"api", "graphql", "-f", "query=" + query}
	for k, v := range vars {
		// -F coerces numbers and booleans; -f keeps strings verbatim.
		// Timestamps and cursors must stay strings.
		if k == "n" || k == "openOnly" || k == "number" {
			args = append(args, "-F", k+"="+v)
		} else {
			args = append(args, "-f", k+"="+v)
		}
	}
	cmd := exec.CommandContext(ctx, c.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return classifyGHError(err, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		return core.WrapAdaptorError(core.KindRequestFailed, err,
			"atab: could not decode the GitHub response")
	}
	return nil
}

// classifyGHError turns a gh failure into a tagged AdaptorError. The
// runtime branches on Kind and Retryable, never on the message, so a
// throttle has to be distinguishable from a permission failure here
// rather than by string-matching later.
func classifyGHError(err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "429"):
		return core.WrapAdaptorError(core.KindRateLimited, err,
			"atab: GitHub throttled the request")
	case strings.Contains(lower, "not found") || strings.Contains(lower, "404"):
		return core.WrapAdaptorError(core.KindSessionNotFound, core.ErrNotFound,
			"atab: GitHub reported the target does not exist")
	case strings.Contains(lower, "resource not accessible"),
		strings.Contains(lower, "must have admin"),
		strings.Contains(lower, "403"),
		strings.Contains(lower, "requires authentication"),
		strings.Contains(lower, "not authorized"),
		strings.Contains(lower, "insufficient"):
		// Deliberately terse. The condition is what the operator needs;
		// echoing GitHub's body here would put the shape of a source the
		// caller cannot read into a response they can.
		return core.WrapAdaptorError(core.KindCapabilityDenied, err,
			"atab: this credential is not authorised for the requested source")
	case msg == "":
		return core.WrapAdaptorError(core.KindRequestFailed, err,
			"atab: the gh CLI call failed")
	default:
		return core.WrapAdaptorError(core.KindRequestFailed, err,
			"atab: the gh CLI call failed: %s", firstLine(msg))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// --- GraphQL response shapes -----------------------------------------

type ghActor struct {
	Login string `json:"login"`
}

type ghRepoRef struct {
	Name  string  `json:"name"`
	Owner ghActor `json:"owner"`
}

type ghIssueRef struct {
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	State      string    `json:"state"`
	URL        string    `json:"url"`
	Repository ghRepoRef `json:"repository"`
}

type ghComment struct {
	Body      string   `json:"body"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
	Author    *ghActor `json:"author"`
}

type ghFieldValue struct {
	Name   *string  `json:"name"`
	Text   *string  `json:"text"`
	Number *float64 `json:"number"`
	Date   *string  `json:"date"`
	Title  *string  `json:"title"`
	Field  struct {
		Name string `json:"name"`
	} `json:"field"`
}

type ghProjectItem struct {
	ID      string `json:"id"`
	Project struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"project"`
	FieldValues struct {
		Nodes []ghFieldValue `json:"nodes"`
	} `json:"fieldValues"`
}

type ghCheckContext struct {
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Conclusion  string  `json:"conclusion"`
	DetailsURL  string  `json:"detailsUrl"`
	CompletedAt *string `json:"completedAt"`
	// StatusContext shape.
	Context   string `json:"context"`
	State     string `json:"state"`
	TargetURL string `json:"targetUrl"`
	CreatedAt string `json:"createdAt"`
}

type ghPullRequest struct {
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	State      string    `json:"state"`
	IsDraft    bool      `json:"isDraft"`
	Merged     bool      `json:"merged"`
	UpdatedAt  string    `json:"updatedAt"`
	Repository ghRepoRef `json:"repository"`
	Comments   struct {
		Nodes []ghComment `json:"nodes"`
	} `json:"comments"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				OID               string `json:"oid"`
				StatusCheckRollup *struct {
					State    string `json:"state"`
					Contexts struct {
						Nodes []ghCheckContext `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type ghIssue struct {
	ID          string   `json:"id"`
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	URL         string   `json:"url"`
	State       string   `json:"state"`
	StateReason string   `json:"stateReason"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
	ClosedAt    *string  `json:"closedAt"`
	Author      *ghActor `json:"author"`
	Labels      struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Assignees struct {
		Nodes []ghActor `json:"nodes"`
	} `json:"assignees"`
	Parent    *ghIssueRef `json:"parent"`
	SubIssues struct {
		Nodes []ghIssueRef `json:"nodes"`
	} `json:"subIssues"`
	Comments struct {
		Nodes []ghComment `json:"nodes"`
	} `json:"comments"`
	ProjectItems struct {
		Nodes []ghProjectItem `json:"nodes"`
	} `json:"projectItems"`
	ClosedByPullRequestsReferences struct {
		Nodes []ghPullRequest `json:"nodes"`
	} `json:"closedByPullRequestsReferences"`
}

func (c *GHClient) convert(owner, repo string, n ghIssue) Issue {
	is := Issue{
		Ref:         IssueRef{Owner: owner, Repo: repo, Number: n.Number},
		NodeID:      n.ID,
		Title:       n.Title,
		Body:        n.Body,
		URL:         n.URL,
		State:       n.State,
		StateReason: n.StateReason,
		CreatedAt:   parseTime(n.CreatedAt),
		UpdatedAt:   parseTime(n.UpdatedAt),
	}
	if n.ClosedAt != nil {
		t := parseTime(*n.ClosedAt)
		is.ClosedAt = &t
	}
	if n.Author != nil {
		is.Author = n.Author.Login
	}
	for _, l := range n.Labels.Nodes {
		if l.Name != "" {
			is.Labels = append(is.Labels, l.Name)
		}
	}
	for _, a := range n.Assignees.Nodes {
		if a.Login != "" {
			is.Assignees = append(is.Assignees, a.Login)
		}
	}
	if n.Parent != nil {
		is.Parent = &ParentRef{
			Ref:   refOf(*n.Parent, owner, repo),
			Title: n.Parent.Title,
			State: n.Parent.State,
			URL:   n.Parent.URL,
		}
	}
	for _, s := range n.SubIssues.Nodes {
		is.SubIssues = append(is.SubIssues, SubIssueRef{
			Ref:   refOf(s, owner, repo),
			Title: s.Title,
			State: s.State,
			URL:   s.URL,
		})
	}
	is.Comments = convertComments(n.Comments.Nodes)

	for _, pi := range n.ProjectItems.Nodes {
		if c.cfg.ProjectNumber != 0 && pi.Project.Number != c.cfg.ProjectNumber {
			continue
		}
		item := &ProjectItem{
			ItemID:        pi.ID,
			ProjectNumber: pi.Project.Number,
			ProjectTitle:  pi.Project.Title,
			Fields:        map[string]string{},
		}
		for _, fv := range pi.FieldValues.Nodes {
			name := fv.Field.Name
			if name == "" {
				continue
			}
			switch {
			case fv.Name != nil:
				item.Fields[name] = *fv.Name
			case fv.Text != nil:
				item.Fields[name] = *fv.Text
			case fv.Title != nil:
				item.Fields[name] = *fv.Title
			case fv.Date != nil:
				item.Fields[name] = *fv.Date
			case fv.Number != nil:
				item.Fields[name] = strconv.FormatFloat(*fv.Number, 'f', -1, 64)
			}
		}
		is.ProjectItem = item
		break
	}

	for _, pr := range n.ClosedByPullRequestsReferences.Nodes {
		prRepo := repo
		prOwner := owner
		if pr.Repository.Name != "" {
			prRepo = pr.Repository.Name
			prOwner = pr.Repository.Owner.Login
		}
		out := PullRequest{
			Ref:       IssueRef{Owner: prOwner, Repo: prRepo, Number: pr.Number},
			Title:     pr.Title,
			URL:       pr.URL,
			State:     pr.State,
			Merged:    pr.Merged,
			Draft:     pr.IsDraft,
			UpdatedAt: parseTime(pr.UpdatedAt),
			Comments:  convertComments(pr.Comments.Nodes),
		}
		if len(pr.Commits.Nodes) > 0 {
			commit := pr.Commits.Nodes[0].Commit
			out.HeadSHA = commit.OID
			if commit.StatusCheckRollup != nil {
				out.CheckRollup = commit.StatusCheckRollup.State
				for _, ctx := range commit.StatusCheckRollup.Contexts.Nodes {
					out.Checks = append(out.Checks, convertCheck(ctx))
				}
			}
		}
		is.LinkedPRs = append(is.LinkedPRs, out)
	}
	return is
}

func convertCheck(ctx ghCheckContext) CheckRun {
	run := CheckRun{
		Name:       ctx.Name,
		Status:     ctx.Status,
		Conclusion: ctx.Conclusion,
		URL:        ctx.DetailsURL,
	}
	if run.Name == "" {
		// StatusContext, the legacy commit-status shape.
		run.Name = ctx.Context
		run.Conclusion = ctx.State
		run.URL = ctx.TargetURL
	}
	if ctx.CompletedAt != nil {
		t := parseTime(*ctx.CompletedAt)
		run.CompletedAt = &t
	}
	return run
}

func convertComments(in []ghComment) []Comment {
	out := make([]Comment, 0, len(in))
	for _, c := range in {
		cm := Comment{
			Body:      c.Body,
			CreatedAt: parseTime(c.CreatedAt),
			UpdatedAt: parseTime(c.UpdatedAt),
		}
		if c.Author != nil {
			cm.Author = c.Author.Login
		}
		out = append(out, cm)
	}
	return out
}

func refOf(r ghIssueRef, fallbackOwner, fallbackRepo string) IssueRef {
	owner, repo := fallbackOwner, fallbackRepo
	if r.Repository.Name != "" {
		owner = r.Repository.Owner.Login
		repo = r.Repository.Name
	}
	return IssueRef{Owner: owner, Repo: repo, Number: r.Number}
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// --- queries ---------------------------------------------------------

const issueFields = `
    id number title body url state stateReason createdAt updatedAt closedAt
    author { login }
    labels(first:30){ nodes { name } }
    assignees(first:10){ nodes { login } }
    parent { number title state url repository { name owner { login } } }
    subIssues(first:50){ nodes { number title state url repository { name owner { login } } } }
    comments(last:20){ nodes { body createdAt updatedAt author { login } } }
    projectItems(first:5){
      nodes {
        id
        project { number title }
        fieldValues(first:30){
          nodes {
            ... on ProjectV2ItemFieldSingleSelectValue { name field { ... on ProjectV2FieldCommon { name } } }
            ... on ProjectV2ItemFieldTextValue { text field { ... on ProjectV2FieldCommon { name } } }
            ... on ProjectV2ItemFieldNumberValue { number field { ... on ProjectV2FieldCommon { name } } }
            ... on ProjectV2ItemFieldDateValue { date field { ... on ProjectV2FieldCommon { name } } }
            ... on ProjectV2ItemFieldIterationValue { title field { ... on ProjectV2FieldCommon { name } } }
          }
        }
      }
    }
    closedByPullRequestsReferences(first:10, includeClosedPrs:true){
      nodes {
        number title url state isDraft merged updatedAt
        repository { name owner { login } }
        comments(last:20){ nodes { body createdAt updatedAt author { login } } }
        commits(last:1){ nodes { commit { oid statusCheckRollup { state contexts(first:20){ nodes {
          ... on CheckRun { name status conclusion detailsUrl completedAt }
          ... on StatusContext { context state targetUrl createdAt }
        } } } } } }
      }
    }`

// listIssuesQueryFor builds the paged issue query. GraphQL has no
// conditional, and gh's -f/-F flags carry only scalars, so the issue
// state set is baked into the query text rather than passed as a
// variable.
func listIssuesQueryFor(includeClosed bool) string {
	states := "[OPEN]"
	if includeClosed {
		states = "[OPEN, CLOSED]"
	}
	return `query($owner:String!,$repo:String!,$n:Int!,$after:String,$since:DateTime){
  repository(owner:$owner, name:$repo){
    issues(first:$n, after:$after,
           orderBy:{field:UPDATED_AT, direction:DESC},
           filterBy:{since:$since},
           states: ` + states + `){
      pageInfo { hasNextPage endCursor }
      nodes {` + issueFields + `
      }
    }
  }
}`
}

var singleIssueQuery = `query($owner:String!,$repo:String!,$number:Int!){
  repository(owner:$owner, name:$repo){
    issue(number:$number){` + issueFields + `
    }
  }
}`
