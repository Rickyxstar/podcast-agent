package agent

import (
	"testing"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/search"
)

var confidenceNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// published returns a pointer to the time age before confidenceNow.
func published(age time.Duration) *time.Time {
	t := confidenceNow.Add(-age)
	return &t
}

const year = 365 * 24 * time.Hour

func evidence(kind search.Kind, pub *time.Time, s stance) scoredEvidence {
	return scoredEvidence{result: search.Result{Kind: kind, Published: pub}, stance: s}
}

func TestConfidence(t *testing.T) {
	tests := []struct {
		name    string
		verdict report.Verdict
		ev      []scoredEvidence
		self    float64
		want    float64
	}{
		{
			name:    "no evidence",
			verdict: report.VerdictUnverifiable,
			self:    0.9,
			want:    noEvidenceConfidence,
		},
		{
			// 0.35·1 + 0.30·1 + 0.20·1 + 0.15·0.8
			name:    "recent kb supports verified",
			verdict: report.VerdictVerified,
			ev:      []scoredEvidence{evidence(search.KindKB, published(30*24*time.Hour), stanceSupports)},
			self:    0.8,
			want:    0.97,
		},
		{
			// 0.35·0.4 + 0.30·0 + 0.20·0.5 + 0.15·0.6
			name:    "undated web contradicts verified",
			verdict: report.VerdictVerified,
			ev:      []scoredEvidence{evidence(search.KindWeb, nil, stanceContradicts)},
			self:    0.6,
			want:    0.33,
		},
		{
			// 0.35·0.4 + 0.30·1 + 0.20·0.7 + 0.15·1
			name:    "contradicting web backs outdated",
			verdict: report.VerdictOutdatedOrInaccurate,
			ev:      []scoredEvidence{evidence(search.KindWeb, published(2*year), stanceContradicts)},
			self:    1,
			want:    0.73,
		},
		{
			// 0.35·1 + 0.30·1 + 0.20·0.4 + 0.15·0.4
			name:    "neutral kb backs unverifiable",
			verdict: report.VerdictUnverifiable,
			ev:      []scoredEvidence{evidence(search.KindKB, published(5*year), stanceNeutral)},
			self:    0.4,
			want:    0.79,
		},
		{
			// 0.35·1 + 0.30·0.5 + 0.20·0.75 + 0.15·0.8
			name:    "mixed evidence averages",
			verdict: report.VerdictVerified,
			ev: []scoredEvidence{
				evidence(search.KindKB, published(30*24*time.Hour), stanceSupports),
				evidence(search.KindKB, nil, stanceContradicts),
			},
			self: 0.8,
			want: 0.77,
		},
		{
			name:    "self rating above 1 clamps",
			verdict: report.VerdictVerified,
			ev:      []scoredEvidence{evidence(search.KindKB, published(0), stanceSupports)},
			self:    1.5,
			want:    1,
		},
		{
			name:    "self rating below 0 clamps",
			verdict: report.VerdictVerified,
			ev:      []scoredEvidence{evidence(search.KindKB, published(0), stanceSupports)},
			self:    -1,
			want:    0.85,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := confidence(tt.verdict, tt.ev, tt.self, confidenceNow); got != tt.want {
				t.Errorf("confidence = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSourceQuality(t *testing.T) {
	for kind, want := range map[search.Kind]float64{
		search.KindKB:  1.0,
		search.KindWeb: 0.4,
		"":             0.4,
	} {
		if got := sourceQuality(search.Result{Kind: kind}); got != want {
			t.Errorf("sourceQuality(%q) = %v, want %v", kind, got, want)
		}
	}
}

func TestRecency(t *testing.T) {
	tests := []struct {
		name string
		pub  *time.Time
		want float64
	}{
		{"undated", nil, 0.5},
		{"today", published(0), 1.0},
		{"just under a year", published(year - time.Second), 1.0},
		{"one year", published(year), 0.7},
		{"just under three years", published(3*year - time.Second), 0.7},
		{"three years", published(3 * year), 0.4},
		{"ten years", published(10 * year), 0.4},
	}
	for _, tt := range tests {
		if got := recency(search.Result{Published: tt.pub}, confidenceNow); got != tt.want {
			t.Errorf("recency(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestAgrees(t *testing.T) {
	want := map[report.Verdict]stance{
		report.VerdictVerified:             stanceSupports,
		report.VerdictOutdatedOrInaccurate: stanceContradicts,
		report.VerdictUnverifiable:         stanceNeutral,
	}
	for v, agreeing := range want {
		for _, s := range []stance{stanceSupports, stanceContradicts, stanceNeutral} {
			if got := agrees(v, s); got != (s == agreeing) {
				t.Errorf("agrees(%s, %s) = %v, want %v", v, s, got, !got)
			}
		}
	}
}
