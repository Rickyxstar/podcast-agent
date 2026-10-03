// Package brave implements search.Provider on the Brave Web Search API.
package brave

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/search"
)

const (
	// DefaultBaseURL is the Brave Search API root.
	DefaultBaseURL = "https://api.search.brave.com/res/v1"
	// DefaultMaxResults is used when a request doesn't set MaxResults.
	DefaultMaxResults = 5
	// maxCount is the most results Brave returns per request.
	maxCount = 20
	// DefaultMaxRetries is how many times a 429 or 5xx is retried. The free
	// plan allows one request per second, and the agent runs tool calls in
	// parallel, so rate limiting is expected.
	DefaultMaxRetries = 3
	// APIKeyEnv is read when Config.APIKey is empty.
	APIKeyEnv = "BRAVE_API_KEY"
)

// ErrNoAPIKey is returned by New when no API key is configured.
var ErrNoAPIKey = errors.New("brave: no API key (set " + APIKeyEnv + ")")

// Config sets per-provider defaults. Fields left zero use the package defaults.
type Config struct {
	// APIKey is the subscription token. Empty reads APIKeyEnv.
	APIKey string
	// BaseURL overrides DefaultBaseURL, for tests.
	BaseURL string
	// MaxRetries caps retries of 429 and 5xx replies. Negative disables them.
	MaxRetries int
	// HTTPClient sends requests. Nil uses a client with a 30s timeout.
	HTTPClient *http.Client
}

// Provider talks to Brave Search.
type Provider struct {
	url        string
	key        string
	maxRetries int
	http       *http.Client
	// sleep waits between retries; tests replace it.
	sleep func(context.Context, time.Duration) error
}

var _ search.Provider = (*Provider)(nil)

// New returns a Provider, or ErrNoAPIKey if no key is configured.
func New(cfg Config) (*Provider, error) {
	key := cmp.Or(cfg.APIKey, os.Getenv(APIKeyEnv))
	if key == "" {
		return nil, ErrNoAPIKey
	}
	retries := cmp.Or(cfg.MaxRetries, DefaultMaxRetries)
	if retries < 0 {
		retries = 0
	}
	return &Provider{
		url:        strings.TrimRight(cmp.Or(cfg.BaseURL, DefaultBaseURL), "/") + "/web/search",
		key:        key,
		maxRetries: retries,
		http:       cmp.Or(cfg.HTTPClient, &http.Client{Timeout: 30 * time.Second}),
		sleep:      sleepCtx,
	}, nil
}

// Name implements search.Provider.
func (p *Provider) Name() string { return "brave" }

// Search implements search.Provider.
func (p *Provider) Search(ctx context.Context, req search.Request) ([]search.Result, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("brave: empty query")
	}
	count := min(cmp.Or(req.MaxResults, DefaultMaxResults), maxCount)
	q := url.Values{"q": {req.Query}, "count": {strconv.Itoa(count)}}

	for attempt := 0; ; attempt++ {
		resp, retryAfter, err := p.do(ctx, p.url+"?"+q.Encode())
		if err == nil {
			return toResults(resp, count), nil
		}
		var se *StatusError
		if !errors.As(err, &se) || !se.Retryable() || attempt >= p.maxRetries {
			return nil, err
		}
		wait := retryAfter
		if wait <= 0 {
			wait = time.Second << attempt
		}
		if err := p.sleep(ctx, min(wait, 10*time.Second)); err != nil {
			return nil, err
		}
	}
}

// do sends one request. On a non-200 reply it returns a *StatusError and how
// long Brave asked to wait, if it said.
func (p *Provider) do(ctx context.Context, u string) (*searchResponse, time.Duration, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("brave: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Subscription-Token", p.key)

	httpResp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("brave: %w", err)
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("brave: read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, retryAfter(httpResp.Header), newStatusError(httpResp.StatusCode, body)
	}

	var resp searchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, 0, fmt.Errorf("brave: decode response: %w", err)
	}
	return &resp, 0, nil
}

// StatusError is a non-200 reply from Brave, such as 401 for a bad key or
// 429 when over the plan's rate limit.
type StatusError struct {
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("brave: %d %s: %s", e.StatusCode, http.StatusText(e.StatusCode), e.Message)
}

// Retryable reports whether the request may succeed if sent again.
func (e *StatusError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

func newStatusError(status int, body []byte) *StatusError {
	var e struct {
		Error struct {
			Detail string `json:"detail"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Error.Detail != "" {
		msg = e.Error.Detail
	}
	return &StatusError{StatusCode: status, Message: msg}
}

// retryAfter reads the wait from Retry-After, or else from the per-second
// window of X-RateLimit-Reset, which Brave sends as "1, 1419704".
func retryAfter(h http.Header) time.Duration {
	for _, v := range []string{h.Get("Retry-After"), h.Get("X-RateLimit-Reset")} {
		first, _, _ := strings.Cut(v, ",")
		if s, err := strconv.Atoi(strings.TrimSpace(first)); err == nil && s > 0 {
			return time.Duration(s) * time.Second
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Wire types for /web/search. Only the fields this provider uses are declared.

type searchResponse struct {
	Web struct {
		Results []webResult `json:"results"`
	} `json:"web"`
}

type webResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Description   string   `json:"description"`
	PageAge       string   `json:"page_age"`
	ExtraSnippets []string `json:"extra_snippets"`
}

func toResults(resp *searchResponse, limit int) []search.Result {
	out := []search.Result{}
	for i, r := range resp.Web.Results {
		if i == limit {
			break
		}
		snippet := plainText(r.Description)
		for _, s := range r.ExtraSnippets {
			snippet += " … " + plainText(s)
		}
		var source string
		if u, err := url.Parse(r.URL); err == nil {
			source = u.Hostname()
		}
		out = append(out, search.Result{
			ID:        r.URL,
			Title:     plainText(r.Title),
			URL:       r.URL,
			Snippet:   snippet,
			Source:    source,
			Kind:      search.KindWeb,
			Published: parsePageAge(r.PageAge),
			// Brave doesn't score results, so rank stands in: 1, 1/2, 1/3, ...
			Score: 1 / float64(i+1),
		})
	}
	return out
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// plainText strips the <strong> highlighting Brave puts around query terms
// and decodes HTML entities.
func plainText(s string) string {
	return strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(s, "")))
}

// parsePageAge reads page_age, an ISO 8601 timestamp usually without a zone,
// e.g. "2024-02-01T15:04:05". Unparseable values are treated as unknown.
func parsePageAge(s string) *time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}
