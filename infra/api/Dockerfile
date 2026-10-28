# syntax=docker/dockerfile:1.7
# Build context is THIS repo's root. go.mod carries
# `replace github.com/datakaveri/dx-common-go => ../dx-common-go`, so the
# library is cloned in its own stage (pinned by DX_COMMON_REF) and placed at
# /dx-common-go — i.e. ../dx-common-go from /app — the same pattern as every
# Go fleet service. Locally, docker compose overrides the `common` stage with
# the workspace checkout (additional_contexts: common: .).
#
#   docker build -f infra/api/Dockerfile .
#   docker build -f infra/api/Dockerfile --build-arg DX_COMMON_REF=<sha-or-branch> .

ARG GO_VERSION=1.26
ARG DX_COMMON_REF=dev

FROM alpine/git AS common
ARG DX_COMMON_REF
RUN git init /dx-common-go \
 && git -C /dx-common-go remote add origin https://github.com/datakaveri/dx-common-go.git \
 && git -C /dx-common-go fetch --depth 1 origin "${DX_COMMON_REF}" \
 && git -C /dx-common-go checkout --detach FETCH_HEAD \
 && rm -rf /dx-common-go/.git

FROM golang:${GO_VERSION}-alpine AS builder

COPY --from=common /dx-common-go /dx-common-go

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
COPY openapi /app/openapi
COPY --from=jupyterlite-builder /app/jupyterlite /app/jupyterlite

EXPOSE 3000

CMD ["./api"]
