# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy coredb module first (local dependency)
COPY coredb /coredb

# Copy coordinator_api files
COPY coordinator_api/go.mod coordinator_api/go.sum ./
RUN go mod download

# Copy source code
COPY coordinator_api .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o coordinator-api .

# Final stage
FROM scratch

# Copy the binary from builder
COPY --from=builder /app/coordinator-api /coordinator-api

# Expose port
EXPOSE 8080

ENTRYPOINT ["/coordinator-api"]