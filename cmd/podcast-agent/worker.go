package main

import (
	"errors"
	"os"

	"github.com/spf13/cobra"
)

func newWorkerCmd(cfg *config) *cobra.Command {
	var queueURL string

	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Consume S3 upload events from SQS and process each transcript",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if queueURL == "" {
				return errors.New("--queue-url or SQS_QUEUE_URL is required")
			}
			if _, err := cfg.build(cmd.Context()); err != nil {
				return err
			}

			// TODO: long-poll queueURL until cmd.Context() is done; per
			// message, extend visibility while processing under cfg.Timeout,
			// skip if results exist for the key+ETag, delete on success only.
			return errors.New("worker: not implemented")
		},
	}

	cmd.Flags().StringVar(&queueURL, "queue-url", os.Getenv("SQS_QUEUE_URL"), "SQS queue URL [SQS_QUEUE_URL]")
	return cmd
}
