package agent

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/search"
)

// factCheckState tracks claims and the evidence the model has been shown.
// Tool calls run concurrently, so every method locks mu.
type factCheckState struct {
	mu     sync.Mutex
	claims []trackedClaim // in plan order
	index  map[string]int // claim ID → position in claims
	// seen holds every search result returned to the model, by ID, so
	// verdicts can cite only real evidence.
	seen map[string]search.Result
}

// trackedClaim is a claim and whether it has a verdict yet.
type trackedClaim struct {
	claim   report.Claim
	decided bool
	// strategy is the plan's approach for checking an open factual claim.
	strategy string
}

func newFactCheckState() *factCheckState {
	return &factCheckState{
		index: map[string]int{},
		seen:  map[string]search.Result{},
	}
}

// addDecided adds a claim that already has its verdict.
func (st *factCheckState) addDecided(c report.Claim) {
	st.add(trackedClaim{claim: c, decided: true})
}

// addOpen adds a factual claim for the verify loop to check.
func (st *factCheckState) addOpen(c report.Claim, strategy string) {
	st.add(trackedClaim{claim: c, strategy: strategy})
}

func (st *factCheckState) add(tc trackedClaim) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.index[tc.claim.ID] = len(st.claims)
	st.claims = append(st.claims, tc)
}

// pending returns the claims without a verdict, in plan order.
func (st *factCheckState) pending() []trackedClaim {
	st.mu.Lock()
	defer st.mu.Unlock()
	var open []trackedClaim
	for _, tc := range st.claims {
		if !tc.decided {
			open = append(open, tc)
		}
	}
	return open
}

// remember records search results so verdicts can cite them.
func (st *factCheckState) remember(results []search.Result) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, r := range results {
		st.seen[r.ID] = r
	}
}

// closeOpen marks every claim without a verdict unverifiable.
func (st *factCheckState) closeOpen(reason string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.claims {
		if tc := &st.claims[i]; !tc.decided {
			markUnverifiable(&tc.claim, reason)
			tc.decided = true
		}
	}
}

// result returns the claims that have verdicts, in plan order.
func (st *factCheckState) result(status report.FactCheckStatus) *factCheckResult {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := &factCheckResult{status: status}
	for _, tc := range st.claims {
		if tc.decided {
			out.claims = append(out.claims, tc.claim)
		}
	}
	return out
}

// markUnverifiable gives c an unverifiable verdict with the fixed
// no-evidence confidence.
func markUnverifiable(c *report.Claim, reason string) {
	c.Verdict = report.VerdictUnverifiable
	c.Confidence = noEvidenceConfidence
	c.Reasoning = reason
}

// verdictInput is submit_verdict's arguments.
type verdictInput struct {
	ClaimID  string          `json:"claim_id"`
	Verdict  report.Verdict  `json:"verdict"`
	Evidence []evidenceInput `json:"evidence"`
	// Reasoning explains the verdict in a sentence or two for the report.
	Reasoning string `json:"reasoning"`
	// SelfRating is the model's own 0-1 confidence, a minor input to the
	// computed confidence.
	SelfRating float64 `json:"self_rating"`
}

type evidenceInput struct {
	ResultID string `json:"result_id"`
	Stance   stance `json:"stance"`
}

var errNoEvidence = errors.New("a verified or outdated_or_inaccurate verdict needs at least one piece of evidence")

// submit records a verdict. Errors go back to the model so it can fix the
// call, e.g. by citing a result ID it was actually shown.
func (st *factCheckState) submit(in verdictInput) (report.Claim, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	i, ok := st.index[in.ClaimID]
	if !ok {
		return report.Claim{}, fmt.Errorf("unknown claim_id %q", in.ClaimID)
	}
	tc := &st.claims[i]
	if tc.decided {
		return report.Claim{}, fmt.Errorf("claim %s already has a verdict", in.ClaimID)
	}
	switch in.Verdict {
	case report.VerdictVerified, report.VerdictOutdatedOrInaccurate:
		if len(in.Evidence) == 0 {
			return report.Claim{}, errNoEvidence
		}
	case report.VerdictUnverifiable:
	default:
		return report.Claim{}, fmt.Errorf("unknown verdict %q", in.Verdict)
	}

	var scored []scoredEvidence
	var evidence []report.Evidence
	for _, e := range in.Evidence {
		r, ok := st.seen[e.ResultID]
		if !ok {
			return report.Claim{}, fmt.Errorf("evidence %q is not a search result you were shown; cite result ids exactly", e.ResultID)
		}
		scored = append(scored, scoredEvidence{result: r, stance: e.Stance})
		evidence = append(evidence, toEvidence(r))
	}

	tc.claim.Verdict = in.Verdict
	tc.claim.Evidence = evidence
	tc.claim.Reasoning = in.Reasoning
	tc.claim.Confidence = confidence(in.Verdict, scored, in.SelfRating)
	tc.decided = true
	return tc.claim, nil
}

func toEvidence(r search.Result) report.Evidence {
	e := report.Evidence{Source: r.Source, Snippet: r.Snippet, URL: r.URL}
	if r.Kind == search.KindKB {
		e.Source = "kb:" + r.ID
	}
	if r.Published != nil {
		e.Date = r.Published.Format(time.DateOnly)
	}
	return e
}
