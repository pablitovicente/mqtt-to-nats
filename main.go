package main

import (
	"flag"
	"fmt"
	"math/rand"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go"
	MQTTClient "github.com/pablitovicente/mqtt-load-generator/pkg/MQTTClient"
)

func main() {
	// Argument parsing
	targetTopic := flag.String("t", "/load", "Target MQTT topic to publish messages to")
	username := flag.String("u", "", "MQTT username")
	password := flag.String("P", "", "MQTT password")
	host := flag.String("h", "localhost", "MQTT host")
	port := flag.Int("p", 1883, "MQTT port")
	qos := flag.Int("q", 1, "MQTT QoS used by all clients")
	cert := flag.String("cert", "", "Path to TLS certificate file")
	ca := flag.String("ca", "", "Path to TLS CA file")
	key := flag.String("key", "", "Path to TLS key file")
	insecure := flag.Bool("insecure", false, "Set to true to allow self signed certificates")
	mqtts := flag.Bool("mqtts", false, "Set to true to use MQTTS")
	cleanSession := flag.Bool("cleanSession", true, "Set to true for clean MQTT sessions or false to keep session")
	clientID := flag.String("clientID", "mqtt-to-nats-bridge", "Custom MQTT clientID")
	keepAliveTimeout := flag.Int64("keepAliveTimeout", 5000, "Set the amount of time (in seconds) that the client should wait before sending a PING request to the broker")
	natsURL := flag.String("N", "nats://localhost:4222", "NATS Stream server url for example nats://localhost:4222")
	natsStreamName := flag.String("SN", "collector", "NATS Stream name used to store MQTT forwarded messages")
	natsStreamReplicas := flag.Int("R", 1, "Number of NATS Stream replicas")
	natsStreamStorage := flag.String("S", "file", "The storage used for the stream it can be either 'memory' or 'file'")
	maxInflightMessages := flag.Int("bufferSize", 1024, "The size of the buffer the NATS client will use before blocking")

	flag.Parse()

	var storageType nats.StorageType
	if *natsStreamStorage == "file" {
		storageType = nats.FileStorage
	} else if *natsStreamStorage == "memory" {
		storageType = nats.MemoryStorage
	} else {
		panic("'S' parameter needs to be either 'file' or 'memory'")
	}

	if *qos < 0 || *qos > 2 {
		panic("QoS should be any of [0, 1, 2]")
	}

	// General Client Config
	mqttClientConfig := MQTTClient.Config{
		TargetTopic:      targetTopic,
		Username:         username,
		Password:         password,
		Host:             host,
		Port:             port,
		QoS:              qos,
		CleanSession:     cleanSession,
		ClientID:         clientID,
		KeepAliveTimeout: keepAliveTimeout,
		Insecure:         insecure,
		MQTTS:            mqtts,
	}

	if TLSOptionsSet() {
		mqttClientConfig.TLSConfigured = true
		mqttClientConfig.CA = ca
		mqttClientConfig.Cert = cert
		mqttClientConfig.Key = key
	}

	rand.Seed(time.Now().UnixNano())
	updates := make(chan int)

	mqttClient := MQTTClient.Client{
		Config:  mqttClientConfig,
		Updates: updates,
	}

	mqttClient.Connect()

	nc, _ := nats.Connect(*natsURL)
	js, _ := nc.JetStream(nats.PublishAsyncMaxPending(*maxInflightMessages))

	_, err := js.AddStream(&nats.StreamConfig{
		Name:     *natsStreamName,
		Subjects: []string{*natsStreamName},
		Replicas: *natsStreamReplicas,
		Storage:  storageType,
	})

	if err != nil {
		fmt.Println("Error creating NATS Stream:", err)
	}

	mqttClient.Connection.Subscribe(*targetTopic, byte(*qos), func(c mqtt.Client, m mqtt.Message) {
		_, err := js.PublishAsync(*natsStreamName, m.Payload())
		if err != nil {
			fmt.Println("Nats publish error:", err)
		}
	})

	select {}
}

func TLSOptionsSet() bool {
	foundCert := false
	foundCA := false
	foundKey := false

	flag.Visit(func(f *flag.Flag) {
		if f.Name == "cert" {
			foundCert = true
		}

		if f.Name == "ca" {
			foundCA = true
		}

		if f.Name == "key" {
			foundKey = true
		}
	})

	return foundCA && foundCert && foundKey
}
