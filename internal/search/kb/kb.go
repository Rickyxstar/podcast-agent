// Package kb implements search.Provider over the curated knowledge base: JSON
// files of fact entries ranked with BM25. It runs offline and needs no keys.
package kb

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Rickyxstar/podcast-agent/internal/search"
)

// DefaultMaxResults is used when a request doesn't set MaxResults.
const DefaultMaxResults = 5

// BM25 parameters, at the usual defaults.
const (
	k1 = 1.2
	b  = 0.75
)

// Entry is one curated fact, as stored in kb/*.json.
type Entry struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
	// Source names the publisher, e.g. "FDA".
	Source string `json:"source"`
	URL    string `json:"url"`
	// Date is when the fact was published or last checked, as YYYY-MM-DD,
	// YYYY-MM or YYYY.
	Date string   `json:"date"`
	Tags []string `json:"tags"`
}

// Provider searches an in-memory index of entries. It is safe for
// concurrent use.
type Provider struct {
	docs   []doc
	df     map[string]int // documents containing each term
	avgLen float64
}

type doc struct {
	entry     Entry
	published *time.Time
	tf        map[string]int
	len       int
}

var _ search.Provider = (*Provider)(nil)

// LoadDir loads every *.json file in dir. See Load.
func LoadDir(dir string) (*Provider, error) {
	return Load(os.DirFS(dir))
}

// Load reads every *.json file at the root of fsys. Each file holds a JSON
// array of entries.
func Load(fsys fs.FS) (*Provider, error) {
	files, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, fmt.Errorf("kb: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("kb: no *.json files found")
	}
	var entries []Entry
	for _, name := range files {
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("kb: %w", err)
		}
		var es []Entry
		if err := json.Unmarshal(raw, &es); err != nil {
			return nil, fmt.Errorf("kb: %s: %w", name, err)
		}
		entries = append(entries, es...)
	}
	return New(entries)
}

// New indexes entries. It fails on a missing or duplicate ID, an empty
// statement or an unparseable date, since the knowledge base is curated by
// hand and a bad entry is a bug.
func New(entries []Entry) (*Provider, error) {
	p := &Provider{df: map[string]int{}}
	seen := map[string]bool{}
	total := 0
	for _, e := range entries {
		if e.ID == "" {
			return nil, fmt.Errorf("kb: entry %q has no id", e.Statement)
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("kb: duplicate id %q", e.ID)
		}
		seen[e.ID] = true
		if strings.TrimSpace(e.Statement) == "" {
			return nil, fmt.Errorf("kb: entry %q has no statement", e.ID)
		}
		published, err := parseDate(e.Date)
		if err != nil {
			return nil, fmt.Errorf("kb: entry %q: %w", e.ID, err)
		}

		// Tags and source are indexed alongside the statement so a query for
		// "telehealth" finds entries tagged with it.
		terms := tokenize(e.Statement + " " + strings.Join(e.Tags, " ") + " " + e.Source)
		d := doc{entry: e, published: published, tf: map[string]int{}, len: len(terms)}
		for _, t := range terms {
			d.tf[t]++
		}
		for t := range d.tf {
			p.df[t]++
		}
		total += d.len
		p.docs = append(p.docs, d)
	}
	if len(p.docs) > 0 {
		p.avgLen = float64(total) / float64(len(p.docs))
	}
	return p, nil
}

// Name implements search.Provider.
func (p *Provider) Name() string { return "kb" }

// Search implements search.Provider. Entries sharing no terms with the query
// are left out, so the result may be empty.
func (p *Provider) Search(ctx context.Context, req search.Request) ([]search.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query := slices.Compact(slices.Sorted(slices.Values(tokenize(req.Query))))
	n := float64(len(p.docs))

	var out []search.Result
	for _, d := range p.docs {
		score := 0.0
		for _, t := range query {
			tf := float64(d.tf[t])
			if tf == 0 {
				continue
			}
			df := float64(p.df[t])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			score += idf * tf * (k1 + 1) / (tf + k1*(1-b+b*float64(d.len)/p.avgLen))
		}
		if score == 0 {
			continue
		}
		out = append(out, search.Result{
			ID:        d.entry.ID,
			Title:     d.entry.Source,
			URL:       d.entry.URL,
			Snippet:   d.entry.Statement,
			Source:    d.entry.Source,
			Kind:      search.KindKB,
			Published: d.published,
			Score:     score,
		})
	}
	// Ties break on ID so output is deterministic for evals.
	slices.SortFunc(out, func(a, b search.Result) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.ID, b.ID))
	})
	limit := cmp.Or(req.MaxResults, DefaultMaxResults)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func parseDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{time.DateOnly, "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("date %q: want YYYY-MM-DD, YYYY-MM or YYYY", s)
}

// tokenize lowercases s, splits it on anything that isn't a letter or digit,
// drops stopwords, and folds simple plurals ("companies" → "company").
func tokenize(s string) []string {
	s = strings.NewReplacer("'s", "", "’s", "").Replace(strings.ToLower(s))
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if stopwords[f] {
			continue
		}
		out = append(out, stem(f))
	}
	return out
}

func stem(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 4 && (strings.HasSuffix(w, "sses") || strings.HasSuffix(w, "shes") ||
		strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "xes")):
		return w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us"):
		return w[:len(w)-1]
	default:
		return w
	}
}

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an and are as at be been but by did do does for from had has have
		how in into is it its of on or that the their there these this those to was were what when
		where which who will with`) {
		m[w] = true
	}
	return m
}()
