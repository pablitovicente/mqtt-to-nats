# MQTT to NATS

Subscribes to MQTT topic filters and stores every message received in NATS JetStream streams.

Each topic filter is a route with its own MQTT client and its own NATS connection, so a busy
part of the topic tree doesn't slow down the others. A broker drops messages for a subscriber
that can't keep up, even at QoS 1 and 2, and splitting the tree into routes spreads that load.
Routes can share one stream or each have their own; one stream tops out at a certain rate, so
separate streams store more per second.

## Requirements

- Go 1.27 or newer
- A NATS server with JetStream

## Build

```bash
make build
```

This builds `bin/mqtt-to-nats`.

| Target | Does |
|---|---|
| `make test` | Runs the tests with the race detector |
| `make lint` | Runs golangci-lint |
| `make nilaway` | Checks for possible nil pointer panics with nilaway |
| `make check` | Runs `lint`, `nilaway` and `test` |
| `make fuzz` | Fuzzes the topic to subject conversion for a minute, on every CPU core |
| `make docker` | Builds the Docker image |

## Run

`-h` is short for `--host`, as in `mosquitto_sub`. Help is `--help` only.

One route, from flags:

```bash
./bin/mqtt-to-nats -h broker -p 1883 -t 'telemetry/#' --stream TELEMETRY -N nats://localhost:4222
```

Several routes, from a YAML file:

```bash
./bin/mqtt-to-nats -h broker --routes routes.yaml
```

```yaml
routes:
  - name: telemetry       # required; letters, digits, '-' and '_'
    filter: telemetry/#
  - name: events
    filter: events/#
    stream: EVENTS        # default: the route name
    prefix: bridge        # default: none
    qos: 0                # default: 1
  - name: alarms
    filter: alarms/+/temperature
    stream: EVENTS        # shares the stream with events
    prefix: bridge
  - name: doors
    filter: doors/#
    subject: DOORS        # every message on this one subject; default: built from the topic
```

`--routes` can't be combined with `--topic`, `--qos`, `--stream`, `--prefix` or `--subject`. Unknown keys
in the file are an error. With flags only, the route's name is the stream name.

The bridge refuses to start when:

- two filters can match the same topic (for example `a/#` and `a/b/#`), since each message
  would be stored twice
- two routes produce overlapping NATS subjects through their prefixes (for example `a/#` with
  no prefix and `x/#` with prefix `a` both give `a.x...`)
- a filter's first level is `+`, `#` or starts with `$` and it has no prefix. NATS refuses a
  stream on subjects like `>` or `*.>`, which overlap its own `$JS.API` subjects, and a topic
  like `$JS/API/...` would be published onto NATS's own API.

## Topics and NATS subjects

Each message is published to a subject built from its topic, with the rules NATS server uses
for its own MQTT support:

| Topic | Subject |
|---|---|
| `telemetry/device42/temperature` | `telemetry.device42.temperature` |
| `/load` | `/.load` |
| `foo//bar` | `foo./.bar` |
| `a.b/c` | `a//b.c` |

NATS rejects topics it can't put in a subject. This tool writes those bytes as `/` and two hex
digits instead:

| Topic | Subject | Why |
|---|---|---|
| `telemetry/device 42/temperature` | `telemetry.device/2042.temperature` | spaces, tabs and newlines aren't allowed in subjects |
| `a/*/b` | `a./2a.b` | a level of just `*` or `>` would be a wildcard |
| `bad\xff` | `bad/ff` | invalid UTF-8, NUL and DEL break NATS's file store |

Consumers filtering on the stream write spaces as `/20`: `telemetry.device/2042.>`.

`topics.SubjectToTopic` in `internal/topics` turns a subject back into the exact topic, after
removing the prefix. A fuzz test checks that every topic comes back unchanged.

With `--prefix bridge`, subjects become `bridge.telemetry.device42.temperature`.

With `--subject TELEMETRY` (or `subject:` in the routes file), every message of the route goes
to the subject `TELEMETRY` and its topic isn't kept, as in 1.x. A subject can't be combined
with a prefix, can't contain wildcards and can't start with `$`. Two routes can't use the same
subject, even when they share a stream. A route with a subject needs no prefix, even for a
filter like `#`.

