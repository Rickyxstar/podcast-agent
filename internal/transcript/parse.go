package transcript

import "io"

// Parser turns a raw transcript file into an Episode.
type Parser interface {
	// Parse reads a transcript from r. name is the source file name (e.g. an
	// S3 object key); its extension (.json, .txt or .text) selects the format.
	Parse(name string, r io.Reader) (*Episode, error)
}

// Episode is a podcast episode transcript as delivered in the input JSON.
type Episode struct {
	EpisodeID  string    `json:"episode_id"`
	Title      string    `json:"title"`
	Host       string    `json:"host"`
	Guests     []string  `json:"guests"`
	Transcript []Segment `json:"transcript"`
}

// Segment is a single speaker turn within an episode transcript.
type Segment struct {
	// Timestamp is the offset from the start of the episode, formatted "MM:SS".
	Timestamp string `json:"timestamp"`
	// Speaker is the short name used in the transcript (e.g. "Sarah", "Dr. Priya"),
	// not necessarily the full name listed in Host or Guests.
	Speaker string `json:"speaker"`
	Section string `json:"section"`
	Text    string `json:"text"`
}
