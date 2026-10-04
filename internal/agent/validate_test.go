package agent

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// validNotes returns notes that pass every check.
func validNotes() *notes {
	return &notes{
		Summary:   strings.Repeat("word ", 250),
		Takeaways: []string{"a", "b", "c", "d", "e"},
		Quotes: []noteQuote{
			{Text: "Welcome back.", Speaker: "Sarah", Timestamp: "00:00"},
			{Text: "GitLab has been all-remote since day one — no offices at all.", Speaker: "Mark", Timestamp: "01:20"},
			{Text: "At my last startup we hit break-even in 18 months.", Speaker: "Mark", Timestamp: "02:10"},
		},
		Topics: []string{"remote-work", "async", "covid-19"},
	}
}

// quotes returns notes quotes with the given texts and made-up attribution.
func quotes(texts ...string) []noteQuote {
	qs := make([]noteQuote, len(texts))
	for i, t := range texts {
		qs[i] = noteQuote{Text: t, Speaker: "Nobody", Timestamp: "99:99"}
	}
	return qs
}

func TestCheckValid(t *testing.T) {
	j := newTestJob(t, newMockLLM(t), Config{})
	n := validNotes()
	c := j.check(n)
	if c.needsRepair() || len(c.failed) != 0 || len(c.issues) != 0 {
		t.Errorf("check = %+v, want no problems", c)
	}
	if !slices.Equal(c.notes.Quotes, n.Quotes) {
		t.Errorf("quotes = %+v, want %+v", c.notes.Quotes, n.Quotes)
	}
	j.finish(c)
	if len(j.warnings) != 0 {
		t.Errorf("warnings = %q, want none", j.warnings)
	}
}

func TestCheckQuotes(t *testing.T) {
	j := newTestJob(t, newMockLLM(t), Config{})
	n := validNotes()
	n.Quotes = []noteQuote{
		// The straight dash differs from the transcript, and the model got
		// the speaker and timestamp wrong; the transcript should win.
		{Text: "GitLab has been all-remote since day one - no offices at all.", Speaker: "Sarah", Timestamp: "09:99"},
		// One word off: not said, but close to a line.
		{Text: "At my last startup we hit breakeven in 18 months.", Speaker: "Sarah", Timestamp: "02:10"},
		{Text: "Something nobody said.", Speaker: "Sarah", Timestamp: "00:00"},
		{Text: "“Welcome back.”", Speaker: "Sarah", Timestamp: "00:00"},
		{Text: "welcome  back.", Speaker: "Sarah", Timestamp: "00:00"},
	}

	c := j.check(n)

	want := []noteQuote{
		{Text: "GitLab has been all-remote since day one - no offices at all.", Speaker: "Mark", Timestamp: "01:20"},
		{Text: "Welcome back.", Speaker: "Sarah", Timestamp: "00:00"},
	}
	if !slices.Equal(c.notes.Quotes, want) {
		t.Errorf("quotes = %+v, want %+v", c.notes.Quotes, want)
	}
	if len(c.failed) != 3 {
		t.Fatalf("failed = %+v, want 3", c.failed)
	}
	if f := c.failed[0]; f.duplicate || f.closest == nil || f.closest.Timestamp != "02:10" {
		t.Errorf("failed[0] = %+v, want not found, closest to 02:10", f)
	}
	if f := c.failed[1]; f.text != "Something nobody said." || f.duplicate || f.closest != nil {
		t.Errorf("failed[1] = %+v, want not found, nothing close", f)
	}
	if f := c.failed[2]; f.text != "welcome  back." || !f.duplicate {
		t.Errorf("failed[2] = %+v, want a duplicate", f)
	}
	if c.missing != 3 || !c.needsRepair() {
		t.Errorf("missing = %d, needsRepair = %v; want 3, true", c.missing, c.needsRepair())
	}
}

