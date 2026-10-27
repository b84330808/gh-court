// Package court decides whose turn it is on a pull request.
package court

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/b84330808/gh-court/internal/github"
	"github.com/b84330808/gh-court/internal/text"
)

// Turn says who has to act next.
type Turn string

const (
	You   Turn = "you"
	Them  Turn = "them"
	Stale Turn = "stale"
)

// Turns lists the turns in display order.
var Turns = []Turn{You, Them, Stale}

// Mode is the point of view: the pull request's author or a reviewer.
type Mode int

const (
	Author Mode = iota
	Reviewer
)

type Options struct {
	Viewer     string
	Mode       Mode
	Now        time.Time
	StaleAfter time.Duration
	// Bots makes bot comments and review threads count like a person's.
	Bots bool
	// TeamRequested holds the IDs of pull requests where a team you belong to
	// is a requested reviewer (reviewer mode, --teams).
	TeamRequested map[string]bool
	// Peers holds check results of other people's recent pull requests per
	// repository (see github.Client.PeerChecks).
	Peers map[string]github.CheckStats
}

// Reason is one fact behind a turn. Code is stable for scripts; Text is for
// people.
type Reason struct {
	Code string    `json:"code"`
	Text string    `json:"text"`
	At   time.Time `json:"at,omitzero"`
	URL  string    `json:"url,omitempty"`
}

// Result is the verdict for one pull request.
type Result struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Author string `json:"author"`
	Draft  bool   `json:"draft"`
	Turn   Turn   `json:"turn"`
	// Since is when the ball landed where it is now.
	Since        time.Time `json:"since"`
	LastActivity time.Time `json:"lastActivity"`
	// Reasons say why the pull request got its turn, most important first.
	Reasons []Reason `json:"reasons"`
	// Notes are context that did not decide the turn.
	Notes []Reason `json:"notes,omitempty"`
	Nudge string   `json:"nudge,omitempty"`
}

// Ref is "owner/repo#123".
func (r Result) Ref() string { return fmt.Sprintf("%s#%d", r.Repo, r.Number) }

type activity struct {
	kind  string // comment, review, thread
	who   string
	bot   bool
	maint bool
	at    time.Time
	body  string
	url   string
	state string
}

func isMaintainer(assoc string) bool {
	return assoc == "OWNER" || assoc == "MEMBER" || assoc == "COLLABORATOR"
}

func login(a *github.Actor) (string, string) {
	if a == nil {
		return "ghost", "User"
	}
	return a.Login, a.Typename
}

