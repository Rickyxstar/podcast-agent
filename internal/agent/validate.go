package agent

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode"

	"github.com/Rickyxstar/podcast-agent/internal/transcript"
)

// Limits the summary stage's output is checked against.
const (
	wantTakeaways   = 5
	minSummaryWords = 200
	maxSummaryWords = 300
	minQuotes       = 3
	maxQuotes       = 5
	minTopics       = 3
	maxTopics       = 8
)

// checked is the result of checking the summary stage's output in code.
type checked struct {
	// notes is the output with only its verified quotes, each with its
	// line's speaker and timestamp, and with topics in kebab-case.
	notes notes
	// failed are the quotes left out of notes.
	failed []failedQuote
	// missing is how many quotes to ask for to replace the failed ones and
	// reach minQuotes, without going over maxQuotes.
	missing int
	// issues are the limits notes doesn't meet.
	issues []issue
}

type failedQuote struct {
	text string
	// duplicate is set when the quote repeats an earlier one; otherwise it
	// wasn't found in the transcript, and closest is the line nearest to
	// it, if any.
	duplicate bool
	closest   *transcript.Segment
}

// String describes q for warnings and for the repair prompt.
func (q failedQuote) String() string {
	if q.duplicate {
		return fmt.Sprintf("%q repeats another quote", q.text)
	}
	if q.closest == nil {
		return fmt.Sprintf("%q was not found in the transcript", q.text)
	}
	return fmt.Sprintf("%q was not found in the transcript (closest line at %s)", q.text, q.closest.Timestamp)
}

// issue is a limit the output doesn't meet.
type issue struct {
	// field is the notes field at fault: "summary", "takeaways" or "topics".
	field   string
	problem string
}

// needsRepair reports whether anything in c is worth asking the model to
// fix. Failed quotes alone aren't when the notes already have maxQuotes.
func (c *checked) needsRepair() bool {
	return c.missing > 0 || len(c.issues) > 0
}

// check verifies n in code: each quote against the transcript, and the
// counts and formats the schema can't express. Nothing here calls the model
// or records warnings; see repair and finish.
func (j *job) check(n *notes) checked {
	c := checked{notes: *n}
	c.notes.Quotes = make([]noteQuote, 0, min(len(n.Quotes), maxQuotes))
	type span struct {
		seg   int
		words string
	}
	seen := map[span]bool{}
	for _, q := range n.Quotes {
		m, ok := j.matchQuote(q.Text)
		key := span{m.index, strings.Join(words(q.Text), " ")}
		switch {
		case !ok:
			c.failed = append(c.failed, failedQuote{text: q.Text, closest: j.closestLine(q.Text)})
		case seen[key]:
			c.failed = append(c.failed, failedQuote{text: q.Text, duplicate: true})
		case len(c.notes.Quotes) < maxQuotes:
			// Trust the transcript over the model for the speaker and the
			// timestamp. Quotes past maxQuotes are left out.
			seen[key] = true
			text := strings.Join(strings.Fields(strings.Trim(q.Text, ` "'“”‘’`)), " ")
			c.notes.Quotes = append(c.notes.Quotes, noteQuote{Text: text, Speaker: m.seg.Speaker, Timestamp: m.seg.Timestamp})
		}
	}
	have := len(c.notes.Quotes)
	c.missing = max(min(len(c.failed), maxQuotes-have), minQuotes-have)

	c.notes.Topics = tidyTopics(n.Topics)
	c.addIssue("summary", summaryProblem(c.notes.Summary))
	c.addIssue("takeaways", takeawaysProblem(c.notes.Takeaways))
	c.addIssue("topics", topicsProblem(c.notes.Topics))
	return c
}

func (c *checked) addIssue(field, problem string) {
	if problem != "" {
		c.issues = append(c.issues, issue{field, problem})
	}
}

// finish records what is still wrong in c as warnings and returns the notes
// to publish.
func (j *job) finish(c checked) *notes {
	for _, q := range c.failed {
		j.warnf("dropped quote: %s", q)
	}
	if n := len(c.notes.Quotes); n < minQuotes {
		j.warnf("got %d quotes, want %d-%d", n, minQuotes, maxQuotes)
	}
	for _, is := range c.issues {
		j.warnf("%s", is.problem)
	}
	return &c.notes
}

// emitValidation emits the trace event for the first check of n.
func (j *job) emitValidation(ctx context.Context, n *notes, c checked) {
	j.emit(ctx, "validation",
		slog.Int("quotes", len(n.Quotes)),
		slog.Int("quotes_verified", len(c.notes.Quotes)),
		slog.Int("takeaways", len(n.Takeaways)),
		slog.Int("summary_words", len(strings.Fields(n.Summary))),
		slog.Int("problems", len(c.failed)+len(c.issues)),
	)
}

func summaryProblem(s string) string {
	if w := len(strings.Fields(s)); w < minSummaryWords || w > maxSummaryWords {
		return fmt.Sprintf("summary is %d words, want %d-%d", w, minSummaryWords, maxSummaryWords)
	}
	return ""
}

func takeawaysProblem(t []string) string {
	if len(t) != wantTakeaways {
		return fmt.Sprintf("got %d takeaways, want %d", len(t), wantTakeaways)
	}
	return ""
}

func topicsProblem(t []string) string {
	if len(t) < minTopics || len(t) > maxTopics {
		return fmt.Sprintf("got %d topics, want %d-%d", len(t), minTopics, maxTopics)
	}
	return ""
}

// tidyTopics puts topics in kebab-case ("Async Culture" → "async-culture"),
// dropping any left empty or repeated. Code fixes this more cheaply and
// reliably than a repair call could.
func tidyTopics(topics []string) []string {
	tidy := make([]string, 0, len(topics))
	for _, t := range topics {
		if t := strings.Join(words(t), "-"); t != "" && !slices.Contains(tidy, t) {
			tidy = append(tidy, t)
		}
	}
	return tidy
}

// quoteMatch is the transcript line a quote was found in.
type quoteMatch struct {
	seg   transcript.Segment
	index int // of seg in the transcript
}

// matchQuote returns the first transcript line that contains quote as a
// run of whole words, ignoring case, whitespace and punctuation, so
// typographic quote and dash styles don't matter.
func (j *job) matchQuote(quote string) (quoteMatch, bool) {
	q := words(quote)
	if len(q) == 0 {
		return quoteMatch{}, false
	}
	want := " " + strings.Join(q, " ") + " "
	for i, s := range j.ep.Transcript {
		if strings.Contains(" "+strings.Join(words(s.Text), " ")+" ", want) {
			return quoteMatch{seg: s, index: i}, true
		}
	}
	return quoteMatch{}, false
}

// closestLine returns the transcript line sharing the most words with
// quote, as a hint for fixing it, or nil if none shares at least half of
// them. Ties go to the earliest line.
func (j *job) closestLine(quote string) *transcript.Segment {
	q := words(quote)
	var best *transcript.Segment
	most := 0
	for i, s := range j.ep.Transcript {
		line := words(s.Text)
		n := 0
		for _, w := range q {
			if slices.Contains(line, w) {
				n++
			}
		}
		if n > most {
			best, most = &j.ep.Transcript[i], n
		}
	}
	if 2*most < len(q) {
		return nil
	}
	return best
}

// words splits s into lowercase runs of letters and digits.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
