package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

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

			// TODO: run the agent pipeline, write report.json + report.md to
			// d.storage under results/<episode>/, and print it (pretty
			// selects the Markdown rendering).
			_ = pretty
			return nil
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
