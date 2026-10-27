// Package github fetches pull requests for gh-court with batched, read-only
// GraphQL queries.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Transport posts one GraphQL request body and returns the raw response.
// The real one goes through go-gh's authenticated HTTP client; tests replay
// recorded responses.
type Transport interface {
	Post(ctx context.Context, body []byte) (status int, header http.Header, resp []byte, err error)
}

// Client runs the two queries gh-court needs. The zero value is not usable;
// set at least T.
type Client struct {
	T           Transport
	BatchSize   int // pull requests per nodes() query, default 10
	Concurrency int // nodes() queries in flight, default 4
	// MinRemaining stops the run before the primary rate limit drops below
	// this many points, so gh-court never uses up the last of your budget.
	MinRemaining int
	// Record, when set, receives every successful request's variables and
	// raw response.
	Record func(kind string, vars map[string]any, raw []byte)
	// Warn receives non-fatal problems (skipped pull requests, retries).
	Warn  io.Writer
	Sleep func(time.Duration)

	mu       sync.Mutex
	rate     RateLimit
	requests int
}

// Stats reports how many requests ran and the last rate limit seen.
func (c *Client) Stats() (requests int, rate RateLimit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests, c.rate
}

var errTimeout = errors.New("GitHub timed out on the query")

// RateLimitError means gh-court stopped to protect the rate limit budget.
type RateLimitError struct {
	Remaining int
	ResetAt   time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("GitHub API rate limit nearly used up (%d points left); it resets at %s",
		e.Remaining, e.ResetAt.Local().Format("15:04"))
}

type gqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

func (c *Client) warnf(format string, args ...any) {
	if c.Warn != nil {
		fmt.Fprintf(c.Warn, "gh-court: "+format+"\n", args...)
	}
}

func (c *Client) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

// do posts one query and decodes its data into out. GraphQL errors that come
// with data (an inaccessible repository, say) are warnings; the rest of the
// data is still used.
func (c *Client) do(ctx context.Context, kind, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	const maxAttempts = 4
	for attempt := 1; ; attempt++ {
		status, header, raw, err := c.T.Post(ctx, body)
		c.mu.Lock()
		c.requests++
		c.mu.Unlock()
		if err != nil {
			if ctx.Err() != nil || attempt == maxAttempts {
				return err
			}
			c.warnf("request failed (%v), retrying", err)
			c.sleep(time.Duration(attempt) * time.Second)
			continue
		}
		switch {
		case status == http.StatusBadGateway || status == http.StatusGatewayTimeout || status == http.StatusServiceUnavailable:
			return errTimeout
		case status == http.StatusForbidden || status == http.StatusTooManyRequests:
			if header.Get("X-Ratelimit-Remaining") == "0" {
				reset, _ := strconv.ParseInt(header.Get("X-Ratelimit-Reset"), 10, 64)
				return &RateLimitError{Remaining: 0, ResetAt: time.Unix(reset, 0)}
			}
			limited := header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(string(raw)), "rate limit")
			if !limited {
				return fmt.Errorf("GitHub refused the request (HTTP %d): %s", status, firstLine(raw))
			}
			if attempt == maxAttempts {
				return fmt.Errorf("GitHub kept refusing the request (HTTP %d): %s", status, firstLine(raw))
			}
			// Secondary rate limit: wait as long as GitHub asks, or a minute.
			wait := time.Minute
			if s, err := strconv.Atoi(header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}
			c.warnf("secondary rate limit hit, waiting %s", wait)
			c.sleep(wait)
			continue
		case status != http.StatusOK:
			return fmt.Errorf("GitHub API returned HTTP %d: %s", status, firstLine(raw))
		}

		var resp struct {
			Data   json.RawMessage `json:"data"`
			Errors []gqlError      `json:"errors"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return fmt.Errorf("decoding GitHub response: %w", err)
		}
		if len(resp.Data) == 0 || string(resp.Data) == "null" {
			if len(resp.Errors) > 0 {
				msgs := make([]string, len(resp.Errors))
				for i, e := range resp.Errors {
					msgs[i] = e.Message
				}
				return fmt.Errorf("GitHub GraphQL error: %s", strings.Join(msgs, "; "))
			}
			return errors.New("GitHub returned no data")
		}
		for _, e := range resp.Errors {
			c.warnf("skipped part of the answer: %s", e.Message)
		}
		if c.Record != nil {
			c.Record(kind, vars, raw)
		}
		if err := json.Unmarshal(resp.Data, out); err != nil {
			return fmt.Errorf("decoding GitHub data: %w", err)
		}
		var rl struct {
			RateLimit *RateLimit `json:"rateLimit"`
		}
		if json.Unmarshal(resp.Data, &rl) == nil && rl.RateLimit != nil {
			c.mu.Lock()
			c.rate = *rl.RateLimit
			c.mu.Unlock()
			if rl.RateLimit.Remaining < c.MinRemaining {
				return &RateLimitError{Remaining: rl.RateLimit.Remaining, ResetAt: rl.RateLimit.ResetAt}
			}
		}
		return nil
	}
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Search returns the viewer's login and the IDs of open pull requests that
// match q, in search order, at most limit of them.
func (c *Client) Search(ctx context.Context, q string, limit int) (viewer string, ids []string, err error) {
	var after any
	for {
		var data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
			Search struct {
				IssueCount int `json:"issueCount"`
				PageInfo   struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"search"`
		}
		var err error
		for try := 0; try < 3; try++ {
			err = c.do(ctx, "search", searchQuery, map[string]any{"q": q, "after": after}, &data)
			if !errors.Is(err, errTimeout) {
				break
			}
			c.sleep(2 * time.Second)
		}
		if err != nil {
			return "", nil, err
		}
		viewer = data.Viewer.Login
		for _, n := range data.Search.Nodes {
			if n.ID != "" {
				ids = append(ids, n.ID)
			}
		}
		if len(ids) >= limit {
			return viewer, ids[:limit], nil
		}
		if !data.Search.PageInfo.HasNextPage {
			return viewer, ids, nil
		}
		after = data.Search.PageInfo.EndCursor
	}
}

