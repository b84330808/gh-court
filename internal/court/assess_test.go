package court

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/b84330808/gh-court/internal/github"
)

var now = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func ago(d string) string {
	dur, err := time.ParseDuration(d)
	if err != nil {
		panic(err)
	}
	return now.Add(-dur).Format(time.RFC3339)
}

// pr builds a pull request from a JSON template; ${…} placeholders are
// durations before now, e.g. ${72h}.
func pr(t *testing.T, js string) *github.PullRequest {
	t.Helper()
	for {
		i := strings.Index(js, "${")
		if i < 0 {
			break
		}
		j := strings.Index(js[i:], "}")
		js = js[:i] + ago(js[i+2:i+j]) + js[i+j+1:]
	}
	var p github.PullRequest
	if err := json.Unmarshal([]byte(js), &p); err != nil {
		t.Fatalf("bad test JSON: %v\n%s", err, js)
	}
	if p.Repository.NameWithOwner == "" {
		p.Repository.NameWithOwner = "acme/widget"
	}
	if p.Author == nil {
		p.Author = &github.Actor{Typename: "User", Login: "me"}
	}
	return &p
}

func assess(p *github.PullRequest) Result {
	return Assess(p, Options{Viewer: "me", Now: now, StaleAfter: 14 * 24 * time.Hour})
}

func codes(rs []Reason) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Code)
	}
	return out
}

func expect(t *testing.T, r Result, turn Turn, code string) {
	t.Helper()
	if r.Turn != turn {
		t.Errorf("turn = %s, want %s; reasons %v, notes %v", r.Turn, turn, r.Reasons, r.Notes)
		return
	}
	if len(r.Reasons) == 0 || r.Reasons[0].Code != code {
		t.Errorf("first reason = %v, want %s", codes(r.Reasons), code)
	}
}

const head = `"commits":{"nodes":[{"commit":{"oid":"abc","committedDate":"${PUSH}"}}]}`

func withPush(js, push string) string { return strings.Replace(js, "${PUSH}", "${"+push+"}", 1) }

func TestChangesRequestedAfterPush(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"latestOpinionatedReviews":{"nodes":[{"author":{"__typename":"User","login":"maint"},"authorAssociation":"MEMBER","state":"CHANGES_REQUESTED","submittedAt":"${24h}"}]}}`, "48h")))
	expect(t, r, You, "changes_requested")
	if !strings.Contains(r.Reasons[0].Text, "@maint") {
		t.Errorf("text %q does not name the reviewer", r.Reasons[0].Text)
	}
}

func TestPushedAfterChangesRequested(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"latestOpinionatedReviews":{"nodes":[{"author":{"__typename":"User","login":"maint"},"state":"CHANGES_REQUESTED","submittedAt":"${72h}"}]}}`, "24h")))
	expect(t, r, Them, "awaiting_rereview")
	if !strings.Contains(r.Reasons[0].Text, "not re-requested") {
		t.Errorf("text %q should say the review was not re-requested", r.Reasons[0].Text)
	}
}

func TestUnresolvedThread(t *testing.T) {
	js := withPush(`{"createdAt":"${400h}",`+head+`,
		"reviewThreads":{"nodes":[
		  {"isResolved":false,"comments":{"nodes":[{"author":{"__typename":"User","login":"rev"},"createdAt":"${10h}","body":"nit"}]}},
		  {"isResolved":true,"comments":{"nodes":[{"author":{"__typename":"User","login":"rev"},"createdAt":"${10h}","body":"done"}]}},
		  {"isResolved":false,"comments":{"nodes":[{"author":{"__typename":"User","login":"me"},"createdAt":"${5h}","body":"fixed"}]}},
		  {"isResolved":false,"comments":{"nodes":[{"author":{"__typename":"Bot","login":"coderabbitai"},"createdAt":"${5h}","body":"Consider X?"}]}}
		]}}`, "48h")
	r := assess(pr(t, js))
	expect(t, r, You, "unresolved_thread")
	if !strings.HasPrefix(r.Reasons[0].Text, "1 unresolved review thread ") {
		t.Errorf("text = %q", r.Reasons[0].Text)
	}
	if !contains(codes(r.Notes), "bot_threads") {
		t.Errorf("bot thread should be a note: %v", r.Notes)
	}
}

