#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mkdir -p "${DOCS_DIR}/static/openapi"
cp "${ROOT_DIR}/docs/swagger.yaml" "${DOCS_DIR}/static/openapi/swagger.yaml"
cp "${ROOT_DIR}/docs/swagger.json" "${DOCS_DIR}/static/openapi/swagger.json"
