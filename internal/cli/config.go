package cli

import (
	"fmt"
	"regexp"
	"time"

	"github.com/pablitovicente/mqtt-to-nats/v2/internal/bridge"
	"github.com/pablitovicente/mqtt-to-nats/v2/internal/broker"
	"github.com/pablitovicente/mqtt-to-nats/v2/internal/topics"
)

// Connection holds the settings needed to open an MQTT connection. Every route's client
// connects the same way.
type Connection struct {
	Host             string
	Port             int
	Username         string
	Password         string
	CleanSession     bool
	ClientID         string
	KeepAliveTimeout int64
	LogLevel         string
	TLS              TLS
	MQTTS            bool
	Insecure         bool
}

// TLS holds the files for TLS: a CA to check the broker against, and a client certificate
// and key for mutual TLS.
type TLS struct {
	CA   string
	Cert string
	Key  string
}

// NATS holds the NATS connection and stream settings. Every stream gets the same replicas and
// storage.
type NATS struct {
	URL             string
	Replicas        int
	Storage         string
	MaxPending      int
	MessageIDs      bool
	DuplicateWindow time.Duration
}

// Bridge holds the settings for moving messages from MQTT to NATS.
type Bridge struct {
	Ordered         bool
	QueueSize       int
	DropPolicy      string
	StatsInterval   time.Duration
	ShutdownTimeout time.Duration
}

// Route is one MQTT topic filter, read by its own client and stored in a NATS stream.
type Route struct {
	Name    string
	Filter  string
	Stream  string
	Prefix  string
	Subject string
	QoS     int
}

// Settings is everything the bridge needs to run, after flags and the routes file are read
// and checked.
type Settings struct {
	Connection Connection
	NATS       NATS
	Bridge     Bridge
	Routes     []Route
}

// namePattern is what route and stream names may contain. Route names go into MQTT client
// IDs and stream names, and NATS stream names can't contain '.', '*', '>', whitespace or
// path separators.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Validate checks the connection settings and returns an error describing the first
// problem found.
func (connection *Connection) Validate() error {
	if connection.Port < 1 || connection.Port > 65535 {
		return fmt.Errorf("--port must be 1..65535 (got %d)", connection.Port)
	}

	if connection.ClientID == "" {
		return fmt.Errorf("--clientID must not be empty")
	}

	if connection.KeepAliveTimeout < 0 {
		return fmt.Errorf("--keepAliveTimeout must be at least 0 (got %d)", connection.KeepAliveTimeout)
	}

	if _, err := parseLogLevel(connection.LogLevel); err != nil {
		return fmt.Errorf("--log-level must be debug, info, warn or error (got %q)", connection.LogLevel)
	}

	certGiven := connection.TLS.Cert != ""
	keyGiven := connection.TLS.Key != ""
	caGiven := connection.TLS.CA != ""

	// A client certificate is useless without its private key and vice versa.
	if certGiven != keyGiven {
		return fmt.Errorf("--cert and --key must be given together or not at all")
	}

	// TLS is on with --mqtts, or with all three files. --ca alone, or --cert and --key without
	// --ca, only change how a TLS connection is made, so they need --mqtts: turning TLS on
	// silently would be a surprise.
	allThreeFiles := caGiven && certGiven
	if (caGiven || certGiven) && !allThreeFiles && !connection.MQTTS {
		return fmt.Errorf("--ca on its own, or --cert and --key without --ca, need --mqtts")
	}

	// Without TLS there is no certificate to skip checking, so --insecure would do nothing and
	// the connection (password included) would go over plain TCP.
	if connection.Insecure && !connection.MQTTS && !certGiven && !caGiven {
		return fmt.Errorf("--insecure only works with --mqtts or --cert/--ca/--key; without them the connection is plain TCP")
	}

	return nil
}

// Validate checks the NATS settings.
func (nats *NATS) Validate() error {
	if nats.URL == "" {
		return fmt.Errorf("--nats-url must not be empty")
	}

	// NATS allows at most 5 replicas per stream.
	if nats.Replicas < 1 || nats.Replicas > 5 {
		return fmt.Errorf("--replicas must be 1..5 (got %d)", nats.Replicas)
	}

	if nats.Storage != "file" && nats.Storage != "memory" {
		return fmt.Errorf("--storage must be file or memory (got %q)", nats.Storage)
	}

	if nats.MaxPending < 1 {
		return fmt.Errorf("--nats-max-pending must be at least 1 (got %d)", nats.MaxPending)
	}

	if nats.DuplicateWindow < time.Second {
		return fmt.Errorf("--duplicate-window must be at least 1s (got %s)", nats.DuplicateWindow)
	}

	return nil
}

// Validate checks the bridge settings.
func (bridge *Bridge) Validate() error {
	if bridge.QueueSize < 1 {
		return fmt.Errorf("--queue-size must be at least 1 (got %d)", bridge.QueueSize)
	}

	if bridge.DropPolicy != "newest" && bridge.DropPolicy != "oldest" {
		return fmt.Errorf("--drop must be newest or oldest (got %q)", bridge.DropPolicy)
	}

	if bridge.StatsInterval < time.Second {
		return fmt.Errorf("--stats-interval must be at least 1s (got %s)", bridge.StatsInterval)
	}

	if bridge.ShutdownTimeout < time.Second {
		return fmt.Errorf("--shutdown-timeout must be at least 1s (got %s)", bridge.ShutdownTimeout)
	}

	return nil
}

