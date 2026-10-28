#!/usr/bin/env bash
# Regenerates the API documents from the swag annotations on the handlers:
#   docs/swagger.{json,yaml}, docs/docs.go  — Swagger 2.0 (ReDoc, user-docs)
#   openapi/openapi.yaml                    — OpenAPI 3 operation manifest
#                                             enforced by dx-gateway-go
# Every operation must carry @ID and @x-dx-authz; cmd/api's conformance test
# fails when the manifest and the router disagree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"

cd "${ROOT_DIR}"
swag init -d cmd/api,pkg/constants -g main.go -o docs --parseGoList=false
go run ./tools/openapi3 -in docs/swagger.json -out openapi/openapi.yaml
