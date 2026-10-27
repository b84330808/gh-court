// Package render prints gh-court results as a terminal table, Markdown or
// JSON.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/b84330808/gh-court/internal/court"
)

// width measures text the same way in every locale. go-runewidth's default
// counts ambiguous characters such as "…" as two columns in CJK locales,
// which most terminals do not do.
var width = &runewidth.Condition{EastAsianWidth: false}

// Group sorts results into turns, each oldest first (longest waiting at the
// top).
func Group(rs []court.Result) map[court.Turn][]court.Result {
	g := map[court.Turn][]court.Result{}
	for _, r := range rs {
		g[r.Turn] = append(g[r.Turn], r)
	}
	for _, list := range g {
		sort.SliceStable(list, func(i, j int) bool {
			if !list[i].Since.Equal(list[j].Since) {
				return list[i].Since.Before(list[j].Since)
			}
			return list[i].Ref() < list[j].Ref()
		})
	}
	return g
}

func heading(t court.Turn, mode court.Mode, staleDays int) string {
	switch t {
	case court.You:
		return "Your turn"
	case court.Them:
		if mode == court.Reviewer {
			return "Their turn (waiting on the author)"
		}
		return "Their turn"
	}
	return fmt.Sprintf("Stale (no activity for %d+ days, consider a nudge)", staleDays)
}

// Options control the table output.
type Options struct {
	Mode      court.Mode
	Now       time.Time
	StaleDays int
	Width     int  // terminal width; 0 means do not truncate titles
	Color     bool // ANSI colors and OSC 8 links
	ShowURL   bool // print the URL under each row (when links are not clickable)
}

const (
	reset = "\x1b[0m"
	bold  = "\x1b[1m"
	dim   = "\x1b[2m"
)

var turnColor = map[court.Turn]string{court.You: "\x1b[33m", court.Them: "\x1b[36m", court.Stale: "\x1b[35m"}

// Table writes the grouped, human-readable report.
func Table(w io.Writer, rs []court.Result, o Options) {
	g := Group(rs)
	paint := func(code, s string) string {
		if !o.Color || code == "" {
			return s
		}
		return code + s + reset
	}
	refW := 0
	for _, r := range rs {
		refW = max(refW, width.StringWidth(r.Ref()))
	}
	refW = min(refW, 42)
	first := true
	for _, t := range court.Turns {
		list := g[t]
		if len(list) == 0 {
			continue
		}
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		fmt.Fprintln(w, paint(bold+turnColor[t], fmt.Sprintf("%s · %d", heading(t, o.Mode, o.StaleDays), len(list))))
		for _, r := range list {
			age := court.Ago(o.Now.Sub(r.Since))
			ref := width.Truncate(r.Ref(), refW, "…")
			pad := strings.Repeat(" ", refW-width.StringWidth(ref))
			if o.Color {
				ref = "\x1b]8;;" + r.URL + "\x1b\\" + ref + "\x1b]8;;\x1b\\"
			}
			title := r.Title
			if r.Draft {
				title = "[draft] " + title
			}
			if o.Width > 0 {
				room := o.Width - (2 + 4 + 2 + refW + 2)
				if room < 10 {
					room = 10
				}
				title = width.Truncate(title, room, "…")
			}
			fmt.Fprintf(w, "  %4s  %s%s  %s\n", age, paint(bold, ref), pad, title)
			for _, x := range r.Reasons {
				fmt.Fprintf(w, "        - %s\n", x.Text)
			}
			for _, x := range r.Notes {
				fmt.Fprintf(w, "        %s\n", paint(dim, "· "+x.Text))
			}
			if r.Nudge != "" {
				fmt.Fprintf(w, "        %s %s\n", paint(turnColor[court.Stale], "nudge:"), r.Nudge)
			}
			if o.ShowURL {
				fmt.Fprintf(w, "        %s\n", paint(dim, r.URL))
			}
		}
	}
	if first {
		fmt.Fprintln(w, "No open pull requests found.")
	}
}

var mdEscaper = strings.NewReplacer("|", `\|`, "<", "&lt;", ">", "&gt;", "\n", " ", "\r", "", "[", `\[`, "]", `\]`)

// Markdown writes one table per turn, for issues, notes or chat.
func Markdown(w io.Writer, rs []court.Result, o Options) {
	g := Group(rs)
	wrote := false
	for _, t := range court.Turns {
		list := g[t]
		if len(list) == 0 {
			continue
		}
		if wrote {
			fmt.Fprintln(w)
		}
		wrote = true
		fmt.Fprintf(w, "### %s (%d)\n\n", heading(t, o.Mode, o.StaleDays), len(list))
		fmt.Fprintln(w, "| Waiting | Pull request | Why |")
		fmt.Fprintln(w, "| --- | --- | --- |")
		for _, r := range list {
			var why []string
			for _, x := range r.Reasons {
				why = append(why, mdEscaper.Replace(x.Text))
			}
			for _, x := range r.Notes {
				why = append(why, "<sub>"+mdEscaper.Replace(x.Text)+"</sub>")
			}
			if r.Nudge != "" {
				why = append(why, "**Nudge:** "+mdEscaper.Replace(r.Nudge))
			}
			title := mdEscaper.Replace(r.Title)
			if r.Draft {
				title = "(draft) " + title
			}
			fmt.Fprintf(w, "| %s | [%s](%s) %s | %s |\n", court.Ago(o.Now.Sub(r.Since)), r.Ref(), r.URL, title, strings.Join(why, "<br>"))
		}
	}
	if !wrote {
		fmt.Fprintln(w, "No open pull requests found.")
	}
}

// JSON writes all results as an array, grouped and sorted like the table.
func JSON(w io.Writer, rs []court.Result) error {
	g := Group(rs)
	out := []court.Result{}
	for _, t := range court.Turns {
		out = append(out, g[t]...)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}
