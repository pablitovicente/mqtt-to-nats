package broker

// This is the only file in the program that imports paho.mqtt.golang.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// renewSubscriptionTimeout is how long renewing one subscription after a reconnect may take
// before it is logged as failed.
const renewSubscriptionTimeout = 30 * time.Second

// Client is a connected MQTT client, backed by paho.mqtt.golang.
type Client struct {
	pahoClient mqtt.Client

	// subscriptions lists every Subscribe call, so they can be sent again after a reconnect.
	subscriptionsMutex sync.Mutex
	subscriptions      []subscription

	// firstConnectDone is set by the OnConnect handler on the first connection. paho calls
	// that handler for the first connection too, and there is nothing to renew then.
	firstConnectDone atomic.Bool
}

// subscription is one Subscribe call, kept so it can be repeated after a reconnect.
type subscription struct {
	topic   string
	qos     byte
	handler mqtt.MessageHandler
}

// Dial connects to the broker described by options, waiting for the connection to complete or
// for ctx to be cancelled, whichever comes first. There is no OnConnect callback: Dial waits
// on the connect token itself.
//
// Auto-reconnect stays on, same as v1. logger reports connection loss and reconnect attempts.
//
// After every reconnect the client sends all its subscriptions again, with clean and
// persistent sessions alike. A clean session keeps nothing, and a broker can lose a persistent
// session too: EMQX under load kills a slow subscriber's connection, and its in-memory session
// and subscriptions go with it. paho does not tell us whether the broker kept the session, so
// without this the client could stay connected but receive nothing. The cost when the session
// did survive: the broker replaces each subscription and re-sends retained messages for it.
func Dial(ctx context.Context, options Options, clientID string, logger *slog.Logger) (*Client, error) {
	tlsConfig, err := buildTLSConfig(options)
	if err != nil {
		return nil, err
	}

	client := &Client{}

	clientOptions := mqtt.NewClientOptions()
	clientOptions.AddBroker(brokerURL(options))
	clientOptions.SetClientID(clientID)
	clientOptions.SetUsername(options.Username)
	clientOptions.SetPassword(options.Password)
	clientOptions.SetCleanSession(options.CleanSession)
	clientOptions.SetOrderMatters(options.Ordered)
	clientOptions.SetKeepAlive(time.Duration(options.KeepAliveSeconds) * time.Second)

	if tlsConfig != nil {
		clientOptions.SetTLSConfig(tlsConfig)
	}

	clientOptions.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		logger.Warn("mqtt connection lost", "clientID", clientID, "error", err)
	})
	clientOptions.SetReconnectingHandler(func(_ mqtt.Client, _ *mqtt.ClientOptions) {
		logger.Warn("mqtt reconnecting", "clientID", clientID)
	})

	if options.OnMessage != nil {
		onMessage := options.OnMessage
		clientOptions.SetDefaultPublishHandler(func(_ mqtt.Client, message mqtt.Message) {
			onMessage(message.Topic(), message.Payload())
		})
	}

	clientOptions.SetOnConnectHandler(func(pahoClient mqtt.Client) {
		if client.firstConnectDone.CompareAndSwap(false, true) {
			return
		}

		client.renewSubscriptions(pahoClient, clientID, logger)
	})

	pahoClient := mqtt.NewClient(clientOptions)
	client.pahoClient = pahoClient

	connectToken := pahoClient.Connect()
	select {
	case <-connectToken.Done():
		if err := connectToken.Error(); err != nil {
			// A broker that expects TLS closes a plain connection straight away, which paho
			// reports as a bare EOF. Say what that usually means.
			if tlsConfig == nil && errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("connecting to %s: the broker closed the connection right away (%w). "+
					"If this port expects TLS, add --mqtts (with --ca or --insecure for a self-signed certificate)", brokerURL(options), err)
			}

			return nil, fmt.Errorf("connecting to %s: %w", brokerURL(options), err)
		}
	case <-ctx.Done():
		// The client never finished connecting before ctx was cancelled, so it isn't usable.
		// Disconnect it now instead of leaving it retrying in the background.
		pahoClient.Disconnect(0)
		return nil, ctx.Err()
	}

	return client, nil
}

