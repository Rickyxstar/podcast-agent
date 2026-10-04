package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// ingest renders the transcript for the model stages, one segment per line:
//
//	[01:20] Mark [Deep Dive]: Companies like GitLab...
//
// It is plain Go; no model is involved.
//
// TODO: resolve speaker labels to full names and roles ("Mark" → "Mark
// Rivera (guest)", stripping honorifics), warning on unknown speakers.
// TODO: strip filler words and stutters from the text sent to the model,
// keeping the raw text for quote validation.
func (j *job) ingest(ctx context.Context) {
	var b strings.Builder
	if j.ep.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", j.ep.Title)
	}
	if j.ep.Host != "" {
		fmt.Fprintf(&b, "Host: %s\n", j.ep.Host)
	}
	if len(j.ep.Guests) > 0 {
		fmt.Fprintf(&b, "Guests: %s\n", strings.Join(j.ep.Guests, ", "))
	}
	b.WriteString("\n")

	for i, s := range j.ep.Transcript {
		if s.Timestamp == "" || s.Speaker == "" {
			j.warnf("segment %d: missing timestamp or speaker", i+1)
		}
		// [01:20] Mark
		fmt.Fprintf(&b, "[%s] %s", s.Timestamp, s.Speaker)
		if s.Section != "" {
			// [Deep Dive]
			fmt.Fprintf(&b, " [%s]", s.Section)
		}
		//: Companies like GitLab...
		fmt.Fprintf(&b, ": %s\n", s.Text)
	}
	j.lines = b.String()
	j.emit(ctx, "ingest",
		slog.Int("segments", len(j.ep.Transcript)),
		slog.String("duration", j.duration()),
	)
}

// duration is the last segment's timestamp as written in the source.
func (j *job) duration() string {
	if n := len(j.ep.Transcript); n > 0 {
		return j.ep.Transcript[n-1].Timestamp
	}
	return ""
}
