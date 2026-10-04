package sqs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// fakeSQS hands out queued messages once each and records deletes and
// visibility changes by receipt handle.
type fakeSQS struct {
	mu       sync.Mutex
	queue    []types.Message
	deleted  []string
	extended map[string]int
	// empty is signalled each time a receive finds no messages.
	empty chan struct{}
}

func newFakeSQS(bodies ...string) *fakeSQS {
	f := &fakeSQS{extended: map[string]int{}, empty: make(chan struct{}, 100)}
	for i, b := range bodies {
		id := string(rune('a' + i))
		f.queue = append(f.queue, types.Message{
			MessageId:     aws.String(id),
			ReceiptHandle: aws.String("rh-" + id),
			Body:          aws.String(b),
			Attributes:    map[string]string{"ApproximateReceiveCount": "1"},
		})
	}
	return f
}

func (f *fakeSQS) ReceiveMessage(ctx context.Context, in *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	if len(f.queue) > 0 {
		m := f.queue[0]
		f.queue = f.queue[1:]
		f.mu.Unlock()
		return &awssqs.ReceiveMessageOutput{Messages: []types.Message{m}}, nil
	}
	f.mu.Unlock()
	f.empty <- struct{}{}
	// Long poll: wait briefly, or until shutdown.
	select {
	case <-time.After(5 * time.Millisecond):
		return &awssqs.ReceiveMessageOutput{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *awssqs.DeleteMessageInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &awssqs.DeleteMessageOutput{}, nil
}

func (f *fakeSQS) ChangeMessageVisibility(_ context.Context, in *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extended[aws.ToString(in.ReceiptHandle)]++
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}

// runUntilDrained runs c until the queue has been emptied, then shuts it
// down and waits for Run to return.
func runUntilDrained(t *testing.T, c *Consumer, f *fakeSQS, h Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, h) }()
	select {
	case <-f.empty:
	case <-time.After(5 * time.Second):
		t.Fatal("queue never drained")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
}

func TestDeletesOnlyOnSuccess(t *testing.T) {
	f := newFakeSQS("ok", "fail")
	c := newConsumer(f, Config{QueueURL: "q"}, time.Minute)

	var got []Message
	runUntilDrained(t, c, f, func(_ context.Context, m Message) error {
		got = append(got, m)
		if m.Body == "fail" {
			return errors.New("boom")
		}
		return nil
	})

	if len(got) != 2 || got[0].ID != "a" || got[0].ReceiveCount != 1 {
		t.Errorf("handled %+v", got)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "rh-a" {
		t.Errorf("deleted %v, want [rh-a]", f.deleted)
	}
}

func TestHeartbeatExtendsVisibility(t *testing.T) {
	f := newFakeSQS("slow")
	// Heartbeat every 10ms; the handler runs for 50ms.
	c := newConsumer(f, Config{QueueURL: "q"}, 30*time.Millisecond)

	runUntilDrained(t, c, f, func(context.Context, Message) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	})

	if n := f.extended["rh-a"]; n < 2 {
		t.Errorf("visibility extended %d times, want at least 2", n)
	}
}

func TestShutdownFinishesInFlightJob(t *testing.T) {
	f := newFakeSQS("job")
	c := newConsumer(f, Config{QueueURL: "q"}, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	var jobErr error
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(jctx context.Context, _ Message) error {
			close(started)
			time.Sleep(20 * time.Millisecond)
			jobErr = jctx.Err()
			return nil
		})
	}()

	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if jobErr != nil {
		t.Errorf("job context cancelled by shutdown: %v", jobErr)
	}
	if len(f.deleted) != 1 {
		t.Errorf("deleted %v, want the finished job", f.deleted)
	}
}

func TestConcurrency(t *testing.T) {
	f := newFakeSQS("1", "2", "3", "4")
	c := newConsumer(f, Config{QueueURL: "q", Concurrency: 2}, time.Minute)

	var mu sync.Mutex
	running, peak := 0, 0
	runUntilDrained(t, c, f, func(context.Context, Message) error {
		mu.Lock()
		running++
		peak = max(peak, running)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	})

	if peak != 2 {
		t.Errorf("peak concurrency = %d, want 2", peak)
	}
	if len(f.deleted) != 4 {
		t.Errorf("deleted %d messages, want 4", len(f.deleted))
	}
}

func TestNewRequiresQueue(t *testing.T) {
	if _, err := New(context.Background(), Config{}); !errors.Is(err, ErrNoQueue) {
		t.Errorf("err = %v, want ErrNoQueue", err)
	}
}