// validateRoutes checks every route on its own, then that no two route names are the same and
// no two filters can match the same topic (each message would be stored twice).
func validateRoutes(routes []Route) error {
	if len(routes) == 0 {
		return fmt.Errorf("no routes: give --topic or a --routes file")
	}

	for index := range routes {
		if err := routes[index].validate(); err != nil {
			return err
		}
	}

	for first := range routes {
		for second := first + 1; second < len(routes); second++ {
			if routes[first].Name == routes[second].Name {
				return fmt.Errorf("two routes are named %q", routes[first].Name)
			}

			if topics.FiltersOverlap(routes[first].Filter, routes[second].Filter) {
				return fmt.Errorf("routes %q (%s) and %q (%s) overlap: some topics match both, so their messages would be stored twice",
					routes[first].Name, routes[first].Filter, routes[second].Name, routes[second].Filter)
			}

			if subject, overlaps := streamSubjectsOverlap(routes[first], routes[second]); overlaps {
				return fmt.Errorf("routes %q and %q both produce NATS subjects matching %q; give one of them a different prefix or subject",
					routes[first].Name, routes[second].Name, subject)
			}
		}
	}

	return nil
}

// streamSubjectsOverlap reports whether two routes' stream subjects overlap, which prefixes and
// fixed subjects can cause even when the MQTT filters don't: a/# with no prefix and x/# with
// prefix "a" both give a.x..., and two routes with the same fixed subject clash. It returns one
// of the overlapping subjects for the error message.
func streamSubjectsOverlap(first, second Route) (string, bool) {
	for _, firstSubject := range bridge.Route(first).StreamSubjects() {
		for _, secondSubject := range bridge.Route(second).StreamSubjects() {
			if topics.SubjectsOverlap(firstSubject, secondSubject) {
				return firstSubject, true
			}
		}
	}

	return "", false
}

func (route *Route) validate() error {
	if !namePattern.MatchString(route.Name) {
		return fmt.Errorf("route name %q may only contain letters, digits, '-' and '_'", route.Name)
	}

	if !namePattern.MatchString(route.Stream) {
		return fmt.Errorf("route %q: stream name %q may only contain letters, digits, '-' and '_'", route.Name, route.Stream)
	}

	if err := topics.ValidateFilter(route.Filter); err != nil {
		return fmt.Errorf("route %q: %w", route.Name, err)
	}

	if route.Prefix != "" {
		if err := topics.ValidatePrefix(route.Prefix); err != nil {
			return fmt.Errorf("route %q: %w", route.Name, err)
		}
	}

	if route.Subject != "" {
		if route.Prefix != "" {
			return fmt.Errorf("route %q: give a prefix or a subject, not both", route.Name)
		}

		if err := topics.ValidateSubject(route.Subject); err != nil {
			return fmt.Errorf("route %q: %w", route.Name, err)
		}
	}

	// A fixed subject doesn't depend on the filter, so a filter like '#' is fine without a prefix.
	if route.Prefix == "" && route.Subject == "" && topics.NeedsPrefix(route.Filter) {
		return fmt.Errorf("route %q: filter %q needs a prefix, because a first level of '+', '#' or one starting "+
			"with '$' would give stream subjects that overlap NATS's own $JS.API subjects", route.Name, route.Filter)
	}

	if route.QoS < 0 || route.QoS > 2 {
		return fmt.Errorf("route %q: QoS must be 0, 1 or 2 (got %d)", route.Name, route.QoS)
	}

	return nil
}

// toBridgeConfig converts the checked settings into bridge.Config. bridge must not import cli
// (cli imports bridge), so this conversion lives here.
func (settings *Settings) toBridgeConfig() bridge.Config {
	routes := make([]bridge.Route, 0, len(settings.Routes))
	for _, route := range settings.Routes {
		routes = append(routes, bridge.Route(route))
	}

	return bridge.Config{
		NATSURL:         settings.NATS.URL,
		ClientID:        settings.Connection.ClientID,
		MQTT:            settings.Connection.toBrokerOptions(settings.Bridge.Ordered),
		QueueSize:       settings.Bridge.QueueSize,
		DropOldest:      settings.Bridge.DropPolicy == "oldest",
		MaxPending:      settings.NATS.MaxPending,
		MessageIDs:      settings.NATS.MessageIDs,
		StatsInterval:   settings.Bridge.StatsInterval,
		ShutdownTimeout: settings.Bridge.ShutdownTimeout,
		Streams: bridge.StreamSettings{
			Replicas:        settings.NATS.Replicas,
			Storage:         settings.NATS.Storage,
			DuplicateWindow: settings.NATS.DuplicateWindow,
		},
		Routes: routes,
	}
}

// toBrokerOptions converts the MQTT connection settings into broker.Options.
func (connection *Connection) toBrokerOptions(ordered bool) broker.Options {
	return broker.Options{
		Ordered:          ordered,
		Host:             connection.Host,
		Port:             connection.Port,
		Username:         connection.Username,
		Password:         connection.Password,
		CleanSession:     connection.CleanSession,
		KeepAliveSeconds: connection.KeepAliveTimeout,
		MQTTS:            connection.MQTTS,
		Insecure:         connection.Insecure,
		TLSCertFile:      connection.TLS.Cert,
		TLSKeyFile:       connection.TLS.Key,
		TLSCAFile:        connection.TLS.CA,
	}
}
