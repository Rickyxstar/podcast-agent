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
	// minQuoteScore is how close a quote must be to some stretch of a
	// transcript line to count as said: 1 is an exact match after folding,
	// 0.9 allows one edit in ten characters.
	minQuoteScore = 0.9
)

// checked is the result of checking the summary stage's output in code.
type checked struct {
	// notes is the output with only its verified quotes, each copied from
	// the transcript as said with that line's speaker and timestamp, and
	// with topics in kebab-case.
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
	// wasn't found in the transcript, and closest is its best match.
	duplicate bool
	closest   quoteMatch
}

// String describes q for warnings and for the repair prompt.
func (q failedQuote) String() string {
	if q.duplicate {
		return fmt.Sprintf("%q repeats another quote", q.text)
	}
	if q.closest.score == 0 {
		return fmt.Sprintf("%q was not found in the transcript", q.text)
	}
	return fmt.Sprintf("%q was not found in the transcript (closest match %.2f at %s)",
		q.text, q.closest.score, q.closest.seg.Timestamp)
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
		seg  int
		text string
	}
	seen := map[span]bool{}
	for _, q := range n.Quotes {
		m, ok := j.matchQuote(q.Text)
		switch {
		case !ok:
			c.failed = append(c.failed, failedQuote{text: q.Text, closest: m})
		case seen[span{m.index, m.text}]:
			c.failed = append(c.failed, failedQuote{text: q.Text, duplicate: true})
		case len(c.notes.Quotes) < maxQuotes:
			// Trust the transcript over the model for the words, the
			// speaker and the timestamp. Quotes past maxQuotes are left out.
			seen[span{m.index, m.text}] = true
			c.notes.Quotes = append(c.notes.Quotes, noteQuote{Text: m.text, Speaker: m.seg.Speaker, Timestamp: m.seg.Timestamp})
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
		words := strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if t := strings.Join(words, "-"); t != "" && !slices.Contains(tidy, t) {
			tidy = append(tidy, t)
		}
	}
	return tidy
}

// quoteMatch is the stretch of a transcript line closest to a quote.
type quoteMatch struct {
	seg   transcript.Segment
	index int // of seg in the transcript
	// text is the matching stretch of seg.Text as said, widened to whole
	// words.
	text string
	// score is 1 minus the edit distance per character of the quote,
	// floored at 0, after folding both sides.
	score float64
}

// matchQuote returns the transcript stretch closest to quote, ignoring
// case, whitespace and typographic quote and dash styles, and reports
// whether it is close enough to count as said. Ties go to the earliest
// segment.
func (j *job) matchQuote(quote string) (quoteMatch, bool) {
	q := fold(strings.Trim(quote, ` "'“”‘’`))
	if len(q.runes) == 0 {
		return quoteMatch{}, false
	}
	var best quoteMatch
	for i, s := range j.ep.Transcript {
		f := fold(s.Text)
		start, end, dist := closest(q.runes, f.runes)
		score := max(0, 1-float64(dist)/float64(len(q.runes)))
		if score <= best.score {
			continue
		}
		best = quoteMatch{seg: s, index: i, text: f.source(start, end), score: score}
		if score == 1 {
			break
		}
	}
	return best, best.score >= minQuoteScore
}

// folded is text folded for quote matching, keeping where each rune came
// from so a match can be copied from the original.
type folded struct {
	src   []rune
	runes []rune
	pos   []int // pos[i] is the index in src of runes[i]
}

// fold lowercases s, collapses its whitespace, and maps typographic quotes,
// dashes and ellipses to plain ASCII.
func fold(s string) folded {
	f := folded{src: []rune(s)}
	space := -1 // index of the pending run of whitespace, if any
	for i, r := range f.src {
		if unicode.IsSpace(r) {
			if space < 0 {
				space = i
			}
			continue
		}
		if space >= 0 && len(f.runes) > 0 {
			f.add(space, ' ')
		}
		space = -1
		switch r {
		case '‘', '’':
			f.add(i, '\'')
		case '“', '”':
			f.add(i, '"')
		case '–', '—':
			f.add(i, '-')
		case '…':
			f.add(i, '.', '.', '.')
		default:
			f.add(i, unicode.ToLower(r))
		}
	}
	return f
}

func (f *folded) add(pos int, rs ...rune) {
	for _, r := range rs {
		f.runes = append(f.runes, r)
		f.pos = append(f.pos, pos)
	}
}

// source returns the original text of runes[start:end], widened so it
// doesn't cut a word in half.
func (f *folded) source(start, end int) string {
	if start >= end {
		return ""
	}
	from, to := f.pos[start], f.pos[end-1]+1
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	for from > 0 && word(f.src[from-1]) && word(f.src[from]) {
		from--
	}
	for to < len(f.src) && word(f.src[to-1]) && word(f.src[to]) {
		to++
	}
	return string(f.src[from:to])
}

// closest finds the stretch text[start:end] with the smallest edit distance
// to q, and returns it with that distance. It is Levenshtein distance with
// a free start and end in text (Sellers' algorithm), O(len(q)·len(text)).
func closest(q, text []rune) (start, end, dist int) {
	// prev and cur are rows of the distance table; their *From twins hold
	// where in text each cell's best stretch starts.
	prev, cur := make([]int, len(text)+1), make([]int, len(text)+1)
	prevFrom, curFrom := make([]int, len(text)+1), make([]int, len(text)+1)
	for j := range prevFrom {
		prevFrom[j] = j
	}
	for i := 1; i <= len(q); i++ {
		cur[0], curFrom[0] = i, 0
		for j := 1; j <= len(text); j++ {
			sub := prev[j-1]
			if q[i-1] != text[j-1] {
				sub++
			}
			cur[j], curFrom[j] = sub, prevFrom[j-1]
			if d := prev[j] + 1; d < cur[j] { // skip a rune of q
				cur[j], curFrom[j] = d, prevFrom[j]
			}
			if d := cur[j-1] + 1; d < cur[j] { // skip a rune of text
				cur[j], curFrom[j] = d, curFrom[j-1]
			}
		}
		prev, cur = cur, prev
		prevFrom, curFrom = curFrom, prevFrom
	}
	end = 0
	for j := 1; j <= len(text); j++ {
		if prev[j] < prev[end] {
			end = j
		}
	}
	return prevFrom[end], end, prev[end]
}
