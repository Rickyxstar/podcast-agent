package brave

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/search"
)

// fakeBrave answers with the next status/reply pair, repeating the last one,
// and records each request.
type fakeBrave struct {
	statuses []int
	replies  []string
	headers  http.Header
	reqs     []*http.Request
}

func (f *fakeBrave) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	i := min(len(f.reqs), len(f.replies)-1)
	f.reqs = append(f.reqs, r)
	for k, v := range f.headers {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	if len(f.statuses) > 0 {
		w.WriteHeader(f.statuses[min(i, len(f.statuses)-1)])
	}
	io.WriteString(w, f.replies[i])
}

func newTestProvider(t *testing.T, f *fakeBrave, cfg Config) (*Provider, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL
	cfg.APIKey = "test-key"
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	p.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return p, &waits
}

const okReply = `{
	"type": "search",
	"query": {"original": "mars sample return"},
	"web": {
		"type": "search",
		"results": [
			{
				"title": "Mars Sample Return - NASA Science",
				"url": "https://science.nasa.gov/mission/mars-sample-return/",
				"description": "<strong>Mars Sample Return</strong> would bring samples &amp; cores to Earth.",
				"page_age": "2025-01-07T18:22:41",
				"age": "January 7, 2025",
				"meta_url": {"hostname": "science.nasa.gov"},
				"extra_snippets": ["NASA is reviewing <strong>two</strong> landing options."]
			},
			{
				"title": "MSR news",
				"url": "https://example.com/msr",
				"description": "No date here."
			},
			{
				"title": "Third",
				"url": "https://example.org/3",
				"description": "Over the limit."
			}
		]
	}
}`

func TestSearch(t *testing.T) {
	f := &fakeBrave{replies: []string{okReply}}
	p, _ := newTestProvider(t, f, Config{})

	got, err := p.Search(context.Background(), search.Request{Query: "mars sample return", MaxResults: 2})
	if err != nil {
		t.Fatal(err)
	}

	r := f.reqs[0]
	if r.URL.Path != "/web/search" {
		t.Errorf("path = %q, want /web/search", r.URL.Path)
	}
	if want := (url.Values{"q": {"mars sample return"}, "count": {"2"}}); r.URL.Query().Encode() != want.Encode() {
		t.Errorf("query = %q, want %q", r.URL.RawQuery, want.Encode())
	}
	if r.Header.Get("X-Subscription-Token") != "test-key" {
		t.Errorf("X-Subscription-Token = %q", r.Header.Get("X-Subscription-Token"))
	}

	if len(got) != 2 {
		t.Fatalf("got %d results, want 2 (MaxResults)", len(got))
	}
	first := got[0]
	want := search.Result{
		ID:      "https://science.nasa.gov/mission/mars-sample-return/",
		Title:   "Mars Sample Return - NASA Science",
		URL:     "https://science.nasa.gov/mission/mars-sample-return/",
		Snippet: "Mars Sample Return would bring samples & cores to Earth. … NASA is reviewing two landing options.",
		Source:  "science.nasa.gov",
		Kind:    search.KindWeb,
		Score:   1,
	}
	gotNoTime := first
	gotNoTime.Published = nil
	if gotNoTime != want {
		t.Errorf("result =\n%+v\nwant\n%+v", gotNoTime, want)
	}
	if first.Published == nil || !first.Published.Equal(time.Date(2025, 1, 7, 18, 22, 41, 0, time.UTC)) {
		t.Errorf("Published = %v", first.Published)
	}
	if got[1].Published != nil || got[1].Score != 0.5 {
		t.Errorf("second result Published = %v, Score = %v", got[1].Published, got[1].Score)
	}
}

func TestSearchNoResults(t *testing.T) {
	f := &fakeBrave{replies: []string{`{"type":"search","query":{"original":"zzz"}}`}}
	p, _ := newTestProvider(t, f, Config{})
	got, err := p.Search(context.Background(), search.Request{Query: "zzz", MaxResults: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want empty non-nil slice", got)
	}
	if c := f.reqs[0].URL.Query().Get("count"); c != "20" {
		t.Errorf("count = %s, want capped at 20", c)
	}
}

func TestSearchRetries(t *testing.T) {
	f := &fakeBrave{
		statuses: []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusOK},
		replies:  []string{`{"error":{"detail":"slow down"}}`, `bad gateway`, okReply},
		headers:  http.Header{"X-Ratelimit-Reset": {"1, 2419200"}},
	}
	p, waits := newTestProvider(t, f, Config{})
	got, err := p.Search(context.Background(), search.Request{Query: "mars"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 3 || len(got) != 3 {
		t.Errorf("requests = %d, results = %d; want 3, 3", len(f.reqs), len(got))
	}
	if want := []time.Duration{time.Second, time.Second}; len(*waits) != 2 || (*waits)[0] != want[0] || (*waits)[1] != want[1] {
		t.Errorf("waits = %v, want %v (from X-RateLimit-Reset)", *waits, want)
	}
}

func TestSearchErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		reply      string
		maxRetries int
		wantReqs   int
		wantStatus int
		wantMsg    string
	}{
		{"bad key not retried", http.StatusUnauthorized, `{"type":"ErrorResponse","error":{"code":"SUBSCRIPTION_TOKEN_INVALID","detail":"The provided subscription token is invalid.","status":401}}`, 0, 1, 401, "The provided subscription token is invalid."},
		{"rate limit gives up", http.StatusTooManyRequests, `{"error":{"detail":"Request rate limit exceeded"}}`, 2, 3, 429, "Request rate limit exceeded"},
		{"retries disabled", http.StatusServiceUnavailable, `down`, -1, 1, 503, "down"},
	}
	for _, tt := range tests {
		f := &fakeBrave{statuses: []int{tt.status}, replies: []string{tt.reply}}
		p, _ := newTestProvider(t, f, Config{MaxRetries: tt.maxRetries})
		_, err := p.Search(context.Background(), search.Request{Query: "x"})
		var se *StatusError
		if !errors.As(err, &se) {
			t.Errorf("%s: err = %v, want *StatusError", tt.name, err)
			continue
		}
		if se.StatusCode != tt.wantStatus || se.Message != tt.wantMsg || len(f.reqs) != tt.wantReqs {
			t.Errorf("%s: status %d msg %q after %d requests; want %d %q after %d",
				tt.name, se.StatusCode, se.Message, len(f.reqs), tt.wantStatus, tt.wantMsg, tt.wantReqs)
		}
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	f := &fakeBrave{replies: []string{okReply}}
	p, _ := newTestProvider(t, f, Config{})
	if _, err := p.Search(context.Background(), search.Request{Query: "  "}); err == nil {
		t.Error("want error for empty query")
	}
	if len(f.reqs) != 0 {
		t.Errorf("sent %d requests for an empty query", len(f.reqs))
	}
}

func TestNewAPIKey(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	if _, err := New(Config{}); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("err = %v, want ErrNoAPIKey", err)
	}
	t.Setenv(APIKeyEnv, "from-env")
	p, err := New(Config{})
	if err != nil || p.key != "from-env" {
		t.Errorf("key = %q, err = %v; want from-env", p.key, err)
	}
}
