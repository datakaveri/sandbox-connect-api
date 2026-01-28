FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o bin/secret-refresher ./cmd/cron/secret-refresher

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/bin/secret-refresher .

CMD ["./secret-refresher"]