// Publish sends payload to topic at the given QoS. The returned Token completes once the
// broker has acknowledged the publish (QoS 1 and 2), or once it is written (QoS 0).
func (client *Client) Publish(topic string, qos byte, retained bool, payload []byte) Token {
	return client.pahoClient.Publish(topic, qos, retained, payload)
}

// Subscribe asks the broker to deliver messages published to topic. callback is called once
// per received message, on paho's own goroutines, until Disconnect is called.
//
// A nil callback sends the messages to Options.OnMessage instead.
//
// The subscription is also remembered and sent again after every reconnect (see Dial).
func (client *Client) Subscribe(topic string, qos byte, callback func(topic string, payload []byte)) Token {
	var handler mqtt.MessageHandler
	if callback != nil {
		handler = func(_ mqtt.Client, message mqtt.Message) {
			callback(message.Topic(), message.Payload())
		}
	}

	client.subscriptionsMutex.Lock()
	client.subscriptions = append(client.subscriptions, subscription{topic: topic, qos: qos, handler: handler})
	client.subscriptionsMutex.Unlock()

	return subscribeToken{Token: client.pahoClient.Subscribe(topic, qos, handler), topic: topic}
}

// refusedSubscription is the code a broker answers a SUBSCRIBE with when it refuses it.
const refusedSubscription = 0x80

// subscribeToken is paho's subscribe token, except that Error also reports a subscription the
// broker refused. paho only records the broker's answer in SubscribeToken.Result and leaves
// Error nil, so a refused subscription would otherwise look like a success.
type subscribeToken struct {
	mqtt.Token
	topic string
}

func (token subscribeToken) Error() error {
	if err := token.Token.Error(); err != nil {
		return err
	}

	pahoToken, ok := token.Token.(*mqtt.SubscribeToken)
	if !ok {
		return nil
	}

	if pahoToken.Result()[token.topic] == refusedSubscription {
		return fmt.Errorf("the broker refused the subscription to %s", token.topic)
	}

	return nil
}

// renewSubscriptions sends every remembered subscription again. It runs on paho's OnConnect
// goroutine after a reconnect, so waiting for each subscription here blocks nothing else.
func (client *Client) renewSubscriptions(pahoClient mqtt.Client, clientID string, logger *slog.Logger) {
	client.subscriptionsMutex.Lock()
	subscriptions := append([]subscription(nil), client.subscriptions...)
	client.subscriptionsMutex.Unlock()

	for _, renewed := range subscriptions {
		token := subscribeToken{Token: pahoClient.Subscribe(renewed.topic, renewed.qos, renewed.handler), topic: renewed.topic}

		if !token.WaitTimeout(renewSubscriptionTimeout) {
			logger.Error("mqtt subscription not renewed after reconnect: timed out",
				"clientID", clientID, "topic", renewed.topic)
			continue
		}

		if err := token.Error(); err != nil {
			logger.Error("mqtt subscription not renewed after reconnect",
				"clientID", clientID, "topic", renewed.topic, "error", err)
			continue
		}

		logger.Info("mqtt subscription renewed after reconnect", "clientID", clientID, "topic", renewed.topic)
	}
}

// Disconnect closes the connection. Packets already queued to be sent get up to
// maxWaitForQueuedSends to go out first. It does not wait for acknowledgements of publishes
// already sent; callers that care wait for those tokens before disconnecting.
func (client *Client) Disconnect(maxWaitForQueuedSends time.Duration) {
	// paho calls this wait "quiesce" and takes it in milliseconds.
	client.pahoClient.Disconnect(uint(maxWaitForQueuedSends.Milliseconds()))
}
