#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_DIR="${JUPYTERLITE_OUTPUT_DIR:-${ROOT_DIR}/jupyterlite}"
PYODIDE_URL="${JUPYTERLITE_PYODIDE_URL:-https://github.com/pyodide/pyodide/releases/download/0.29.0/pyodide-0.29.0.tar.bz2}"

mkdir -p "${OUTPUT_DIR}"

if command -v python3.12 >/dev/null 2>&1; then
  PYTHON_BIN="python3.12"
elif command -v python3.11 >/dev/null 2>&1; then
  PYTHON_BIN="python3.11"
elif command -v python3.10 >/dev/null 2>&1; then
  PYTHON_BIN="python3.10"
elif command -v python3.9 >/dev/null 2>&1; then
  PYTHON_BIN="python3.9"
else
  PYTHON_BIN=""
fi

if [[ -n "${PYTHON_BIN}" ]]; then
  VENV_DIR="${ROOT_DIR}/.venv-jupyterlite"
  "${PYTHON_BIN}" -m venv "${VENV_DIR}"
  # shellcheck source=/dev/null
  source "${VENV_DIR}/bin/activate"
  python -m pip install --upgrade pip
  python -m pip install \
    jupyterlite-core==0.7.5 \
    jupyterlite-pyodide-kernel==0.7.2
  jupyter lite build \
    --output-dir="${OUTPUT_DIR}" \
    --pyodide="${PYODIDE_URL}"
  exit 0
fi

if command -v docker >/dev/null 2>&1; then
  docker run --rm \
    -v "${ROOT_DIR}:/work" \
    -w /work \
    -e PYODIDE_URL="${PYODIDE_URL}" \
    -e HOST_UID="$(id -u)" \
    -e HOST_GID="$(id -g)" \
    python:3.12-slim \
    sh -lc 'python -m pip install --no-cache-dir jupyterlite-core==0.7.5 jupyterlite-pyodide-kernel==0.7.2 && jupyter lite build --output-dir=/work/jupyterlite --pyodide="${PYODIDE_URL}" && { chown -R "${HOST_UID}:${HOST_GID}" /work/jupyterlite /work/.cache /work/.jupyterlite.doit.db 2>/dev/null || true; }'
  exit 0
fi

echo "Need Python 3.9+ or Docker to build JupyterLite assets." >&2
exit 1
