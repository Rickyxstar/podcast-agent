package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewParser(t *testing.T) {
	tests := []struct {
		name string
		want Parser
	}{
		{name: "ep001.json", want: JSONParser{}},
		{name: "uploads/2026/10/ep001.json", want: JSONParser{}},
		{name: "EP001.JSON", want: JSONParser{}},
		{name: "ep004.txt", want: TextParser{}},
		{name: "ep004.text", want: TextParser{}},
		{name: "uploads/ep004.TXT", want: TextParser{}},
		{name: "ep004.backup.txt", want: TextParser{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewParser(tt.name)
			if err != nil {
				t.Fatalf("NewParser: %v", err)
			}
			if got != tt.want {
				t.Errorf("NewParser = %T, want %T", got, tt.want)
			}
		})
	}
}

func TestNewParserUnsupported(t *testing.T) {
	for _, name := range []string{
		"ep001",
		"ep001.pdf",
		"ep001.json.gz",
		"ep001.txt.bak",
		"uploads.json/ep001",
		"",
	} {
		t.Run(name, func(t *testing.T) {
			p, err := NewParser(name)
			if !errors.Is(err, ErrUnsupportedFormat) {
				t.Fatalf("err = %v, want %v", err, ErrUnsupportedFormat)
			}
			if p != nil {
				t.Errorf("NewParser returned %T alongside error", p)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("err = %v, want it to mention %q", err, name)
			}
		})
	}
}

// TestNewParserE2E runs every fixture through NewParser and Parse, the way a
// caller handling an uploaded file would.
func TestNewParserE2E(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures found in testdata")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			p, err := NewParser(path)
			if err != nil {
				t.Fatalf("NewParser: %v", err)
			}
			ep, err := p.Parse(path, f)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if ep.EpisodeID == "" {
				t.Error("EpisodeID is empty")
			}
			if len(ep.Transcript) == 0 {
				t.Fatal("Transcript is empty")
			}
			for i, seg := range ep.Transcript {
				if seg.Timestamp == "" || seg.Speaker == "" || seg.Text == "" {
					t.Errorf("Transcript[%d] = %+v, want timestamp, speaker and text set", i, seg)
				}
			}
		})
	}
}
