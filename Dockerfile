# syntax=docker/dockerfile:1
FROM golang:1.19.11-alpine3.18 AS builder

WORKDIR /

COPY . .

RUN go mod download

RUN CGO_ENABLED=0 go build -o mqtt-to-nats .

FROM alpine:3

COPY --from=builder /mqtt-to-nats /mqtt-to-nats
COPY configs /configs

WORKDIR /app
ENTRYPOINT [ "/mqtt-to-nats" ]
