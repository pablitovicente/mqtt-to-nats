package bridge

import "sync/atomic"

// message is one MQTT message waiting to be published to NATS.
type message struct {
	topic   string
	payload []byte
}

// queue holds messages between one route's MQTT client and its NATS publisher. It never
// blocks the MQTT client: when the queue is full, a message is dropped and counted instead.
//
// It relies on having a single writer. The MQTT client calls its message handler for one
// message at a time (paho's ordered mode), so add is never called concurrently.
type queue struct {
	messages   chan message
	dropOldest bool

	received atomic.Uint64
	dropped  atomic.Uint64
}

// newQueue makes a queue holding up to size messages. When full, it drops the message just
// received, or with dropOldest the message that has waited longest.
func newQueue(size int, dropOldest bool) *queue {
	return &queue{
		messages:   make(chan message, size),
		dropOldest: dropOldest,
	}
}

// add puts a message in the queue without ever waiting.
func (q *queue) add(received message) {
	q.received.Add(1)

	select {
	case q.messages <- received:
		return
	default:
	}

	if !q.dropOldest {
		q.dropped.Add(1)
		return
	}

	// Take out the oldest message to make room. The publisher may have taken one out in the
	// meantime, in which case there is room already and nothing is dropped.
	select {
	case <-q.messages:
		q.dropped.Add(1)
	default:
	}

	// Only add writes to the queue, so there is room now. The default case can't happen; it
	// is there so a mistake shows up as a counted drop instead of a stuck MQTT client.
	select {
	case q.messages <- received:
	default:
		q.dropped.Add(1)
	}
}

// receive returns the channel the publisher reads messages from.
func (q *queue) receive() <-chan message {
	return q.messages
}

// length returns how many messages are waiting.
func (q *queue) length() int {
	return len(q.messages)
}

// capacity returns how many messages the queue can hold.
func (q *queue) capacity() int {
	return cap(q.messages)
}
