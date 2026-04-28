#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"

cd "${ROOT_DIR}"
swag init -d cmd/api,pkg/constants -g main.go -o docs --parseGoList=false
