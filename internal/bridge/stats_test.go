package bridge

import (
	"testing"
	"time"
)

func TestQueueWarning(t *testing.T) {
	tests := []struct {
		name     string
		previous int
		current  int
		want     bool
	}{
		{name: "empty", previous: 0, current: 0, want: false},
		{name: "70% full", previous: 700, current: 700, want: true},
		{name: "half full and shrinking", previous: 600, current: 500, want: false},
		{name: "half full, growing slowly", previous: 490, current: 500, want: false},
		{name: "half full, full within 30s", previous: 300, current: 500, want: true},
	}

	for _, test := range tests {
		previous := routeCounts{queueLength: test.previous, queueCapacity: 1000}
		current := routeCounts{queueLength: test.current, queueCapacity: 1000}

		if _, got := queueWarning(current, previous, 10*time.Second); got != test.want {
			t.Errorf("%s: queueWarning = %v, want %v", test.name, got, test.want)
		}
	}
}
