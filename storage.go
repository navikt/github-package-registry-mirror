package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"cloud.google.com/go/storage"
)

// FileMetadata holds metadata about a stored file.
type FileMetadata struct {
	TimeCreated time.Time
}

// FileHandle represents a single file in storage.
type FileHandle interface {
	Exists(ctx context.Context) (bool, error)
	GetMetadata(ctx context.Context) (FileMetadata, error)
	NewReader(ctx context.Context) (io.ReadCloser, error)
	NewWriter(ctx context.Context) (io.WriteCloser, error)
	Delete(ctx context.Context) error
}

// Storage is the interface for accessing files.
type Storage interface {
	File(name string) FileHandle
	Ping(ctx context.Context) error
	Close() error
}

// ============================================================
// GCS Storage
// ============================================================

type gcsStorage struct {
	client *storage.Client
	bucket *storage.BucketHandle
}

// NewGCSStorage creates a Storage backed by Google Cloud Storage.
func NewGCSStorage(bucketName string) (Storage, error) {
	client, err := storage.NewClient(context.Background())
	if err != nil {
		return nil, err
	}
	return &gcsStorage{client: client, bucket: client.Bucket(bucketName)}, nil
}

func (g *gcsStorage) File(name string) FileHandle {
	return &gcsFileHandle{obj: g.bucket.Object(name)}
}

func (g *gcsStorage) Ping(ctx context.Context) error {
	_, err := g.bucket.Attrs(ctx)
	return err
}

func (g *gcsStorage) Close() error {
	return g.client.Close()
}

type gcsFileHandle struct {
	obj *storage.ObjectHandle
}

func (h *gcsFileHandle) Exists(ctx context.Context) (bool, error) {
	_, err := h.obj.Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (h *gcsFileHandle) GetMetadata(ctx context.Context) (FileMetadata, error) {
	attrs, err := h.obj.Attrs(ctx)
	if err != nil {
		return FileMetadata{}, err
	}
	return FileMetadata{TimeCreated: attrs.Created}, nil
}

func (h *gcsFileHandle) NewReader(ctx context.Context) (io.ReadCloser, error) {
	return h.obj.NewReader(ctx)
}

func (h *gcsFileHandle) NewWriter(ctx context.Context) (io.WriteCloser, error) {
	return h.obj.NewWriter(ctx), nil
}

func (h *gcsFileHandle) Delete(ctx context.Context) error {
	return h.obj.Delete(ctx)
}

// ============================================================
// Local Storage
// ============================================================

type localStorage struct {
	basePath string
}

// NewLocalStorage creates a Storage backed by the local filesystem.
func NewLocalStorage(basePath string) Storage {
	return &localStorage{basePath: basePath}
}

func (l *localStorage) File(name string) FileHandle {
	return &localFileHandle{path: filepath.Join(l.basePath, name)}
}

func (l *localStorage) Ping(_ context.Context) error { return nil }

func (l *localStorage) Close() error { return nil }

type localFileHandle struct {
	path string
}

func (h *localFileHandle) Exists(ctx context.Context) (bool, error) {
	_, err := os.Stat(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (h *localFileHandle) GetMetadata(ctx context.Context) (FileMetadata, error) {
	info, err := os.Stat(h.path)
	if err != nil {
		return FileMetadata{}, err
	}
	return FileMetadata{TimeCreated: info.ModTime()}, nil
}

func (h *localFileHandle) NewReader(ctx context.Context) (io.ReadCloser, error) {
	return os.Open(h.path)
}

func (h *localFileHandle) NewWriter(ctx context.Context) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return nil, err
	}
	return os.Create(h.path)
}

func (h *localFileHandle) Delete(ctx context.Context) error {
	return os.Remove(h.path)
}
