package agent

import (
	"math"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/search"
)

// Confidence is confidence in the assigned verdict, computed in code rather
// than taken from the model (ARCHITECTURE §5):
//
//	0.35·source_quality + 0.30·agreement + 0.20·recency + 0.15·self_rating
const (
	weightSource    = 0.35
	weightAgreement = 0.30
	weightRecency   = 0.20
	weightSelf      = 0.15

	// noEvidenceConfidence is the fixed confidence for verdicts no evidence
	// bears on: anecdotes, predictions, opinions and unchecked claims.
	noEvidenceConfidence = 0.40
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

// confidence scores verdict v given its evidence, the model's own 0-1
// rating, and the current time.
func confidence(v report.Verdict, ev []scoredEvidence, selfRating float64, now time.Time) float64 {
	if len(ev) == 0 {
		return noEvidenceConfidence
	}
	var quality, agreement, recent float64
	for _, e := range ev {
		quality += sourceQuality(e.result)
		recent += recency(e.result, now)
		if agrees(v, e.stance) {
			agreement++
		}
	}
	n := float64(len(ev))
	c := weightSource*quality/n +
		weightAgreement*agreement/n +
		weightRecency*recent/n +
		weightSelf*clamp01(selfRating)
	return math.Round(clamp01(c)*100) / 100
}

// sourceQuality rates where evidence came from.
//
// TODO: grade web results by domain: official sites 0.9, reputable news 0.7.
func sourceQuality(r search.Result) float64 {
	if r.Kind == search.KindKB {
		return 1.0
	}
	return 0.4
}

// recency rates how current evidence is.
//
// TODO: decay relative to the claim's time sensitivity; a founding date
// doesn't go stale the way a launch date does.
func recency(r search.Result, now time.Time) float64 {
	if r.Published == nil {
		return 0.5
	}
	switch age := now.Sub(*r.Published); {
	case age < 365*24*time.Hour:
		return 1.0
	case age < 3*365*24*time.Hour:
		return 0.7
	default:
		return 0.4
	}
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
