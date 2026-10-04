package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Rickyxstar/podcast-agent/internal/agent"
	"github.com/Rickyxstar/podcast-agent/internal/queue/sqs"
	"github.com/Rickyxstar/podcast-agent/internal/search"
	"github.com/Rickyxstar/podcast-agent/internal/storage"
	"github.com/Rickyxstar/podcast-agent/internal/trace"
	"github.com/Rickyxstar/podcast-agent/internal/transcript"
)

func newWorkerCmd(cfg *config) *cobra.Command {
	var queueURL string
	var concurrency int

	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Consume S3 upload events from SQS and process each transcript",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if queueURL == "" {
				return errors.New("--queue-url or SQS_QUEUE_URL is required")
			}
			ctx := cmd.Context()
			d, err := cfg.build(ctx)
			if err != nil {
				return err
			}
			consumer, err := sqs.New(ctx, sqs.Config{
				QueueURL:    queueURL,
				Region:      cfg.LLM.AWSRegion,
				Concurrency: concurrency,
			})
			if err != nil {
				return err
			}

			w := &worker{
				store:   d.storage,
				agent:   agent.New(d.llm, []search.Provider{d.search}, cfg.agentConfig()),
				bucket:  cfg.Storage.Bucket,
				timeout: cfg.Timeout,
			}
			slog.Info("worker started",
				"queue", queueURL,
				"concurrency", concurrency,
				"llm", d.llm.Name(),
				"search", d.search.Name(),
				"storage", d.storage.Name(),
			)
			// Run returns once ctx is done (SIGTERM) and in-flight jobs finish.
			err = consumer.Run(ctx, w.handle)
			slog.Info("worker stopped")
			return err
		},
	}

	cmd.Flags().StringVar(&queueURL, "queue-url", os.Getenv("SQS_QUEUE_URL"), "SQS queue URL [SQS_QUEUE_URL]")
	cmd.Flags().IntVar(&concurrency, "concurrency", envInt("WORKER_CONCURRENCY", 2), "episodes processed at once [WORKER_CONCURRENCY]")
	return cmd
}

// worker turns S3 upload events into reports.
type worker struct {
	store storage.Provider
	agent *agent.Agent
	// bucket is the S3 bucket store reads from; empty for disk storage.
	bucket  string
	timeout time.Duration
}

// source records which upload a report was made from, so a redelivered or
// duplicate event for the same content is skipped.
type source struct {
	Key  string `json:"key"`
	ETag string `json:"etag"`
}

// handle processes every object in an S3 event message. An error leaves the
// message on the queue to be retried.
func (w *worker) handle(ctx context.Context, m sqs.Message) error {
	objs, err := sqs.ParseS3Event(m.Body)
	if err != nil {
		return err
	}
	if len(objs) == 0 {
		slog.Info("skipping S3 test event", "message", m.ID)
		return nil
	}
	for _, obj := range objs {
		if err := w.process(ctx, obj); err != nil {
			return fmt.Errorf("%s: %w", obj.Key, err)
		}
	}
	return nil
}

// process runs the pipeline on one uploaded transcript and saves its report,
// unless a report for the same key and ETag already exists.
func (w *worker) process(ctx context.Context, obj sqs.S3Object) error {
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	if w.bucket != "" && obj.Bucket != w.bucket {
		return fmt.Errorf("event is for bucket %q, storage uses %q", obj.Bucket, w.bucket)
	}
	ep, err := w.fetch(ctx, obj.Key)
	if err != nil {
		return err
	}
	log := slog.With("key", obj.Key, "etag", obj.ETag, "episode", ep.EpisodeID)

	src := source{Key: obj.Key, ETag: obj.ETag}
	done, err := w.done(ctx, ep.EpisodeID, src)
	if err != nil {
		return err
	}
	if done {
		log.Info("already processed; skipping")
		return nil
	}

	log.Info("processing", "segments", len(ep.Transcript))
	rec := &trace.Recorder{}
	rep, err := w.agent.Run(trace.WithSink(ctx, rec), ep)
	if err != nil {
		return err
	}
	if err := saveReport(ctx, w.store, rep, rec); err != nil {
		return err
	}
	// Written last: a crash before this point reprocesses the upload.
	if err := w.markDone(ctx, ep.EpisodeID, src); err != nil {
		return err
	}
	log.Info("processed",
		"fact_check", rep.FactCheck.Status,
		"cost_usd", rep.Run.CostUSD,
		"duration_ms", rep.Run.DurationMS,
	)
	return nil
}

// fetch downloads and parses the transcript at key.
func (w *worker) fetch(ctx context.Context, key string) (*transcript.Episode, error) {
	p, err := transcript.NewParser(key)
	if err != nil {
		return nil, err
	}
	r, err := w.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	ep, err := p.Parse(key, r)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", key, err)
	}
	return ep, nil
}

func sourceKey(episodeID string) string {
	return "results/" + episodeID + "/source.json"
}

// done reports whether the episode's results were made from src.
func (w *worker) done(ctx context.Context, episodeID string, src source) (bool, error) {
	if src.ETag == "" {
		return false, nil
	}
	r, err := w.store.Get(ctx, sourceKey(episodeID))
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer r.Close()
	var got source
	if err := json.NewDecoder(r).Decode(&got); err != nil {
		// A corrupt marker just means the work is redone.
		slog.Warn("unreadable source marker; reprocessing", "key", sourceKey(episodeID), "err", err)
		return false, nil
	}
	return got == src, nil
}

// markDone records that the episode's results were made from src.
func (w *worker) markDone(ctx context.Context, episodeID string, src source) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	if err := w.store.Put(ctx, sourceKey(episodeID), bytes.NewReader(b), "application/json"); err != nil {
		return fmt.Errorf("save source marker: %w", err)
	}
	return nil
}
