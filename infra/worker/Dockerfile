# Build context is THIS repo's root. go.mod carries
# `replace github.com/datakaveri/dx-common-go => ../dx-common-go`, so the
# library is cloned in its own stage (pinned by DX_COMMON_REF) and placed at
# /dx-common-go — i.e. ../dx-common-go from /app — the same pattern as every
# Go fleet service. Locally, docker compose overrides the `common` stage with
# the workspace checkout (additional_contexts: common: .).
#
#   docker build -f infra/worker/Dockerfile .
#   docker build -f infra/worker/Dockerfile --build-arg DX_COMMON_REF=<sha-or-branch> .

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

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o worker ./cmd/worker

FROM alpine:3.18

WORKDIR /app

RUN apk --no-cache add ca-certificates

COPY --from=builder /app/worker .

CMD ["./worker"]
