// Package text holds the small heuristics gh-court applies to comment text
// and logins.
package text

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	fence      = regexp.MustCompile("(?s)```.*?(```|$)|~~~.*?(~~~|$)")
	inlineCode = regexp.MustCompile("`[^`\n]*`")
	htmlCmt    = regexp.MustCompile(`(?s)<!--.*?-->`)
	details    = regexp.MustCompile(`(?is)<details>.*?</details>`)
	urlRe      = regexp.MustCompile(`https?://\S+`)
	// requestRe matches requests that are not phrased as questions.
	requestRe  = regexp.MustCompile(`(?i)\b(could|can|would|will) you\b|\bplease\b|\bpls\b|\bplz\b|\bkindly\b|\bmind (adding|updating|rebasing|fixing|changing)\b|\b(needs?|have) to be (rebased|updated|fixed|changed)\b|\bsign (the|our) cla\b|\bwaiting (on|for) you\b|お願い|ください|いただけ|麻煩|請`)
	thanksOnly = regexp.MustCompile(`(?i)^\W*(thanks?( you)?|thx|lgtm|merged|nice|great|looks good( to me)?)\b`)
	mention    = regexp.MustCompile(`(^|\s)@[A-Za-z0-9-]+(/[A-Za-z0-9_.-]+)?`)
)

// Prose returns what a person wrote in a Markdown comment: quotes, code,
// HTML comments, collapsed details and URLs are removed.
func Prose(body string) string {
	s := htmlCmt.ReplaceAllString(body, " ")
	s = details.ReplaceAllString(s, " ")
	s = fence.ReplaceAllString(s, " ")
	s = inlineCode.ReplaceAllString(s, " ")
	s = urlRe.ReplaceAllString(s, " ")
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, ">") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// Ask reports whether a comment asks the reader something: a question mark
// in the prose, or a request such as "could you rebase" or "please add a
// test". The first matching sentence is returned for display.
func Ask(body string) (bool, string) {
	p := Prose(body)
	if p == "" {
		return false, ""
	}
	for _, s := range sentences(p) {
		if strings.ContainsAny(s, "?？") || requestRe.MatchString(s) {
			if thanksOnly.MatchString(s) && !strings.ContainsAny(s, "?？") {
				continue
			}
			return true, s
		}
	}
	return false, ""
}

// IsAck reports whether a comment only acknowledges or thanks, e.g.
// "Thanks @you" or "LGTM!", which asks nothing of the reader.
func IsAck(body string) bool {
	p := strings.TrimSpace(mention.ReplaceAllString(Prose(body), " "))
	if p == "" {
		return true
	}
	if strings.ContainsAny(p, "?？") || requestRe.MatchString(p) {
		return false
	}
	return len([]rune(p)) <= 80 && thanksOnly.MatchString(p)
}

// Mentions reports whether the prose of body @-mentions login.
func Mentions(body, login string) bool {
	for _, m := range mention.FindAllString(Prose(body), -1) {
		m = strings.TrimSpace(m)
		if strings.EqualFold(strings.TrimPrefix(m, "@"), login) {
			return true
		}
	}
	return false
}

// MentionLogins returns the logins @-mentioned in the prose of body.
func MentionLogins(body string) []string {
	var out []string
	for _, m := range mention.FindAllString(Prose(body), -1) {
		out = append(out, strings.TrimPrefix(strings.TrimSpace(m), "@"))
	}
	return out
}

func sentences(p string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	rs := []rune(p)
	for i, r := range rs {
		if r == '\n' {
			flush()
			continue
		}
		b.WriteRune(r)
		end := r == '?' || r == '!' || r == '？' || r == '！' || r == '。'
		if r == '.' && (i+1 == len(rs) || unicode.IsSpace(rs[i+1])) {
			end = true
		}
		if end {
			flush()
		}
	}
	flush()
	return out
}

// Snippet shortens s to at most n runes on one line.
func Snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

var botLogin = regexp.MustCompile(`(?i)(\[bot\]$|-bot$|bot$|-ci$|^codecov|^claassistant$|^cla-assistant|^sonarcloud|^sonarqubecloud|^netlify|^vercel|^coderabbit|^copilot|^linux-foundation-easycla|^google-cla|^changeset-bot|^k8s-ci-robot|^openshift-ci|^welcome|^allcontributors|^mergify|^gitguardian|^socket-security|^pre-commit-ci|^github-actions)`)

// IsBot reports whether an actor is automation. GitHub marks app accounts
// as "Bot", but many CI and CLA accounts are plain users, so well-known
// login patterns count too.
func IsBot(typename, login string) bool {
	return typename == "Bot" || botLogin.MatchString(login)
}

// Redact replaces a comment body with neutral text that keeps the signals
// gh-court reads from it (whether it asks something, whom it mentions), so
// recorded fixtures do not republish other people's words.
func Redact(body string) string {
	if strings.TrimSpace(body) == "" {
		return body
	}
	out := "[redacted]"
	if IsAck(body) {
		out = "Thanks."
	} else if ok, s := Ask(body); ok {
		if strings.ContainsAny(s, "?？") {
			out += " Question?"
		} else {
			out += " Please do it."
		}
	}
	for _, m := range mention.FindAllString(Prose(body), -1) {
		out += " " + strings.TrimSpace(m)
	}
	return out
}
