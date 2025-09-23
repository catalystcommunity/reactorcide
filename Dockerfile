# Multi-stage build for Reactorcide Coordinator API

#####################################################################
# Build Stage
#####################################################################
FROM golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /build

# Copy go mod files from coordinator_api first for better layer caching
COPY coordinator_api/go.mod coordinator_api/go.sum ./

# Copy coredb for the local replace directive
COPY coredb/ ../coredb/

# Download dependencies
RUN go mod download

# Copy coordinator_api source code
COPY coordinator_api/ .

# Build the application
# CGO_ENABLED=0 for static binary
# -ldflags='-w -s' to strip debug info and reduce binary size
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -o coordinator_api .

#####################################################################
# Runtime Stage
#####################################################################
FROM alpine:3.18

# Install runtime dependencies
# - ca-certificates: for HTTPS connections
# - python3: for runnerlib integration
# - py3-pip: for Python package management
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    python3 \
    py3-pip \
    && rm -rf /var/cache/apk/*

# Create non-root user
RUN addgroup -g 1001 -S reactorcide && \
    adduser -u 1001 -S reactorcide -G reactorcide

# Create application directory
WORKDIR /app

# Copy binary from build stage
COPY --from=builder /build/coordinator_api /app/coordinator_api

# Make binary executable
RUN chmod +x /app/coordinator_api

# Install runnerlib Python package
# This is required for the worker to execute jobs
RUN pip3 install --no-cache-dir --break-system-packages \
    /app/../runnerlib || echo "Runnerlib not found, will need to be installed separately"

# Change ownership of app directory to non-root user
RUN chown -R reactorcide:reactorcide /app

# Switch to non-root user
USER reactorcide

# Create data directory for temporary files (worker needs this)
RUN mkdir -p /app/data

# Expose port (can be overridden)
EXPOSE 6080

# Health check endpoint
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD /app/coordinator_api serve --help > /dev/null || exit 1

# Default command runs the API server
# Can be overridden to run worker or other commands
ENTRYPOINT ["/app/coordinator_api"]
CMD ["serve"]