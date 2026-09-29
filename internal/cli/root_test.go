package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runCommand runs the root command with args and returns the settings the bridge would have
// been started with, or the error.
func runCommand(t *testing.T, args ...string) (Settings, error) {
	t.Helper()

	var received Settings
	command := newRootCommand(func(_ context.Context, settings Settings) error {
		received = settings
		return nil
	})
	command.SetArgs(args)
	command.SetOut(&strings.Builder{})
	command.SetErr(&strings.Builder{})

	err := command.ExecuteContext(context.Background())
	return received, err
}

func writeRoutesFile(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "routes.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing routes file: %v", err)
	}

	return path
}

func TestRoot_SingleRouteFromFlags(t *testing.T) {
	settings, err := runCommand(t, "-h", "broker", "-t", "telemetry/#", "-q", "0", "--stream", "telemetry", "--prefix", "mqtt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []Route{{Name: "telemetry", Filter: "telemetry/#", Stream: "telemetry", Prefix: "mqtt", QoS: 0}}
	if !slices.Equal(settings.Routes, want) {
		t.Errorf("routes = %+v, want %+v", settings.Routes, want)
	}

	if settings.Connection.Host != "broker" {
		t.Errorf("host = %q, want broker", settings.Connection.Host)
	}
}

func TestRoot_Defaults(t *testing.T) {
	settings, err := runCommand(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []Route{{Name: "collector", Filter: "/load", Stream: "collector", QoS: 1}}
	if !slices.Equal(settings.Routes, want) {
		t.Errorf("routes = %+v, want %+v", settings.Routes, want)
	}

	if settings.Connection.CleanSession {
		t.Error("cleanSession defaults to true, want false")
	}
}

func TestRoot_RoutesFile(t *testing.T) {
	path := writeRoutesFile(t, `
routes:
  - name: telemetry
    filter: telemetry/#
  - name: events
    filter: events/#
    stream: shared
    prefix: mqtt
    qos: 0
`)

	settings, err := runCommand(t, "--routes", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []Route{
		{Name: "telemetry", Filter: "telemetry/#", Stream: "telemetry", QoS: 1},
		{Name: "events", Filter: "events/#", Stream: "shared", Prefix: "mqtt", QoS: 0},
	}
	if !slices.Equal(settings.Routes, want) {
		t.Errorf("routes = %+v, want %+v", settings.Routes, want)
	}
}

func TestRoot_Errors(t *testing.T) {
	tests := []struct {
		name string
		// routesFile, when not nil, is written to a file passed with --routes.
		routesFile  *string
		args        []string
		errorPhrase string
	}{
		{
			name:        "route flag with routes file",
			routesFile:  pointerTo("routes:\n  - name: a\n    filter: a/#\n"),
			args:        []string{"-t", "b/#"},
			errorPhrase: "--topic can't be used with --routes",
		},
		{
			name:        "unknown key in routes file",
			routesFile:  pointerTo("routes:\n  - name: a\n    fliter: a/#\n"),
			errorPhrase: "fliter",
		},
		{
			name:        "empty routes file",
			routesFile:  pointerTo(""),
			errorPhrase: "no routes",
		},
		{
			name:        "overlapping filters",
			routesFile:  pointerTo("routes:\n  - name: a\n    filter: a/#\n  - name: b\n    filter: a/b/#\n"),
			errorPhrase: "overlap",
		},
		{
			name:        "overlapping subjects through a prefix",
			routesFile:  pointerTo("routes:\n  - name: a\n    filter: a/#\n  - name: b\n    filter: x/#\n    prefix: a\n"),
			errorPhrase: "both produce NATS subjects",
		},
		{
			name:        "duplicate names",
			routesFile:  pointerTo("routes:\n  - name: a\n    filter: a/#\n  - name: a\n    filter: b/#\n"),
			errorPhrase: "two routes are named",
		},
		{
			name:        "filter needs prefix",
			args:        []string{"-t", "#"},
			errorPhrase: "needs a prefix",
		},
		{
			name:        "bad stream name",
			args:        []string{"--stream", "a.b"},
			errorPhrase: "may only contain letters",
		},
		{
			name:        "bad QoS",
			args:        []string{"-q", "3"},
			errorPhrase: "QoS",
		},
		{
			name:        "bad storage",
			args:        []string{"-S", "disk"},
			errorPhrase: "--storage",
		},
		{
			name:        "bad drop policy",
			args:        []string{"--drop", "middle"},
			errorPhrase: "--drop",
		},
		{
			name:        "ca without mqtts",
			args:        []string{"--ca", "ca.pem"},
			errorPhrase: "need --mqtts",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := test.args
			if test.routesFile != nil {
				args = append(args, "--routes", writeRoutesFile(t, *test.routesFile))
			}

			_, err := runCommand(t, args...)
			if err == nil || !strings.Contains(err.Error(), test.errorPhrase) {
				t.Errorf("error = %v, want one containing %q", err, test.errorPhrase)
			}
		})
	}
}

func pointerTo(value string) *string {
	return &value
}

func TestRoot_EnvironmentFallback(t *testing.T) {
	t.Setenv("MQTT_USERNAME", "from-env")
	t.Setenv("MQTT_PASSWORD", "secret")

	settings, err := runCommand(t, "-u", "from-flag")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if settings.Connection.Username != "from-flag" {
		t.Errorf("username = %q, want the flag to win", settings.Connection.Username)
	}
	if settings.Connection.Password != "secret" {
		t.Errorf("password = %q, want it from MQTT_PASSWORD", settings.Connection.Password)
	}
}
