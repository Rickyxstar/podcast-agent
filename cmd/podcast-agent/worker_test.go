package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Rickyxstar/podcast-agent/internal/queue/sqs"
	"github.com/Rickyxstar/podcast-agent/internal/storage/disk"
)

func newTestWorker(t *testing.T) *worker {
	t.Helper()
	store, err := disk.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sample, err := os.ReadFile("../../samples/ep001_remote_work.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "incoming/ep001.json", strings.NewReader(string(sample)), ""); err != nil {
		t.Fatal(err)
	}
	// agent is nil: these tests must never reach the pipeline.
	return &worker{store: store, bucket: "podcasts", timeout: time.Minute}
}

func event(bucket, key, etag string) sqs.Message {
	return sqs.Message{ID: "m1", Body: fmt.Sprintf(
		`{"Records":[{"s3":{"bucket":{"name":%q},"object":{"key":%q,"eTag":%q}}}]}`, bucket, key, etag)}
}

func TestWorkerSkipsProcessedUpload(t *testing.T) {
	ctx := context.Background()
	w := newTestWorker(t)
	src := source{Key: "incoming/ep001.json", ETag: "v1"}
	if err := w.markDone(ctx, "ep001", src); err != nil {
		t.Fatal(err)
	}

	if err := w.handle(ctx, event("podcasts", src.Key, src.ETag)); err != nil {
		t.Errorf("handle = %v, want skip", err)
	}

	// A new ETag means new content, which must not be skipped.
	if done, err := w.done(ctx, "ep001", source{Key: src.Key, ETag: "v2"}); err != nil || done {
		t.Errorf("done for new ETag = %v, %v; want false", done, err)
	}
	// Without an ETag nothing can be proven, so the upload is processed.
	if done, err := w.done(ctx, "ep001", source{Key: src.Key}); err != nil || done {
		t.Errorf("done without ETag = %v, %v; want false", done, err)
	}
	if done, err := w.done(ctx, "ep002", src); err != nil || done {
		t.Errorf("done with no marker = %v, %v; want false", done, err)
	}
}

func TestWorkerAcksTestEvent(t *testing.T) {
	w := newTestWorker(t)
	if err := w.handle(context.Background(), sqs.Message{Body: `{"Service":"Amazon S3","Event":"s3:TestEvent"}`}); err != nil {
		t.Errorf("handle = %v", err)
	}
}

func TestWorkerRejectsBadEvents(t *testing.T) {
	ctx := context.Background()
	w := newTestWorker(t)
	for name, m := range map[string]sqs.Message{
		"not json":     {Body: "nope"},
		"other bucket": event("elsewhere", "incoming/ep001.json", "v1"),
		"missing":      event("podcasts", "incoming/missing.json", "v1"),
		"unsupported":  event("podcasts", "incoming/ep001.mp3", "v1"),
	} {
		if err := w.handle(ctx, m); err == nil {
			t.Errorf("%s: handle = nil, want error", name)
		}
	}
}
