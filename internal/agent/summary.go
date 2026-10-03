package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
)

// notes is the summary stage's structured output.
type notes struct {
	Summary   string      `json:"summary"`
	Takeaways []string    `json:"takeaways"`
	Quotes    []noteQuote `json:"quotes"`
	Topics    []string    `json:"topics"`
}

type noteQuote struct {
	Text      string `json:"text"`
	Speaker   string `json:"speaker"`
	Timestamp string `json:"timestamp"`
}

// notesSchema constrains the summary stage's output. Counts and lengths
// aren't expressible in every provider's schema dialect, so validate checks
// them in code.
var notesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary": {"type": "string"},
    "takeaways": {"type": "array", "items": {"type": "string"}},
    "quotes": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "text": {"type": "string"},
          "speaker": {"type": "string"},
          "timestamp": {"type": "string"}
        },
        "required": ["text", "speaker", "timestamp"],
        "additionalProperties": false
      }
    },
    "topics": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["summary", "takeaways", "quotes", "topics"],
  "additionalProperties": false
}`)

const summarySystem = `You write show notes for an ad agency that publishes podcast highlights as episode pages and social posts.

From the transcript, produce:
- summary: 200-300 words covering the core themes, the key discussions, and the outcomes or opinions shared.
- takeaways: exactly 5 concise, self-contained takeaways a listener could act on or repeat.
- quotes: 3-5 notable lines suitable for social media. Copy each one verbatim from a single transcript line, with that line's speaker and timestamp. Do not paraphrase, merge lines, or fix grammar.
- topics: 3-8 lowercase kebab-case tags, e.g. "remote-work", "async-culture".

Use only what is said in the transcript. Do not add facts, numbers or names that the speakers did not say.`

// summarize runs the summary + notes stage: one structured call, no tools.
//
// TODO: repair malformed JSON from weaker local models before giving up.
func (j *job) summarize(ctx context.Context, m *meter) (*notes, error) {
	resp, err := j.chat(ctx, "summary", m, llm.ChatRequest{
		System: summarySystem,
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Text: "<transcript>\n" + j.lines + "</transcript>",
		}},
		ResponseSchema: notesSchema,
		Effort:         j.cfg.SummaryEffort,
	})
	if err != nil {
		return nil, err
	}
	var n notes
	if err := json.Unmarshal([]byte(resp.Message.Text), &n); err != nil {
		return nil, fmt.Errorf("decode notes: %w", err)
	}
	return &n, nil
}
