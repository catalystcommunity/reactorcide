package objects

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound      = errors.New("object not found")
	ErrNotSupported  = errors.New("operation not supported")
	ErrInvalidKey    = errors.New("invalid object key")
	ErrAlreadyExists = errors.New("object already exists")
)

// ObjectStore defines the interface for interacting with object storage
type ObjectStore interface {
	// Put stores an object and returns the key
	Put(ctx context.Context, key string, data io.Reader, contentType string) error

	// Get retrieves an object
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// GetURL returns a pre-signed URL for accessing the object (optional)
	GetURL(ctx context.Context, key string, expires time.Duration) (string, error)

	// Delete removes an object
	Delete(ctx context.Context, key string) error

	// Exists checks if an object exists
	Exists(ctx context.Context, key string) (bool, error)

	// List objects with a prefix
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
}

// ObjectInfo contains metadata about an object
type ObjectInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
	ContentType  string    `json:"content_type"`
}

// ObjectStoreConfig contains configuration for object store implementations
type ObjectStoreConfig struct {
	Type   string            `json:"type"` // "s3", "gcs", "filesystem", "memory"
	Config map[string]string `json:"config"`
}

// NewObjectStore creates a new object store based on the provided configuration
func NewObjectStore(config ObjectStoreConfig) (ObjectStore, error) {
	switch config.Type {
	case "filesystem":
		basePath := config.Config["base_path"]
		if basePath == "" {
			basePath = "./objects"
		}
		return NewFilesystemObjectStore(basePath), nil
	case "memory":
		return NewMemoryObjectStore(), nil
	case "s3":
		return nil, errors.New("S3 object store not implemented yet")
	case "gcs":
		return nil, errors.New("GCS object store not implemented yet")
	default:
		return nil, errors.New("unsupported object store type: " + config.Type)
	}
}
