package worker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
)

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
	config LogShipperConfig
	buffer *bytes.Buffer
	mu     sync.Mutex

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

	// Generate object key
	objectKey := fmt.Sprintf("logs/%s/%s.log", config.JobID, config.StreamType)

	return &LogShipper{
		config:    config,
		buffer:    new(bytes.Buffer),
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

		// Write to buffer (with newline)
		ls.mu.Lock()
		ls.buffer.WriteString(maskedLine)
		ls.buffer.WriteString("\n")
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

// uploadChunk uploads the current buffer contents to object storage
func (ls *LogShipper) uploadChunk(ctx context.Context) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	// Nothing to upload if buffer is empty
	if ls.buffer.Len() == 0 {
		return nil
	}

	logger := logging.Log.WithFields(map[string]interface{}{
		"job_id":      ls.config.JobID,
		"stream_type": ls.config.StreamType,
		"chunk_size":  ls.buffer.Len(),
		"chunk_num":   ls.chunksWritten + 1,
	})

	// Get chunk size before we manipulate the buffer
	chunkSize := int64(ls.buffer.Len())

	// Upload to object storage
	// Note: For chunked uploads, we append to the same object key
	// The filesystem backend will append if the file exists
	contentType := "text/plain"

	// For first chunk, use Put. For subsequent chunks, we need to append
	// Since the ObjectStore interface doesn't have Append, we'll read existing content,
	// append new content, and write back
	var finalContent []byte

	if ls.chunksWritten > 0 {
		// Read existing content
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

			// Append new chunk to existing content
			finalContent = append(existingData, ls.buffer.Bytes()...)
		} else {
			// File doesn't exist yet (shouldn't happen for chunks > 0, but handle it)
			finalContent = ls.buffer.Bytes()
		}
	} else {
		// First chunk - just use buffer content
		finalContent = ls.buffer.Bytes()
	}

	// Write the complete content
	finalReader := bytes.NewReader(finalContent)
	if err := ls.config.ObjectStore.Put(ctx, ls.objectKey, finalReader, contentType); err != nil {
		logger.WithError(err).Error("Failed to upload log chunk")
		return fmt.Errorf("failed to upload chunk: %w", err)
	}

	// Update statistics
	ls.totalBytes += chunkSize
	ls.chunksWritten++

	// Clear the buffer
	ls.buffer.Reset()

	logger.WithField("total_bytes", ls.totalBytes).Debug("Log chunk uploaded successfully")

	// Call the callback if provided
	if ls.config.OnChunkUploaded != nil {
		if err := ls.config.OnChunkUploaded(ls.objectKey, ls.totalBytes); err != nil {
			logger.WithError(err).Warn("Chunk upload callback failed")
			// Don't return error - callback failure shouldn't stop log shipping
		}
	}

	return nil
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
