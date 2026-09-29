package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/pablitovicente/mqtt-to-nats/v2/internal/topics"
)

// StreamSettings are the settings every stream gets.
type StreamSettings struct {
	Replicas int

	// Storage is "file" or "memory".
	Storage string

	// DuplicateWindow is how long the stream remembers message IDs, so a retried publish that
	// NATS already stored isn't stored again.
	DuplicateWindow time.Duration
}

// stream is one NATS stream and the subjects it stores: the stream subjects of every route
// that names it.
type stream struct {
	name     string
	subjects []string
}

// streamCreator is the part of jetstream.JetStream that creates streams. Tests pass a fake.
type streamCreator interface {
	CreateOrUpdateStream(ctx context.Context, config jetstream.StreamConfig) (jetstream.Stream, error)
}

// streamsFromRoutes groups routes by stream name, in the order the streams first appear.
func streamsFromRoutes(routes []Route) []stream {
	streams := make([]stream, 0, len(routes))
	indexByName := map[string]int{}

	for _, route := range routes {
		index, found := indexByName[route.Stream]
		if !found {
			index = len(streams)
			indexByName[route.Stream] = index
			streams = append(streams, stream{name: route.Stream})
		}

		streams[index].subjects = append(streams[index].subjects, topics.StreamSubjects(route.Filter, route.Prefix)...)
	}

	return streams
}

// createStreams creates every stream the routes need, or updates it to the current subjects
// and settings. The bridge owns its streams: settings changed by hand are overwritten.
func createStreams(ctx context.Context, creator streamCreator, routes []Route, settings StreamSettings, logger *slog.Logger) error {
	storage := jetstream.FileStorage
	if settings.Storage == "memory" {
		storage = jetstream.MemoryStorage
	}

	for _, wanted := range streamsFromRoutes(routes) {
		config := jetstream.StreamConfig{
			Name:       wanted.name,
			Subjects:   wanted.subjects,
			Replicas:   settings.Replicas,
			Storage:    storage,
			Duplicates: settings.DuplicateWindow,
		}

		if _, err := creator.CreateOrUpdateStream(ctx, config); err != nil {
			return fmt.Errorf("creating or updating stream %q with subjects %q: %w", wanted.name, wanted.subjects, err)
		}

		logger.Info("stream ready", "stream", wanted.name, "subjects", wanted.subjects,
			"replicas", settings.Replicas, "storage", settings.Storage, "duplicateWindow", settings.DuplicateWindow.String())
	}

	return nil
}