func TestOldThreadCommentBeforePushIsNotYourTurn(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"reviewThreads":{"nodes":[{"isResolved":false,"comments":{"nodes":[{"author":{"__typename":"User","login":"rev"},"createdAt":"${72h}","body":"nit"}]}}]}}`, "24h")))
	if r.Turn != Them {
		t.Errorf("turn = %s, want them: %v", r.Turn, r.Reasons)
	}
}

func TestCIFailure(t *testing.T) {
	r := assess(pr(t, `{"createdAt":"${400h}","commits":{"nodes":[{"commit":{"committedDate":"${30h}",
		"statusCheckRollup":{"state":"FAILURE","contexts":{"nodes":[
		  {"__typename":"CheckRun","name":"test (windows)","conclusion":"FAILURE"},
		  {"__typename":"CheckRun","name":"lint","conclusion":"SUCCESS"},
		  {"__typename":"StatusContext","context":"license/cla","state":"FAILURE"}]}}}}]}}`))
	expect(t, r, You, "ci_failed")
	if !strings.Contains(r.Reasons[0].Text, "test (windows), license/cla") {
		t.Errorf("text = %q", r.Reasons[0].Text)
	}
}

func TestMaintainerOnlyChecksAreTheirTurn(t *testing.T) {
	r := assess(pr(t, `{"createdAt":"${400h}","commits":{"nodes":[{"commit":{"committedDate":"${30h}",
		"statusCheckRollup":{"state":"FAILURE","contexts":{"nodes":[
		  {"__typename":"CheckRun","name":"enforce-label","conclusion":"FAILURE"},
		  {"__typename":"CheckRun","name":"check","conclusion":"FAILURE","checkSuite":{"workflowRun":{"workflow":{"name":"Approval"}}}},
		  {"__typename":"StatusContext","context":"Vercel – app","state":"FAILURE","description":"Authorization required to deploy."},
		  {"__typename":"StatusContext","context":"argos","state":"FAILURE","description":"1 changed — waiting for your decision"}]}}}}]}}`))
	expect(t, r, Them, "maintainer_check")
	if !strings.Contains(r.Reasons[0].Text, "enforce-label, check, Vercel – app (Authorization required to deploy.) (+1 more)") {
		t.Errorf("text = %q", r.Reasons[0].Text)
	}
}

func TestFailureAlsoOnBaseIsANote(t *testing.T) {
	r := assess(pr(t, `{"createdAt":"${400h}","baseRefName":"main",
		"baseRef":{"target":{"statusCheckRollup":{"state":"FAILURE","contexts":{"nodes":[{"__typename":"CheckRun","name":"test (3.15)","conclusion":"FAILURE"}]}}}},
		"commits":{"nodes":[{"commit":{"committedDate":"${30h}",
		"statusCheckRollup":{"state":"FAILURE","contexts":{"nodes":[{"__typename":"CheckRun","name":"test (3.15)","conclusion":"FAILURE"}]}}}}]}}`))
	if r.Turn != Them || !contains(codes(r.Notes), "ci_failed_on_base") {
		t.Errorf("turn %s, reasons %v, notes %v", r.Turn, codes(r.Reasons), codes(r.Notes))
	}
}

func TestFailureOnOthersPullRequestsIsANote(t *testing.T) {
	p := pr(t, `{"createdAt":"${400h}","commits":{"nodes":[{"commit":{"committedDate":"${30h}",
		"statusCheckRollup":{"state":"FAILURE","contexts":{"nodes":[
		  {"__typename":"StatusContext","context":"buildkite/unit-tests","state":"FAILURE"},
		  {"__typename":"CheckRun","name":"lint","conclusion":"FAILURE"}]}}}}]}}`)
	peers := map[string]github.CheckStats{"acme/widget": {
		"buildkite/unit-tests": {Ran: 8, Failed: 8},
		"lint":                 {Ran: 8, Failed: 1},
	}}
	r := Assess(p, Options{Viewer: "me", Now: now, Peers: peers})
	expect(t, r, You, "ci_failed")
	if r.Reasons[0].Text != "CI failed on your head commit: lint" {
		t.Errorf("text = %q", r.Reasons[0].Text)
	}
	if !contains(codes(r.Notes), "ci_failed_on_peers") {
		t.Errorf("notes = %v", r.Notes)
	}
}

func TestThanksMentionIsNotAQuestion(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"reviews":{"nodes":[{"author":{"__typename":"User","login":"maint"},"authorAssociation":"MEMBER","state":"APPROVED","submittedAt":"${20h}","body":"Thanks @me "}]},
		"latestOpinionatedReviews":{"nodes":[{"author":{"__typename":"User","login":"maint"},"state":"APPROVED","submittedAt":"${20h}"}]}}`, "48h")))
	expect(t, r, Them, "approved")
}