When testing against NATS's own MQTT support, give every route a prefix. NATS hands the
bridge's publishes back to MQTT subscribers, and without a prefix the bridge receives its own
messages again, in a loop.

## Streams

Each route stores into a stream. Without `stream:`, the stream is named after the route, so
every route gets its own stream. Routes that name the same stream share it: the bridge creates
one stream with the subjects of all of them. The routes file under "Run" creates three streams:

| Stream | Subjects | Routes |
|---|---|---|
| `telemetry` | `telemetry.>`, `telemetry` | telemetry |
| `EVENTS` | `bridge.events.>`, `bridge.events`, `bridge.alarms.*.temperature` | events, alarms |
| `doors` | `DOORS` | doors |

A filter ending in `/#` gets two subjects, because MQTT's `telemetry/#` also matches the topic
`telemetry` itself and NATS's `telemetry.>` doesn't. With one route from flags, `--stream`
names the stream (default `collector`).

In NATS each stream has its own leader and replication, so one stream per route splits the
work between streams. A shared stream keeps related messages in one place. Which nodes the
leaders end up on is NATS's choice; `nats stream report` shows it.

The bridge owns its streams. At startup it creates each one, or updates it to the routes'
subjects and the current `--replicas` and `--duplicate-window`. Don't change them by hand; the
next start overwrites the changes. Use `--stream` or `stream:` to pick a name that doesn't
clash with other streams.

Storage (`--storage file` or `memory`) can't change on an existing stream. NATS refuses the
update and the bridge stops with that error.

Streams have no size or age limits. Size the server for the run.

## Sessions and client IDs

Each route's MQTT client ID is `<clientID>-<route name>`, for example
`mqtt-to-nats-bridge-telemetry`. The NATS connection gets the same name, so it can be found in
`nats server report connections`.

Sessions are persistent by default: the broker keeps the subscription and queues messages
while the bridge is down, and delivers them when it comes back. `--cleanSession` turns this
off.

After every reconnect the bridge subscribes again, with persistent sessions too. A broker can
lose a persistent session: EMQX under load kills a slow subscriber's connection, and its
in-memory session, subscriptions and queued messages go with it. Without subscribing again
the bridge would stay connected and receive nothing. When the session did survive, the broker
replaces the subscription and re-sends retained messages for that filter.

Client IDs don't change between runs. If a route's filter changes but its client ID doesn't,
the old subscription stays in the persistent session and its messages keep arriving. Change
`--clientID`, or run once with `--cleanSession`.

## When messages are lost or stored twice

| Situation | What happens |
|---|---|
| A route's queue is full | The newest message is dropped (`--drop oldest` drops the oldest instead) and counted |
| NATS fails to store a message | It is retried with the same message ID until `--duplicate-window` runs out, then counted as failed |
| Ctrl-C | The bridge stops reading MQTT, stores what is queued, and waits for NATS to confirm it, for up to `--shutdown-timeout` |
| The bridge crashes | Messages in the queue and those waiting for NATS to confirm them are lost |
| The broker redelivers after a reconnect | Messages the broker sent again are stored again |

Messages are confirmed to the broker as soon as they reach the queue, not after NATS stores
them. That keeps the bridge fast, and is why a crash loses what was queued.

Every message carries `Nats-Msg-Id: <route>-<start time>-<counter>`. NATS ignores a retry of a
message it already stored, as long as the retry comes within `--duplicate-window`. A broker
redelivery arrives as a new message with a new ID, so it can't be caught this way.

## Tuning

