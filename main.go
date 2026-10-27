// Command gh-court lists your open pull requests across GitHub and says whose
// turn it is on each one.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/cli/go-gh/v2/pkg/term"

	"github.com/b84330808/gh-court/internal/court"
	"github.com/b84330808/gh-court/internal/github"
	"github.com/b84330808/gh-court/internal/render"
	"github.com/b84330808/gh-court/internal/text"
)

var version = "dev"

const usage = `Usage: gh court [flags]

List your open pull requests across GitHub and say whose turn it is on each:
yours, theirs, or stale (theirs, but quiet for too long). Every row says why.

With --reviewer, list pull requests you review instead: where your review is
requested, or where the author pushed or replied after your last review.

Flags:
`

type config struct {
	reviewer  bool
	teams     bool
	asJSON    bool
	markdown  bool
	staleDays int
	turns     string
	search    string
	limit     int
	bots      bool
	batch     int
	parallel  int
	color     string
	urls      bool
	minRemain int
	recordDir string
	peers     int
	showVer   bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var c config
	fs := flag.NewFlagSet("gh court", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	fs.BoolVar(&c.reviewer, "reviewer", false, "list pull requests you review instead of ones you wrote")
	fs.BoolVar(&c.teams, "teams", false, "with --reviewer, include review requests sent to your teams")
	fs.BoolVar(&c.asJSON, "json", false, "print JSON")
	fs.BoolVar(&c.markdown, "markdown", false, "print Markdown tables")
	fs.IntVar(&c.staleDays, "stale-days", 14, "call a pull request stale after this many `days` without activity")
	fs.StringVar(&c.turns, "turn", "", "only show these turns, comma separated: you, them, stale")
	fs.StringVar(&c.search, "search", "", "extra search qualifiers, e.g. \"org:cli\" or \"repo:cli/cli\"")
	fs.IntVar(&c.limit, "limit", 300, "look at most at this many pull requests")
	fs.BoolVar(&c.bots, "bots", false, "count comments and review threads from bots like a person's")
	fs.IntVar(&c.batch, "batch", 8, "pull requests per GraphQL request")
	fs.IntVar(&c.parallel, "parallel", 6, "GraphQL requests in flight")
	fs.StringVar(&c.color, "color", "auto", "use colors and links: auto, always or never")
	fs.BoolVar(&c.urls, "urls", false, "print each pull request's URL in the table")
	fs.IntVar(&c.minRemain, "min-rate-limit", 200, "stop before the GraphQL rate limit drops below this many points")
	fs.IntVar(&c.peers, "peers", 8, "compare failed checks with this many recent pull requests by others in the same repository (0 turns it off)")
	fs.StringVar(&c.recordDir, "record", "", "")
	fs.BoolVar(&c.showVer, "version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if c.showVer {
		v := version
		if bi, ok := debug.ReadBuildInfo(); ok && v == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version // set by go build from the git tag
		}
		fmt.Fprintln(stdout, "gh-court", v)
		return 0
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "gh-court: unexpected argument %q (use --search for filters)\n", fs.Arg(0))
		return 2
	}
	if c.asJSON && c.markdown {
		fmt.Fprintln(stderr, "gh-court: choose one of --json and --markdown")
		return 2
	}
	want, err := parseTurns(c.turns)
	if err != nil {
		fmt.Fprintln(stderr, "gh-court:", err)
		return 2
	}

	host, _ := auth.DefaultHost()
	httpClient, err := api.NewHTTPClient(api.ClientOptions{Host: host})
	if err != nil {
		fmt.Fprintln(stderr, "gh-court:", err)
		return 1
	}
	client := &github.Client{
		T:            &github.HTTPTransport{Client: httpClient, Endpoint: github.GraphQLEndpoint(host)},
		BatchSize:    c.batch,
		Concurrency:  c.parallel,
		MinRemaining: c.minRemain,
		Warn:         stderr,
	}
	if c.recordDir != "" {
		client.Record = recorder(c.recordDir, stderr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	t := term.FromEnv()
	start := time.Now()
	results, err := collect(ctx, client, c, start)
	if err != nil {
		fmt.Fprintln(stderr, "gh-court:", err)
		return 1
	}
	var shown []court.Result
	for _, r := range results {
		if want[r.Turn] {
			shown = append(shown, r)
		}
	}

	ropt := render.Options{Now: start, StaleDays: c.staleDays}
	if c.reviewer {
		ropt.Mode = court.Reviewer
	}
	switch {
	case c.asJSON:
		if err := render.JSON(stdout, shown); err != nil {
			fmt.Fprintln(stderr, "gh-court:", err)
			return 1
		}
	case c.markdown:
		render.Markdown(stdout, shown, ropt)
	default:
		tty := t.IsTerminalOutput() && stdout == os.Stdout
		switch c.color {
		case "always":
			ropt.Color = true
		case "never":
		default:
			ropt.Color = tty && t.IsColorEnabled()
		}
		if tty {
			if w, _, err := t.Size(); err == nil {
				ropt.Width = w
			}
		}
		ropt.ShowURL = c.urls || !ropt.Color
		render.Table(stdout, shown, ropt)
		if tty {
			n, rl := client.Stats()
			fmt.Fprintf(stderr, "\n%d pull requests, %d GraphQL requests in %.1fs, rate limit %d left\n",
				len(results), n, time.Since(start).Seconds(), rl.Remaining)
		}
	}
	return 0
}

func parseTurns(s string) (map[court.Turn]bool, error) {
	want := map[court.Turn]bool{}
	if strings.TrimSpace(s) == "" {
		for _, t := range court.Turns {
			want[t] = true
		}
		return want, nil
	}
	for _, part := range strings.Split(s, ",") {
		t := court.Turn(strings.ToLower(strings.TrimSpace(part)))
		switch t {
		case court.You, court.Them, court.Stale:
			want[t] = true
		case "yours", "mine":
			want[court.You] = true
		case "theirs":
			want[court.Them] = true
		default:
			return nil, fmt.Errorf("unknown turn %q (use you, them or stale)", part)
		}
	}
	return want, nil
}

// collect runs the searches, fetches the pull requests and assesses them.
func collect(ctx context.Context, client *github.Client, c config, now time.Time) ([]court.Result, error) {
	base := "is:pr is:open archived:false " + c.search
	opt := court.Options{Now: now, StaleAfter: time.Duration(c.staleDays) * 24 * time.Hour, Bots: c.bots}
	var ids []string
	seen := map[string]bool{}
	add := func(list []string) {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if !c.reviewer {
		viewer, list, err := client.Search(ctx, base+" author:@me", c.limit)
		if err != nil {
			return nil, err
		}
		opt.Viewer = viewer
		add(list)
	} else {
		opt.Mode = court.Reviewer
		direct := map[string]bool{}
		for _, q := range []string{"user-review-requested:@me", "reviewed-by:@me", "commenter:@me"} {
			viewer, list, err := client.Search(ctx, base+" -author:@me "+q, c.limit)
			if err != nil {
				return nil, err
			}
			opt.Viewer = viewer
			if q == "user-review-requested:@me" {
				for _, id := range list {
					direct[id] = true
				}
			}
			add(list)
		}
		if c.teams {
			_, list, err := client.Search(ctx, base+" -author:@me review-requested:@me", c.limit)
			if err != nil {
				return nil, err
			}
			opt.TeamRequested = map[string]bool{}
			for _, id := range list {
				if !direct[id] {
					opt.TeamRequested[id] = true
				}
			}
			add(list)
		}
		if len(ids) > c.limit {
			ids = ids[:c.limit]
		}
	}
	prs, err := client.PullRequests(ctx, ids)
	if err != nil {
		return nil, err
	}
	if !c.reviewer && c.peers > 0 {
		var repos []string
		for i := range prs {
			r := prs[i].Repository.NameWithOwner
			if court.CIFailed(&prs[i]) && !slices.Contains(repos, r) {
				repos = append(repos, r)
			}
		}
		if len(repos) > 0 {
			peers, err := client.PeerChecks(ctx, repos, c.peers)
			var rl *github.RateLimitError
			switch {
			case errors.As(err, &rl):
				return nil, err
			case err != nil:
				fmt.Fprintln(client.Warn, "gh-court: could not compare failed checks with other pull requests:", err)
			}
			opt.Peers = peers
		}
	}
	out := make([]court.Result, 0, len(prs))
	for i := range prs {
		if prs[i].Repository.IsArchived {
			continue
		}
		out = append(out, court.Assess(&prs[i], opt))
	}
	return out, nil
}

// recorder saves raw responses for test fixtures, with comment bodies
// replaced by text.Redact so other people's words are not copied.
func recorder(dir string, stderr io.Writer) func(string, map[string]any, []byte) {
	var n atomic.Int32
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(stderr, "gh-court: --record:", err)
	}
	return func(kind string, vars map[string]any, raw []byte) {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return
		}
		rec := map[string]any{"variables": vars, "response": redact(v)}
		b, _ := json.Marshal(rec)
		name := filepath.Join(dir, fmt.Sprintf("%s-%02d.json", kind, n.Add(1)))
		if err := os.WriteFile(name, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintln(stderr, "gh-court: --record:", err)
		}
	}
}

func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && k == "body" {
				x[k] = text.Redact(s)
				continue
			}
			x[k] = redact(val)
		}
	case []any:
		for i := range x {
			x[i] = redact(x[i])
		}
	}
	return v
}
