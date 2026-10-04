package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
)

// repair sends the problems in c back to the model once, continuing the
// summary conversation in msgs (which ends with the model's reply), and
// returns the check of the repaired notes.
//
// The model is asked only for the fields at fault, and a fix is kept only
// if it passes, so a repair can't make the notes worse. A failed repair
// call is a warning, not an error: the notes as they were are still worth
// publishing.
func (j *job) repair(ctx context.Context, m *meter, msgs []llm.Message, c checked) checked {
	fields := c.repairFields()
	resp, err := j.chat(ctx, "repair", m, llm.ChatRequest{
		System:         summarySystem,
		Messages:       append(msgs, llm.Message{Role: llm.RoleUser, Text: c.repairPrompt(fields)}),
		ResponseSchema: repairSchema(fields),
		Effort:         j.cfg.SummaryEffort,
	})
	var fix notes
	if err == nil {
		if err = json.Unmarshal([]byte(resp.Message.Text), &fix); err != nil {
			err = fmt.Errorf("decode: %w", err)
		}
	}
	if err != nil {
		j.warnf("repair failed: %v", err)
		j.emit(ctx, "repair", slog.String("error", err.Error()))
		return c
	}

	after := j.check(c.merge(&fix))
	issueFields := make([]string, len(c.issues))
	for i, is := range c.issues {
		issueFields[i] = is.field
	}
	j.emit(ctx, "repair",
		slog.Int("quotes_requested", c.missing),
		slog.Int("quotes_accepted", len(after.notes.Quotes)-len(c.notes.Quotes)),
		slog.Int("quotes_rejected", len(after.failed)),
		slog.String("fields", strings.Join(issueFields, ",")),
		slog.Int("fixes_requested", len(c.issues)),
		slog.Int("fixes_accepted", len(c.issues)-len(after.issues)),
	)
	return after
}

// notesFields lists the fields of notes in schema order.
var notesFields = []string{"summary", "takeaways", "quotes", "topics"}

// repairFields returns the fields of notes the repair asks for.
func (c *checked) repairFields() []string {
	var fields []string
	for _, f := range notesFields {
		if f == "quotes" && c.missing > 0 || slices.ContainsFunc(c.issues, func(is issue) bool { return is.field == f }) {
			fields = append(fields, f)
		}
	}
	return fields
}

// repairPrompt describes the problems in c and asks for fields.
func (c *checked) repairPrompt(fields []string) string {
	var b strings.Builder
	b.WriteString("Some of your output failed validation. Fix the problems below.\n")
	if c.missing > 0 {
		b.WriteString("\nQuotes must be copied verbatim from a single transcript line.\n")
		for _, q := range c.failed {
			switch {
			case q.duplicate:
				fmt.Fprintf(&b, "- %q repeats another quote.\n", q.text)
			case q.closest == nil:
				fmt.Fprintf(&b, "- %q was not found in the transcript.\n", q.text)
			default:
				fmt.Fprintf(&b, "- %q was not found in the transcript. The closest line is [%s] %s: %q\n",
					q.text, q.closest.Timestamp, q.closest.Speaker, q.closest.Text)
			}
		}
		if len(c.notes.Quotes) > 0 {
			b.WriteString("These quotes passed and are kept, so don't repeat them:\n")
			for _, q := range c.notes.Quotes {
				fmt.Fprintf(&b, "- %q\n", q.Text)
			}
		}
		noun := "new quotes"
		if c.missing == 1 {
			noun = "new quote"
		}
		fmt.Fprintf(&b, "Return %d %s in \"quotes\". Copy a line exactly, or choose a different line.\n", c.missing, noun)
	}
	if len(c.issues) > 0 {
		b.WriteString("\nOther problems:\n")
		for _, is := range c.issues {
			fmt.Fprintf(&b, "- %s.\n", is.problem)
		}
	}
	fmt.Fprintf(&b, "\nReturn only %s. Everything else is kept from your first answer.", strings.Join(fields, ", "))
	return b.String()
}

// merge applies fix to c's notes. New quotes go after the verified ones, so
// checking the result drops any that repeat them; every other field is
// taken only if it now passes.
func (c *checked) merge(fix *notes) *notes {
	n := c.notes
	if c.missing > 0 {
		n.Quotes = append(slices.Clone(c.notes.Quotes), fix.Quotes...)
	}
	for _, is := range c.issues {
		switch is.field {
		case "summary":
			if summaryProblem(fix.Summary) == "" {
				n.Summary = fix.Summary
			}
		case "takeaways":
			if takeawaysProblem(fix.Takeaways) == "" {
				n.Takeaways = fix.Takeaways
			}
		case "topics":
			if t := tidyTopics(fix.Topics); topicsProblem(t) == "" {
				n.Topics = t
			}
		}
	}
	return &n
}

// notesProperties are the property schemas of notesSchema, by field.
var notesProperties = func() map[string]json.RawMessage {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(notesSchema, &s); err != nil {
		panic(err)
	}
	return s.Properties
}()

// repairSchema is notesSchema cut down to fields, all required, as strict
// structured output needs.
func repairSchema(fields []string) json.RawMessage {
	props := make(map[string]json.RawMessage, len(fields))
	for _, f := range fields {
		props[f] = notesProperties[f]
	}
	b, err := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             fields,
		"additionalProperties": false,
	})
	if err != nil {
		panic(err)
	}
	return b
}