func TestMentionAddressesYou(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"comments":{"nodes":[{"author":{"__typename":"User","login":"maint"},"createdAt":"${20h}","body":"@me this breaks the Windows build."}]}}`, "48h")))
	expect(t, r, You, "question")
}

func TestThreadAnsweredInConversation(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"reviewThreads":{"nodes":[{"isResolved":false,"comments":{"nodes":[{"author":{"__typename":"User","login":"rev"},"createdAt":"${10h}","body":"Guard for BigInt?"}]}}]},
		"comments":{"nodes":[{"author":{"__typename":"User","login":"me"},"createdAt":"${9h}","body":"Every supported environment has BigInt."}]}}`, "48h")))
	if r.Turn != Them {
		t.Errorf("turn = %s, want them: %v", r.Turn, r.Reasons)
	}
}

func TestCommunityApprovalIsANote(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"reviews":{"nodes":[{"author":{"__typename":"User","login":"helper"},"authorAssociation":"NONE","state":"APPROVED","submittedAt":"${20h}","body":"Looks good if tests pass"}]}}`, "48h")))
	expect(t, r, Them, "awaiting_review")
	if !contains(codes(r.Notes), "community_approval") {
		t.Errorf("notes = %v", r.Notes)
	}
}

func TestCommunityChangeRequest(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"reviews":{"nodes":[{"author":{"__typename":"User","login":"helper"},"authorAssociation":"CONTRIBUTOR","state":"CHANGES_REQUESTED","submittedAt":"${20h}","body":"This leaks."}]}}`, "48h")))
	expect(t, r, You, "changes_requested")
}

func TestConflict(t *testing.T) {
	r := assess(pr(t, `{"createdAt":"${400h}","mergeable":"CONFLICTING","baseRefName":"main"}`))
	expect(t, r, You, "conflict")
}

func TestMaintainerQuestion(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"comments":{"nodes":[
		  {"author":{"__typename":"User","login":"me"},"createdAt":"${40h}","body":"Ready for review."},
		  {"author":{"__typename":"User","login":"maint"},"authorAssociation":"OWNER","createdAt":"${20h}","body":"Thanks. Does this also work on Python 3.9?"}]}}`, "48h")))
	expect(t, r, You, "question")
	if !strings.Contains(r.Reasons[0].Text, "(maintainer)") || !strings.Contains(r.Reasons[0].Text, "Python 3.9?") {
		t.Errorf("text = %q", r.Reasons[0].Text)
	}
}

func TestQuestionToSomeoneElse(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"comments":{"nodes":[{"author":{"__typename":"User","login":"maint"},"authorAssociation":"OWNER","createdAt":"${20h}","body":"@other-maint can you take a look?"}]}}`, "48h")))
	if r.Turn != Them {
		t.Errorf("turn = %s, want them: %v", r.Turn, r.Reasons)
	}
}

func TestAnsweredQuestion(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"comments":{"nodes":[
		  {"author":{"__typename":"User","login":"maint"},"createdAt":"${20h}","body":"Why?"},
		  {"author":{"__typename":"User","login":"me"},"createdAt":"${10h}","body":"Because."}]}}`, "48h")))
	expect(t, r, Them, "awaiting_reply")
}

func TestBotQuestionIgnored(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"comments":{"nodes":[{"author":{"__typename":"User","login":"CLAassistant"},"createdAt":"${20h}","body":"Please sign our CLA?"}]}}`, "48h")))
	if r.Turn != Them {
		t.Errorf("turn = %s, want them: %v", r.Turn, r.Reasons)
	}
}

func TestReviewRequested(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}",`+head+`,
		"reviewRequests":{"nodes":[{"requestedReviewer":{"__typename":"User","login":"rev"}},{"requestedReviewer":{"__typename":"Team","combinedSlug":"acme/core"}}]},
		"timelineItems":{"nodes":[{"__typename":"ReviewRequestedEvent","createdAt":"${30h}","requestedReviewer":{"__typename":"User","login":"rev"}}]}}`, "48h")))
	expect(t, r, Them, "review_requested")
	if r.Reasons[0].Text != "review requested from @rev, @acme/core 30h ago" {
		t.Errorf("text = %q", r.Reasons[0].Text)
	}
	if !r.Since.Equal(now.Add(-30 * time.Hour)) {
		t.Errorf("since = %v", r.Since)
	}
}

func TestApprovedNotMerged(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${400h}","reviewDecision":"APPROVED","mergeStateStatus":"CLEAN",`+head+`,
		"latestOpinionatedReviews":{"nodes":[{"author":{"__typename":"User","login":"maint"},"state":"APPROVED","submittedAt":"${24h}"}]}}`, "48h")))
	expect(t, r, Them, "approved")
}

