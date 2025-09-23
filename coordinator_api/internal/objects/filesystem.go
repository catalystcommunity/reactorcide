package objects

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FilesystemObjectStore implements ObjectStore using the local filesystem
type FilesystemObjectStore struct {
	basePath string
}

// NewFilesystemObjectStore creates a new filesystem-based object store
func NewFilesystemObjectStore(basePath string) *FilesystemObjectStore {
	return &FilesystemObjectStore{
		basePath: basePath,
	}
}

// Put stores an object in the filesystem
func (f *FilesystemObjectStore) Put(ctx context.Context, key string, data io.Reader, contentType string) error {
	if err := f.validateKey(key); err != nil {
		return err
	}

	fullPath := filepath.Join(f.basePath, key)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}

	file, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, data)
	return err
}

// Get retrieves an object from the filesystem
func (f *FilesystemObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := f.validateKey(key); err != nil {
		return nil, err
	}

	fullPath := filepath.Join(f.basePath, key)
	file, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return file, nil
}

// GetURL returns a file URL for filesystem objects (not really useful but implements interface)
func (f *FilesystemObjectStore) GetURL(ctx context.Context, key string, expires time.Duration) (string, error) {
	if err := f.validateKey(key); err != nil {
		return "", err
	}

	fullPath := filepath.Join(f.basePath, key)
	if _, err := os.Stat(fullPath); err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}

	// Return a file:// URL (note: this is not a pre-signed URL, just a file path)
	return "file://" + filepath.ToSlash(fullPath), nil
}

// Delete removes an object from the filesystem
func (f *FilesystemObjectStore) Delete(ctx context.Context, key string) error {
	if err := f.validateKey(key); err != nil {
		return err
	}

	fullPath := filepath.Join(f.basePath, key)
	err := os.Remove(fullPath)
	if err != nil && os.IsNotExist(err) {
		return ErrNotFound
	}
	return err
}

// Exists checks if an object exists in the filesystem
func (f *FilesystemObjectStore) Exists(ctx context.Context, key string) (bool, error) {
	if err := f.validateKey(key); err != nil {
		return false, err
	}

	fullPath := filepath.Join(f.basePath, key)
	_, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// List objects with a prefix in the filesystem
func (f *FilesystemObjectStore) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var objects []ObjectInfo

	baseSearchPath := filepath.Join(f.basePath, filepath.Dir(prefix))

	err := filepath.Walk(baseSearchPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Get relative path from base
		relPath, err := filepath.Rel(f.basePath, path)
		if err != nil {
			return err
		}

		// Convert to forward slashes for consistency
		relPath = filepath.ToSlash(relPath)

		// Check if this path matches our prefix
		if strings.HasPrefix(relPath, prefix) {
			objects = append(objects, ObjectInfo{
				Key:          relPath,
				Size:         info.Size(),
				LastModified: info.ModTime(),
				ContentType:  f.guessContentType(relPath),
			})
		}

		return nil
	})

	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	return objects, nil
}

// validateKey ensures the key is safe for filesystem operations
func (f *FilesystemObjectStore) validateKey(key string) error {
	if key == "" {
		return ErrInvalidKey
	}

	// Prevent path traversal attacks
	if strings.Contains(key, "..") {
		return ErrInvalidKey
	}

	// Ensure key doesn't start with /
	if strings.HasPrefix(key, "/") {
		return ErrInvalidKey
	}

	return nil
}

// guessContentType makes a simple guess about content type based on file extension
func (f *FilesystemObjectStore) guessContentType(key string) string {
	ext := strings.ToLower(filepath.Ext(key))
	switch ext {
	case ".txt", ".log":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".html":
		return "text/html"
	case ".xml":
		return "application/xml"
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	case ".tar":
		return "application/x-tar"
	case ".gz":
		return "application/gzip"
	default:
		return "application/octet-stream"
	}
}
