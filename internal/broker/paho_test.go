package broker

import (
	"context"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.mqtt.golang/packets"
)

// TestDial_PlainConnectionClosedRightAwayHintsAtTLS checks the error when a broker closes a
// plain connection straight away, which is what a TLS-only port does. paho reports a bare EOF;
// the error should point at --mqtts.
func TestDial_PlainConnectionClosedRightAwayHintsAtTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	// Accept each connection, read the CONNECT packet and close without a word, like a TLS port
	// seeing plain MQTT. Reading first matters: closing with unread data makes the kernel send a
	// reset instead of a normal close, and paho then reports "connection reset" instead of EOF.
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}

			buffer := make([]byte, 1024)
			_, _ = connection.Read(buffer)

			_ = connection.Close()
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err = Dial(context.Background(), Options{Host: "127.0.0.1", Port: port, CleanSession: true, KeepAliveSeconds: 5}, "test-client", logger)
	if err == nil {
		t.Fatal("expected an error, got none")
	}
	if !strings.Contains(err.Error(), "--mqtts") {
		t.Errorf("expected the error to suggest --mqtts, got: %v", err)
	}
}

// subscribeSeen is one SUBSCRIBE packet received by fakeBroker: which connection it came on
// (1 for the first, 2 after the first reconnect, and so on) and the topics in it.
type subscribeSeen struct {
	connectionNumber int
	topics           []string
}

// fakeBroker answers just enough MQTT for a paho client to connect and subscribe. It reports
// every SUBSCRIBE on subscribes. On the first connection it closes the connection right after
// the first SUBSCRIBE, so the client has to reconnect.
func fakeBroker(t *testing.T, listener net.Listener, subscribes chan<- subscribeSeen) {
	t.Helper()

	connectionNumber := 0
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		connectionNumber++

		go serveFakeBrokerConnection(connection, connectionNumber, subscribes)
	}
}

func serveFakeBrokerConnection(connection net.Conn, connectionNumber int, subscribes chan<- subscribeSeen) {
	defer func() { _ = connection.Close() }()

	for {
		packet, err := packets.ReadPacket(connection)
		if err != nil {
			return
		}

		switch received := packet.(type) {
		case *packets.ConnectPacket:
			connack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
			if err := connack.Write(connection); err != nil {
				return
			}

		case *packets.SubscribePacket:
			suback := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
			suback.MessageID = received.MessageID
			suback.ReturnCodes = received.Qoss

			// Refuse one topic, the way a broker answers a subscription it won't accept.
			for index, topic := range received.Topics {
				if topic == "refused/#" {
					suback.ReturnCodes[index] = refusedSubscription
				}
			}

			if err := suback.Write(connection); err != nil {
				return
			}

			subscribes <- subscribeSeen{connectionNumber: connectionNumber, topics: received.Topics}

			if connectionNumber == 1 {
				return
			}

		case *packets.PingreqPacket:
			pingresp := packets.NewControlPacket(packets.Pingresp)
			if err := pingresp.Write(connection); err != nil {
				return
			}
		}
	}
}

// TestSubscribe_RenewedAfterReconnect checks that a subscription is sent again on the new
// connection after the broker drops the old one, and that the first connection gets it only
// once. It uses a persistent session, the case where a broker that lost the session would
// otherwise leave the client subscribed to nothing.
func TestSubscribe_RenewedAfterReconnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	subscribes := make(chan subscribeSeen, 10)
	go fakeBroker(t, listener, subscribes)

	port := listener.Addr().(*net.TCPAddr).Port
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	client, err := Dial(context.Background(), Options{Host: "127.0.0.1", Port: port, KeepAliveSeconds: 5}, "test-client", logger)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Disconnect(0)

	token := client.Subscribe("telemetry/#", 1, func(string, []byte) {})
	if !token.WaitTimeout(5 * time.Second) {
		t.Fatal("subscribe timed out")
	}
	if err := token.Error(); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	want := []subscribeSeen{
		{connectionNumber: 1, topics: []string{"telemetry/#"}},
		{connectionNumber: 2, topics: []string{"telemetry/#"}},
	}
	for _, expected := range want {
		select {
		case got := <-subscribes:
			if got.connectionNumber != expected.connectionNumber || !slices.Equal(got.topics, expected.topics) {
				t.Fatalf("got subscribe %+v, want %+v", got, expected)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no subscribe seen, want %+v", expected)
		}
	}

	select {
	case extra := <-subscribes:
		t.Fatalf("unexpected extra subscribe %+v", extra)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestSubscribe_RefusedIsAnError checks that a subscription the broker refuses (SUBACK code
// 0x80) comes back as an error. paho itself reports it only in SubscribeToken.Result.
func TestSubscribe_RefusedIsAnError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	subscribes := make(chan subscribeSeen, 10)
	go fakeBroker(t, listener, subscribes)

	port := listener.Addr().(*net.TCPAddr).Port
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	client, err := Dial(context.Background(), Options{Host: "127.0.0.1", Port: port, KeepAliveSeconds: 5}, "test-client", logger)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Disconnect(0)

	token := client.Subscribe("refused/#", 1, nil)
	if !token.WaitTimeout(5 * time.Second) {
		t.Fatal("subscribe timed out")
	}

	if err := token.Error(); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("error = %v, want one saying the broker refused the subscription", err)
	}
}