func activities(pr *github.PullRequest, bots bool) []activity {
	var out []activity
	add := func(kind string, a *github.Actor, assoc string, at time.Time, body, url, state string) {
		l, t := login(a)
		out = append(out, activity{kind, l, !bots && text.IsBot(t, l), isMaintainer(assoc), at, body, url, state})
	}
	for _, c := range pr.Comments.Nodes {
		add("comment", c.Author, c.AuthorAssociation, c.CreatedAt, c.Body, c.URL, "")
	}
	for _, r := range pr.Reviews.Nodes {
		if r.State == "PENDING" || r.SubmittedAt.IsZero() {
			continue
		}
		add("review", r.Author, r.AuthorAssociation, r.SubmittedAt, r.Body, r.URL, r.State)
	}
	for _, t := range pr.ReviewThreads.Nodes {
		for _, c := range t.Comments.Nodes {
			add("thread", c.Author, c.AuthorAssociation, c.CreatedAt, c.Body, c.URL, "")
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

// LastPush estimates when the head commit was pushed. GitHub does not expose
// push times for ordinary pushes, so this is the latest of the commit date,
// the first check suite created for the commit (suites start on push) and
// any force push event.
func LastPush(pr *github.PullRequest) time.Time {
	t := pr.CreatedAt
	if h := pr.Head(); h != nil {
		t = h.CommittedDate
		var first time.Time
		for _, s := range h.CheckSuites.Nodes {
			if first.IsZero() || s.CreatedAt.Before(first) {
				first = s.CreatedAt
			}
		}
		if first.After(t) {
			t = first
		}
	}
	for _, e := range pr.TimelineItems.Nodes {
		if e.Typename == "HeadRefForcePushedEvent" && e.CreatedAt.After(t) {
			t = e.CreatedAt
		}
	}
	return t
}

func latest(ts ...time.Time) time.Time {
	var m time.Time
	for _, t := range ts {
		if t.After(m) {
			m = t
		}
	}
	return m
}

// Ago formats a duration the way the table shows ages: 45m, 5h, 3d, 7w, 4mo, 2y.
func Ago(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
	return fmt.Sprintf("%dy", int(d.Hours()/24/365))
}

type assessor struct {
	pr   *github.PullRequest
	opt  Options
	me   string
	acts []activity
	push time.Time
	res  Result
}

func (a *assessor) ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return Ago(a.opt.Now.Sub(t)) + " ago"
}

func (a *assessor) isMe(l string) bool { return strings.EqualFold(l, a.me) }

func (a *assessor) reason(code, url string, at time.Time, format string, args ...any) {
	a.res.Reasons = append(a.res.Reasons, Reason{Code: code, Text: fmt.Sprintf(format, args...), At: at, URL: url})
}

func (a *assessor) note(code, url string, at time.Time, format string, args ...any) {
	a.res.Notes = append(a.res.Notes, Reason{Code: code, Text: fmt.Sprintf(format, args...), At: at, URL: url})
}

// Assess decides whose turn it is on pr.
func Assess(pr *github.PullRequest, opt Options) Result {
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	if opt.StaleAfter == 0 {
		opt.StaleAfter = 14 * 24 * time.Hour
	}
	a := &assessor{pr: pr, opt: opt, me: opt.Viewer, acts: activities(pr, opt.Bots), push: LastPush(pr)}
	author, _ := login(pr.Author)
	a.res = Result{
		Repo: pr.Repository.NameWithOwner, Number: pr.Number, Title: pr.Title, URL: pr.URL,
		Author: author, Draft: pr.IsDraft,
	}
	last := latest(pr.CreatedAt, a.push)
	for _, x := range a.acts {
		if !x.bot {
			last = latest(last, x.at)
		}
	}
	a.res.LastActivity = last
	if opt.Mode == Reviewer {
		a.reviewer()
	} else {
		a.author()
	}
	if a.res.Turn == Them && opt.Now.Sub(last) >= opt.StaleAfter {
		a.res.Turn = Stale
		a.res.Since = last
		a.res.Reasons = append(a.res.Reasons, Reason{Code: "stale",
			Text: fmt.Sprintf("no activity for %s", Ago(opt.Now.Sub(last))), At: last})
	} else {
		a.res.Nudge = ""
	}
	return a.res
}

// maintainerCheck matches failed checks that only a maintainer can clear:
// label gates, approval gates, deploy previews that need authorization and
// visual tests waiting for a reviewer's decision.
var maintainerCheck = regexp.MustCompile(`(?i)\blabels?\b|\bapprov|authoriz|waiting for (your|a|the) (decision|review|approval)|requires? (a )?maintainer`)

type ciResult struct {
	yours      []string // failures the author has to look at
	maintainer []string // failures only a maintainer can clear
	onBase     []string // also failing on the base branch
	onPeers    []string // also failing on most of other people's pull requests
	flaky      []string // yours, but also failing on some of theirs
}

// CIFailed reports whether the head commit's combined CI status failed;
// callers use it to decide which repositories need PeerChecks.
func CIFailed(pr *github.PullRequest) bool {
	c := pr.Head()
	if c == nil || c.StatusCheckRollup == nil {
		return false
	}
	s := c.StatusCheckRollup.State
	return s == "FAILURE" || s == "ERROR"
}

// checkCI sorts the failed checks on the head commit.
func checkCI(pr *github.PullRequest, peers github.CheckStats) ciResult {
	var r ciResult
	if !CIFailed(pr) {
		return r
	}
	c := pr.Head()
	baseFailed := map[string]bool{}
	if b := pr.BaseRollup(); b != nil {
		for _, x := range b.Contexts.Nodes {
			if x.Failed() {
				baseFailed[x.CheckName()] = true
			}
		}
	}
	for _, x := range c.StatusCheckRollup.Contexts.Nodes {
		if !x.Failed() {
			continue
		}
		name := x.CheckName()
		desc := x.Description
		if desc == "" {
			desc = x.Title
		}
		switch {
		case maintainerCheck.MatchString(name + " | " + x.Workflow() + " | " + desc):
			if desc != "" {
				name += " (" + text.Snippet(desc, 50) + ")"
			}
			r.maintainer = append(r.maintainer, name)
		case baseFailed[name]:
			r.onBase = append(r.onBase, name)
		case peers[name] != nil && peers[name].Failed >= 2 && 2*peers[name].Failed >= peers[name].Ran:
			r.onPeers = append(r.onPeers, fmt.Sprintf("%s (%d of %d)", name, peers[name].Failed, peers[name].Ran))
		default:
			r.yours = append(r.yours, name)
			if st := peers[name]; st != nil && st.Failed > 0 {
				r.flaky = append(r.flaky, fmt.Sprintf("%s (%d of %d)", name, st.Failed, st.Ran))
			}
		}
	}
	r.yours, r.maintainer, r.onBase, r.onPeers, r.flaky = uniq(r.yours), uniq(r.maintainer), uniq(r.onBase), uniq(r.onPeers), uniq(r.flaky)
	return r
}

// awaitingApproval lists GitHub Actions workflows on the head commit that
// wait for a maintainer to approve them (runs from first-time contributors'
// forks), skipping workflows that also have a run that did start.
func awaitingApproval(c *github.Commit) []string {
	if c == nil {
		return nil
	}
	ran := map[string]bool{}
	var waiting []string
	for _, s := range c.CheckSuites.Nodes {
		if s.App == nil || s.App.Slug != "github-actions" || s.WorkflowRun == nil {
			continue
		}
		name := s.WorkflowRun.Workflow.Name
		if s.Conclusion == "ACTION_REQUIRED" {
			waiting = append(waiting, name)
		} else {
			ran[name] = true
		}
	}
	var out []string
	for _, n := range uniq(waiting) {
		if !ran[n] {
			out = append(out, n)
		}
	}
	return out
}

func uniq(xs []string) []string {
	var out []string
	for _, x := range xs {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func list(names []string, max int) string {
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:max], ", ") + fmt.Sprintf(" (+%d more)", len(names)-max)
}

func ats(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "@" + n
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// opinions returns each person's latest approving or change-requesting
// review. latestOpinionatedReviews (queried with writersOnly) leaves out
// people without write access, so recent reviews fill those in. writers holds
// the logins whose reviews count toward merging: listed there, or with an
// owner, member or collaborator association. (GitHub has been seen to return
// different latestOpinionatedReviews for the same pull request seconds apart,
// so neither source is trusted alone.)
func opinions(pr *github.PullRequest) ([]github.Review, map[string]bool) {
	by := map[string]github.Review{}
	var order []string
	put := func(r github.Review) {
		who, _ := login(r.Author)
		k := strings.ToLower(who)
		old, seen := by[k]
		if !seen {
			order = append(order, k)
		}
		if !seen || r.SubmittedAt.After(old.SubmittedAt) {
			by[k] = r
		}
	}
	writers := map[string]bool{}
	for _, r := range pr.LatestOpinionatedReviews.Nodes {
		who, _ := login(r.Author)
		writers[strings.ToLower(who)] = true
		put(r)
	}
	for _, r := range pr.Reviews.Nodes {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			put(r)
			if isMaintainer(r.AuthorAssociation) {
				who, _ := login(r.Author)
				writers[strings.ToLower(who)] = true
			}
		}
	}
	out := make([]github.Review, 0, len(order))
	for _, k := range order {
		out = append(out, by[k])
	}
	return out, writers
}

// author applies the rules for your own pull requests.
func (a *assessor) author() {
	pr := a.pr
	head := pr.Head()

	// Your last activity: a push, a comment, a review reply, marking ready.
	mine := latest(pr.CreatedAt, a.push)
	for _, x := range a.acts {
		if a.isMe(x.who) {
			mine = latest(mine, x.at)
		}
	}
	for _, e := range pr.TimelineItems.Nodes {
		if e.Typename == "ReadyForReviewEvent" {
			mine = latest(mine, e.CreatedAt)
		}
	}

	// --- Your turn ---
	if pr.Mergeable == "CONFLICTING" {
		a.reason("conflict", pr.URL, time.Time{}, "merge conflict with %s", pr.BaseRefName)
	}
	ci := checkCI(pr, a.opt.Peers[pr.Repository.NameWithOwner])
	if len(ci.yours) > 0 {
		a.reason("ci_failed", pr.URL+"/checks", a.push, "CI failed on your head commit: %s", list(ci.yours, 4))
	}
	var rerequest []string
	ops, writers := opinions(pr)
	for _, r := range ops {
		who, typ := login(r.Author)
		if r.State != "CHANGES_REQUESTED" || a.isMe(who) || (!a.opt.Bots && text.IsBot(typ, who)) {
			continue
		}
		if r.SubmittedAt.After(a.push) {
			a.reason("changes_requested", r.URL, r.SubmittedAt, "@%s requested changes %s, after your last push", who, a.ago(r.SubmittedAt))
		} else {
			rerequest = append(rerequest, who)
		}
	}
	// A reply in the main conversation answers a review comment too; a reply
	// in another thread does not.
	var lastWord time.Time
	for _, x := range a.acts {
		if a.isMe(x.who) && x.kind != "thread" && strings.TrimSpace(x.body) != "" {
			lastWord = latest(lastWord, x.at)
		}
	}
	threadCut := latest(a.push, lastWord)
	var threadWho []string
	var threadAt time.Time
	var threadURL string
	threads, botThreads := 0, 0
	for _, t := range pr.ReviewThreads.Nodes {
		if t.IsResolved || len(t.Comments.Nodes) == 0 {
			continue
		}
		c := t.Comments.Nodes[len(t.Comments.Nodes)-1]
		who, typ := login(c.Author)
		if a.isMe(who) {
			continue
		}
		if !a.opt.Bots && text.IsBot(typ, who) {
			botThreads++
			continue
		}
		if c.CreatedAt.After(threadCut) {
			threads++
			threadWho = append(threadWho, who)
			if threadAt.IsZero() || c.CreatedAt.Before(threadAt) {
				threadAt, threadURL = c.CreatedAt, c.URL
			}
		}
	}
	if threads > 0 {
		a.reason("unresolved_thread", threadURL, threadAt, "%d unresolved review %s with a comment from %s after your last push or reply",
			threads, plural(threads, "thread", "threads"), list(ats(uniq(threadWho)), 3))
	}
	// Someone asked you something, or addressed you, after your last activity.
	var asked, said *activity
	for i := range a.acts {
		x := &a.acts[i]
		if x.bot || a.isMe(x.who) || !x.at.After(mine) || x.kind == "thread" || x.state == "CHANGES_REQUESTED" {
			continue
		}
		ok, _ := text.Ask(x.body)
		addressed := text.Mentions(x.body, a.me) && !text.IsAck(x.body)
		if ok && !addressed && len(text.MentionLogins(x.body)) > 0 {
			// A question to someone else, e.g. one maintainer asking another.
			ok = false
		}
		if ok || addressed {
			if asked == nil {
				asked = x
			}
		} else if strings.TrimSpace(x.body) != "" {
			said = x
		}
	}
	if asked != nil {
		_, s := text.Ask(asked.body)
		if s == "" {
			s = text.Prose(asked.body)
		}
		role := ""
		if asked.maint {
			role = " (maintainer)"
		}
		a.reason("question", asked.url, asked.at, "@%s%s asked %s: “%s”", asked.who, role, a.ago(asked.at), text.Snippet(s, 80))
	}
	if pr.IsDraft {
		a.reason("draft", pr.URL, time.Time{}, "still a draft: mark it ready for review when done")
	}
	if len(a.res.Reasons) > 0 {
		a.res.Turn = You
		a.res.Since = a.push
		var first time.Time
		for _, r := range a.res.Reasons {
			if !r.At.IsZero() && (first.IsZero() || r.At.Before(first)) {
				first = r.At
			}
		}
		if !first.IsZero() {
			a.res.Since = first
		}
	}

	// --- Their turn (also kept as notes when it is yours) ---
	themStart := len(a.res.Reasons)
	since := mine
	if wf := awaitingApproval(head); len(wf) > 0 {
		a.reason("awaiting_approval", pr.URL+"/checks", a.push, "%d %s %s for a maintainer to approve %s: %s",
			len(wf), plural(len(wf), "workflow", "workflows"), plural(len(wf), "waits", "wait"), plural(len(wf), "its run", "their runs"), list(wf, 3))
	}
	if len(ci.maintainer) > 0 {
		a.reason("maintainer_check", pr.URL+"/checks", a.push, "%s only a maintainer can clear: %s",
			plural(len(ci.maintainer), "a failed check", "failed checks"), list(ci.maintainer, 3))
	}
	var approvers, others []string
	var approvedAt time.Time
	for _, r := range ops {
		who, typ := login(r.Author)
		if r.State != "APPROVED" || a.isMe(who) || text.IsBot(typ, who) {
			continue
		}
		if writers[strings.ToLower(who)] {
			approvers = append(approvers, who)
			approvedAt = latest(approvedAt, r.SubmittedAt)
		} else {
			others = append(others, who)
		}
	}
	if len(others) > 0 {
		a.note("community_approval", pr.URL, time.Time{}, "approved by %s, who %s no write access", list(ats(others), 3), plural(len(others), "has", "have"))
	}
	if len(approvers) > 0 && pr.ReviewDecision != "CHANGES_REQUESTED" {
		extra := ", not merged yet"
		if pr.ReviewDecision == "REVIEW_REQUIRED" {
			extra = "; more approvals required"
		} else if pr.MergeStateStatus == "BLOCKED" {
			extra = "; merge is blocked by branch protection"
		}
		a.reason("approved", pr.URL, approvedAt, "approved by %s %s%s", list(ats(approvers), 3), a.ago(approvedAt), extra)
		since = latest(since, approvedAt)
	}
	requested := map[string]bool{}
	var reqNames []string
	var reqAt time.Time
	for _, n := range pr.ReviewRequests.Nodes {
		if r := n.RequestedReviewer; r != nil {
			reqNames = append(reqNames, r.Name())
			requested[strings.ToLower(r.Login)] = r.Typename != "Team"
		}
	}
	for _, e := range pr.TimelineItems.Nodes {
		if e.Typename == "ReviewRequestedEvent" && e.RequestedReviewer != nil && slices.Contains(reqNames, e.RequestedReviewer.Name()) {
			reqAt = latest(reqAt, e.CreatedAt)
		}
	}
	if len(rerequest) > 0 {
		var not []string
		for _, w := range rerequest {
			if !requested[strings.ToLower(w)] {
				not = append(not, w)
			}
		}
		t := "you pushed after %s requested changes; waiting for re-review"
		if len(not) > 0 {
			t += " (review not re-requested from " + list(ats(not), 3) + ")"
		}
		a.reason("awaiting_rereview", pr.URL, a.push, t, list(ats(rerequest), 3))
	}
	if len(reqNames) > 0 {
		t := "review requested from " + list(reqNames, 3)
		if !reqAt.IsZero() {
			t += " " + a.ago(reqAt)
		}
		a.reason("review_requested", pr.URL, reqAt, "%s", t)
		since = latest(since, reqAt)
	}
	reviewed := false
	for _, x := range a.acts {
		if !x.bot && !a.isMe(x.who) && x.kind != "comment" {
			reviewed = true
		}
	}
	if len(a.res.Reasons) == themStart && a.res.Turn != You {
		var lastMine *activity
		for i := range a.acts {
			if a.isMe(a.acts[i].who) {
				lastMine = &a.acts[i]
			}
		}
		switch {
		case lastMine != nil && !lastMine.at.Before(mine):
			a.reason("awaiting_reply", lastMine.url, lastMine.at, "you replied last, %s", a.ago(lastMine.at))
		case !reviewed:
			a.reason("awaiting_review", pr.URL, mine, "waiting for a first review")
		default:
			a.reason("awaiting_review", pr.URL, mine, "waiting for maintainers since your last push %s", a.ago(a.push))
		}
	}
	if len(ci.onBase) > 0 {
		a.note("ci_failed_on_base", pr.URL+"/checks", time.Time{}, "also failing on %s, so probably not your change: %s", pr.BaseRefName, list(ci.onBase, 3))
	}
	if len(ci.flaky) > 0 {
		a.note("ci_flaky", pr.URL+"/checks", time.Time{}, "some recent pull requests by others fail this too, so it may be flaky: %s", list(ci.flaky, 3))
	}
	if len(ci.onPeers) > 0 {
		a.note("ci_failed_on_peers", pr.URL+"/checks", time.Time{}, "also failing on recent pull requests by others, so probably not your change: %s", list(ci.onPeers, 3))
	}
	if said != nil && asked == nil {
		verb := "commented"
		switch said.state {
		case "APPROVED":
			verb = "approved"
		case "COMMENTED", "DISMISSED":
			verb = "reviewed"
		}
		a.note("comment", said.url, said.at, "@%s %s %s: “%s”", said.who, verb, a.ago(said.at), text.Snippet(text.Prose(said.body), 80))
	}
	if botThreads > 0 {
		a.note("bot_threads", pr.URL+"/files", time.Time{}, "%d unresolved %s from bots", botThreads, plural(botThreads, "thread", "threads"))
	}
	if pr.Mergeable == "UNKNOWN" {
		a.note("mergeable_unknown", pr.URL, time.Time{}, "GitHub has not computed mergeability yet")
	}
	if pr.MergeStateStatus == "BEHIND" {
		a.note("behind", pr.URL, time.Time{}, "branch is behind %s; protection requires it to be up to date", pr.BaseRefName)
	}

	if a.res.Turn == You {
		// Their-turn facts are context here.
		a.res.Notes = append(a.res.Reasons[themStart:len(a.res.Reasons):len(a.res.Reasons)], a.res.Notes...)
		a.res.Reasons = a.res.Reasons[:themStart]
		return
	}
	a.res.Turn = Them
	a.res.Since = since
	a.res.Nudge = a.nudge(rerequest, reqNames, approvers)
}

// nudge suggests a polite next step for a stalled pull request.
func (a *assessor) nudge(rerequest, requested, approvers []string) string {
	r := a.res.Reasons[0]
	switch r.Code {
	case "awaiting_approval":
		return "ask a maintainer to approve the workflow runs"
	case "maintainer_check":
		return "ask a maintainer to clear the failed check (add the label, approve, or review the visual diff)"
	case "approved":
		return "ask " + list(ats(approvers), 2) + " whether anything still blocks the merge"
	case "awaiting_rereview":
		return "re-request review from " + list(ats(rerequest), 2) + " and summarize what changed"
	case "review_requested":
		return "ping " + list(requested, 2) + " for a review"
	}
	// Who has been involved from the project side?
	var maint []string
	for i := len(a.acts) - 1; i >= 0; i-- {
		x := a.acts[i]
		if x.maint && !x.bot && !a.isMe(x.who) && !slices.Contains(maint, x.who) {
			maint = append(maint, x.who)
		}
	}
	if len(maint) > 0 {
		return "ping " + list(ats(maint), 2) + " with a short status update"
	}
	return "leave a short, friendly ping or ask in the project's chat who reviews this area"
}

// reviewer applies the rules for pull requests you review.
func (a *assessor) reviewer() {
	pr := a.pr
	author := a.res.Author
	var mine time.Time
	var myState string
	for _, x := range a.acts {
		if a.isMe(x.who) {
			mine = x.at
			if x.kind == "review" {
				myState = x.state
			}
		}
	}
	var reqAt time.Time
	direct := false
	for _, n := range pr.ReviewRequests.Nodes {
		if r := n.RequestedReviewer; r != nil && r.Typename != "Team" && a.isMe(r.Login) {
			direct = true
		}
	}
	for _, e := range pr.TimelineItems.Nodes {
		if e.Typename == "ReviewRequestedEvent" && e.RequestedReviewer != nil && a.isMe(e.RequestedReviewer.Login) {
			reqAt = latest(reqAt, e.CreatedAt)
		}
	}
	if reqAt.IsZero() {
		reqAt = pr.CreatedAt
	}
	switch {
	case direct && !mine.IsZero() && mine.Before(reqAt):
		a.reason("review_requested", pr.URL, reqAt, "@%s re-requested your review %s", author, a.ago(reqAt))
	case direct:
		a.reason("review_requested", pr.URL, reqAt, "your review was requested %s", a.ago(reqAt))
	case a.opt.TeamRequested[pr.ID]:
		var teams []string
		for _, n := range pr.ReviewRequests.Nodes {
			if r := n.RequestedReviewer; r != nil && r.Typename == "Team" {
				teams = append(teams, r.Name())
			}
		}
		a.reason("team_review_requested", pr.URL, pr.CreatedAt, "review requested from your team %s", list(teams, 2))
	}
	if !mine.IsZero() {
		if a.push.After(mine) {
			a.reason("author_pushed", pr.URL+"/files", a.push, "@%s pushed %s, after your last review or comment", author, a.ago(a.push))
		}
		for _, x := range a.acts {
			if x.at.After(mine) && !x.bot && !a.isMe(x.who) && strings.EqualFold(x.who, author) {
				a.reason("author_replied", x.url, x.at, "@%s replied %s: “%s”", author, a.ago(x.at), text.Snippet(text.Prose(x.body), 80))
				break
			}
		}
	}
	for _, x := range a.acts {
		if x.at.After(mine) && !x.bot && !a.isMe(x.who) && !strings.EqualFold(x.who, author) && text.Mentions(x.body, a.me) {
			a.reason("mentioned", x.url, x.at, "@%s mentioned you %s", x.who, a.ago(x.at))
			break
		}
	}
	if len(a.res.Reasons) > 0 {
		a.res.Turn = You
		a.res.Since = a.res.Reasons[0].At
		for _, r := range a.res.Reasons {
			if r.At.Before(a.res.Since) {
				a.res.Since = r.At
			}
		}
	} else {
		a.res.Turn = Them
		a.res.Since = latest(mine, pr.CreatedAt)
		did := "commented"
		switch myState {
		case "APPROVED":
			did = "approved"
		case "CHANGES_REQUESTED":
			did = "requested changes"
		case "COMMENTED":
			did = "reviewed"
		}
		if mine.IsZero() {
			a.reason("awaiting_author", pr.URL, pr.CreatedAt, "nothing for you yet; waiting on @%s", author)
		} else if did == "approved" {
			a.reason("approved", pr.URL, mine, "you approved %s; waiting for the merge", a.ago(mine))
		} else {
			a.reason("awaiting_author", pr.URL, mine, "you %s %s; waiting on @%s", did, a.ago(mine), author)
		}
		a.res.Nudge = "ask @" + author + " whether they plan to continue, or close it"
		if did == "approved" {
			a.res.Nudge = "merge it, or ask who can"
		}
	}
	if ci := checkCI(pr, a.opt.Peers[pr.Repository.NameWithOwner]); len(ci.yours) > 0 {
		a.note("ci_failed", pr.URL+"/checks", a.push, "CI failing: %s", list(ci.yours, 3))
	}
	if pr.Mergeable == "CONFLICTING" {
		a.note("conflict", pr.URL, time.Time{}, "merge conflict with %s", pr.BaseRefName)
	}
	if wf := awaitingApproval(pr.Head()); len(wf) > 0 {
		a.note("awaiting_approval", pr.URL+"/checks", a.push, "%d %s %s for approval: %s", len(wf),
			plural(len(wf), "workflow", "workflows"), plural(len(wf), "waits", "wait"), list(wf, 3))
	}
}
