package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/Rickyxstar/podcast-agent/internal/agent"
	llmfactory "github.com/Rickyxstar/podcast-agent/internal/llm/factory"
	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/search"
	"github.com/Rickyxstar/podcast-agent/internal/storage"
	"github.com/Rickyxstar/podcast-agent/internal/trace"
	"github.com/Rickyxstar/podcast-agent/internal/transcript"
)

func newRunCmd(cfg *config) *cobra.Command {
	var pretty bool

	cmd := &cobra.Command{
		Use:   "run <file>",
		Short: "Process one local transcript (.json or .txt) and write its report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout)
			defer cancel()

			ep, err := parseFile(args[0])
			if err != nil {
				return err
			}
			d, err := cfg.build(ctx)
			if err != nil {
				return err
			}
			slog.Info("parsed transcript",
				"episode", ep.EpisodeID,
				"segments", len(ep.Transcript),
				"llm", d.llm.Name(),
				"search", d.search.Name(),
				"storage", d.storage.Name(),
			)

			model := llmfactory.Model(cfg.LLM)
			price, ok := agent.PriceFor(model)
			if !ok {
				slog.Info("no price for model; report cost will be 0", "model", model)
			}
			acfg := agent.Config{Price: price}
			rec := &trace.Recorder{}
			var sink trace.Sink = rec
			if pretty {
				sink = trace.Tee(rec, trace.NewPretty(os.Stderr))
				// The pretty trace replaces the agent's per-event log lines.
				if !cfg.Debug {
					acfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
				}
			}
			a := agent.New(d.llm, []search.Provider{d.search}, acfg)
			rep, err := a.Run(trace.WithSink(ctx, sink), ep)
			if err != nil {
				return err
			}
			if err := saveReport(ctx, d.storage, rep, rec); err != nil {
				return err
			}
			if pretty {
				return rep.WriteMarkdown(os.Stdout)
			}
			return rep.WriteJSON(os.Stdout)
		},
	}

	cmd.Flags().BoolVar(&pretty, "pretty", false, "print the Markdown report to stdout and a readable agent trace to stderr")
	return cmd
}

func parseFile(name string) (*transcript.Episode, error) {
	p, err := transcript.NewParser(name)
	if err != nil {
		return nil, err
	}

	// run command can only be used with local storage
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ep, err := p.Parse(name, f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	return ep, nil
}

// saveReport writes rep as report.json and report.md, and tr as
// trace.jsonl, under results/<episode>/ in store.
func saveReport(ctx context.Context, store storage.Provider, rep *report.Report, tr *trace.Recorder) error {
	dir := "results/" + rep.Episode.ID + "/"

	var js, md, tl bytes.Buffer
	if err := rep.WriteJSON(&js); err != nil {
		return err
	}
	if err := rep.WriteMarkdown(&md); err != nil {
		return err
	}
	if err := tr.WriteJSONL(&tl); err != nil {
		return err
	}
	if err := store.Put(ctx, dir+"report.json", &js, "application/json"); err != nil {
		return fmt.Errorf("save report: %w", err)
	}
	if err := store.Put(ctx, dir+"report.md", &md, "text/markdown; charset=utf-8"); err != nil {
		return fmt.Errorf("save report: %w", err)
	}
	if err := store.Put(ctx, dir+"trace.jsonl", &tl, "application/x-ndjson"); err != nil {
		return fmt.Errorf("save trace: %w", err)
	}
	slog.Info("saved report", "storage", store.Name(), "key", dir+"report.json")
	return nil
}
