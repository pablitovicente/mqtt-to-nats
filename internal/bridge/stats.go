package bridge

import (
	"context"
	"log/slog"
	"time"
)

const (
	// queueWarningPercent is how full a queue gets before a warning is logged.
	queueWarningPercent = 70

	// queueFullSoon is how close to full, at the current growth rate, a queue may get before
	// a warning is logged even under queueWarningPercent.
	queueFullSoon = 30 * time.Second
)

// routeCounts is a snapshot of one route's counters.
type routeCounts struct {
	received uint64
	dropped  uint64
	stored   uint64
	retried  uint64
	failed   uint64

	queueLength   int
	queueCapacity int
}

func (route *runningRoute) counts() routeCounts {
	return routeCounts{
		received:      route.queue.received.Load(),
		dropped:       route.queue.dropped.Load(),
		stored:        route.publisher.stored.Load(),
		retried:       route.publisher.retried.Load(),
		failed:        route.publisher.failed.Load(),
		queueLength:   route.queue.length(),
		queueCapacity: route.queue.capacity(),
	}
}

// reportStats logs every route's counters every interval until ctx is cancelled, and warns
// when a route's queue is filling up.
func reportStats(ctx context.Context, routes []*runningRoute, interval time.Duration, logger *slog.Logger) {
	previous := make([]routeCounts, len(routes))
	for index, route := range routes {
		previous[index] = route.counts()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			for index, route := range routes {
				current := route.counts()
				logRouteStats(logger, route.name, current, previous[index], interval)
				previous[index] = current
			}
		}
	}
}

func logRouteStats(logger *slog.Logger, routeName string, current, previous routeCounts, interval time.Duration) {
	seconds := interval.Seconds()

	logger.Info("stats", "route", routeName,
		"received", current.received,
		"stored", current.stored,
		"dropped", current.dropped,
		"retried", current.retried,
		"failed", current.failed,
		"queueLength", current.queueLength,
		"queuePercent", percent(current.queueLength, current.queueCapacity),
		"receivedPerSecond", float64(current.received-previous.received)/seconds,
		"storedPerSecond", float64(current.stored-previous.stored)/seconds,
	)

	if current.dropped > previous.dropped {
		logger.Warn("messages dropped because the queue was full", "route", routeName,
			"droppedThisInterval", current.dropped-previous.dropped)
	}

	if current.failed > previous.failed {
		logger.Warn("messages not stored in NATS after retrying until the duplicate window ran out", "route", routeName,
			"failedThisInterval", current.failed-previous.failed)
	}

	if reason, warn := queueWarning(current, previous, interval); warn {
		logger.Warn("queue filling up, messages may be dropped soon", "route", routeName, "reason", reason,
			"queueLength", current.queueLength, "queueCapacity", current.queueCapacity)
	}
}

// queueWarning reports whether a queue is at least queueWarningPercent full, or growing fast
// enough to be full within queueFullSoon, and says which.
func queueWarning(current, previous routeCounts, interval time.Duration) (string, bool) {
	if percent(current.queueLength, current.queueCapacity) >= queueWarningPercent {
		return "over 70% full", true
	}

	growthPerSecond := float64(current.queueLength-previous.queueLength) / interval.Seconds()
	if growthPerSecond <= 0 {
		return "", false
	}

	secondsUntilFull := float64(current.queueCapacity-current.queueLength) / growthPerSecond
	if secondsUntilFull < queueFullSoon.Seconds() {
		return "growing fast enough to be full within 30s", true
	}

	return "", false
}

func percent(part, whole int) int {
	if whole == 0 {
		return 0
	}
	return part * 100 / whole
}

// logSummary logs every route's final counters.
func logSummary(routes []*runningRoute, logger *slog.Logger) {
	for _, route := range routes {
		final := route.counts()
		logger.Info("summary", "route", route.name,
			"received", final.received,
			"stored", final.stored,
			"dropped", final.dropped,
			"retried", final.retried,
			"failed", final.failed,
			"leftInQueue", final.queueLength,
		)
	}
}
