// Package bridge moves messages from MQTT to NATS JetStream: one MQTT client per route, each
// feeding a memory queue that a NATS publisher empties into the route's stream.
package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pablitovicente/mqtt-to-nats/v2/internal/broker"
)

// Route is one MQTT topic filter and the stream its messages go to.
type Route struct {
	Name   string
	Filter string
	Stream string
	Prefix string
	QoS    int
}

// Config is everything Run needs. The cli package has already checked it.
type Config struct {
	NATSURL string

	// ClientID is the base for MQTT client IDs and NATS connection names:
	// <ClientID>-<route name>.
	ClientID string

	// MQTT is how every route's client connects. Run sets Ordered and OnMessage itself.
	MQTT broker.Options

	// QueueSize and DropOldest set up each route's memory queue.
	QueueSize  int
	DropOldest bool

	// MaxPending is how many publishes each route may have waiting for NATS to confirm.
	MaxPending int

	// StatsInterval is how often every route's counters are logged.
	StatsInterval time.Duration

	// ShutdownTimeout is how long stopping may take to store what is still queued.
	ShutdownTimeout time.Duration

	Streams StreamSettings
	Routes  []Route
}

// Run creates the streams, starts every route, and runs until ctx is cancelled.
func Run(ctx context.Context, config Config, logger *slog.Logger) error {
	if err := setUpStreams(ctx, config, logger); err != nil {
		return err
	}

	routes := make([]*runningRoute, 0, len(config.Routes))

	for _, route := range config.Routes {
		started, err := startRoute(ctx, route, config, logger)
		if err != nil {
			stopRoutes(routes, config.ShutdownTimeout)
			return err
		}
		routes = append(routes, started)
	}

	go reportStats(ctx, routes, config.StatsInterval, logger)

	<-ctx.Done()
	logger.Info("stopping: storing what is still queued", "timeout", config.ShutdownTimeout.String())
	stopRoutes(routes, config.ShutdownTimeout)
	logSummary(routes, logger)

	return nil
}

// stopRoutes stops every route at the same time, so they share one timeout instead of each
// getting its own in turn.
func stopRoutes(routes []*runningRoute, timeout time.Duration) {
	shutdownContext, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var stopping sync.WaitGroup
	for _, route := range routes {
		stopping.Go(func() {
			route.stop(shutdownContext)
		})
	}
	stopping.Wait()
}

// setUpStreams creates or updates the streams over a connection of its own, closed when done.
func setUpStreams(ctx context.Context, config Config, logger *slog.Logger) error {
	connection, err := nats.Connect(config.NATSURL, nats.Name(config.ClientID+"-setup"))
	if err != nil {
		return fmt.Errorf("connecting to NATS at %s: %w", config.NATSURL, err)
	}
	defer connection.Close()

	jetStream, err := jetstream.New(connection)
	if err != nil {
		return fmt.Errorf("opening JetStream: %w", err)
	}

	return createStreams(ctx, jetStream, config.Routes, config.Streams, logger)
}
