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

func TestJSONParser(t *testing.T) {
	tests := []struct {
		file string
		want *Episode
	}{
		{
			file: "single_guest.json",
			want: &Episode{
				EpisodeID: "ep100",
				Title:     "Single Guest",
				Host:      "Ann Host",
				Guests:    []string{"Ben Guest"},
				Transcript: []Segment{
					{Timestamp: "00:00", Speaker: "Ann", Section: "Introduction", Text: "Welcome to the show."},
					{Timestamp: "00:10", Speaker: "Ben", Section: "Introduction", Text: "Thanks for having me."},
				},
			},
		},
		{
			file: "multi_guest.json",
			want: &Episode{
				EpisodeID: "ep101",
				Title:     "Multi Guest",
				Host:      "Ann Host",
				Guests:    []string{"Dr. Cara Guest", "Dan Guest"},
				Transcript: []Segment{
					{Timestamp: "00:00", Speaker: "Ann", Section: "Introduction", Text: "Two guests today."},
					{Timestamp: "00:15", Speaker: "Dr. Cara", Section: "Deep Dive", Text: "Glad to be here."},
					{Timestamp: "01:05", Speaker: "Dan", Section: "Pros & Cons", Text: "Same here."},
				},
			},
		},
		{
			file: "no_guests.json",
			want: &Episode{
				EpisodeID: "ep102",
				Title:     "Solo",
				Host:      "Ann Host",
				Guests:    []string{},
				Transcript: []Segment{
					{Timestamp: "00:00", Speaker: "Ann", Section: "Closing", Text: "Just me this week."},
				},
			},
		},
		{
			file: "unicode_escapes.json",
			want: &Episode{
				EpisodeID: "ep103",
				Title:     "Escapes — Decoded",
				Host:      "Ann Host",
				Guests:    []string{"Ben Guest"},
				Transcript: []Segment{
					{Timestamp: "00:00", Speaker: "Ann", Section: "Introduction", Text: "It’s working."},
				},
			},
		},
		{
			file: "unknown_fields.json",
			want: &Episode{
				EpisodeID: "ep104",
				Title:     "Extra Fields",
				Host:      "Ann Host",
				Guests:    []string{"Ben Guest"},
				Transcript: []Segment{
					{Timestamp: "00:00", Speaker: "Ann", Section: "Introduction", Text: "Hello."},
				},
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

			got, err := JSONParser{}.Parse(tt.file, f)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestJSONParserErrors(t *testing.T) {
	readErr := errors.New("connection reset")

	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{name: "empty input", in: ""},
		{name: "malformed", in: `{"episode_id": "ep001",`},
		{name: "not an object", in: `["ep001"]`},
		{name: "wrong field type", in: `{"guests": "Mark Rivera", "transcript": [{"text": "hi"}]}`},
		{name: "trailing data", in: `{"transcript": [{"text": "hi"}]} {}`},
		{name: "missing transcript", in: `{"episode_id": "ep001"}`, wantErr: ErrEmptyTranscript},
		{name: "empty transcript", in: `{"episode_id": "ep001", "transcript": []}`, wantErr: ErrEmptyTranscript},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep, err := JSONParser{}.Parse("bad.json", strings.NewReader(tt.in))
			if err == nil {
				t.Fatalf("Parse succeeded with %+v, want error", ep)
			}
			if ep != nil {
				t.Errorf("Parse returned episode %+v alongside error", ep)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), "bad.json") {
				t.Errorf("err = %v, want it to mention the file name", err)
			}
		})
	}

	t.Run("read error", func(t *testing.T) {
		_, err := JSONParser{}.Parse("bad.json", iotest.ErrReader(readErr))
		if !errors.Is(err, readErr) {
			t.Errorf("err = %v, want %v", err, readErr)
		}
	})
}
