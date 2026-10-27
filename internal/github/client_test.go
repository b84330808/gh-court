package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTransport answers nodes() queries with one stub pull request per ID
// and lets a test inject failures.
type fakeTransport struct {
	mu       sync.Mutex
	calls    [][]string
	failOver int // batches larger than this get HTTP 502
	limited  int // answer this many first requests with a secondary rate limit
}

func (f *fakeTransport) Post(_ context.Context, body []byte) (int, http.Header, []byte, error) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0, nil, nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.limited > 0 {
		f.limited--
		h := http.Header{}
		h.Set("Retry-After", "3")
		return 403, h, []byte(`{"message":"You have exceeded a secondary rate limit"}`), nil
	}
	if strings.Contains(req.Query, "search(") {
		return 200, nil, []byte(`{"data":{"viewer":{"login":"me"},"search":{"issueCount":2,"pageInfo":{"hasNextPage":false},"nodes":[{"id":"A"},{},{"id":"B"}]},"rateLimit":{"cost":1,"remaining":4000}}}`), nil
	}
	var ids []string
	for _, id := range req.Variables["ids"].([]any) {
		ids = append(ids, id.(string))
	}
	f.calls = append(f.calls, ids)
	if f.failOver > 0 && len(ids) > f.failOver {
		return 502, nil, []byte("Bad Gateway"), nil
	}
	var nodes []string
	for i, id := range ids {
		if id == "gone" {
			nodes = append(nodes, "null")
			continue
		}
		nodes = append(nodes, fmt.Sprintf(`{"id":%q,"number":%d}`, id, i+1))
	}
	return 200, nil, []byte(`{"data":{"nodes":[` + strings.Join(nodes, ",") + `],"rateLimit":{"cost":1,"remaining":4000}},
		"errors":[{"type":"FORBIDDEN","message":"Resource protected by organization SAML enforcement"}]}`), nil
}

func ids(n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("PR_%02d", i))
	}
	return out
}

func TestBatchesKeepOrder(t *testing.T) {
	f := &fakeTransport{}
	c := &Client{T: f, BatchSize: 10, Concurrency: 3, Sleep: func(time.Duration) {}}
	in := ids(25)
	prs, err := c.PullRequests(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 {
		t.Errorf("%d requests, want 3", len(f.calls))
	}
	for i, p := range prs {
		if p.ID != in[i] {
			t.Fatalf("result %d is %s, want %s", i, p.ID, in[i])
		}
	}
}

func TestTimeoutSplitsBatch(t *testing.T) {
	f := &fakeTransport{failOver: 3}
	var warn strings.Builder
	c := &Client{T: f, BatchSize: 10, Concurrency: 1, Sleep: func(time.Duration) {}, Warn: &warn}
	prs, err := c.PullRequests(context.Background(), append(ids(9), "gone"))
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 9 {
		t.Errorf("got %d pull requests, want 9 (the null node is dropped)", len(prs))
	}
	for _, call := range f.calls[1:] {
		if len(call) >= 10 {
			t.Errorf("batch of %d was not split", len(call))
		}
	}
	if !strings.Contains(warn.String(), "SAML") {
		t.Errorf("partial GraphQL error not reported: %q", warn.String())
	}
}

func TestSecondaryRateLimitWaits(t *testing.T) {
	f := &fakeTransport{limited: 2}
	var slept []time.Duration
	c := &Client{T: f, Sleep: func(d time.Duration) { slept = append(slept, d) }}
	viewer, got, err := c.Search(context.Background(), "is:pr", 100)
	if err != nil {
		t.Fatal(err)
	}
	if viewer != "me" || len(got) != 2 {
		t.Errorf("viewer %q, ids %v", viewer, got)
	}
	if len(slept) != 2 || slept[0] != 3*time.Second {
		t.Errorf("slept %v, want two 3s waits", slept)
	}
	if n, _ := c.Stats(); n != 3 {
		t.Errorf("%d requests, want 3", n)
	}
}

func TestStopsBeforeRateLimitRunsOut(t *testing.T) {
	c := &Client{T: &fakeTransport{}, MinRemaining: 5000}
	_, err := c.PullRequests(context.Background(), ids(3))
	if _, ok := err.(*RateLimitError); !ok {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
}

func TestGraphQLEndpoint(t *testing.T) {
	for host, want := range map[string]string{
		"github.com":      "https://api.github.com/graphql",
		"acme.ghe.com":    "https://api.acme.ghe.com/graphql",
		"git.example.com": "https://git.example.com/api/graphql",
	} {
		if got := GraphQLEndpoint(host); got != want {
			t.Errorf("GraphQLEndpoint(%q) = %q, want %q", host, got, want)
		}
	}
}
