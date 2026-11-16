# syntax=docker/dockerfile:1.7
FROM golang:1.24.2-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY internal ./internal
COPY cmd/output-uploader ./cmd/output-uploader
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/output-uploader ./cmd/output-uploader

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/output-uploader /usr/local/bin/output-uploader
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/output-uploader"]
