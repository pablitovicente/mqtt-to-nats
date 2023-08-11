# MQTT to Nats

This program subscribes to an MQTT topic and republishes all incoming messages to a NATS server

## Requirements

- Go 1.19.1+ (probably works with older versions too!)

## Build

```
./build_all.sh
```
will generate the binary `mqtt-to-nats`

## Run

```
./mqtt-to-nats --help
```
will show all the supported options for the program

## Docker image


### Build

```
docker build -t mqtt-to-nats .
```
will build the `mqtt-to-nats` docker image based on alpine.

### Run

```
docker run --rm mqtt-to-nats -h <mqtt_broker> -t <topic> -N <nats_url> -SN <stream_name>
```
will run a docker container forwarding all messages from an MQTT broker to a NATS stream.


## TODO

- don't rush it
