package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
)

// LogEntry represents a single log line in JSON format
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Level     string `json:"level,omitempty"`
	Message   string `json:"message"`
}

// LogShipperConfig holds configuration for log shipping
type LogShipperConfig struct {
	ObjectStore    objects.ObjectStore
	JobID          string
	StreamType     string // "stdout" or "stderr"
	ChunkInterval  time.Duration
	OnChunkUploaded func(objectKey string, bytesWritten int64) error // Callback for chunk uploads
}

// LogShipper handles streaming logs to object storage in chunks
type LogShipper struct {
	config  LogShipperConfig
	entries []LogEntry
	mu      sync.Mutex

	// Statistics
	totalBytes    int64
	chunksWritten int
	objectKey     string

	// Secret masking
	masker *secrets.Masker
}

// NewLogShipper creates a new log shipper
func NewLogShipper(config LogShipperConfig, masker *secrets.Masker) *LogShipper {
	// Default chunk interval to 3 seconds if not specified
	if config.ChunkInterval == 0 {
		config.ChunkInterval = 3 * time.Second
	}

	// Generate object key - use .json extension for JSON array format
	objectKey := fmt.Sprintf("logs/%s/%s.json", config.JobID, config.StreamType)

	return &LogShipper{
		config:    config,
		entries:   make([]LogEntry, 0),
		objectKey: objectKey,
		masker:    masker,
	}
}

// StreamAndShip reads from the input stream, masks secrets, and ships logs to object storage
// Returns the object key where logs were stored and total bytes written
func (ls *LogShipper) StreamAndShip(ctx context.Context, reader io.ReadCloser) (string, int64, error) {
	defer reader.Close()

	logger := logging.Log.WithFields(map[string]interface{}{
		"job_id":      ls.config.JobID,
		"stream_type": ls.config.StreamType,
	})

	logger.Info("Starting log streaming and shipping")

	// Create a ticker for periodic chunk uploads
	ticker := time.NewTicker(ls.config.ChunkInterval)
	defer ticker.Stop()

	// Create a done channel to signal completion
	done := make(chan struct{})
	defer close(done)

	// Start background goroutine for periodic chunk uploads
	uploadErrors := make(chan error, 1)
	go ls.periodicUploader(ctx, ticker, done, uploadErrors)

	// Read lines from the input stream
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()

		// Mask secrets in the line
		maskedLine := line
		if ls.masker != nil {
			maskedLine = ls.masker.MaskString(line)
		}

		// Create log entry
		entry := ls.parseLogLine(maskedLine)

		// Add to entries slice
		ls.mu.Lock()
		ls.entries = append(ls.entries, entry)
		ls.mu.Unlock()
	}

	// Check for scanner errors
	if err := scanner.Err(); err != nil {
		logger.WithError(err).Error("Error reading from stream")
		return ls.objectKey, ls.totalBytes, fmt.Errorf("error reading stream: %w", err)
	}

	// Upload any remaining buffered data
	if err := ls.uploadChunk(ctx); err != nil {
		logger.WithError(err).Error("Failed to upload final chunk")
		return ls.objectKey, ls.totalBytes, fmt.Errorf("failed to upload final chunk: %w", err)
	}

	// Check if there were any upload errors from background goroutine
	select {
	case err := <-uploadErrors:
		if err != nil {
			logger.WithError(err).Error("Background upload error")
			return ls.objectKey, ls.totalBytes, err
		}
	default:
		// No errors
	}

	logger.WithFields(map[string]interface{}{
		"object_key":    ls.objectKey,
		"total_bytes":   ls.totalBytes,
		"chunks_written": ls.chunksWritten,
	}).Info("Log streaming completed")

	return ls.objectKey, ls.totalBytes, nil
}

