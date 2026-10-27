package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/b84330808/gh-court/internal/court"
	"github.com/b84330808/gh-court/internal/github"
	"github.com/b84330808/gh-court/internal/render"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// replay answers GraphQL requests from responses recorded with --record.
// Requests are matched by their variables.
type replay struct {
	t    *testing.T
	resp map[string][]byte
}

func key(vars any) string {
	b, _ := json.Marshal(vars) // map keys are sorted
	return string(b)
}

func loadReplay(t *testing.T, dir string) *replay {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s", dir)
	}
	r := &replay{t: t, resp: map[string][]byte{}}
	for _, f := range files {
		if strings.HasSuffix(f, "meta.json") || strings.Contains(f, "golden") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var rec struct {
			Variables map[string]any  `json:"variables"`
			Response  json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		r.resp[key(rec.Variables)] = rec.Response
	}
	return r
}

func (r *replay) Post(_ context.Context, body []byte) (int, http.Header, []byte, error) {
	var req struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0, nil, nil, err
	}
	if b, ok := r.resp[key(req.Variables)]; ok {
		return 200, nil, b, nil
	}
	return 0, nil, nil, fmt.Errorf("no recorded response for variables %s", key(req.Variables))
}

type meta struct {
	Now      time.Time `json:"now"`
	Reviewer bool      `json:"reviewer"`
	Teams    bool      `json:"teams"`
}

// TestRecordedAccount replays real GraphQL responses (comment bodies
// redacted) for b84330808's open pull requests and compares the verdicts
// with golden files. Run with -update after an intended change and review
// the diff.
func TestRecordedAccount(t *testing.T) {
	dirs, _ := filepath.Glob(filepath.Join("testdata", "*"))
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		t.Run(filepath.Base(dir), func(t *testing.T) {
			var m meta
			b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			client := &github.Client{T: loadReplay(t, dir), BatchSize: 8, Concurrency: 3, Sleep: func(time.Duration) {}}
			c := config{reviewer: m.Reviewer, teams: m.Teams, staleDays: 14, limit: 300, peers: 8}
			results, err := collect(context.Background(), client, c, m.Now)
			if err != nil {
				t.Fatal(err)
			}
			var js, table, md bytes.Buffer
			if err := render.JSON(&js, results); err != nil {
				t.Fatal(err)
			}
			opt := render.Options{Now: m.Now, StaleDays: 14, ShowURL: true}
			if m.Reviewer {
				opt.Mode = court.Reviewer
			}
			render.Table(&table, results, opt)
			render.Markdown(&md, results, opt)
			golden(t, filepath.Join(dir, "golden.json"), js.Bytes())
			golden(t, filepath.Join(dir, "golden.txt"), table.Bytes())
			golden(t, filepath.Join(dir, "golden.md"), md.Bytes())
		})
	}
}

func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		gl, wl := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
		for i := 0; i < max(len(gl), len(wl)); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("%s differs at line %d:\n got: %s\nwant: %s\n(run go test -update if the change is intended)", path, i+1, g, w)
			}
		}
	}
}

func TestParseTurns(t *testing.T) {
	w, err := parseTurns("you, Stale")
	if err != nil || !w[court.You] || !w[court.Stale] || w[court.Them] {
		t.Errorf("parseTurns = %v, %v", w, err)
	}
	if _, err := parseTurns("mine,nobody"); err == nil {
		t.Error("unknown turn accepted")
	}
}

func TestFlagErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--json", "--markdown"}, &out, &errb); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if code := run([]string{"--turn", "bogus"}, &out, &errb); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if code := run([]string{"--version"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "gh-court ") {
		t.Errorf("--version: exit %d, %q", code, out.String())
	}
}
