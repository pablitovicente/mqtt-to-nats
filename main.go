package main

import (
	"flag"
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
	natsURL := flag.String("N", "nats://localhost:4222", "NATS Stream server url for example nats://localhost:4222")

	flag.Parse()

	if *qos < 0 || *qos > 2 {
		panic("QoS should be any of [0, 1, 2]")
	}

	// General Client Config
	mqttClientConfig := MQTTClient.Config{
		TargetTopic: targetTopic,
		Username:    username,
		Password:    password,
		Host:        host,
		Port:        port,
		QoS:         qos,
	}

	rand.Seed(time.Now().UnixNano())
	updates := make(chan int)

	mqttClient := MQTTClient.Client{
		ID:      rand.Intn(100000),
		Config:  mqttClientConfig,
		Updates: updates,
	}

	mqttClient.Connect()

	nc, _ := nats.Connect(*natsURL)

	mqttClient.Connection.Subscribe(*targetTopic, byte(*qos), func(c mqtt.Client, m mqtt.Message) {
		nc.Publish("foo", m.Payload())
	})

	select {}
}
