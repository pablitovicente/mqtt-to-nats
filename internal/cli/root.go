package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/pablitovicente/mqtt-to-nats/v2/internal/bridge"
)

// runFunc runs the bridge with checked settings until ctx is cancelled. Tests pass a fake that
// records the settings.
type runFunc func(ctx context.Context, settings Settings) error

// routeFlagNames are the flags that describe the single route built from flags. They can't be
// combined with --routes.
var routeFlagNames = []string{"topic", "qos", "stream", "prefix"}

// NewRootCommand builds the mqtt-to-nats command. It has no subcommands.
func NewRootCommand() *cobra.Command {
	return newRootCommand(runBridge)
}

// runBridge is the real runFunc: it logs to stderr and runs the bridge.
func runBridge(ctx context.Context, settings Settings) error {
	logger := newLogger(settings.Connection.LogLevel, os.Stderr)

	return bridge.Run(ctx, settings.toBridgeConfig(), logger)
}

func newRootCommand(run runFunc) *cobra.Command {
	settings := Settings{}
	singleRoute := Route{}
	var routesFilePath string

	rootCommand := &cobra.Command{
		Use:   "mqtt-to-nats",
		Short: "Forward MQTT messages to NATS JetStream streams",
		Long: "Subscribes to one or more MQTT topic filters, each with its own client, and stores " +
			"every received message in a NATS JetStream stream.\n\n" +
			"One route comes from --topic, --qos, --stream and --prefix. For several, list them in a " +
			"YAML file given with --routes.",
		Args: cobra.NoArgs,

		RunE: func(cmd *cobra.Command, args []string) error {
			if err := applyEnvironmentFallback(cmd); err != nil {
				return err
			}

			routes, err := chooseRoutes(cmd.Flags(), routesFilePath, singleRoute)
			if err != nil {
				return err
			}
			settings.Routes = routes

			if err := validateSettings(&settings); err != nil {
				return err
			}

			return run(cmd.Context(), settings)
		},

		SilenceUsage: true,
	}

	// A "help" flag with no shorthand, so -h stays free for the MQTT host, as in
	// mosquitto_sub and mqtt-load-generator.
	rootCommand.Flags().Bool("help", false, "Show help")

	registerConnectionFlags(rootCommand.Flags(), &settings.Connection)
	registerNATSFlags(rootCommand.Flags(), &settings.NATS)
	registerBridgeFlags(rootCommand.Flags(), &settings.Bridge)
	registerRouteFlags(rootCommand.Flags(), &singleRoute, &routesFilePath)

	return rootCommand
}

// chooseRoutes returns the routes from the --routes file, or the single route from flags.
func chooseRoutes(flags *pflag.FlagSet, routesFilePath string, singleRoute Route) ([]Route, error) {
	if routesFilePath == "" {
		singleRoute.Name = singleRoute.Stream
		return []Route{singleRoute}, nil
	}

	for _, name := range routeFlagNames {
		if flags.Changed(name) {
			return nil, fmt.Errorf("--%s can't be used with --routes; set it in the routes file instead", name)
		}
	}

	return loadRoutesFile(routesFilePath)
}

func validateSettings(settings *Settings) error {
	if err := settings.Connection.Validate(); err != nil {
		return err
	}

	if err := settings.NATS.Validate(); err != nil {
		return err
	}

	if err := settings.Bridge.Validate(); err != nil {
		return err
	}

	return validateRoutes(settings.Routes)
}

