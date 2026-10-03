package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/Rickyxstar/podcast-agent/internal/agent"
	"github.com/Rickyxstar/podcast-agent/internal/report"
	"github.com/Rickyxstar/podcast-agent/internal/search"
	"github.com/Rickyxstar/podcast-agent/internal/storage"
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

			// TODO: set agent.Config.Price from a per-model price table so
			// reports carry cost.
			a := agent.New(d.llm, []search.Provider{d.search}, agent.Config{})
			rep, err := a.Run(ctx, ep)
			if err != nil {
				return err
			}
			if err := saveReport(ctx, d.storage, rep); err != nil {
				return err
			}
			if pretty {
				return rep.WriteMarkdown(os.Stdout)
			}
			return rep.WriteJSON(os.Stdout)
		},
	}

	cmd.Flags().BoolVar(&pretty, "pretty", false, "print the Markdown report to stdout")
	return cmd
}

func parseFile(name string) (*transcript.Episode, error) {
	p, err := transcript.NewParser(name)
	if err != nil {
		return nil, err
	}
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

// saveReport writes rep as report.json and report.md under
// results/<episode>/ in store.
func saveReport(ctx context.Context, store storage.Provider, rep *report.Report) error {
	dir := "results/" + rep.Episode.ID + "/"

	var js, md bytes.Buffer
	if err := rep.WriteJSON(&js); err != nil {
		return err
	}
	if err := rep.WriteMarkdown(&md); err != nil {
		return err
	}
	if err := store.Put(ctx, dir+"report.json", &js, "application/json"); err != nil {
		return fmt.Errorf("save report: %w", err)
	}
	if err := store.Put(ctx, dir+"report.md", &md, "text/markdown; charset=utf-8"); err != nil {
		return fmt.Errorf("save report: %w", err)
	}
	slog.Info("saved report", "storage", store.Name(), "key", dir+"report.json")
	return nil
}
