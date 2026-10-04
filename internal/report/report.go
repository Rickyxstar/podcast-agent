// Package report defines the pipeline's output, report.json, and renders it
// as JSON for machines and Markdown for people.
package report

// Report is everything the pipeline produced for one episode. Field order is
// the key order in report.json, so keep it matching ARCHITECTURE §6.
type Report struct {
	Episode   Episode   `json:"episode"`
	Summary   string    `json:"summary"`
	Takeaways []string  `json:"takeaways"`
	Quotes    []Quote   `json:"quotes"`
	Topics    []string  `json:"topics"`
	FactCheck FactCheck `json:"fact_check"`
	Run       Run       `json:"run"`
}

// Episode identifies the episode the report is about.
type Episode struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Host   string   `json:"host"`
	Guests []string `json:"guests"`
	// Duration is the last transcript timestamp as written in the source,
	// e.g. "06:00".
	Duration string `json:"duration"`
}

// Quote is a notable line from the transcript. Text is copied exactly as
// said; quotes the agent couldn't find in the transcript are left out.
type Quote struct {
	Text    string `json:"text"`
	Speaker string `json:"speaker"`
	// Timestamp is the segment timestamp the quote came from, e.g. "04:10".
	Timestamp string `json:"timestamp"`
}

// FactCheck is the outcome of the fact-check stage.
type FactCheck struct {
	Status FactCheckStatus `json:"status"`
	Claims []Claim         `json:"claims"`
}

// FactCheckStatus says whether the fact-check stage ran to completion.
type FactCheckStatus string

const (
	// StatusCompleted means every extracted claim was checked.
	StatusCompleted FactCheckStatus = "completed"
	// StatusPartial means the stage failed part way; Claims holds what was
	// checked before the failure.
	StatusPartial FactCheckStatus = "partial"
	// StatusFailed means the stage produced no usable claims.
	StatusFailed FactCheckStatus = "failed"
)

// Claim is one checked statement from the transcript.
type Claim struct {
	ID        string    `json:"id"`
	Claim     string    `json:"claim"`
	Speaker   string    `json:"speaker"`
	Timestamp string    `json:"timestamp"`
	Type      ClaimType `json:"type"`
	Verdict   Verdict   `json:"verdict"`
	// Confidence is confidence in Verdict, from 0 to 1, computed in code as
	// described in ARCHITECTURE §5.
	Confidence float64    `json:"confidence"`
	Evidence   []Evidence `json:"evidence"`
	Reasoning  string     `json:"reasoning"`
}

// ClaimType classifies a claim, which decides how it can be checked.
type ClaimType string

const (
	ClaimFactual    ClaimType = "factual"
	ClaimPrediction ClaimType = "prediction"
	ClaimAnecdote   ClaimType = "anecdote"
	// ClaimOpinion can't be fact-checked, so the Markdown table leaves it out.
	ClaimOpinion ClaimType = "opinion"
)

// Verdict is the fact-check result for a claim. See ARCHITECTURE §5.
type Verdict string

const (
	// VerdictVerified means current evidence from a trusted source supports
	// the claim.
	VerdictVerified Verdict = "verified"
	// VerdictOutdatedOrInaccurate means evidence contradicts the claim, or it
	// was once true but no longer is.
	VerdictOutdatedOrInaccurate Verdict = "outdated_or_inaccurate"
	// VerdictUnverifiable means the claim is an anecdote or prediction, or no
	// sufficient evidence was found.
	VerdictUnverifiable Verdict = "unverifiable"
)

// Evidence is one source consulted for a claim.
type Evidence struct {
	// Source names where the evidence came from, e.g. "kb:gitlab-handbook".
	Source  string `json:"source"`
	Snippet string `json:"snippet"`
	URL     string `json:"url,omitempty"`
	// Date is when the evidence was published, as YYYY-MM-DD, if known.
	Date string `json:"date,omitempty"`
}

// Run describes how the report was produced.
type Run struct {
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	Tokens     Tokens   `json:"tokens"`
	CostUSD    float64  `json:"cost_usd"`
	DurationMS int64    `json:"duration_ms"`
	TraceID    string   `json:"trace_id"`
	Warnings   []string `json:"warnings"`
}

// Tokens totals token usage across every model call in the run.
type Tokens struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}
