package bridge

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pablitovicente/mqtt-to-nats/v2/internal/topics"
)

const (
	// publishTimeout is how long one publish attempt waits for NATS to confirm it.
	publishTimeout = 5 * time.Second

	// retryPause is the wait between two attempts to publish the same message.
	retryPause = 250 * time.Millisecond
)

// jetStreamPublisher is the part of jetstream.JetStream the publisher uses. Tests pass a fake.
type jetStreamPublisher interface {
	PublishMsgAsync(msg *nats.Msg, opts ...jetstream.PublishOpt) (jetstream.PubAckFuture, error)
	PublishMsg(ctx context.Context, msg *nats.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

// pendingPublish is an async publish waiting for NATS to confirm it.
type pendingPublish struct {
	future jetstream.PubAckFuture

	// giveUpAt is when retrying this message stops: its first attempt plus the stream's
	// duplicate window. After that a retry could store it twice.
	giveUpAt time.Time
}

// publisher takes one route's messages from its queue and publishes them to NATS. Two
// goroutines do the work: run publishes, confirm waits for NATS to confirm each publish and
// retries the ones that failed.
type publisher struct {
	routeName       string
	prefix          string
	fixedSubject    string
	messageIDs      bool
	jetStream       jetStreamPublisher
	queue           *queue
	duplicateWindow time.Duration
	logger          *slog.Logger

	// messageIDPrefix is "<route name>-<start time>-", and each message ID adds a counter.
	// The start time keeps IDs unique across restarts of the bridge.
	messageIDPrefix string
	nextMessageID   uint64

	stored  atomic.Uint64
	retried atomic.Uint64
	failed  atomic.Uint64
}

func newPublisher(routeName, prefix, fixedSubject string, messageIDs bool, jetStream jetStreamPublisher, messageQueue *queue, duplicateWindow time.Duration, logger *slog.Logger) *publisher {
	return &publisher{
		routeName:       routeName,
		prefix:          prefix,
		fixedSubject:    fixedSubject,
		messageIDs:      messageIDs,
		jetStream:       jetStream,
		queue:           messageQueue,
		duplicateWindow: duplicateWindow,
		logger:          logger,
		messageIDPrefix: routeName + "-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "-",
	}
}

// run publishes every message from the queue and hands each publish to confirm through
// pending. When drain is closed it publishes what is left in the queue without waiting for
// more, then returns. It also returns when ctx is cancelled. It closes pending when it returns.
func (publisher *publisher) run(ctx context.Context, drain <-chan struct{}, pending chan<- pendingPublish) {
	defer close(pending)

	for {
		select {
		case <-ctx.Done():
			return

		case received := <-publisher.queue.receive():
			publisher.publish(ctx, received, pending)

		case <-drain:
			publisher.publishWhatIsLeft(ctx, pending)
			return
		}
	}
}

func (publisher *publisher) publishWhatIsLeft(ctx context.Context, pending chan<- pendingPublish) {
	for {
		select {
		case received := <-publisher.queue.receive():
			publisher.publish(ctx, received, pending)
		default:
			return
		}

		if ctx.Err() != nil {
			return
		}
	}
}

func (publisher *publisher) publish(ctx context.Context, received message, pending chan<- pendingPublish) {
	subject := publisher.fixedSubject
	if subject == "" {
		subject = topics.TopicToSubject(publisher.prefix, received.topic)
	}

	msg := nats.NewMsg(subject)
	msg.Data = received.payload

	if publisher.messageIDs {
		publisher.nextMessageID++
		msg.Header.Set(jetstream.MsgIDHeader, publisher.messageIDPrefix+strconv.FormatUint(publisher.nextMessageID, 10))
	}

	giveUpAt := time.Now().Add(publisher.duplicateWindow)

	future, err := publisher.publishAsync(ctx, msg)
	if err != nil {
		if ctx.Err() != nil {
			return
		}

		// NATS is unreachable. Retrying here also stops this goroutine from taking more
		// messages, so the queue fills up and starts dropping instead of the broker.
		publisher.retryUntilStored(ctx, msg, giveUpAt, err)
		return
	}

	select {
	case pending <- pendingPublish{future: future, giveUpAt: giveUpAt}:
	case <-ctx.Done():
	}
}

// publishAsync publishes msg without waiting for NATS to confirm it. When --nats-max-pending
// publishes are already waiting, NATS makes PublishMsgAsync wait a little and then return
// ErrTooManyStalledMsgs. That only means NATS is behind, not that anything failed, so it keeps
// trying until there is room or ctx is cancelled.
func (publisher *publisher) publishAsync(ctx context.Context, msg *nats.Msg) (jetstream.PubAckFuture, error) {
	for {
		future, err := publisher.jetStream.PublishMsgAsync(msg)
		if !errors.Is(err, jetstream.ErrTooManyStalledMsgs) {
			return future, err
		}

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

// confirm waits, in publish order, for NATS to confirm each publish, and retries the ones
// that failed. It returns when pending is closed and empty, or ctx is cancelled.
func (publisher *publisher) confirm(ctx context.Context, pending <-chan pendingPublish) {
	for waiting := range pending {
		select {
		case <-waiting.future.Ok():
			publisher.stored.Add(1)

		case err := <-waiting.future.Err():
			publisher.retryUntilStored(ctx, waiting.future.Msg(), waiting.giveUpAt, err)

		case <-ctx.Done():
			return
		}
	}
}

// retryUntilStored publishes msg again, waiting for each attempt, until NATS stores it or
// giveUpAt passes. The message ID stays the same, so if an earlier attempt was stored after
// all, NATS ignores the repeat. Without message IDs that repeat is stored a second time.
// firstError is why the first attempt failed.
func (publisher *publisher) retryUntilStored(ctx context.Context, msg *nats.Msg, giveUpAt time.Time, firstError error) {
	lastError := firstError

	for time.Now().Before(giveUpAt) {
		publisher.retried.Add(1)

		attemptDeadline := time.Now().Add(publishTimeout)
		if giveUpAt.Before(attemptDeadline) {
			attemptDeadline = giveUpAt
		}
		attemptContext, cancel := context.WithDeadline(ctx, attemptDeadline)
		_, err := publisher.jetStream.PublishMsg(attemptContext, msg)
		cancel()

		if err == nil {
			publisher.stored.Add(1)
			return
		}

		if ctx.Err() != nil {
			return
		}
		lastError = err

		select {
		case <-time.After(retryPause):
		case <-ctx.Done():
			return
		}
	}

	publisher.failed.Add(1)
	// Logged at debug level: during a long NATS outage this could be one line per message.
	// The stats line carries the failed count.
	publisher.logger.Debug("message not stored in NATS, giving up", "route", publisher.routeName,
		"subject", msg.Subject, "messageID", msg.Header.Get(jetstream.MsgIDHeader), "error", lastError)
}
