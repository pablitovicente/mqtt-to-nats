package bridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// fakeStreamCreator records the stream configs it is asked to create, and fails with err
// when set.
type fakeStreamCreator struct {
	configs []jetstream.StreamConfig
	err     error
}

func (creator *fakeStreamCreator) CreateOrUpdateStream(_ context.Context, config jetstream.StreamConfig) (jetstream.Stream, error) {
	creator.configs = append(creator.configs, config)
	return nil, creator.err
}

func TestCreateStreams(t *testing.T) {
	routes := []Route{
		{Name: "telemetry", Filter: "telemetry/#", Stream: "telemetry"},
		{Name: "events", Filter: "events/+/door", Stream: "shared", Prefix: "mqtt"},
		{Name: "alarms", Filter: "alarms/#", Stream: "shared", Prefix: "mqtt"},
		{Name: "doors", Filter: "doors/#", Stream: "shared", Subject: "DOORS"},
	}
	settings := StreamSettings{Replicas: 3, Storage: "memory", DuplicateWindow: 20 * time.Second}

	creator := &fakeStreamCreator{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := createStreams(context.Background(), creator, routes, settings, logger); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(creator.configs) != 2 {
		t.Fatalf("created %d streams, want 2", len(creator.configs))
	}

	wantSubjects := map[string][]string{
		"telemetry": {"telemetry.>", "telemetry"},
		"shared":    {"mqtt.events.*.door", "mqtt.alarms.>", "mqtt.alarms", "DOORS"},
	}
	for _, config := range creator.configs {
		if !slices.Equal(config.Subjects, wantSubjects[config.Name]) {
			t.Errorf("stream %q subjects = %q, want %q", config.Name, config.Subjects, wantSubjects[config.Name])
		}

		if config.Replicas != 3 || config.Storage != jetstream.MemoryStorage || config.Duplicates != 20*time.Second {
			t.Errorf("stream %q settings = replicas %d, storage %v, duplicates %s; want 3, memory, 20s",
				config.Name, config.Replicas, config.Storage, config.Duplicates)
		}
	}
}

func TestCreateStreams_ReturnsError(t *testing.T) {
	creator := &fakeStreamCreator{err: errors.New("boom")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	routes := []Route{{Name: "a", Filter: "a/#", Stream: "a"}}

	err := createStreams(context.Background(), creator, routes, StreamSettings{Replicas: 1, Storage: "file"}, logger)
	if err == nil {
		t.Fatal("expected an error, got none")
	}
}
