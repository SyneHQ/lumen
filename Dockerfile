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

# Final lightweight container
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/lumen /app/lumen

EXPOSE 50051 50052 9090

ENTRYPOINT ["/app/lumen"]
