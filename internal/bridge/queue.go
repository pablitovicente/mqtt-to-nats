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
// add may be called from several goroutines at once: by default (without --ordered) paho runs the
// message handler on a new goroutine for every message.
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

	// Take out the oldest message to make room, then try again. Another add running at the
	// same time can fill the freed slot first, so this repeats until the message fits. Each
	// round drops one old message to make room for one new one, so the count stays right.
	// When the publisher takes a message out in the meantime, there is room without dropping.
	for {
		select {
		case <-q.messages:
			q.dropped.Add(1)
		default:
		}

		select {
		case q.messages <- received:
			return
		default:
		}
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