By default the MQTT library (paho) handles every message on a goroutine of its own, so messages
can reach NATS in a different order than the broker sent them. `--ordered` makes the MQTT library
hand each route's messages to the bridge one at a time, in the order they arrive. In a test on
one machine (8 routes, 1,000,000 messages, NATS's MQTT as the broker), ordering off stored about
19% more messages per second and used 10% less CPU. With either setting every message is counted, stored or dropped the same way.

`--queue-size` (default 100000) is how many messages each route holds between MQTT and NATS.
It absorbs bursts and short NATS slowdowns. Memory is about queue size times message size.

`--nats-max-pending` (default 4000) is how many publishes each route can have waiting for NATS
to confirm. It caps a route at about this number divided by the time NATS takes to confirm:
with 4 ms, 4000 allows about 1,000,000 messages per second, and 1000 about 250,000. Very small
values stall because of a nats.go bug
([nats-io/nats.go#1612](https://github.com/nats-io/nats.go/issues/1612)).

`--duplicate-window` (default 30s) is how long a stream remembers message IDs, and how long a
failed publish is retried. NATS keeps every ID in the window in memory: at 250,000 messages
per second, 30 seconds is 7.5 million IDs.

## Stats

Every `--stats-interval` (default 10s), each route logs a `stats` line: messages received,
stored, dropped, retried and failed, the queue's length and how full it is, and messages
received and stored per second. It warns when messages were dropped or failed in the
interval, and when the queue is over 70% full or would be full within 30 seconds at its
current rate. On exit, each route logs a `summary` line with the totals and what was left in
the queue.

Logs are JSON on stderr.

## Credentials and TLS

The MQTT username, password and the three TLS file paths can be given as flags or as
environment variables. A flag given on the command line wins over its environment variable.

| Flag | Environment variable |
|---|---|
| `-u`, `--username` | `MQTT_USERNAME` |
| `-P`, `--password` | `MQTT_PASSWORD` |
| `--ca` | `MQTT_CA` |
| `--cert` | `MQTT_CERT` |
| `--key` | `MQTT_KEY` |

A password given with `-P` is visible to every user on the machine in `ps` output.
`MQTT_PASSWORD` is not.

TLS is on with `--mqtts`, or when all three of `--ca`, `--cert` and `--key` are given:

| Flags | Broker's certificate checked against | Client certificate |
|---|---|---|
| `--mqtts` | the system's trusted CAs | none |
| `--mqtts --ca ca.pem` | `ca.pem` | none |
| `--mqtts --cert c.pem --key k.pem` | the system's trusted CAs | `c.pem` (mutual TLS) |
| `--ca ca.pem --cert c.pem --key k.pem` | `ca.pem` | `c.pem` (mutual TLS) |

`--insecure` turns off every check of the broker's certificate, including the host name.
Anyone between you and the broker can then pose as the broker and read the password. Use it
only against test brokers.

The NATS connection has no authentication options yet.

## Docker

```bash
docker build -t mqtt-to-nats .
docker run --rm -e MQTT_PASSWORD=secret mqtt-to-nats \
  -h broker -u user -t 'telemetry/#' --stream TELEMETRY -N nats://nats:4222
```

The image runs as a non-root user (uid 65534). To use a routes file, mount it and pass its
path with `--routes`.

## Upgrading from 1.x

2.0.0 changes flags, defaults and how messages are stored. The "2.0.0" section of
`CHANGELOG.md` lists every change.

### Running like 1.x

1.x subscribed to one filter and published every message to one subject, the stream's name.
`--subject` does the same:

```bash
# 1.x
mqtt-to-nats -h broker -t 'telemetry/#' -SN TELEMETRY -N nats://nats:4222 -R 3 -S file -bufferSize 1024

# 2.0.0
mqtt-to-nats -h broker -t 'telemetry/#' --stream TELEMETRY --subject TELEMETRY -N nats://nats:4222 -R 3 -S file --nats-max-pending 1024
```

Consumers that read the stream in 1.x keep working. What still differs:

| | 1.x | 2.0.0 |
|---|---|---|
| Headers | none | `Nats-Msg-Id` |
| Stream's duplicate window | NATS's default (2 minutes) | `--duplicate-window` (30s) |
| MQTT session | clean | persistent; add `--cleanSession` for 1.x behavior |
| MQTT client ID | `mqtt-to-nats-bridge` | `mqtt-to-nats-bridge-TELEMETRY` |
| Order of messages in the stream | as received | can differ from arrival order; `--ordered` keeps it |

Without `--subject`, each message goes to a subject built from its topic
(`telemetry.device42.temperature`), and the stream's subjects are `telemetry.>` and
`telemetry`. A consumer with no filter subject still reads every message; one filtering on
`TELEMETRY` gets nothing. Starting without `--subject` on a stream 1.x created changes its
subjects, and the messages already in it keep the subject `TELEMETRY`.
