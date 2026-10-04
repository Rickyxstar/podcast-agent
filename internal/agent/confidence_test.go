package agent

import (
	"testing"

	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/search"
)

func evidence(kind search.Kind, s stance) scoredEvidence {
	return scoredEvidence{result: search.Result{Kind: kind}, stance: s}
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
			// 0.8·1 + 0.2·0.8
			name:    "kb supports verified",
			verdict: report.VerdictVerified,
			ev:      []scoredEvidence{evidence(search.KindKB, stanceSupports)},
			self:    0.8,
			want:    0.96,
		},
		{
			// 0.8·0.6 + 0.2·1
			name:    "contradicting web backs outdated",
			verdict: report.VerdictOutdatedOrInaccurate,
			ev:      []scoredEvidence{evidence(search.KindWeb, stanceContradicts)},
			self:    1,
			want:    0.68,
		},
		{
			// 0.8·1 + 0.2·0.4
			name:    "neutral kb backs unverifiable",
			verdict: report.VerdictUnverifiable,
			ev:      []scoredEvidence{evidence(search.KindKB, stanceNeutral)},
			self:    0.4,
			want:    0.88,
		},
		{
			// The best source counts, so adding a web result to a KB result
			// doesn't lower confidence.
			name:    "web alongside kb takes the best source",
			verdict: report.VerdictVerified,
			ev: []scoredEvidence{
				evidence(search.KindKB, stanceSupports),
				evidence(search.KindWeb, stanceSupports),
			},
			self: 0.8,
			want: 0.96,
		},
		{
			name:    "mixed evidence caps",
			verdict: report.VerdictVerified,
			ev: []scoredEvidence{
				evidence(search.KindKB, stanceSupports),
				evidence(search.KindKB, stanceContradicts),
			},
			self: 0.8,
			want: mixedEvidenceConfidence,
		},
		{
			// 0.8·0.6 + 0.2·0, already under the cap
			name:    "mixed evidence below the cap is unchanged",
			verdict: report.VerdictVerified,
			ev:      []scoredEvidence{evidence(search.KindWeb, stanceContradicts)},
			self:    0,
			want:    0.48,
		},
		{
			name:    "self rating above 1 clamps",
			verdict: report.VerdictVerified,
			ev:      []scoredEvidence{evidence(search.KindKB, stanceSupports)},
			self:    1.5,
			want:    1,
		},
		{
			name:    "self rating below 0 clamps",
			verdict: report.VerdictVerified,
			ev:      []scoredEvidence{evidence(search.KindKB, stanceSupports)},
			self:    -1,
			want:    0.8,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := confidence(tt.verdict, tt.ev, tt.self); got != tt.want {
				t.Errorf("confidence = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSourceQuality(t *testing.T) {
	for kind, want := range map[search.Kind]float64{
		search.KindKB:  1.0,
		search.KindWeb: 0.6,
		"":             0.6,
	} {
		if got := sourceQuality(search.Result{Kind: kind}); got != want {
			t.Errorf("sourceQuality(%q) = %v, want %v", kind, got, want)
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
