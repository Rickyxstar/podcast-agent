package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata")

func sampleReport() *Report {
	return &Report{
		Episode: Episode{
			ID:       "ep001",
			Title:    "The Future of Remote Work",
			Host:     "Sarah Chen",
			Guests:   []string{"Mark Rivera"},
			Duration: "06:00",
		},
		Summary: "Sarah and Mark discuss how remote work changed after 2020 & what async-first teams do differently.",
		Takeaways: []string{
			"Async-first teams write things down by default.",
			"Handbooks replace hallway conversations.",
			"Meetings are for decisions, not status.",
			"Time zones become a feature when work is documented.",
			"Remote work needs explicit\nonboarding.",
		},
		Quotes: []Quote{
			{Text: "If it isn't written down, it didn't happen.", Speaker: "Mark Rivera", Timestamp: "04:10", Verified: true},
			{Text: "Offices were never about productivity.", Speaker: "Sarah Chen", Timestamp: "05:02", Verified: false},
		},
		Topics: []string{"remote-work", "async-culture"},
		FactCheck: FactCheck{
			Status: StatusCompleted,
			Claims: []Claim{
				{
					ID: "c1", Claim: "GitLab, Automattic and Doist all publish | public handbooks.",
					Speaker: "Mark Rivera", Timestamp: "01:20", Type: ClaimFactual,
					Verdict: VerdictVerified, Confidence: 0.88,
					Evidence: []Evidence{
						{Source: "kb:gitlab-handbook", Snippet: "The GitLab handbook is public.", URL: "https://handbook.gitlab.com/(about)", Date: "2025-06-01"},
						{Source: "kb:doist-guide", Snippet: "Doist documents its async culture."},
					},
					Reasoning: "Both KB entries confirm public handbooks.",
				},
				{
					ID: "c2", Claim: "Half of all companies will be fully remote by 2030.",
					Speaker: "Sarah Chen", Timestamp: "03:45", Type: ClaimPrediction,
					Verdict: VerdictUnverifiable, Confidence: 0.4,
					Reasoning: "Predictions can't be checked.",
				},
				{
					ID: "c3", Claim: "Remote work is better for everyone.",
					Speaker: "Mark Rivera", Timestamp: "04:30", Type: ClaimOpinion,
					Verdict: VerdictUnverifiable, Confidence: 0.4,
				},
				{
					ID: "c4", Claim: "Zoom had 10 million daily users in 2024.",
					Speaker: "Sarah Chen", Timestamp: "02:00", Type: ClaimFactual,
					Verdict: VerdictOutdatedOrInaccurate, Confidence: 0.715,
					Evidence: []Evidence{{Source: "kb:zoom-stats", Snippet: "300 million daily meeting participants.", Date: "2024-01-10"}},
				},
			},
		},
		Run: Run{
			Provider: "anthropic", Model: "claude-opus-5-5",
			Tokens:  Tokens{Input: 12000, Output: 3400, CacheRead: 8000},
			CostUSD: 0.42, DurationMS: 18250, TraceID: "tr-123",
			Warnings: []string{"quote q2 not found verbatim in transcript"},
		},
	}
}

func TestGolden(t *testing.T) {
	tests := []struct {
		file  string
		write func(*Report, *bytes.Buffer) error
	}{
		{file: "ep001.json", write: func(r *Report, b *bytes.Buffer) error { return r.WriteJSON(b) }},
		{file: "ep001.md", write: func(r *Report, b *bytes.Buffer) error { return r.WriteMarkdown(b) }},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			var got bytes.Buffer
			if err := tt.write(sampleReport(), &got); err != nil {
				t.Fatal(err)
			}

			path := filepath.Join("testdata", tt.file)
			if *update {
				if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test -update to create it)", err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Errorf("output differs from %s (run go test -update to accept):\n%s", path, got.String())
			}
		})
	}
}

