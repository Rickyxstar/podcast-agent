package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func TestTextParser(t *testing.T) {
	tests := []struct {
		file string
		want []Segment
	}{
		{
			file: "basic.txt",
			want: []Segment{
				{Timestamp: "00:00:00", Speaker: "Host", Text: "Welcome to the show."},
				{Timestamp: "00:00:10", Speaker: "Guest", Text: "Thanks for having me."},
				{Timestamp: "00:00:20", Speaker: "Host", Text: "Let's get started."},
			},
		},
		{
			file: "same_line.txt",
			want: []Segment{
				{Timestamp: "00:02:14", Speaker: "Host", Text: "Anything new this week?"},
				{Timestamp: "00:02:18", Speaker: "Guest", Text: "Yes, a launch is planned for 2026."},
				{Timestamp: "00:03:01", Speaker: "Host", Text: "Wow, that's exciting."},
			},
		},
		{
			file: "wrapped_lines.txt",
			want: []Segment{
				{Timestamp: "00:00:00", Speaker: "Host", Text: "This turn wraps across two lines."},
				{Timestamp: "00:00:30", Speaker: "Guest", Text: "Here's the plan: ship it. [laughs] And then celebrate."},
			},
		},
		{
			file: "short_timestamps.txt",
			want: []Segment{
				{Timestamp: "00:00", Speaker: "Ann", Text: "Two guests today."},
				{Timestamp: "00:15", Speaker: "Dr. Cara", Text: "Glad to be here."},
				{Timestamp: "01:05", Speaker: "Dan", Text: "Same here."},
			},
		},
		{
			file: "preamble_crlf.txt",
			want: []Segment{
				{Timestamp: "00:00:00", Speaker: "Host", Text: "Hello."},
				{Timestamp: "00:00:05", Speaker: "Guest", Text: "Hi there."},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			got, err := TextParser{}.Parse(tt.file, f)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			want := &Episode{
				EpisodeID:  strings.TrimSuffix(tt.file, ".txt"),
				Transcript: tt.want,
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Parse =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

func TestTextParserErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty input", in: ""},
		{name: "whitespace only", in: "\n  \n"},
		{name: "no markers", in: "Host: Hello.\nGuest: Hi."},
		{name: "timestamp without speaker", in: "[00:00:00] Hello there."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep, err := TextParser{}.Parse("bad.txt", strings.NewReader(tt.in))
			if !errors.Is(err, ErrEmptyTranscript) {
				t.Fatalf("err = %v, want %v", err, ErrEmptyTranscript)
			}
			if ep != nil {
				t.Errorf("Parse returned episode %+v alongside error", ep)
			}
			if !strings.Contains(err.Error(), "bad.txt") {
				t.Errorf("err = %v, want it to mention the file name", err)
			}
		})
	}

	t.Run("read error", func(t *testing.T) {
		readErr := errors.New("connection reset")
		_, err := TextParser{}.Parse("bad.txt", iotest.ErrReader(readErr))
		if !errors.Is(err, readErr) {
			t.Errorf("err = %v, want %v", err, readErr)
		}
	})
}

func TestEpisodeID(t *testing.T) {
	tests := map[string]string{
		"ep004.txt":                 "ep004",
		"ep004_mars.text":           "ep004_mars",
		"uploads/2026/10/ep004.txt": "ep004",
		"ep004":                     "ep004",
		"uploads/ep004.backup.txt":  "ep004.backup",
	}
	for name, want := range tests {
		if got := episodeID(name); got != want {
			t.Errorf("episodeID(%q) = %q, want %q", name, got, want)
		}
	}
}
