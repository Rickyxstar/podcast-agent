package transcript

import "io"

// Parser turns a raw transcript file into an Episode.
type Parser interface {
	// Parse reads a transcript from r. name is the source file name (e.g. an
	// S3 object key); its extension (.json, .txt or .text) selects the format.
	Parse(name string, r io.Reader) (*Episode, error)
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
