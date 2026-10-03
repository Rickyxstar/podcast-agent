package kb

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/search"
)

var testEntries = []Entry{
	{
		ID:        "gitlab-all-remote",
		Statement: "GitLab is one of the world's largest all-remote companies, with team members in over 60 countries.",
		Source:    "GitLab Handbook",
		URL:       "https://handbook.gitlab.com/handbook/company/culture/all-remote/",
		Date:      "2025-01-15",
		Tags:      []string{"remote-work", "gitlab"},
	},
	{
		ID:        "msr-replan",
		Statement: "NASA paused the Mars Sample Return architecture and is reviewing cheaper options; no sample return before the 2030s.",
		Source:    "NASA",
		URL:       "https://science.nasa.gov/mission/mars-sample-return/",
		Date:      "2025-06",
		Tags:      []string{"nasa", "mars", "space"},
	},
	{
		ID:        "telehealth-growth",
		Statement: "Telehealth visits grew sharply during the pandemic, peaking in April 2020.",
		Source:    "CDC",
		Date:      "2021",
		Tags:      []string{"telehealth", "healthcare"},
	},
}

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New(testEntries)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ids(rs []search.Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func TestSearch(t *testing.T) {
	p := newTestProvider(t)
	tests := []struct {
		query string
		want  []string
	}{
		{"GitLab remote company", []string{"gitlab-all-remote"}},
		// Plural folding: "companies" in the entry matches "company" here.
		{"how many companies are fully remote", []string{"gitlab-all-remote"}},
		{"NASA's Mars Sample Return launch date", []string{"msr-replan"}},
		// Tags are searchable.
		{"healthcare", []string{"telehealth-growth"}},
		// Source is searchable.
		{"CDC", []string{"telehealth-growth"}},
		{"bitcoin price", nil},
		{"the of and", nil},
		{"", nil},
	}
	for _, tt := range tests {
		got, err := p.Search(context.Background(), search.Request{Query: tt.query})
		if err != nil {
			t.Fatalf("Search(%q): %v", tt.query, err)
		}
		if !slices.Equal(ids(got), tt.want) {
			t.Errorf("Search(%q) = %v, want %v", tt.query, ids(got), tt.want)
		}
	}
}

func TestSearchResultFields(t *testing.T) {
	p := newTestProvider(t)
	got, err := p.Search(context.Background(), search.Request{Query: "mars sample return"})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	r := got[0]
	if r.Kind != search.KindKB || r.Source != "NASA" || r.URL != testEntries[1].URL || r.Snippet != testEntries[1].Statement {
		t.Errorf("result = %+v", r)
	}
	if r.Published == nil || !r.Published.Equal(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Published = %v, want 2025-06-01", r.Published)
	}
	if r.Score <= 0 {
		t.Errorf("Score = %v, want > 0", r.Score)
	}
}

func TestSearchRanking(t *testing.T) {
	p, err := New([]Entry{
		{ID: "b", Statement: "Remote work is common."},
		{ID: "a", Statement: "Remote work is common."},
		{ID: "c", Statement: "Remote work at GitLab is the default for every GitLab employee."},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := p.Search(context.Background(), search.Request{Query: "gitlab remote"})
	// c matches both terms; a and b tie and break on ID.
	if want := []string{"c", "a", "b"}; !slices.Equal(ids(got), want) {
		t.Errorf("ranking = %v, want %v", ids(got), want)
	}
	got, _ = p.Search(context.Background(), search.Request{Query: "remote", MaxResults: 2})
	if len(got) != 2 {
		t.Errorf("MaxResults 2 returned %d results", len(got))
	}
}

func TestSearchCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newTestProvider(t).Search(ctx, search.Request{Query: "nasa"}); err == nil {
		t.Error("want error for canceled context")
	}
}

func TestNewInvalid(t *testing.T) {
	tests := []struct {
		name    string
		entries []Entry
		wantErr string
	}{
		{"missing id", []Entry{{Statement: "x"}}, "no id"},
		{"duplicate id", []Entry{{ID: "a", Statement: "x"}, {ID: "a", Statement: "y"}}, "duplicate"},
		{"empty statement", []Entry{{ID: "a", Statement: "  "}}, "no statement"},
		{"bad date", []Entry{{ID: "a", Statement: "x", Date: "June 2025"}}, "date"},
	}
	for _, tt := range tests {
		_, err := New(tt.entries)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: err = %v, want containing %q", tt.name, err, tt.wantErr)
		}
	}
}

func TestLoad(t *testing.T) {
	fsys := fstest.MapFS{
		"facts.json": {Data: []byte(`[{"id":"a","statement":"Doist is a remote-first company.","tags":["remote-work"]}]`)},
		"more.json":  {Data: []byte(`[{"id":"b","statement":"Automattic has no main office.","date":"2024"}]`)},
		"README.md":  {Data: []byte("not loaded")},
	}
	p, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := p.Search(context.Background(), search.Request{Query: "automattic office"})
	if want := []string{"b"}; !slices.Equal(ids(got), want) {
		t.Errorf("Search = %v, want %v", ids(got), want)
	}

	if _, err := Load(fstest.MapFS{}); err == nil {
		t.Error("Load of empty dir: want error")
	}
	if _, err := Load(fstest.MapFS{"bad.json": {Data: []byte(`{`)}}); err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("Load of bad JSON: err = %v, want naming the file", err)
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"NASA's Mars Sample-Return!", "nasa mar sample return"},
		{"Bitcoin will reach $200K by 2025", "bitcoin reach 200k 2025"},
		{"status focus", "status focus"},
	}
	for _, tt := range tests {
		if got := strings.Join(tokenize(tt.in), " "); got != tt.want {
			t.Errorf("tokenize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestTokenizeFolds checks that singular and plural forms match. The stems
// themselves can be odd ("mars" → "mar"); queries and entries get the same
// ones, so only equality matters.
func TestTokenizeFolds(t *testing.T) {
	for _, pair := range [][2]string{
		{"company", "companies"},
		{"study", "studies"},
		{"glass", "glasses"},
		{"device", "devices"},
		{"tax", "taxes"},
		{"search", "searches"},
		{"Mars", "mars"},
	} {
		a, b := tokenize(pair[0]), tokenize(pair[1])
		if !slices.Equal(a, b) {
			t.Errorf("tokenize(%q) = %v, tokenize(%q) = %v; want equal", pair[0], a, pair[1], b)
		}
	}
}

// TestRepoKB loads the committed knowledge base, so a malformed entry fails
// CI, and checks that typical claims find their evidence in the top 3, which
// is what the agent sees.
func TestRepoKB(t *testing.T) {
	p, err := LoadDir("../../../kb")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ query, want string }{
		{"NASA's Mars Sample Return launching in 2026", "msr-cancelled-fy2026"},
		{"Bitcoin will reach $200K by 2025", "btc-peak-2025"},
		{"GitLab Automattic Doist handbooks", "gitlab-handbook-first"},
		{"FDA reviews AI systems for reading X-rays", "fda-ai-radiology-share"},
		{"pandemic accelerated telehealth adoption", "cdc-telehealth-early-2020"},
		{"bootstrapped company with no outside capital", "mailchimp-bootstrapped-exit"},
	}
	for _, tt := range tests {
		got, err := p.Search(context.Background(), search.Request{Query: tt.query, MaxResults: 3})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(ids(got), tt.want) {
			t.Errorf("Search(%q) = %v, want %s in the top 3", tt.query, ids(got), tt.want)
		}
	}
}
