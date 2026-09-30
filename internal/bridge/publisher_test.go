package bridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// fakeFuture is an async publish result that is already decided: stored, or failed with err.
type fakeFuture struct {
	msg *nats.Msg
	ok  chan *jetstream.PubAck
	err chan error
}

func newFakeFuture(msg *nats.Msg, err error) *fakeFuture {
	future := &fakeFuture{msg: msg, ok: make(chan *jetstream.PubAck, 1), err: make(chan error, 1)}
	if err != nil {
		future.err <- err
	} else {
		future.ok <- &jetstream.PubAck{}
	}
	return future
}

func (future *fakeFuture) Ok() <-chan *jetstream.PubAck { return future.ok }
func (future *fakeFuture) Err() <-chan error            { return future.err }
func (future *fakeFuture) Msg() *nats.Msg               { return future.msg }

// fakeJetStream records every publish. stalls is how many async publishes are refused with
// ErrTooManyStalledMsgs first; asyncError fails every async publish result; syncFailures is how
// many retries fail before one succeeds (-1: all of them).
type fakeJetStream struct {
	mutex        sync.Mutex
	published    []*nats.Msg
	stalls       int
	asyncError   error
	syncFailures int
}

func (jetStream *fakeJetStream) PublishMsgAsync(msg *nats.Msg, _ ...jetstream.PublishOpt) (jetstream.PubAckFuture, error) {
	jetStream.mutex.Lock()
	defer jetStream.mutex.Unlock()

	if jetStream.stalls > 0 {
		jetStream.stalls--
		return nil, jetstream.ErrTooManyStalledMsgs
	}

	jetStream.published = append(jetStream.published, msg)
	return newFakeFuture(msg, jetStream.asyncError), nil
}

func (jetStream *fakeJetStream) PublishMsg(_ context.Context, msg *nats.Msg, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	jetStream.mutex.Lock()
	defer jetStream.mutex.Unlock()

	jetStream.published = append(jetStream.published, msg)
	if jetStream.syncFailures == 0 {
		return &jetstream.PubAck{}, nil
	}
	if jetStream.syncFailures > 0 {
		jetStream.syncFailures--
	}
	return nil, errors.New("nats unavailable")
}

// publishAll sends messages through a publisher with prefix "mqtt" backed by jetStream, drains
// it as shutdown does, and waits until both of its goroutines have finished.
func publishAll(t *testing.T, jetStream *fakeJetStream, duplicateWindow time.Duration, messages ...message) *publisher {
	t.Helper()

	return publishAllWith(t, jetStream, "mqtt", "", duplicateWindow, messages...)
}

// publishAllWith is publishAll with the publisher's prefix and fixed subject given.
func publishAllWith(t *testing.T, jetStream *fakeJetStream, prefix, fixedSubject string, duplicateWindow time.Duration, messages ...message) *publisher {
	t.Helper()

	messageQueue := newQueue(len(messages), false)
	for _, queued := range messages {
		messageQueue.add(queued)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	routePublisher := newPublisher("telemetry", prefix, fixedSubject, jetStream, messageQueue, duplicateWindow, logger)

	// A closed drain makes run publish what is queued and then return, as on shutdown.
	drain := make(chan struct{})
	close(drain)

	pending := make(chan pendingPublish, 10)
	go routePublisher.run(context.Background(), drain, pending)
	routePublisher.confirm(context.Background(), pending)

	return routePublisher
}

func TestPublisher_FixedSubject(t *testing.T) {
	jetStream := &fakeJetStream{}
	publishAllWith(t, jetStream, "", "TELEMETRY", time.Minute,
		message{topic: "telemetry/device42", payload: []byte("one")},
		message{topic: "telemetry/device 43", payload: []byte("two")},
	)

	for _, published := range jetStream.published {
		if published.Subject != "TELEMETRY" {
			t.Errorf("subject = %q, want TELEMETRY", published.Subject)
		}
	}
}

func TestPublisher_Stored(t *testing.T) {
	jetStream := &fakeJetStream{}
	routePublisher := publishAll(t, jetStream, time.Minute,
		message{topic: "telemetry/device 42", payload: []byte("one")},
		message{topic: "telemetry/device43", payload: []byte("two")},
	)

	if routePublisher.stored.Load() != 2 || routePublisher.retried.Load() != 0 || routePublisher.failed.Load() != 0 {
		t.Errorf("stored %d, retried %d, failed %d; want 2, 0, 0",
			routePublisher.stored.Load(), routePublisher.retried.Load(), routePublisher.failed.Load())
	}

	first, second := jetStream.published[0], jetStream.published[1]
	if first.Subject != "mqtt.telemetry.device/2042" || string(first.Data) != "one" {
		t.Errorf("first message = %q %q, want mqtt.telemetry.device/2042 one", first.Subject, first.Data)
	}

	firstID, secondID := first.Header.Get(jetstream.MsgIDHeader), second.Header.Get(jetstream.MsgIDHeader)
	if firstID == "" || firstID == secondID {
		t.Errorf("message IDs %q and %q, want two different, non-empty IDs", firstID, secondID)
	}
}

func TestPublisher_RetriesWithTheSameMessageID(t *testing.T) {
	jetStream := &fakeJetStream{asyncError: errors.New("timeout"), syncFailures: 1}
	routePublisher := publishAll(t, jetStream, time.Minute, message{topic: "telemetry/device42"})

	if routePublisher.stored.Load() != 1 || routePublisher.retried.Load() != 2 || routePublisher.failed.Load() != 0 {
		t.Errorf("stored %d, retried %d, failed %d; want 1, 2, 0",
			routePublisher.stored.Load(), routePublisher.retried.Load(), routePublisher.failed.Load())
	}

	firstID := jetStream.published[0].Header.Get(jetstream.MsgIDHeader)
	for _, attempt := range jetStream.published[1:] {
		if id := attempt.Header.Get(jetstream.MsgIDHeader); id != firstID {
			t.Errorf("retry used message ID %q, want %q", id, firstID)
		}
	}
}

func TestPublisher_GivesUpAfterTheDuplicateWindow(t *testing.T) {
	jetStream := &fakeJetStream{asyncError: errors.New("timeout"), syncFailures: -1}
	routePublisher := publishAll(t, jetStream, 600*time.Millisecond, message{topic: "telemetry/device42"})

	if routePublisher.stored.Load() != 0 || routePublisher.failed.Load() != 1 || routePublisher.retried.Load() == 0 {
		t.Errorf("stored %d, retried %d, failed %d; want 0, at least 1, 1",
			routePublisher.stored.Load(), routePublisher.retried.Load(), routePublisher.failed.Load())
	}
}

func TestPublisher_StalledIsNotARetry(t *testing.T) {
	jetStream := &fakeJetStream{stalls: 3}
	routePublisher := publishAll(t, jetStream, time.Minute, message{topic: "telemetry/device42"})

	if routePublisher.stored.Load() != 1 || routePublisher.retried.Load() != 0 || routePublisher.failed.Load() != 0 {
		t.Errorf("stored %d, retried %d, failed %d; want 1, 0, 0",
			routePublisher.stored.Load(), routePublisher.retried.Load(), routePublisher.failed.Load())
	}
}
