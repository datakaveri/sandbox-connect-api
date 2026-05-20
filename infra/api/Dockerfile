FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o api ./cmd/api

FROM python:3.12-slim AS jupyterlite-builder

WORKDIR /lite

COPY jupyterlite-content/files /lite/files

RUN python -m pip install --no-cache-dir \
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
