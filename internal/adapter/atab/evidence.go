package atab

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// Comment markers the org's pipeline writes onto a pull request. The
// local-CI marker is exact by contract: the marker line, then a json
// fence on the very next line.
const (
	localCIMarker = "<!-- atab-local-ci -->"
	verifyMarker  = "<!-- atab-verify -->"
)

// Evidence source labels. These land in core.Evidence.Source, which is
// what the SPA groups the evidence list by.
const (
	EvidenceSourcePR       = "github:pull_request"
	EvidenceSourceCheck    = "github:check_run"
	EvidenceSourceLocalCI  = "atab:local-ci"
	EvidenceSourceVerifier = "atab:verifier"
)

// fencedJSONAfterMarker pulls the json fence that immediately follows a
// marker line. Anchored to the marker with no prose between, which is
// the contract the Land gate string-matches on.
func fencedJSONAfterMarker(body, marker string) (string, bool) {
	idx := strings.Index(body, marker)
	if idx < 0 {
		return "", false
	}
	rest := body[idx+len(marker):]
	re := regexp.MustCompile("(?s)^\\s*```json\\s*\\n(.*?)\\n\\s*```")
	m := re.FindStringSubmatch(rest)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// LocalCIReport is the local-CI evidence the Work agent publishes. The
// org tests locally rather than through GitHub Checks, so this report is
// the primary test evidence and a green check rollup alone is not a
// substitute for it.
type LocalCIReport struct {
	HeadSHA  string `json:"head_sha"`
	AllGreen bool   `json:"all_green"`
	Source   string `json:"source,omitempty"`
	RunBy    string `json:"run_by,omitempty"`
	Commands []struct {
		Name     string `json:"name"`
		Cmd      string `json:"cmd"`
		Exit     *int   `json:"exit,omitempty"`
		Skipped  string `json:"skipped,omitempty"`
		Duration *int   `json:"duration_s,omitempty"`
	} `json:"commands,omitempty"`
	Dispositions []struct {
		Test   string `json:"test"`
		Kind   string `json:"kind"`
		Reason string `json:"reason,omitempty"`
		Issue  *int   `json:"issue,omitempty"`
		Note   string `json:"note,omitempty"`
	} `json:"dispositions,omitempty"`
}

// VerifierEnvelope is the merged verdict the verifier agent returns.
type VerifierEnvelope struct {
	Verifier        string `json:"verifier,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	VerifiedAt      string `json:"verified_at,omitempty"`
	OverallVerdict  string `json:"overall_verdict"`
	EscalateToHuman bool   `json:"escalate_to_human,omitempty"`
	BounceBack      string `json:"bounce_back_message,omitempty"`
	Criteria        []struct {
		ID        string `json:"id"`
		Criterion string `json:"criterion"`
		Verdict   string `json:"verdict"`
		Notes     string `json:"notes,omitempty"`
	} `json:"criteria,omitempty"`
}

// newestReport walks comments newest-first and returns the first body
// that yields a decoded value. Several reports may sit on one pull
// request (one per push); the contract is always "newest by timestamp".
func newestReport[T any](comments []Comment, marker string) (T, time.Time, bool) {
	var zero T
	sorted := append([]Comment(nil), comments...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return commentTime(sorted[i]).After(commentTime(sorted[j]))
	})
	for _, c := range sorted {
		raw, ok := fencedJSONAfterMarker(c.Body, marker)
		if !ok {
			continue
		}
		var out T
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			continue
		}
		return out, commentTime(c), true
	}
	return zero, time.Time{}, false
}

func commentTime(c Comment) time.Time {
	if !c.UpdatedAt.IsZero() {
		return c.UpdatedAt
	}
	return c.CreatedAt
}

// CollectEvidence synthesises the evidence list for one issue from its
// linked pull requests.
//
// Four kinds land here, in the order the reviewer reads them:
//
//  1. the pull request itself, as a URL,
//  2. its local-CI report, as a test result — the org's primary test
//     evidence, because testing is local rather than through Checks,
//  3. its verifier verdict, when one was published,
//  4. the GitHub check runs, as one test result per run.
//
// A pull request with no evidence at all still contributes its URL, so a
// reviewer can always see that a build exists even when nothing reported
// on it.
func CollectEvidence(source SourceID, issue Issue) []core.Evidence {
	var out []core.Evidence
	prs := append([]PullRequest(nil), issue.LinkedPRs...)
	sort.SliceStable(prs, func(i, j int) bool { return prs[i].Ref.Number < prs[j].Ref.Number })

	for _, pr := range prs {
		prID := fmt.Sprintf("%s/%s/pr-%d", source, pr.Ref.Repo, pr.Ref.Number)
		state := strings.ToUpper(pr.State)
		if pr.Merged {
			state = "MERGED"
		}
		summary := fmt.Sprintf("%s #%d %s", state, pr.Ref.Number, pr.Title)
		if pr.Draft {
			summary = "DRAFT " + summary
		}
		out = append(out, core.Evidence{
			ID:         prID,
			Kind:       core.EvidenceURL,
			Source:     EvidenceSourcePR,
			Ref:        pr.URL,
			Summary:    summary,
			CapturedAt: pr.UpdatedAt,
			Payload: map[string]any{
				"number":       pr.Ref.Number,
				"repo":         pr.Ref.Owner + "/" + pr.Ref.Repo,
				"state":        state,
				"merged":       pr.Merged,
				"draft":        pr.Draft,
				"head_sha":     pr.HeadSHA,
				"check_rollup": pr.CheckRollup,
			},
		})

		if report, at, ok := newestReport[LocalCIReport](pr.Comments, localCIMarker); ok {
			// A report stamped against a commit that is no longer the
			// head is stale evidence: the suite ran, but not against
			// what would merge. Say so rather than presenting it green.
			stale := pr.HeadSHA != "" && report.HeadSHA != "" && report.HeadSHA != pr.HeadSHA
			verdict := "red"
			if report.AllGreen {
				verdict = "green"
			}
			summary := fmt.Sprintf("local CI %s (%d command(s))", verdict, len(report.Commands))
			if stale {
				summary += fmt.Sprintf("; stale — ran against %s, head is %s",
					shortSHA(report.HeadSHA), shortSHA(pr.HeadSHA))
			}
			out = append(out, core.Evidence{
				ID:         prID + "/local-ci",
				Kind:       core.EvidenceTestResult,
				Source:     EvidenceSourceLocalCI,
				Ref:        report.HeadSHA,
				Summary:    summary,
				CapturedAt: at,
				Payload: map[string]any{
					"all_green":     report.AllGreen,
					"stale":         stale,
					"head_sha":      report.HeadSHA,
					"pr_head_sha":   pr.HeadSHA,
					"source":        report.Source,
					"run_by":        report.RunBy,
					"command_count": len(report.Commands),
					"dispositions":  len(report.Dispositions),
				},
			})
		}

		if env, at, ok := newestReport[VerifierEnvelope](pr.Comments, verifyMarker); ok {
			passed, total := 0, len(env.Criteria)
			for _, c := range env.Criteria {
				if strings.EqualFold(c.Verdict, "PASS") {
					passed++
				}
			}
			out = append(out, core.Evidence{
				ID:     prID + "/verifier",
				Kind:   core.EvidenceCustom,
				Source: EvidenceSourceVerifier,
				Ref:    env.TaskID,
				Summary: fmt.Sprintf("verifier %s (%d/%d criteria passed)",
					strings.ToUpper(env.OverallVerdict), passed, total),
				CapturedAt: at,
				Payload: map[string]any{
					"overall_verdict":   strings.ToUpper(env.OverallVerdict),
					"criteria_total":    total,
					"criteria_passed":   passed,
					"escalate_to_human": env.EscalateToHuman,
					"bounce_back":       env.BounceBack,
					"verifier":          env.Verifier,
				},
			})
		}

		checks := append([]CheckRun(nil), pr.Checks...)
		sort.SliceStable(checks, func(i, j int) bool { return checks[i].Name < checks[j].Name })
		for _, ck := range checks {
			captured := pr.UpdatedAt
			if ck.CompletedAt != nil {
				captured = *ck.CompletedAt
			}
			outcome := ck.Conclusion
			if outcome == "" {
				outcome = ck.Status
			}
			out = append(out, core.Evidence{
				ID:         fmt.Sprintf("%s/check/%s", prID, ck.Name),
				Kind:       core.EvidenceTestResult,
				Source:     EvidenceSourceCheck,
				Ref:        ck.URL,
				Summary:    fmt.Sprintf("%s: %s", ck.Name, strings.ToUpper(outcome)),
				CapturedAt: captured,
				Payload: map[string]any{
					"name":       ck.Name,
					"status":     ck.Status,
					"conclusion": ck.Conclusion,
					"head_sha":   pr.HeadSHA,
				},
			})
		}
	}
	return out
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
