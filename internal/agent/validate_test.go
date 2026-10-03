package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Rickyxstar/podcast-agent/internal/report"
)

// validNotes returns notes that pass every check in validate.
func validNotes() *notes {
	return &notes{
		Summary:   strings.Repeat("word ", 250),
		Takeaways: []string{"a", "b", "c", "d", "e"},
		Quotes:    []noteQuote{{Text: "hit break-even in 18 months", Speaker: "Mark", Timestamp: "02:10"}},
		Topics:    []string{"remote-work", "async", "web3", "covid-19"},
	}
}

func TestValidateQuotes(t *testing.T) {
	j := newTestJob(t, newMockLLM(t), Config{})
	n := validNotes()
	n.Quotes = []noteQuote{
		// Straight dash differs from the transcript, and the model got the
		// speaker and timestamp wrong; the transcript should win.
		{Text: "GitLab has been all-remote since day one - no offices at all.", Speaker: "Sarah", Timestamp: "09:99"},
		{Text: "Something nobody said.", Speaker: "Sarah", Timestamp: "00:00"},
		{Text: "Welcome back.", Speaker: "Sarah", Timestamp: "00:00"},
	}

	got := j.validate(context.Background(), n)

	want := []report.Quote{
		{Text: n.Quotes[0].Text, Speaker: "Mark", Timestamp: "01:20", Verified: true},
		{Text: "Something nobody said.", Speaker: "Sarah", Timestamp: "00:00"},
		{Text: "Welcome back.", Speaker: "Sarah", Timestamp: "00:00", Verified: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d quotes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("quote %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(j.warnings) != 1 || !containsPrefix(j.warnings, `quote not found verbatim in transcript: "Something nobody said."`) {
		t.Errorf("warnings = %q, want only the missing quote", j.warnings)
	}
}

func TestValidateNoQuotes(t *testing.T) {
	j := newTestJob(t, newMockLLM(t), Config{})
	n := validNotes()
	n.Quotes = nil
	if got := j.validate(context.Background(), n); got == nil || len(got) != 0 {
		t.Errorf("quotes = %#v, want empty non-nil slice", got)
	}
	if len(j.warnings) != 0 {
		t.Errorf("warnings = %q, want none", j.warnings)
	}
}

func TestValidateLimits(t *testing.T) {
	words := func(n int) string { return strings.Repeat("word ", n) }
	tests := []struct {
		name  string
		edit  func(*notes)
		warns []string
	}{
		{name: "valid", edit: func(*notes) {}},
		{name: "minimum summary", edit: func(n *notes) { n.Summary = words(minSummaryWords) }},
		{name: "maximum summary", edit: func(n *notes) { n.Summary = words(maxSummaryWords) }},
		{
			name:  "short summary",
			edit:  func(n *notes) { n.Summary = words(minSummaryWords - 1) },
			warns: []string{"summary is 199 words, want 200-300"},
		},
		{
			name:  "long summary",
			edit:  func(n *notes) { n.Summary = words(maxSummaryWords + 1) },
			warns: []string{"summary is 301 words, want 200-300"},
		},
		{
			name:  "empty summary",
			edit:  func(n *notes) { n.Summary = "" },
			warns: []string{"summary is 0 words, want 200-300"},
		},
		{
			name:  "too few takeaways",
			edit:  func(n *notes) { n.Takeaways = n.Takeaways[:4] },
			warns: []string{"got 4 takeaways, want 5"},
		},
		{
			name:  "too many takeaways",
			edit:  func(n *notes) { n.Takeaways = append(n.Takeaways, "f") },
			warns: []string{"got 6 takeaways, want 5"},
		},
		{
			name: "bad topics",
			edit: func(n *notes) {
				n.Topics = []string{"remote-work", "Async Culture", "remote_work", "-remote", "remote-", "remote--work", "Remote", ""}
			},
			warns: []string{
				`topic "Async Culture" is not kebab-case`,
				`topic "remote_work" is not kebab-case`,
				`topic "-remote" is not kebab-case`,
				`topic "remote-" is not kebab-case`,
				`topic "remote--work" is not kebab-case`,
				`topic "Remote" is not kebab-case`,
				`topic "" is not kebab-case`,
			},
		},
		{
			name: "everything wrong",
			edit: func(n *notes) {
				n.Summary = words(10)
				n.Takeaways = nil
				n.Topics = []string{"Bad Topic"}
			},
			warns: []string{
				"got 0 takeaways, want 5",
				"summary is 10 words, want 200-300",
				`topic "Bad Topic" is not kebab-case`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := newTestJob(t, newMockLLM(t), Config{})
			n := validNotes()
			tt.edit(n)
			j.validate(context.Background(), n)
			if fmt.Sprint(j.warnings) != fmt.Sprint(tt.warns) {
				t.Errorf("warnings = %q, want %q", j.warnings, tt.warns)
			}
		})
	}
}

func TestFindQuote(t *testing.T) {
	j := newTestJob(t, newMockLLM(t), Config{})
	tests := []struct {
		name  string
		quote string
		want  string // timestamp of the matching segment; empty for no match
	}{
		{"exact segment", "At my last startup we hit break-even in 18 months.", "02:10"},
		{"substring", "break-even in 18 months", "02:10"},
		{"ignores case", "WELCOME BACK", "00:00"},
		{"straight apostrophe", "Today we're diving", "00:00"},
		{"straight dash", "since day one - no offices", "01:20"},
		{"en dash", "since day one – no offices", "01:20"},
		{"collapses whitespace", "  GitLab   has been\n\tall-remote ", "01:20"},
		{"trims straight quotes", `"Welcome back."`, "00:00"},
		{"trims curly quotes", "“Welcome back.”", "00:00"},
		{"trims single quotes", "‘Welcome back.’", "00:00"},
		{"not said", "Something nobody said.", ""},
		{"spans segments", "remote work. GitLab has been", ""},
		{"paraphrase", "GitLab has always been all-remote", ""},
		{"empty", "", ""},
		{"only whitespace", "  \n ", ""},
		{"only quote marks", `"“”'`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seg, ok := j.findQuote(tt.quote)
			if ok != (tt.want != "") || seg.Timestamp != tt.want {
				t.Errorf("findQuote(%q) = %q %v, want %q", tt.quote, seg.Timestamp, ok, tt.want)
			}
		})
	}
}

func TestFindQuoteFirstSegment(t *testing.T) {
	j := newTestJob(t, newMockLLM(t), Config{})
	// "remote" appears in both the first and second segments.
	seg, ok := j.findQuote("remote")
	if !ok || seg.Timestamp != "00:00" || seg.Speaker != "Sarah" {
		t.Errorf("findQuote = %+v %v, want the first segment", seg, ok)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"Hello World":            "hello world",
		"  a \t b\n\nc  ":        "a b c",
		"it’s ‘fine’":            "it's 'fine'",
		"“quoted”":               `"quoted"`,
		"2020–2025 — done":       "2020-2025 - done",
		"wait…":                  "wait...",
		"ALREADY normal - 'ok'.": "already normal - 'ok'.",
	} {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