func TestWriteJSONRoundTrip(t *testing.T) {
	want := sampleReport()
	var buf bytes.Buffer
	if err := want.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Claims without evidence come back as [] rather than nil.
	want.FactCheck.Claims[1].Evidence = []Evidence{}
	want.FactCheck.Claims[2].Evidence = []Evidence{}
	if !reflect.DeepEqual(&got, want) {
		t.Errorf("round trip =\n%+v\nwant\n%+v", got, *want)
	}
}

func TestWriteJSONEmptyReport(t *testing.T) {
	r := &Report{FactCheck: FactCheck{Claims: []Claim{{ID: "c1"}}}}

	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if strings.Contains(out, "null") {
		t.Errorf("nil slices should encode as [], got:\n%s", out)
	}
	if !strings.HasSuffix(out, "}\n") {
		t.Errorf("output should end with a newline, got %q", out[len(out)-3:])
	}
	if r.Takeaways != nil || r.FactCheck.Claims[0].Evidence != nil {
		t.Error("WriteJSON modified the report")
	}
}

func TestWriteJSONStable(t *testing.T) {
	var a, b bytes.Buffer
	if err := sampleReport().WriteJSON(&a); err != nil {
		t.Fatal(err)
	}
	if err := sampleReport().WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("two encodings of the same report differ")
	}
}

func TestWriteMarkdownEmptyReport(t *testing.T) {
	var buf bytes.Buffer
	if err := (&Report{Episode: Episode{ID: "ep009"}}).WriteMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{
		"# ep009\n",
		"_No summary._",
		"_No takeaways._",
		"_No quotes._",
		"_No topics._",
		"_No checkable claims found._",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "| Claim |") {
		t.Errorf("empty report should have no fact-check table:\n%s", out)
	}
	if strings.Contains(out, "Generated by") {
		t.Errorf("footer should be omitted without run info:\n%s", out)
	}
}

func TestWriteMarkdownFactCheckStatus(t *testing.T) {
	tests := []struct {
		name    string
		fc      FactCheck
		want    []string
		notWant []string
	}{
		{
			name: "partial",
			fc: FactCheck{Status: StatusPartial, Claims: []Claim{
				{Claim: "A checked claim.", Type: ClaimFactual, Verdict: VerdictVerified, Confidence: 0.9},
			}},
			want: []string{"Fact-check did not finish", "| A checked claim. |"},
		},
		{
			name:    "failed",
			fc:      FactCheck{Status: StatusFailed},
			want:    []string{"Fact-check failed"},
			notWant: []string{"| Claim |", "_No checkable claims found._"},
		},
		{
			name: "only opinions",
			fc: FactCheck{Status: StatusCompleted, Claims: []Claim{
				{Claim: "Pineapple belongs on pizza.", Type: ClaimOpinion, Verdict: VerdictUnverifiable},
			}},
			want:    []string{"_No checkable claims found._"},
			notWant: []string{"Pineapple"},
		},
		{
			name: "unknown verdict",
			fc: FactCheck{Status: StatusCompleted, Claims: []Claim{
				{Claim: "Something.", Type: ClaimFactual, Verdict: "mostly|true"},
			}},
			want: []string{`| mostly\|true |`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := (&Report{FactCheck: tt.fc}).WriteMarkdown(&buf); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			for _, s := range tt.want {
				if !strings.Contains(out, s) {
					t.Errorf("missing %q in:\n%s", s, out)
				}
			}
			for _, s := range tt.notWant {
				if strings.Contains(out, s) {
					t.Errorf("unexpected %q in:\n%s", s, out)
				}
			}
		})
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errBroken }

var errBroken = errors.New("broken pipe")

func TestWriteErrors(t *testing.T) {
	r := sampleReport()
	if err := r.WriteJSON(failWriter{}); !errors.Is(err, errBroken) {
		t.Errorf("WriteJSON error = %v, want %v", err, errBroken)
	}
	if err := r.WriteMarkdown(failWriter{}); !errors.Is(err, errBroken) {
		t.Errorf("WriteMarkdown error = %v, want %v", err, errBroken)
	}
}
