package text

import "testing"

func TestAsk(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"Could you add a test for the Windows path?", true},
		{"Thanks! Can you rebase on main", true},
		{"Please sign the CLA.", true},
		{"LGTM, thanks!", false},
		{"Thanks, merging after the release.", false},
		{"> Why not use X?\n\nBecause Y does not handle Z.", false},
		{"```go\nif x? {}\n```\nLooks fine to me.", false},
		{"See https://example.com/?q=1 for the docs.", false},
		{"このPRの意図を教えてください", true},
		{"為什麼要改這個？", true},
		{"", false},
	}
	for _, c := range cases {
		if got, _ := Ask(c.body); got != c.want {
			t.Errorf("Ask(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}

func TestAskReturnsTheSentence(t *testing.T) {
	_, s := Ask("Nice work. One thing: does this also run on Windows? I have not tried.")
	if s != "One thing: does this also run on Windows?" {
		t.Errorf("sentence = %q", s)
	}
}

func TestMentions(t *testing.T) {
	if !Mentions("@b84330808 this breaks on Windows", "B84330808") {
		t.Error("mention not found")
	}
	if Mentions("> @b84330808 wrote: hi\n\nok", "b84330808") {
		t.Error("quoted mention counted")
	}
	if Mentions("mail me at x@b84330808.dev", "b84330808") {
		t.Error("email address counted as a mention")
	}
	got := MentionLogins("cc @alice and @org/team")
	if len(got) != 2 || got[0] != "alice" || got[1] != "org/team" {
		t.Errorf("MentionLogins = %q", got)
	}
}

func TestIsBot(t *testing.T) {
	for _, l := range []string{"dependabot", "github-actions", "codecov-commenter", "CLAassistant", "k8s-ci-robot", "coderabbitai", "copilot-pull-request-reviewer"} {
		if !IsBot("User", l) {
			t.Errorf("%s not seen as a bot", l)
		}
	}
	for _, l := range []string{"alice", "robotics-dev", "b84330808"} {
		if IsBot("User", l) {
			t.Errorf("%s seen as a bot", l)
		}
	}
	if !IsBot("Bot", "anything") {
		t.Error("Bot typename ignored")
	}
}

func TestRedactKeepsSignals(t *testing.T) {
	for _, body := range []string{
		"Could you add a test?",
		"Please sign the CLA.",
		"Merged, thanks.",
		"@alice what do you think",
		"Thanks @me ",
		"@me this breaks on Windows.",
	} {
		r := Redact(body)
		if IsAck(body) != IsAck(r) {
			t.Errorf("Redact(%q) = %q changed IsAck", body, r)
		}
		a1, _ := Ask(body)
		a2, _ := Ask(r)
		if a1 != a2 {
			t.Errorf("Redact(%q) = %q changed Ask from %v to %v", body, r, a1, a2)
		}
		if len(MentionLogins(body)) != len(MentionLogins(r)) {
			t.Errorf("Redact(%q) = %q lost mentions", body, r)
		}
	}
}
