package trace

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Pretty is a Sink that prints each event as one readable line, e.g.
//
//	🧭 Plan: 6 claims → 3 factual, 2 predictions, 1 anecdote
//
// It is meant for a person watching a run, not for parsing.
type Pretty struct {
	mu sync.Mutex
	w  io.Writer
}

// NewPretty returns a Pretty that writes to w.
func NewPretty(w io.Writer) *Pretty { return &Pretty{w: w} }

// Emit prints e.
func (p *Pretty) Emit(e Event) {
	line := format(e)
	if line == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintln(p.w, line)
}

// format renders e, or returns "" for events not worth a line of their own.
func format(e Event) string {
	// The accessors return zero for a missing or mistyped attribute rather
	// than panic, as slog.Value's do.
	str := func(k string) string {
		if v := e.Value(k); v.Any() != nil {
			return v.String()
		}
		return ""
	}
	num := func(k string) int64 {
		if v := e.Value(k); v.Kind() == slog.KindInt64 {
			return v.Int64()
		}
		return 0
	}
	float := func(k string) float64 {
		if v := e.Value(k); v.Kind() == slog.KindFloat64 {
			return v.Float64()
		}
		return 0
	}
	flag := func(k string) bool {
		v := e.Value(k)
		return v.Kind() == slog.KindBool && v.Bool()
	}

	switch e.Type {
	case "ingest":
		return fmt.Sprintf("📥 Ingest: %s, %s long", count(num("segments"), "segment"), str("duration"))

	case "llm_call":
		s := fmt.Sprintf("🤖 %s call: %s in / %s out tokens", str("stage"),
			thousands(num("input_tokens")), thousands(num("output_tokens")))
		if n := num("cache_read_tokens"); n > 0 {
			s += fmt.Sprintf(" (%s cached)", thousands(n))
		}
		if n := num("tool_calls"); n > 0 {
			s += ", " + count(n, "tool call")
		}
		return s + ", " + millis(num("duration_ms"))

	case "plan":
		var kinds []string
		if n := num("factual"); n > 0 {
			kinds = append(kinds, fmt.Sprintf("%d factual", n))
		}
		for _, k := range []struct{ key, noun string }{
			{"predictions", "prediction"},
			{"anecdotes", "anecdote"},
			{"opinions", "opinion"},
		} {
			if n := num(k.key); n > 0 {
				kinds = append(kinds, count(n, k.noun))
			}
		}
		s := "🧭 Plan: " + count(num("claims"), "claim")
		if len(kinds) > 0 {
			s += " → " + strings.Join(kinds, ", ")
		}
		return s

	case "claim":
		s := fmt.Sprintf("   %s (%s) %q", str("id"), str("type"), truncate(str("claim"), 80))
		if st := str("strategy"); st != "" && st != "none" {
			s += " → " + truncate(st, 60)
		}
		return s

	case "tool_call":
		// The verdict event says the same thing more readably.
		if str("tool") == "submit_verdict" {
			return ""
		}
		return fmt.Sprintf("🔎 %s %s", str("tool"), truncate(str("input"), 100))

	case "tool_result":
		if flag("error") {
			return fmt.Sprintf("   ↳ ⚠ %s returned an error", str("tool"))
		}
		if str("tool") == "submit_verdict" {
			return ""
		}
		return fmt.Sprintf("   ↳ %s bytes of results", thousands(num("bytes")))

	case "verdict":
		return fmt.Sprintf("%s %s %s (confidence %.2f): %s", verdictIcon(str("verdict")),
			str("id"), str("verdict"), float("confidence"), truncate(str("reasoning"), 120))

	case "validation":
		return fmt.Sprintf("🧪 Validation: %d of %s found verbatim, %s, %d-word summary",
			num("quotes_verified"), count(num("quotes"), "quote"), count(num("takeaways"), "takeaway"), num("summary_words"))

	case "done":
		return fmt.Sprintf("🏁 Done in %s: fact-check %s, %s, %s, $%.4f",
			millis(num("duration_ms")), str("fact_check"), count(num("claims"), "claim"),
			count(num("warnings"), "warning"), float("cost_usd"))
	}

	// Unknown events still show up, as key=value pairs.
	var b strings.Builder
	b.WriteString("• " + e.Type)
	for _, a := range e.Attrs {
		fmt.Fprintf(&b, " %s=%s", a.Key, truncate(a.Value.String(), 60))
	}
	return b.String()
}

func verdictIcon(v string) string {
	switch v {
	case "verified":
		return "✅"
	case "outdated_or_inaccurate":
		return "⚠️"
	default:
		return "❓"
	}
}

// count returns "1 claim" or "3 claims".
func count(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return thousands(n) + " " + noun + "s"
}

// thousands formats n with comma separators, e.g. 12,345.
func thousands(n int64) string {
	if n < 0 {
		return "-" + thousands(-n)
	}
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func millis(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}

// truncate shortens s to at most n runes on one line, marking any cut with
// an ellipsis.
func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
