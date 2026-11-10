FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o platform-token-sidecar ./cmd/platform-token-sidecar

FROM alpine:3.22

WORKDIR /app

RUN apk --no-cache add ca-certificates

COPY --from=builder /app/platform-token-sidecar .

EXPOSE 8081

CMD ["./platform-token-sidecar"]
