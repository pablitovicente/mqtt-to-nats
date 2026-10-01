package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pablitovicente/mqtt-to-nats/v2/internal/broker"
)

const (
	// subscribeTimeout is how long to wait for the broker to accept a subscription.
	subscribeTimeout = 30 * time.Second

	// mqttDisconnectWait is how long the MQTT client may take to send what it still has
	// queued when disconnecting.
	mqttDisconnectWait = 250 * time.Millisecond

	// lateHandlerWait is how long to wait after disconnecting from MQTT before storing what is
	// left in the queue. Without --ordered paho runs each message's handler on its own
	// goroutine and doesn't wait for them when disconnecting, so a message can still be on its
	// way into the queue. Without this wait it could land there after the last drain and never
	// be stored.
	lateHandlerWait = 200 * time.Millisecond
)

// runningRoute is one route's MQTT client, queue, NATS connection and publisher.
type runningRoute struct {
	name           string
	logger         *slog.Logger
	mqttClient     *broker.Client
	natsConnection *nats.Conn
	queue          *queue
	publisher      *publisher
	control        publisherControl
}

// publisherControl stops a running publisher: close drain to have it store what is left in the
// queue and finish, or call cancel to stop it right away. done is closed once it has finished.
type publisherControl struct {
	drain  chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

// startRoute starts one route in three steps: connect to NATS, start the publisher, then
// connect to MQTT. The publisher starts before MQTT because, with a persistent session, the
// broker delivers queued messages as soon as the MQTT client connects.
func startRoute(ctx context.Context, route Route, config Config, logger *slog.Logger) (*runningRoute, error) {
	clientID := config.ClientID + "-" + route.Name
	routeLogger := logger.With("route", route.Name)

	natsConnection, jetStream, err := connectToNATS(config, clientID, routeLogger)
	if err != nil {
		return nil, fmt.Errorf("route %q: %w", route.Name, err)
	}

	messageQueue, routePublisher, control := startPublisher(route, config, jetStream, routeLogger)

	mqttClient, err := connectToMQTT(ctx, route, config.MQTT, clientID, messageQueue, routeLogger)
	if err != nil {
		control.cancel()
		<-control.done
		natsConnection.Close()
		return nil, fmt.Errorf("route %q: %w", route.Name, err)
	}

	routeLogger.Info("route started", "filter", route.Filter, "stream", route.Stream, "clientID", clientID, "qos", route.QoS, "ordered", config.MQTT.Ordered, "messageIDs", config.MessageIDs)

	return &runningRoute{
		name:           route.Name,
		logger:         routeLogger,
		mqttClient:     mqttClient,
		natsConnection: natsConnection,
		queue:          messageQueue,
		publisher:      routePublisher,
		control:        control,
	}, nil
}

// connectToNATS opens the route's own NATS connection, named after its client ID so it can be
// found in the server's connection list. It reconnects forever and logs every disconnect and
// reconnect.
func connectToNATS(config Config, clientID string, logger *slog.Logger) (*nats.Conn, jetstream.JetStream, error) {
	natsConnection, err := nats.Connect(config.NATSURL,
		nats.Name(clientID),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			// A nil error is our own Close on shutdown, not a lost connection.
			if err != nil {
				logger.Warn("nats disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(connection *nats.Conn) {
			logger.Info("nats reconnected", "server", connection.ConnectedUrl())
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to NATS at %s: %w", config.NATSURL, err)
	}

	jetStream, err := jetstream.New(natsConnection,
		jetstream.WithPublishAsyncMaxPending(config.MaxPending),
		jetstream.WithPublishAsyncTimeout(publishTimeout),
	)
	if err != nil {
		natsConnection.Close()
		return nil, nil, fmt.Errorf("opening JetStream: %w", err)
	}

	return natsConnection, jetStream, nil
}

// startPublisher makes the route's queue and starts the two publisher goroutines. They have
// their own lifetime, separate from the bridge's context, so on shutdown they can keep storing
// queued messages after everything else has been told to stop.
func startPublisher(route Route, config Config, jetStream jetStreamPublisher, logger *slog.Logger) (*queue, *publisher, publisherControl) {
	messageQueue := newQueue(config.QueueSize, config.DropOldest)
	routePublisher := newPublisher(route.Name, route.Prefix, route.Subject, config.MessageIDs, jetStream, messageQueue, config.Streams.DuplicateWindow, logger)

	publisherContext, cancel := context.WithCancel(context.Background())
	control := publisherControl{
		drain:  make(chan struct{}),
		cancel: cancel,
		done:   make(chan struct{}),
	}

	pending := make(chan pendingPublish, config.MaxPending)
	go routePublisher.run(publisherContext, control.drain, pending)
	go func() {
		routePublisher.confirm(publisherContext, pending)
		close(control.done)
	}()

	return messageQueue, routePublisher, control
}

// connectToMQTT connects the route's MQTT client, delivering every message into messageQueue,
// and subscribes to the route's filter.
func connectToMQTT(ctx context.Context, route Route, options broker.Options, clientID string, messageQueue *queue, logger *slog.Logger) (*broker.Client, error) {
	options.OnMessage = func(topic string, payload []byte) {
		messageQueue.add(message{topic: topic, payload: payload})
	}

	mqttClient, err := broker.Dial(ctx, options, clientID, logger)
	if err != nil {
		return nil, err
	}

	// A nil callback sends the subscription's messages to OnMessage.
	token := mqttClient.Subscribe(route.Filter, byte(route.QoS), nil)
	if !token.WaitTimeout(subscribeTimeout) {
		mqttClient.Disconnect(0)
		return nil, fmt.Errorf("subscribing to %s: timed out", route.Filter)
	}
	if err := token.Error(); err != nil {
		mqttClient.Disconnect(0)
		return nil, fmt.Errorf("subscribing to %s: %w", route.Filter, err)
	}

	return mqttClient, nil
}

// stop shuts the route down without losing queued messages where it can:
//  1. disconnect from MQTT, so no new messages arrive
//  2. wait lateHandlerWait for handlers still running to put their messages in the queue
//  3. let the publisher store what is left in the queue and wait for NATS to confirm it
//  4. close the NATS connection
//
// If shutdownContext ends first, the publisher is stopped right away and whatever it hadn't
// stored yet is lost; the summary's leftInQueue shows how much was still queued.
func (route *runningRoute) stop(shutdownContext context.Context) {
	route.mqttClient.Disconnect(mqttDisconnectWait)

	time.Sleep(lateHandlerWait)

	close(route.control.drain)

	select {
	case <-route.control.done:
	case <-shutdownContext.Done():
		route.logger.Warn("shutdown timed out before every queued message was stored",
			"leftInQueue", route.queue.length())
		route.control.cancel()
		<-route.control.done
	}
	route.control.cancel()

	route.natsConnection.Close()
}