// PullRequests fetches the details for ids in batches, several batches at a
// time, and returns them in the order of ids. A batch that times out is split
// in half and retried; a single pull request that still times out is skipped
// with a warning.
func (c *Client) PullRequests(ctx context.Context, ids []string) ([]PullRequest, error) {
	size := c.BatchSize
	if size <= 0 {
		size = 10
	}
	conc := c.Concurrency
	if conc <= 0 {
		conc = 4
	}
	var batches [][]string
	for i := 0; i < len(ids); i += size {
		batches = append(batches, ids[i:min(i+size, len(ids))])
	}
	results := make([][]PullRequest, len(batches))
	errs := make([]error, len(batches))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, b := range batches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			results[i], errs[i] = c.fetchBatch(ctx, b)
			if errs[i] != nil {
				cancel()
			}
		}()
	}
	wg.Wait()
	var out []PullRequest
	for i := range batches {
		if errs[i] != nil {
			return nil, errs[i]
		}
		out = append(out, results[i]...)
	}
	return out, nil
}

func (c *Client) fetchBatch(ctx context.Context, ids []string) ([]PullRequest, error) {
	var data struct {
		Nodes []*PullRequest `json:"nodes"`
	}
	err := c.do(ctx, "nodes", nodesQuery, map[string]any{"ids": ids}, &data)
	if errors.Is(err, errTimeout) {
		if len(ids) == 1 {
			c.sleep(2 * time.Second)
			err = c.do(ctx, "nodes", nodesQuery, map[string]any{"ids": ids}, &data)
			if errors.Is(err, errTimeout) {
				c.warnf("skipped %s: GitHub timed out on it twice", ids[0])
				return nil, nil
			}
		} else {
			half := len(ids) / 2
			a, err := c.fetchBatch(ctx, ids[:half])
			if err != nil {
				return nil, err
			}
			b, err := c.fetchBatch(ctx, ids[half:])
			if err != nil {
				return nil, err
			}
			return append(a, b...), nil
		}
	}
	if err != nil {
		return nil, err
	}
	var out []PullRequest
	for _, p := range data.Nodes {
		if p != nil && p.ID != "" {
			out = append(out, *p)
		}
	}
	return out, nil
}

