package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/llm"
	"github.com/Rickyxstar/podcast-agent/internal/search"
)

// tool is a function the fact-check model can call.
type tool struct {
	def llm.ToolDef
	// run executes the call. An error goes back to the model as an IsError
	// result so it can correct itself; it doesn't end the loop.
	run func(ctx context.Context, input json.RawMessage) (string, error)
}

// toolset is the tools offered to the model, by name.
type toolset map[string]tool

func (ts toolset) add(t tool) { ts[t.def.Name] = t }

// defs returns the tool definitions sorted by name, so the request prefix
// is stable across calls and stays cacheable.
func (ts toolset) defs() []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(ts))
	for _, t := range ts {
		defs = append(defs, t.def)
	}
	slices.SortFunc(defs, func(a, b llm.ToolDef) int { return strings.Compare(a.Name, b.Name) })
	return defs
}

// runAll runs calls concurrently and returns one result per call, in call
// order, for a single user message.
func (ts toolset) runAll(ctx context.Context, calls []llm.ToolCall) []llm.ToolResult {
	results := make([]llm.ToolResult, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Go(func() {
			r := llm.ToolResult{ToolCallID: c.ID, Name: c.Name}
			t, ok := ts[c.Name]
			if !ok {
				r.Content, r.IsError = fmt.Sprintf("unknown tool %q", c.Name), true
			} else if out, err := t.run(ctx, c.Input); err != nil {
				r.Content, r.IsError = err.Error(), true
			} else {
				r.Content = out
			}
			results[i] = r
		})
	}
	wg.Wait()
	return results
}

// decodeInput unmarshals a tool call's arguments. Strict mode isn't
// guaranteed on every provider, so callers still check required fields.
func decodeInput[T any](input json.RawMessage) (T, error) {
	var v T
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(input, &v); err != nil {
		return v, fmt.Errorf("invalid arguments: %w", err)
	}
	return v, nil
}

var (
	searchSchema = json.RawMessage(`{
  "type": "object",
  "properties": {"query": {"type": "string", "description": "Keywords describing the fact to look up."}},
  "required": ["query"],
  "additionalProperties": false
}`)

	emptySchema = json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`)

	verdictSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "claim_id": {"type": "string"},
    "verdict": {"type": "string", "enum": ["verified", "outdated_or_inaccurate", "unverifiable"]},
    "evidence": {
      "type": "array",
      "description": "Search results the verdict rests on. Empty only for unverifiable.",
      "items": {
        "type": "object",
        "properties": {
          "result_id": {"type": "string", "description": "The id of a search result you were shown."},
          "stance": {"type": "string", "enum": ["supports", "contradicts", "neutral"]}
        },
        "required": ["result_id", "stance"],
        "additionalProperties": false
      }
    },
    "reasoning": {"type": "string", "description": "One or two sentences a reader of the report can follow."},
    "self_rating": {"type": "number", "description": "Your confidence in the verdict, from 0 to 1."}
  },
  "required": ["claim_id", "verdict", "evidence", "reasoning", "self_rating"],
  "additionalProperties": false
}`)
)

// factCheckTools returns the fact-check agent's tools: one search tool per
// search provider, get_current_date and submit_verdict.
func (j *job) factCheckTools(st *factCheckState) toolset {
	ts := toolset{}
	for _, p := range j.searches {
		ts.add(searchTool(p, st))
	}
	ts.add(tool{
		def: llm.ToolDef{
			Name:        "get_current_date",
			Description: "Returns today's date as YYYY-MM-DD.",
			InputSchema: emptySchema,
			Strict:      true,
		},
		run: func(context.Context, json.RawMessage) (string, error) {
			return j.cfg.Now().Format(time.DateOnly), nil
		},
	})
	ts.add(tool{
		def: llm.ToolDef{
			Name:        "submit_verdict",
			Description: "Records the verdict for one claim. Call it once per claim.",
			InputSchema: verdictSchema,
			Strict:      true,
		},
		run: func(ctx context.Context, input json.RawMessage) (string, error) {
			in, err := decodeInput[verdictInput](input)
			if err != nil {
				return "", err
			}
			c, err := st.submit(in)
			if err != nil {
				return "", err
			}
			j.emit(ctx, "verdict",
				slog.String("id", c.ID),
				slog.String("verdict", string(c.Verdict)),
				slog.Float64("confidence", c.Confidence),
				slog.Int("evidence", len(c.Evidence)),
				slog.String("reasoning", c.Reasoning),
			)
			return fmt.Sprintf("Recorded %s for %s.", c.Verdict, c.ID), nil
		},
	})
	return ts
}

// searchResult is a search hit as shown to the model.
type searchResult struct {
	ID        string      `json:"id"`
	Kind      search.Kind `json:"kind"`
	Source    string      `json:"source"`
	Title     string      `json:"title,omitempty"`
	Snippet   string      `json:"snippet"`
	URL       string      `json:"url,omitempty"`
	Published string      `json:"published,omitempty"`
}

// searchTool exposes p as "search_<name>", e.g. "search_kb".
func searchTool(p search.Provider, st *factCheckState) tool {
	return tool{
		def: llm.ToolDef{
			Name:        "search_" + p.Name(),
			Description: fmt.Sprintf("Searches %s for evidence about a claim. Cite results by id in submit_verdict.", p.Name()),
			InputSchema: searchSchema,
			Strict:      true,
		},
		run: func(ctx context.Context, input json.RawMessage) (string, error) {
			in, err := decodeInput[struct {
				Query string `json:"query"`
			}](input)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(in.Query) == "" {
				return "", errors.New("query is required")
			}
			results, err := p.Search(ctx, search.Request{Query: in.Query})
			if err != nil {
				return "", fmt.Errorf("search failed: %w", err)
			}
			st.remember(results)
			if len(results) == 0 {
				return "No results. Try different keywords.", nil
			}

			out := make([]searchResult, len(results))
			for i, r := range results {
				out[i] = searchResult{ID: r.ID, Kind: r.Kind, Source: r.Source, Title: r.Title, Snippet: r.Snippet, URL: r.URL}
				if r.Published != nil {
					out[i].Published = r.Published.Format(time.DateOnly)
				}
			}
			b, err := json.Marshal(out)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	}
}
