package agent

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/transcript"
)

// Limits the summary stage's output is checked against.
const (
	wantTakeaways   = 5
	minSummaryWords = 200
	maxSummaryWords = 300
)

var kebabCase = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validate checks the summary stage's output in code: each quote against the
// transcript, and the counts and formats the schema can't express. Problems
// become warnings; nothing here calls the model.
//
// TODO: fuzzy-match quotes (≥ 0.9 similarity) instead of exact substring.
// TODO: send failed quotes back to the model once for repair, then drop the
// ones that still fail.
func (j *job) validate(ctx context.Context, n *notes) []report.Quote {
	quotes := make([]report.Quote, 0, len(n.Quotes))
	verified := 0
	for _, q := range n.Quotes {
		rq := report.Quote{Text: q.Text, Speaker: q.Speaker, Timestamp: q.Timestamp}
		if seg, ok := j.findQuote(q.Text); ok {
			// Trust the transcript over the model for where the quote came from.
			rq.Verified, rq.Speaker, rq.Timestamp = true, seg.Speaker, seg.Timestamp
			verified++
		} else {
			j.warnf("quote not found verbatim in transcript: %q", q.Text)
		}
		quotes = append(quotes, rq)
	}

	if len(n.Takeaways) != wantTakeaways {
		j.warnf("got %d takeaways, want %d", len(n.Takeaways), wantTakeaways)
	}
	if w := len(strings.Fields(n.Summary)); w < minSummaryWords || w > maxSummaryWords {
		j.warnf("summary is %d words, want %d-%d", w, minSummaryWords, maxSummaryWords)
	}
	for _, t := range n.Topics {
		if !kebabCase.MatchString(t) {
			j.warnf("topic %q is not kebab-case", t)
		}
	}

	j.emit(ctx, "validation",
		slog.Int("quotes", len(quotes)),
		slog.Int("quotes_verified", verified),
		slog.Int("takeaways", len(n.Takeaways)),
		slog.Int("summary_words", len(strings.Fields(n.Summary))),
	)
	return quotes
}

// findQuote returns the segment whose text contains quote, ignoring case,
// whitespace and typographic quote and dash styles.
func (j *job) findQuote(quote string) (transcript.Segment, bool) {
	q := normalize(strings.Trim(quote, ` "'“”‘’`))
	if q == "" {
		return transcript.Segment{}, false
	}
	for _, s := range j.ep.Transcript {
		if strings.Contains(normalize(s.Text), q) {
			return s, true
		}
	}
	return transcript.Segment{}, false
}

var typography = strings.NewReplacer(
	"‘", "'", "’", "'", "“", `"`, "”", `"`,
	"–", "-", "—", "-", "…", "...",
)

// normalize folds s for quote matching.
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(typography.Replace(s))), " ")
}
