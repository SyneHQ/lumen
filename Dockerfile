# Multi-stage Docker build for Lumen Event Ingestion Service
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build statically-linked binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /app/lumen ./cmd/lumen

# Final production runtime image using Google Distroless nonroot
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/lumen /app/lumen

# AGPL-3.0 section 4 requires the license to accompany the conveyed work.
COPY --from=builder /app/LICENSE /app/NOTICE /app/

LABEL org.opencontainers.image.title="Lumen" \
      org.opencontainers.image.description="Multi-tenant event ingestion and session analytics" \
      org.opencontainers.image.source="https://github.com/SyneHQ/lumen" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later"

# Run as unprivileged nonroot user
USER nonroot:nonroot

EXPOSE 50051 50052 9090

ENTRYPOINT ["/app/lumen"]
