package transcript

import (
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// ErrUnsupportedFormat is returned by NewParser for file extensions with no parser.
var ErrUnsupportedFormat = errors.New("unsupported transcript format")

// Parser turns a raw transcript file into an Episode.
type Parser interface {
	// Parse reads a transcript from r. name is the source file name (e.g. an
	// S3 object key), used in error messages and to derive IDs where needed.
	Parse(name string, r io.Reader) (*Episode, error)
}

// NewParser returns the Parser for name's extension: JSONParser for .json and
// TextParser for .txt or .text. The match is case-insensitive.
func NewParser(name string) (Parser, error) {
	switch strings.ToLower(path.Ext(name)) {
	case ".json":
		return JSONParser{}, nil
	case ".txt", ".text":
		return TextParser{}, nil
	default:
		return nil, fmt.Errorf("%s: %w", name, ErrUnsupportedFormat)
	}
}

// Episode is a parsed podcast episode transcript. JSON tags match the input JSON format.
type Episode struct {
	EpisodeID  string    `json:"episode_id"`
	Title      string    `json:"title"`
	Host       string    `json:"host"`
	Guests     []string  `json:"guests"`
	Transcript []Segment `json:"transcript"`
}

// Segment is a single speaker turn within an episode transcript.
type Segment struct {
	// Timestamp is the offset from the start of the episode as written in the
	// source: "MM:SS" in the JSON samples, "HH:MM:SS" or "MM:SS" in text files.
	Timestamp string `json:"timestamp"`
	// Speaker is the short name used in the transcript (e.g. "Sarah", "Dr. Priya"),
	// not necessarily the full name listed in Host or Guests.
	Speaker string `json:"speaker"`
	Section string `json:"section"`
	Text    string `json:"text"`
}
