# Changelog

## 2.0.0

Rewritten with Cobra, split into packages, and tested. 1.x releases remain available.

### New

- Routes: several topic filters, each with its own MQTT client, NATS connection, memory queue
  and stream. One route comes from flags; several from a YAML file given with `--routes`.
  Routes can share a stream.
- Each message is published to a subject built from its topic, using NATS server's own MQTT
  rules (`telemetry/temperature` becomes `telemetry.temperature`). Bytes NATS doesn't allow in subjects, such as
  spaces, are written as `/` and two hex digits (`/20`). `--prefix` puts a fixed start in
  front of every subject.
- `--subject` (or `subject:` in the routes file) publishes every message of a route to one
  fixed subject, as 1.x did with the stream's name.
- The MQTT library (paho) handles every message on its own goroutine instead of one at a
  time, which is faster. Messages can reach NATS in a different order than they arrived.
  `--ordered` brings back one at a time, in arrival order.
- The bridge refuses to start when two filters overlap, when two routes' subjects overlap, or
  when a filter starting with `+`, `#` or `$` has no prefix.
- Messages wait in a memory queue per route between MQTT and NATS, so a slow NATS doesn't hold
  up the MQTT client. `--queue-size` (default 100000) sets its size, and `--drop` (`newest`
  or `oldest`) what is dropped when it is full.
- Every publish is checked. Failed publishes are retried with the same `Nats-Msg-Id`, so NATS
  doesn't store them twice, until `--duplicate-window` (default 30s) runs out. The flag also
  sets each stream's duplicate window.
- Stats per route every `--stats-interval` (default 10s), warnings when messages are dropped
  or failed or the queue is filling up, and a summary per route on exit.
- Ctrl-C stops reading MQTT and stores what is still queued, for up to `--shutdown-timeout`
  (default 30s).
- `--log-level`: `debug`, `info`, `warn` or `error` (default `info`). Logs are JSON on stderr.
- Environment variables for credentials and TLS files: `MQTT_USERNAME`, `MQTT_PASSWORD`,
  `MQTT_CA`, `MQTT_CERT`, `MQTT_KEY`. A flag given on the command line wins.
- More TLS combinations with `--mqtts`: `--ca` on its own checks the broker's certificate
  against that CA file, and `--cert` with `--key` presents a client certificate while checking
  the broker against the system's trusted CAs. 1.x only supported all three files together.
- Subscriptions are sent again after every reconnect, with clean and persistent sessions. 1.x
  received nothing after a reconnect whenever the broker had no session for it: always with a
  clean session, and with a persistent one when the broker lost it (EMQX does under load).
- A subscription the broker refuses is an error at startup. The MQTT library (paho) reports a
  refusal only in the subscribe result, so it used to look like a success.

### Bugs fixed

- Publish results were never checked, so failed publishes went unnoticed.
- A failed stream creation was printed and ignored, and the bridge kept running.
- The error from opening JetStream was discarded.
- `-keepAliveTimeout` defaulted to 5000 seconds (about 83 minutes). The default is now 5.
- Giving only one or two of `-cert`/`-ca`/`-key` silently connected over plain TCP. It is now
  an error.
- The Docker image ran as root. It now runs as uid 65534.
- With a persistent session, messages the broker delivered before the subscription was made
  went to a handler that waited forever on a channel nobody read, and delivery stopped. The
  message handler is now registered before connecting.

### Breaking changes

- Messages are published to subjects built from their topics. 1.x published every message to
  one subject, the stream's name, so the topic was lost. Consumers need new subject filters.
- Sessions are persistent by default (`--cleanSession` defaults to false). 1.x used clean
  sessions.
- `--clientID` is a base: each route's client ID is `<clientID>-<route name>`, for example
  `mqtt-to-nats-bridge-collector`. A 1.x persistent session under `mqtt-to-nats-bridge` is not
  picked up.
- Renamed flags:

  | 1.x | 2.0.0 |
  |---|---|
  | `-SN` | `--stream` |
  | `-bufferSize` | `--nats-max-pending` (default 4000, was 1024) |

- Long flags need two dashes: `--cert`, `--ca`, `--key`, `--insecure`, `--mqtts`,
  `--cleanSession`, `--clientID`, `--keepAliveTimeout`. Short flags stay: `-t`, `-u`, `-P`,
  `-h`, `-p`, `-q`, `-N`, `-R`, `-S`.
- `-h` means `--host`. Help is `--help` only.
- These are now errors: `--replicas` outside 1 to 5, only one or two of the TLS file flags,
  and `--insecure` without `--mqtts` or the TLS file flags.
- The bridge owns its streams: at startup it updates their subjects, replicas and duplicate
  window, overwriting changes made by hand.
- Logs are JSON on stderr.
- Build with `make build`; `build_all.sh` is removed. The binary is `bin/mqtt-to-nats`.

## 1.0.0

- Forward messages from one MQTT topic to one NATS JetStream stream
- MQTTS and mutual TLS
- Dockerfile