// periodicUploader runs in the background and uploads chunks at regular intervals
func (ls *LogShipper) periodicUploader(ctx context.Context, ticker *time.Ticker, done chan struct{}, errors chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if err := ls.uploadChunk(ctx); err != nil {
				select {
				case errors <- err:
				default:
					// Error channel full, log it
					logging.Log.WithError(err).Error("Failed to send upload error to channel")
				}
				return
			}
		}
	}
}

// uploadChunk uploads the current entries to object storage as a JSON array
func (ls *LogShipper) uploadChunk(ctx context.Context) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	// Nothing to upload if no new entries
	if len(ls.entries) == 0 {
		return nil
	}

	logger := logging.Log.WithFields(map[string]interface{}{
		"job_id":      ls.config.JobID,
		"stream_type": ls.config.StreamType,
		"entry_count": len(ls.entries),
		"chunk_num":   ls.chunksWritten + 1,
	})

	contentType := "application/json"

	// Read existing entries from storage if this isn't the first chunk
	var allEntries []LogEntry

	if ls.chunksWritten > 0 {
		existingReader, err := ls.config.ObjectStore.Get(ctx, ls.objectKey)
		if err != nil && err != objects.ErrNotFound {
			logger.WithError(err).Error("Failed to read existing log content")
			return fmt.Errorf("failed to read existing content: %w", err)
		}

		if err == nil {
			defer existingReader.Close()
			existingData, err := io.ReadAll(existingReader)
			if err != nil {
				logger.WithError(err).Error("Failed to read existing log data")
				return fmt.Errorf("failed to read existing data: %w", err)
			}

			// Parse existing JSON array
			if err := json.Unmarshal(existingData, &allEntries); err != nil {
				logger.WithError(err).Warn("Failed to parse existing log data as JSON array, starting fresh")
				allEntries = nil
			}
		}
	}

	// Append new entries
	allEntries = append(allEntries, ls.entries...)

	// Marshal to JSON array
	jsonData, err := json.Marshal(allEntries)
	if err != nil {
		logger.WithError(err).Error("Failed to marshal log entries to JSON")
		return fmt.Errorf("failed to marshal entries: %w", err)
	}

	// Write the complete JSON array
	if err := ls.config.ObjectStore.Put(ctx, ls.objectKey, bytes.NewReader(jsonData), contentType); err != nil {
		logger.WithError(err).Error("Failed to upload log chunk")
		return fmt.Errorf("failed to upload chunk: %w", err)
	}

	// Update statistics
	ls.totalBytes = int64(len(jsonData))
	ls.chunksWritten++

	// Clear the entries buffer
	ls.entries = ls.entries[:0]

	logger.WithField("total_bytes", ls.totalBytes).Debug("Log chunk uploaded successfully")

	// Call the callback if provided
	if ls.config.OnChunkUploaded != nil {
		if err := ls.config.OnChunkUploaded(ls.objectKey, ls.totalBytes); err != nil {
			logger.WithError(err).Warn("Chunk upload callback failed")
		}
	}

	return nil
}

// parseLogLine parses a log line, preserving existing JSON structure if present
func (ls *LogShipper) parseLogLine(line string) LogEntry {
	// Try to parse the line as JSON
	var existing LogEntry
	if err := json.Unmarshal([]byte(line), &existing); err == nil {
		// Line is valid JSON, check if it has the required fields
		if existing.Timestamp != "" && existing.Message != "" {
			// Use existing timestamp and message, fill in stream if missing
			if existing.Stream == "" {
				existing.Stream = ls.config.StreamType
			}
			return existing
		}
	}

	// Line is not valid JSON or missing required fields, create new entry
	return LogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Stream:    ls.config.StreamType,
		Level:     "info",
		Message:   line,
	}
}

// GetObjectKey returns the object key where logs are being stored
func (ls *LogShipper) GetObjectKey() string {
	return ls.objectKey
}

// GetTotalBytes returns the total number of bytes written
func (ls *LogShipper) GetTotalBytes() int64 {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.totalBytes
}