func registerConnectionFlags(flags *pflag.FlagSet, connection *Connection) {
	flags.StringVarP(&connection.Host, "host", "h", "localhost", "MQTT host")
	flags.IntVarP(&connection.Port, "port", "p", 1883, "MQTT port")
	flags.StringVarP(&connection.Username, "username", "u", "", "MQTT username (env MQTT_USERNAME)")
	flags.StringVarP(&connection.Password, "password", "P", "", "MQTT password (env MQTT_PASSWORD)")
	flags.StringVar(&connection.TLS.CA, "ca", "", "CA file to check the broker's certificate against, "+
		"instead of the system's trusted CAs (env MQTT_CA)")
	flags.StringVar(&connection.TLS.Cert, "cert", "", "Client certificate file for mutual TLS, with --key (env MQTT_CERT)")
	flags.StringVar(&connection.TLS.Key, "key", "", "Private key file for --cert (env MQTT_KEY)")
	flags.BoolVar(&connection.Insecure, "insecure", false,
		"Skip all checks of the broker's TLS certificate, including the host name. Anyone between "+
			"you and the broker can then pose as the broker and read the password. Only works "+
			"with --mqtts or --cert/--ca/--key")
	flags.BoolVar(&connection.MQTTS, "mqtts", false, "Use MQTTS (TLS)")
	flags.BoolVar(&connection.CleanSession, "cleanSession", false,
		"Use a clean MQTT session. Off by default, so sessions are persistent and the broker "+
			"queues messages while the bridge is disconnected. With a clean session the broker "+
			"keeps nothing")
	flags.StringVar(&connection.ClientID, "clientID", "mqtt-to-nats-bridge",
		"MQTT client ID base; each route's client uses <clientID>-<route name>")
	flags.Int64Var(&connection.KeepAliveTimeout, "keepAliveTimeout", 5, "Seconds to wait before sending a PING request to the broker")
	flags.StringVar(&connection.LogLevel, "log-level", "info", "Log level (debug, info, warn or error)")
}

func registerNATSFlags(flags *pflag.FlagSet, nats *NATS) {
	flags.StringVarP(&nats.URL, "nats-url", "N", "nats://localhost:4222", "NATS server URL")
	flags.IntVarP(&nats.Replicas, "replicas", "R", 1, "Replicas for every stream (1..5)")
	flags.StringVarP(&nats.Storage, "storage", "S", "file", "Storage for every stream: file or memory")
	// 4000 is nats.go's own default. Very small values stall badly because of a nats.go bug:
	// a publish that has to wait for room counts itself as pending, so it is released one
	// confirmation late, and with a limit of 1 only when the 200ms stall wait runs out.
	// https://github.com/nats-io/nats.go/issues/1612 (fix proposed in PR #2153, not merged
	// as of nats.go v1.54.0).
	flags.IntVar(&nats.MaxPending, "nats-max-pending", 4000,
		"Publishes each client may have waiting for NATS to confirm before it waits")
	flags.DurationVar(&nats.DuplicateWindow, "duplicate-window", 30*time.Second,
		"How long every stream remembers message IDs, so a retried publish isn't stored twice. "+
			"NATS keeps every ID in this window in memory; retries stop when it runs out")
}

func registerBridgeFlags(flags *pflag.FlagSet, bridge *Bridge) {
	flags.IntVar(&bridge.QueueSize, "queue-size", 100000,
		"Messages each client can hold in memory between MQTT and NATS")
	flags.StringVar(&bridge.DropPolicy, "drop", "newest",
		"What to drop when a client's queue is full: newest (the message just received) or oldest")
	flags.DurationVar(&bridge.StatsInterval, "stats-interval", 10*time.Second, "How often to log stats for each client")
	flags.DurationVar(&bridge.ShutdownTimeout, "shutdown-timeout", 30*time.Second,
		"On Ctrl-C, how long to keep storing messages still in the queues before giving up on them")
}

func registerRouteFlags(flags *pflag.FlagSet, route *Route, routesFilePath *string) {
	flags.StringVarP(&route.Filter, "topic", "t", "/load", "MQTT topic filter to subscribe to")
	flags.IntVarP(&route.QoS, "qos", "q", 1, "MQTT QoS for the subscription (0, 1 or 2)")
	flags.StringVar(&route.Stream, "stream", "collector", "NATS stream name; also the route name in client IDs and stats")
	flags.StringVar(&route.Prefix, "prefix", "", "Put this in front of every NATS subject, followed by '.'")
	flags.StringVar(routesFilePath, "routes", "", "YAML file listing several routes (instead of --topic, --qos, --stream, --prefix)")
}
