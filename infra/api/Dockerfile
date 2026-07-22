# syntax=docker/dockerfile:1.7
FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY pkg ./pkg
COPY docs ./docs

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -o api ./cmd/api

FROM python:3.12-slim AS jupyterlite-builder

WORKDIR /lite

COPY jupyterlite-content/files /lite/files

RUN --mount=type=cache,target=/root/.cache/pip \
    python -m pip install \
    jupyterlite-core==0.7.5 \
    jupyterlite-pyodide-kernel==0.7.2 \
    jupyter-server==2.18.2

RUN jupyter lite build \
    --contents=/lite/files \
    --output-dir=/app/jupyterlite \
    --pyodide=https://github.com/pyodide/pyodide/releases/download/0.29.0/pyodide-0.29.0.tar.bz2

FROM alpine:3.18

WORKDIR /app

RUN apk --no-cache add ca-certificates

COPY --from=builder /app/api .

COPY docs /app/docs
COPY --from=jupyterlite-builder /app/jupyterlite /app/jupyterlite

EXPOSE 3000

CMD ["./api"]
