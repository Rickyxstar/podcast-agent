package transcript

import (
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// segmentStart matches the "[HH:MM:SS] Speaker:" (or "[MM:SS] Speaker:") marker
// that opens each turn. Submatches are the timestamp and the speaker.
var segmentStart = regexp.MustCompile(`\[(\d{1,2}:\d{2}(?::\d{2})?)\][ \t]*([^:\[\]\r\n]{1,40}):`)

// TextParser parses plain-text transcripts where each turn starts with a
// "[timestamp] Speaker:" marker, e.g.
//
//	[00:02:14] Host: So NASA recently announced a new Mars mission.
//	[00:02:18] Guest: Yes, it's launching in early 2026.
//
// A turn's text runs until the next marker, so turns may wrap across lines or
// share a line. Text before the first marker is ignored. Plain text carries no
// metadata, so the episode ID comes from the file name and Title, Host, Guests
// and Section are left empty.
type TextParser struct{}

var _ Parser = TextParser{}

func (TextParser) Parse(name string, r io.Reader) (*Episode, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	s := string(b)

	matches := segmentStart.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("%s: %w", name, ErrEmptyTranscript)
	}

	ep := &Episode{EpisodeID: episodeID(name)}
	for i, m := range matches {
		end := len(s)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		ep.Transcript = append(ep.Transcript, Segment{
			Timestamp: s[m[2]:m[3]],
			Speaker:   strings.TrimSpace(s[m[4]:m[5]]),
			Text:      strings.Join(strings.Fields(s[m[1]:end]), " "),
		})
	}
	return ep, nil
}

// episodeID derives an episode ID from a file name or S3 key,
// e.g. "uploads/ep004_mars.txt" -> "ep004_mars".
func episodeID(name string) string {
	base := path.Base(name)
	return strings.TrimSuffix(base, path.Ext(base))
}