func TestCheckMissing(t *testing.T) {
	// Distinct stretches of testEpisode.
	said := []string{
		"Welcome back.",
		"Today we’re diving into remote work.",
		"GitLab has been all-remote",
		"no offices at all.",
		"At my last startup",
		"hit break-even in 18 months.",
	}
	bad := []string{"Never said.", "Also never said.", "Not this either.", "Nor this."}
	tests := []struct {
		name         string
		quotes       []string
		kept, failed int
		missing      int
	}{
		{"enough", said[:3], 3, 0, 0},
		{"too few", said[:1], 1, 0, 2},
		{"none", nil, 0, 0, 3},
		{"replace one", append(slices.Clone(said[:4]), bad[0]), 4, 1, 1},
		{"replace and top up", append(slices.Clone(said[:1]), bad[0]), 1, 1, 2},
		{"all bad", bad, 0, 4, 4},
		{"full", append(slices.Clone(said[:5]), bad[0]), 5, 1, 0},
		{"too many", said, 5, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := newTestJob(t, newMockLLM(t), Config{})
			n := validNotes()
			n.Quotes = quotes(tt.quotes...)
			c := j.check(n)
			if len(c.notes.Quotes) != tt.kept || len(c.failed) != tt.failed || c.missing != tt.missing {
				t.Errorf("kept %d, failed %d, missing %d; want %d, %d, %d",
					len(c.notes.Quotes), len(c.failed), c.missing, tt.kept, tt.failed, tt.missing)
			}
		})
	}
}

func TestCheckLimits(t *testing.T) {
	words := func(n int) string { return strings.Repeat("word ", n) }
	tests := []struct {
		name   string
		edit   func(*notes)
		issues []string
	}{
		{name: "valid", edit: func(*notes) {}},
		{name: "minimum summary", edit: func(n *notes) { n.Summary = words(minSummaryWords) }},
		{name: "maximum summary", edit: func(n *notes) { n.Summary = words(maxSummaryWords) }},
		{
			name:   "short summary",
			edit:   func(n *notes) { n.Summary = words(minSummaryWords - 1) },
			issues: []string{"summary: summary is 199 words, want 200-300"},
		},
		{
			name:   "long summary",
			edit:   func(n *notes) { n.Summary = words(maxSummaryWords + 1) },
			issues: []string{"summary: summary is 301 words, want 200-300"},
		},
		{
			name:   "empty summary",
			edit:   func(n *notes) { n.Summary = "" },
			issues: []string{"summary: summary is 0 words, want 200-300"},
		},
		{
			name:   "too few takeaways",
			edit:   func(n *notes) { n.Takeaways = n.Takeaways[:4] },
			issues: []string{"takeaways: got 4 takeaways, want 5"},
		},
		{
			name:   "too many takeaways",
			edit:   func(n *notes) { n.Takeaways = append(n.Takeaways, "f") },
			issues: []string{"takeaways: got 6 takeaways, want 5"},
		},
		{
			name: "messy topics are tidied, not flagged",
			edit: func(n *notes) { n.Topics = []string{"Remote Work", "async_culture", "-hiring-"} },
		},
		{
			name:   "too few topics after tidying",
			edit:   func(n *notes) { n.Topics = []string{"remote-work", "Remote Work", "remote_work", "!!"} },
			issues: []string{"topics: got 1 topics, want 3-8"},
		},
		{
			name:   "too many topics",
			edit:   func(n *notes) { n.Topics = strings.Fields("a b c d e f g h i") },
			issues: []string{"topics: got 9 topics, want 3-8"},
		},
		{
			name: "everything wrong",
			edit: func(n *notes) {
				n.Summary = words(10)
				n.Takeaways = nil
				n.Topics = []string{"Bad Topic"}
			},
			issues: []string{
				"summary: summary is 10 words, want 200-300",
				"takeaways: got 0 takeaways, want 5",
				"topics: got 1 topics, want 3-8",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := newTestJob(t, newMockLLM(t), Config{})
			n := validNotes()
			tt.edit(n)
			c := j.check(n)
			var got []string
			for _, is := range c.issues {
				got = append(got, is.field+": "+is.problem)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.issues) {
				t.Errorf("issues = %q, want %q", got, tt.issues)
			}
			if c.needsRepair() != (len(tt.issues) > 0) {
				t.Errorf("needsRepair = %v with issues %q", c.needsRepair(), got)
			}
		})
	}
}

