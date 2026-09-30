package bridge

import (
	"slices"
	"strconv"
	"sync"
	"testing"
)

// fill adds messages with topics "0", "1", ... up to count.
func fill(q *queue, count int) {
	for index := range count {
		q.add(message{topic: strconv.Itoa(index)})
	}
}

// drain takes every waiting message out of the queue and returns their topics.
func drain(q *queue) []string {
	var topics []string
	for q.length() > 0 {
		topics = append(topics, (<-q.receive()).topic)
	}
	return topics
}

func TestQueue_DropNewest(t *testing.T) {
	q := newQueue(3, false)
	fill(q, 5)

	if got, want := drain(q), []string{"0", "1", "2"}; !slices.Equal(got, want) {
		t.Errorf("queue held %q, want %q", got, want)
	}
	if q.received.Load() != 5 || q.dropped.Load() != 2 {
		t.Errorf("received %d, dropped %d; want 5 and 2", q.received.Load(), q.dropped.Load())
	}
}

func TestQueue_DropOldest(t *testing.T) {
	q := newQueue(3, true)
	fill(q, 5)

	if got, want := drain(q), []string{"2", "3", "4"}; !slices.Equal(got, want) {
		t.Errorf("queue held %q, want %q", got, want)
	}
	if q.received.Load() != 5 || q.dropped.Load() != 2 {
		t.Errorf("received %d, dropped %d; want 5 and 2", q.received.Load(), q.dropped.Load())
	}
}

// TestQueue_CountsAddUpWithAReader checks, with a reader running at the same time as the
// writers, that every message is either read or counted as dropped. Several writers is what
// paho does without --ordered.
func TestQueue_CountsAddUpWithAReader(t *testing.T) {
	for _, dropOldest := range []bool{false, true} {
		for _, writerCount := range []int{1, 8} {
			q := newQueue(16, dropOldest)
			const perWriter = 20000
			total := uint64(perWriter * writerCount)

			readCount := make(chan int)
			go func() {
				count := 0
				for range q.receive() {
					count++
				}
				readCount <- count
			}()

			var writers sync.WaitGroup
			for range writerCount {
				writers.Go(func() { fill(q, perWriter) })
			}
			writers.Wait()
			close(q.messages)

			read := <-readCount
			if uint64(read)+q.dropped.Load() != total {
				t.Errorf("dropOldest %v, %d writers: read %d + dropped %d != %d",
					dropOldest, writerCount, read, q.dropped.Load(), total)
			}
		}
	}
}
