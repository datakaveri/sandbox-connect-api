# syntax=docker/dockerfile:1.7
# Temporary baseline copied from nha-agentic-evaluation-framework/Dockerfile.runner.
# Override PS_TAG when the PS4 runtime is confirmed.
ARG PS_TAG=nha-ps1-v3
FROM golang:1.24.2-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY internal ./internal
COPY cmd/output-runner ./cmd/output-runner
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/output-runner ./cmd/output-runner

FROM 098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:${PS_TAG}

USER root

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    CONDA_DIR=/opt/conda \
    PATH=/opt/conda/bin:$PATH

RUN apt-get update && apt-get install -y --no-install-recommends \
        libcairo2 libcairo2-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*

RUN conda install -n base -c conda-forge -y --freeze-installed \
        tesseract=5.* \
        pytesseract \
        pdfplumber \
        python-docx \
        openpyxl \
        xlrd \
        pyyaml \
        jsonschema \
        tenacity \
        nbformat \
        nbconvert \
        ipykernel \
        jupyter_client \
    && conda clean -afy

RUN pip install --no-cache-dir \
        "boto3>=1.35" "botocore>=1.35" \
        reportlab \
        "mini-swe-agent>=2.2,<3" \
        "litellm>=1.75" \
        markdown xhtml2pdf

# The same starter notebook is used interactively and by the output workflow.
# Fail the runner build if its selected notebook base image does not contain
# the NHA SDK and the optional interactive widget imported by that notebook.
RUN python -c "from nha_data_access import ABACClient; from nha_connect_widget import NHAConnectWidget, PlatformTokenReadyWidget"

RUN python -m ipykernel install --sys-prefix --name=python3 \
        --display-name "Python 3 (evaluation)"

COPY --from=builder /out/output-runner /usr/local/bin/output-runner
USER 1000:1000
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/output-runner"]