// HTTPTransport posts to a GraphQL endpoint with an http.Client that already
// adds authentication (go-gh's).
type HTTPTransport struct {
	Client   *http.Client
	Endpoint string
}

func (t *HTTPTransport) Post(ctx context.Context, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.Client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw, err
}

// GraphQLEndpoint returns the GraphQL URL for a GitHub host.
func GraphQLEndpoint(host string) string {
	switch {
	case host == "" || host == "github.com":
		return "https://api.github.com/graphql"
	case strings.HasSuffix(host, ".ghe.com"):
		return "https://api." + host + "/graphql"
	case host == "github.localhost":
		return "http://api.github.localhost/graphql"
	}
	return "https://" + host + "/api/graphql"
}

const peerFields = `{ nodes { ... on PullRequest { commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 80) { nodes {
  __typename ... on CheckRun { name conclusion } ... on StatusContext { context state }
} } } } } } } } }`

// CheckStats counts, per check name, how many recent pull requests by other
// people ran the check and how many of them failed it.
type CheckStats map[string]*struct{ Ran, Failed int }

// PeerChecks looks at the latest commit of up to perRepo recently updated
// pull requests by other people in each repository, four repositories per
// request and several requests at a time, so a check that fails for everyone
// is not blamed on you.
func (c *Client) PeerChecks(ctx context.Context, repos []string, perRepo int) (map[string]CheckStats, error) {
	out := map[string]CheckStats{}
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, max(c.Concurrency, 1))
	var wg sync.WaitGroup
	for start := 0; start < len(repos); start += 4 {
		chunk := repos[start:min(start+4, len(repos))]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			part := map[string]CheckStats{}
			err := c.peerChecks(ctx, chunk, perRepo, part)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			for k, v := range part {
				out[k] = v
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// peerChecks fills out for one request's worth of repositories, splitting the
// request when GitHub times out and skipping a repository that still does.
func (c *Client) peerChecks(ctx context.Context, chunk []string, perRepo int, out map[string]CheckStats) error {
	{
		var q strings.Builder
		vars := map[string]any{}
		q.WriteString("query(")
		for i := range chunk {
			if i > 0 {
				q.WriteString(", ")
			}
			fmt.Fprintf(&q, "$q%d: String!", i)
		}
		q.WriteString(") {\n")
		for i, repo := range chunk {
			vars[fmt.Sprintf("q%d", i)] = "repo:" + repo + " is:pr -author:@me sort:updated-desc"
			fmt.Fprintf(&q, "  r%d: search(query: $q%d, type: ISSUE, first: %d) %s\n", i, i, perRepo, peerFields)
		}
		q.WriteString("  rateLimit { cost remaining resetAt }\n}")
		var data map[string]json.RawMessage
		err := c.do(ctx, "peers", q.String(), vars, &data)
		if errors.Is(err, errTimeout) {
			if len(chunk) == 1 {
				c.warnf("skipped comparing checks with other pull requests in %s: GitHub timed out", chunk[0])
				return nil
			}
			half := len(chunk) / 2
			if err := c.peerChecks(ctx, chunk[:half], perRepo, out); err != nil {
				return err
			}
			return c.peerChecks(ctx, chunk[half:], perRepo, out)
		}
		if err != nil {
			return err
		}
		for i, repo := range chunk {
			var s struct {
				Nodes []struct {
					Commits struct {
						Nodes []struct {
							Commit Commit `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
				} `json:"nodes"`
			}
			if raw, ok := data[fmt.Sprintf("r%d", i)]; !ok || json.Unmarshal(raw, &s) != nil {
				continue
			}
			stats := CheckStats{}
			for _, n := range s.Nodes {
				if len(n.Commits.Nodes) == 0 || n.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
					continue
				}
				seen := map[string]bool{}
				for _, x := range n.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes {
					name := x.CheckName()
					if seen[name] || (x.Typename == "CheckRun" && (x.Conclusion == "" || x.Conclusion == "SKIPPED" || x.Conclusion == "CANCELLED")) {
						continue
					}
					seen[name] = true
					st := stats[name]
					if st == nil {
						st = &struct{ Ran, Failed int }{}
						stats[name] = st
					}
					st.Ran++
					if x.Failed() {
						st.Failed++
					}
				}
			}
			out[repo] = stats
		}
	}
	return nil
}