func TestActionRequired(t *testing.T) {
	r := assess(pr(t, `{"createdAt":"${400h}","commits":{"nodes":[{"commit":{"committedDate":"${30h}",
		"checkSuites":{"nodes":[
		  {"createdAt":"${29h}","conclusion":"ACTION_REQUIRED","app":{"slug":"github-actions"},"workflowRun":{"workflow":{"name":"CI"}}},
		  {"createdAt":"${29h}","conclusion":"ACTION_REQUIRED","app":{"slug":"github-actions"},"workflowRun":{"workflow":{"name":"Docs"}}},
		  {"createdAt":"${29h}","conclusion":"SUCCESS","app":{"slug":"github-actions"},"workflowRun":{"workflow":{"name":"Docs"}}}]}}}]}}`))
	expect(t, r, Them, "awaiting_approval")
	if !strings.Contains(r.Reasons[0].Text, "1 workflow waits for a maintainer to approve its run: CI") {
		t.Errorf("text = %q", r.Reasons[0].Text)
	}
}

func TestStaleGetsNudge(t *testing.T) {
	r := assess(pr(t, withPush(`{"createdAt":"${2000h}",`+head+`,
		"reviewRequests":{"nodes":[{"requestedReviewer":{"__typename":"User","login":"rev"}}]}}`, "1000h")))
	expect(t, r, Stale, "review_requested")
	if r.Nudge != "ping @rev for a review" {
		t.Errorf("nudge = %q", r.Nudge)
	}
	if !contains(codes(r.Reasons), "stale") {
		t.Errorf("reasons = %v", codes(r.Reasons))
	}
}

func TestStaleYourTurnStaysYours(t *testing.T) {
	r := assess(pr(t, `{"createdAt":"${2000h}","mergeable":"CONFLICTING","baseRefName":"main"}`))
	if r.Turn != You {
		t.Errorf("turn = %s, want you", r.Turn)
	}
}

func TestPushTimeFromCheckSuite(t *testing.T) {
	p := pr(t, `{"createdAt":"${400h}","commits":{"nodes":[{"commit":{"committedDate":"${300h}",
		"checkSuites":{"nodes":[{"createdAt":"${20h}","conclusion":"SUCCESS","app":{"slug":"github-actions"}}]}}}]},
		"timelineItems":{"nodes":[{"__typename":"HeadRefForcePushedEvent","createdAt":"${50h}"}]}}`)
	if got := LastPush(p); !got.Equal(now.Add(-20 * time.Hour)) {
		t.Errorf("LastPush = %v", got)
	}
}

func TestDraft(t *testing.T) {
	r := assess(pr(t, `{"createdAt":"${400h}","isDraft":true}`))
	expect(t, r, You, "draft")
}

func TestReviewerRequested(t *testing.T) {
	p := pr(t, `{"createdAt":"${100h}","author":{"__typename":"User","login":"alice"},
		"reviewRequests":{"nodes":[{"requestedReviewer":{"__typename":"User","login":"me"}}]},
		"timelineItems":{"nodes":[{"__typename":"ReviewRequestedEvent","createdAt":"${50h}","requestedReviewer":{"__typename":"User","login":"me"}}]}}`)
	r := Assess(p, Options{Viewer: "me", Mode: Reviewer, Now: now})
	expect(t, r, You, "review_requested")
}

func TestReviewerAuthorPushed(t *testing.T) {
	p := pr(t, withPush(`{"createdAt":"${300h}","author":{"__typename":"User","login":"alice"},`+head+`,
		"reviews":{"nodes":[{"author":{"__typename":"User","login":"me"},"state":"CHANGES_REQUESTED","submittedAt":"${100h}"}]}}`, "20h"))
	r := Assess(p, Options{Viewer: "me", Mode: Reviewer, Now: now})
	expect(t, r, You, "author_pushed")
}

func TestReviewerWaitingOnAuthor(t *testing.T) {
	p := pr(t, withPush(`{"createdAt":"${300h}","author":{"__typename":"User","login":"alice"},`+head+`,
		"reviews":{"nodes":[{"author":{"__typename":"User","login":"me"},"state":"CHANGES_REQUESTED","submittedAt":"${20h}"}]}}`, "100h"))
	r := Assess(p, Options{Viewer: "me", Mode: Reviewer, Now: now})
	expect(t, r, Them, "awaiting_author")
	if r.Reasons[0].Text != "you requested changes 20h ago; waiting on @alice" {
		t.Errorf("text = %q", r.Reasons[0].Text)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
