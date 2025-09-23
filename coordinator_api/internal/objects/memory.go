package objects

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"time"
)

// MemoryObjectStore implements ObjectStore using in-memory storage (for testing)
type MemoryObjectStore struct {
	mu      sync.RWMutex
	objects map[string]*MemoryObject
}

// MemoryObject represents an object stored in memory
type MemoryObject struct {
	Data         []byte
	ContentType  string
	LastModified time.Time
}

// NewMemoryObjectStore creates a new memory-based object store
func NewMemoryObjectStore() *MemoryObjectStore {
	return &MemoryObjectStore{
		objects: make(map[string]*MemoryObject),
	}
}

// Put stores an object in memory
func (m *MemoryObjectStore) Put(ctx context.Context, key string, data io.Reader, contentType string) error {
	if err := m.validateKey(key); err != nil {
		return err
	}

	dataBytes, err := io.ReadAll(data)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.objects[key] = &MemoryObject{
		Data:         dataBytes,
		ContentType:  contentType,
		LastModified: time.Now(),
	}
	return nil
}

// Get retrieves an object from memory
func (m *MemoryObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := m.validateKey(key); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	obj, exists := m.objects[key]
	if !exists {
		return nil, ErrNotFound
	}

	return io.NopCloser(bytes.NewReader(obj.Data)), nil
}

// GetURL returns a data URL for memory objects (not really useful but implements interface)
func (m *MemoryObjectStore) GetURL(ctx context.Context, key string, expires time.Duration) (string, error) {
	if err := m.validateKey(key); err != nil {
		return "", err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	_, exists := m.objects[key]
	if !exists {
		return "", ErrNotFound
	}

	// Memory store doesn't support pre-signed URLs
	return "", ErrNotSupported
}

// Delete removes an object from memory
func (m *MemoryObjectStore) Delete(ctx context.Context, key string) error {
	if err := m.validateKey(key); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.objects[key]; !exists {
		return ErrNotFound
	}

	delete(m.objects, key)
	return nil
}

// Exists checks if an object exists in memory
func (m *MemoryObjectStore) Exists(ctx context.Context, key string) (bool, error) {
	if err := m.validateKey(key); err != nil {
		return false, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	_, exists := m.objects[key]
	return exists, nil
}

// List objects with a prefix in memory
func (m *MemoryObjectStore) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var objects []ObjectInfo
	for key, obj := range m.objects {
		if strings.HasPrefix(key, prefix) {
			objects = append(objects, ObjectInfo{
				Key:          key,
				Size:         int64(len(obj.Data)),
				LastModified: obj.LastModified,
				ContentType:  obj.ContentType,
			})
		}
	}

	return objects, nil
}

// validateKey ensures the key is valid
func (m *MemoryObjectStore) validateKey(key string) error {
	if key == "" {
		return ErrInvalidKey
	}

	// Prevent path traversal (though not strictly necessary for memory store)
	if strings.Contains(key, "..") {
		return ErrInvalidKey
	}

	return nil
}

// Clear removes all objects (useful for testing)
func (m *MemoryObjectStore) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects = make(map[string]*MemoryObject)
}

// Size returns the number of objects stored
func (m *MemoryObjectStore) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.objects)
}
