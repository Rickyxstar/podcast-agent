// Package sqs consumes jobs from an Amazon SQS queue: it long-polls for
// messages, keeps each one hidden from other consumers while its handler
// runs, and deletes it only when the handler succeeds.
package sqs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// ErrNoQueue is returned by New when Config.QueueURL is empty.
var ErrNoQueue = errors.New("sqs: no queue URL configured")

const (
	// waitTime is the long-poll duration, the most SQS allows.
	waitTime = 20 * time.Second
	// receiveBackoff is the pause after a failed ReceiveMessage.
	receiveBackoff = 5 * time.Second
	// deleteTimeout bounds DeleteMessage after a handler succeeds.
	deleteTimeout = 30 * time.Second
)

// Config configures a Consumer.
type Config struct {
	// QueueURL is the queue to consume. Required.
	QueueURL string
	// Region is the queue's AWS region. Empty uses AWS_REGION.
	Region string
	// Concurrency is how many messages are handled at once. Zero means 1.
	Concurrency int
	// Logger receives consumer events. Nil uses slog.Default().
	Logger *slog.Logger
}

// Message is one received SQS message.
type Message struct {
	ID   string
	Body string
	// ReceiveCount is how many times SQS has delivered the message,
	// including this time.
	ReceiveCount int
}

// Handler processes one message. Returning nil deletes it from the queue;
// returning an error leaves it to reappear after the visibility timeout,
// and to move to the dead-letter queue after the queue's maxReceiveCount.
type Handler func(ctx context.Context, m Message) error

// api is the subset of the SQS client the Consumer uses.
type api interface {
	ReceiveMessage(ctx context.Context, in *awssqs.ReceiveMessageInput, optFns ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *awssqs.DeleteMessageInput, optFns ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *awssqs.ChangeMessageVisibilityInput, optFns ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error)
}

// Consumer receives messages from one queue and hands them to a Handler.
type Consumer struct {
	client      api
	queueURL    string
	concurrency int
	// visibility is the queue's visibility timeout; the heartbeat renews it
	// every third of that.
	visibility time.Duration
	log        *slog.Logger
}

// New returns a Consumer for cfg.QueueURL. Credentials come from the default
// AWS chain, and AWS_ENDPOINT_URL_SQS or AWS_ENDPOINT_URL override the
// endpoint, e.g. for LocalStack. It reads the queue's visibility timeout, so
// the queue must exist. optFns adjust the client after Config is applied.
func New(ctx context.Context, cfg Config, optFns ...func(*awssqs.Options)) (*Consumer, error) {
	if cfg.QueueURL == "" {
		return nil, ErrNoQueue
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("sqs: %w", err)
	}
	client := awssqs.NewFromConfig(awsCfg, optFns...)

	out, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(cfg.QueueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameVisibilityTimeout},
	})
	if err != nil {
		return nil, fmt.Errorf("sqs: get queue attributes: %w", err)
	}
	secs, err := strconv.Atoi(out.Attributes[string(types.QueueAttributeNameVisibilityTimeout)])
	if err != nil || secs <= 0 {
		return nil, fmt.Errorf("sqs: queue has no usable visibility timeout: %q", out.Attributes[string(types.QueueAttributeNameVisibilityTimeout)])
	}
	return newConsumer(client, cfg, time.Duration(secs)*time.Second), nil
}

func newConsumer(client api, cfg Config, visibility time.Duration) *Consumer {
	return &Consumer{
		client:      client,
		queueURL:    cfg.QueueURL,
		concurrency: max(cfg.Concurrency, 1),
		visibility:  visibility,
		log:         cmp.Or(cfg.Logger, slog.Default()),
	}
}

// Run receives messages and handles up to Concurrency of them at once until
// ctx is done. Then it stops receiving and waits for in-flight handlers,
// which run on a context that ignores ctx's cancellation so a shutdown lets
// them finish; bound them with a timeout inside h. A message whose handler
// never finishes, because the process is killed, reappears on the queue.
// Run returns nil after a shutdown.
func (c *Consumer) Run(ctx context.Context, h Handler) error {
	jobCtx := context.WithoutCancel(ctx)
	slots := make(chan struct{}, c.concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		msgs, err := c.receive(ctx)
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				return nil
			}
			c.log.Warn("receive failed; retrying", "err", err, "in", receiveBackoff)
			select {
			case <-time.After(receiveBackoff):
			case <-ctx.Done():
				return nil
			}
			continue
		}
		if len(msgs) == 0 {
			<-slots
			continue
		}
		m := msgs[0]
		wg.Go(func() {
			defer func() { <-slots }()
			c.handle(jobCtx, m, h)
		})
	}
}

// receive long-polls for at most one message, so a busy consumer never
// holds messages it can't start yet.
func (c *Consumer) receive(ctx context.Context) ([]types.Message, error) {
	out, err := c.client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(c.queueURL),
		MaxNumberOfMessages:         1,
		WaitTimeSeconds:             int32(waitTime / time.Second),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
	})
	if err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// handle runs h on m with a visibility heartbeat, then deletes m if h
// succeeded.
func (c *Consumer) handle(ctx context.Context, raw types.Message, h Handler) {
	m := Message{ID: aws.ToString(raw.MessageId), Body: aws.ToString(raw.Body)}
	m.ReceiveCount, _ = strconv.Atoi(raw.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	log := c.log.With("message", m.ID, "receive_count", m.ReceiveCount)

	hbCtx, stop := context.WithCancel(ctx)
	var hb sync.WaitGroup
	hb.Go(func() { c.heartbeat(hbCtx, raw.ReceiptHandle, log) })
	err := h(ctx, m)
	stop()
	hb.Wait()

	if err != nil {
		log.Error("job failed; message will be retried", "err", err)
		return
	}
	dctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	if _, err := c.client.DeleteMessage(dctx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: raw.ReceiptHandle,
	}); err != nil {
		// The job is done but the message will come back; the handler's
		// idempotency check turns the redelivery into a no-op.
		log.Error("delete message failed", "err", err)
	}
}

// heartbeat renews the message's visibility timeout every third of it until
// ctx is done, so no other consumer receives the message while it's being
// handled.
func (c *Consumer) heartbeat(ctx context.Context, receipt *string, log *slog.Logger) {
	t := time.NewTicker(c.visibility / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		_, err := c.client.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
			QueueUrl:          aws.String(c.queueURL),
			ReceiptHandle:     receipt,
			VisibilityTimeout: int32(c.visibility / time.Second),
		})
		if err != nil && ctx.Err() == nil {
			log.Warn("extend visibility failed", "err", err)
		}
	}
}
