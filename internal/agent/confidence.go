package agent

import (
	"math"

	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/search"
)

// Confidence is confidence in the assigned verdict, computed in code rather
// than taken from the model (ARCHITECTURE §5):
//
//	0.8·best_source_quality + 0.2·self_rating
//
// capped at mixedEvidenceConfidence when any cited evidence disagrees with
// the verdict.
const (
	weightSource = 0.8
	weightSelf   = 0.2

	// noEvidenceConfidence is the fixed confidence for verdicts no evidence
	// bears on: anecdotes, predictions, opinions and unchecked claims.
	noEvidenceConfidence = 0.40

	// mixedEvidenceConfidence caps verdicts whose own cited evidence
	// disagrees with them.
	mixedEvidenceConfidence = 0.50
)

// stance is how a piece of evidence bears on a claim.
type stance string

const (
	stanceSupports    stance = "supports"
	stanceContradicts stance = "contradicts"
	stanceNeutral     stance = "neutral"
)

type scoredEvidence struct {
	result search.Result
	stance stance
}

// confidence scores verdict v given its evidence and the model's own 0-1
// rating. It takes the best source rather than the mean, so citing a weaker
// source alongside a strong one doesn't lower confidence.
func confidence(v report.Verdict, ev []scoredEvidence, selfRating float64) float64 {
	if len(ev) == 0 {
		return noEvidenceConfidence
	}
	var best float64
	mixed := false
	for _, e := range ev {
		best = math.Max(best, sourceQuality(e.result))
		if !agrees(v, e.stance) {
			mixed = true
		}
	}
	c := weightSource*best + weightSelf*clamp01(selfRating)
	if mixed {
		c = math.Min(c, mixedEvidenceConfidence)
	}
	return math.Round(clamp01(c)*100) / 100
}

// sourceQuality rates where evidence came from.
//
// TODO: grade web results by domain: official sites 0.9, reputable news 0.7.
func sourceQuality(r search.Result) float64 {
	if r.Kind == search.KindKB {
		return 1.0
	}
	return 0.6
}

// agrees reports whether evidence with stance s backs verdict v.
func agrees(v report.Verdict, s stance) bool {
	switch v {
	case report.VerdictVerified:
		return s == stanceSupports
	case report.VerdictOutdatedOrInaccurate:
		return s == stanceContradicts
	default:
		return s == stanceNeutral
	}
}

func clamp01(x float64) float64 {
	return math.Max(0, math.Min(1, x))
}
