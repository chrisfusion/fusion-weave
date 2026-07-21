# SPDX-License-Identifier: GPL-3.0-or-later
# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=linux go build -a -o manager ./cmd/
RUN CGO_ENABLED=0 GOOS=linux go build -a -o api-server ./cmd/api/
RUN CGO_ENABLED=0 GOOS=linux go build -a -o loader ./cmd/loader/
RUN CGO_ENABLED=0 GOOS=linux go build -a -o backup ./cmd/backup/

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/manager .
COPY --from=builder /workspace/api-server .
COPY --from=builder /workspace/loader .
COPY --from=builder /workspace/backup .

USER 65532:65532

# Default entrypoint is the operator. Override with /api-server for the REST API deployment,
# /loader for code-source init containers (WeaveServiceTemplate.spec.codeSource.loaderImage),
# or /backup for the CRD backup CronJob / manual restore (see cmd/backup).
ENTRYPOINT ["/manager"]
