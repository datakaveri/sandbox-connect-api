# syntax=docker/dockerfile:1.7
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/output-argo-service ./cmd/output-argo-service
COPY internal ./internal
COPY pkg ./pkg
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/output-argo-service ./cmd/output-argo-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/output-argo-service /output-argo-service
ENTRYPOINT ["/output-argo-service"]