func TestFinish(t *testing.T) {
	j := newTestJob(t, newMockLLM(t), Config{})
	n := validNotes()
	n.Summary = "too short"
	n.Quotes = quotes("Welcome back.", "Something nobody said.", "Welcome back.")

	got := j.finish(j.check(n))

	if len(got.Quotes) != 1 {
		t.Errorf("quotes = %+v, want only the verified one", got.Quotes)
	}
	want := []string{
		`dropped quote: "Something nobody said." was not found in the transcript`,
		`dropped quote: "Welcome back." repeats another quote`,
		"got 1 quotes, want 3-5",
		"summary is 2 words, want 200-300",
	}
	if len(j.warnings) != len(want) {
		t.Fatalf("warnings = %q, want %d", j.warnings, len(want))
	}
	for i, w := range want {
		if !strings.HasPrefix(j.warnings[i], w) {
			t.Errorf("warning %d = %q, want prefix %q", i, j.warnings[i], w)
		}
	}
}

func TestTidyTopics(t *testing.T) {
	got := tidyTopics([]string{"remote-work", "Async Culture", "remote_work", "  AI/ML ", "covid-19", "--", "", "Café Talk", "async-culture"})
	want := []string{"remote-work", "async-culture", "ai-ml", "covid-19", "café-talk"}
	if !slices.Equal(got, want) {
		t.Errorf("tidyTopics = %q, want %q", got, want)
	}
}

func TestMatchQuote(t *testing.T) {
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
		{"comma for dash", "GitLab has been all-remote since day one, no offices at all", "01:20"},
		{"collapses whitespace", "  GitLab   has been\n\tall-remote ", "01:20"},
		{"trims quote marks", "“Welcome back.”", "00:00"},
		{"partial word", "last startup we hit break-even in 18 month", ""},
		{"dropped hyphen", "At my last startup we hit breakeven in 18 months.", ""},
		{"not said", "Something nobody said.", ""},
		{"spans segments", "remote work. GitLab has been", ""},
		{"paraphrase", "GitLab has always been all-remote", ""},
		{"empty", "", ""},
		{"only whitespace", "  \n ", ""},
		{"only quote marks", `"“”'`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ok := j.matchQuote(tt.quote)
			if ok != (tt.want != "") || ok && m.seg.Timestamp != tt.want {
				t.Errorf("matchQuote(%q) = %s, %v; want %q", tt.quote, m.seg.Timestamp, ok, tt.want)
			}
		})
	}
}

func TestMatchQuoteFirstSegment(t *testing.T) {
	j := newTestJob(t, newMockLLM(t), Config{})
	// "remote" appears in both the first and second segments.
	m, ok := j.matchQuote("remote")
	if !ok || m.seg.Timestamp != "00:00" || m.seg.Speaker != "Sarah" || m.index != 0 {
		t.Errorf("matchQuote = %+v %v, want the first segment", m, ok)
	}
}

func TestClosestLine(t *testing.T) {
	j := newTestJob(t, newMockLLM(t), Config{})
	tests := []struct {
		quote string
		want  string // timestamp of the closest segment; empty for none
	}{
		{"At our last startup we broke even in 18 months.", "02:10"},
		{"GitLab has never had offices", "01:20"},
		{"remote", "00:00"}, // ties go to the earliest
		{"Something nobody said.", ""},
		{"Welcome to the show, everybody", ""}, // shares under half
		{"", ""},
	}
	for _, tt := range tests {
		got := j.closestLine(tt.quote)
		if tt.want == "" && got != nil || tt.want != "" && (got == nil || got.Timestamp != tt.want) {
			t.Errorf("closestLine(%q) = %+v, want %q", tt.quote, got, tt.want)
		}
	}
}
