// Package storage implements P2 shipment documents: a small backend
// interface with a local-disk implementation. Production points
// TRACKSPHERE_STORAGE_DIR at a volume (or plugs an S3 backend into Store);
// the API never touches the filesystem directly.
package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Store persists and retrieves document bytes.
type Store interface {
	// Put stores content under key and returns the canonical storage key.
	Put(key string, content io.Reader) (string, error)
	// Open returns a reader for key (caller closes).
	Open(key string) (io.ReadCloser, error)
	// Size returns the size in bytes of the stored content.
	Size(key string) (int64, error)
	// Delete removes key (missing = nil).
	Delete(key string) error
}

// NewKey mints a safe namespaced key: tenant/shipment/uuid-filename.
func NewKey(tenantID, shipmentID uuid.UUID, filename string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, filepath.Base(filename))
	if safe == "" || safe == "." {
		safe = "file"
	}
	return fmt.Sprintf("%s/%s/%s-%s", tenantID, shipmentID, uuid.NewString()[:8], safe)
}

// Dir returns the configured storage root (./var/docs default).
func Dir() string {
	if d := os.Getenv("TRACKSPHERE_STORAGE_DIR"); d != "" {
		return d
	}
	return "./var/docs"
}

// Local is the disk implementation.
type Local struct{ root string }

// NewLocal builds a disk store, creating the root.
func NewLocal(root string) (*Local, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Local{root: root}, nil
}

func (l *Local) path(key string) (string, error) {
	clean := filepath.Clean("/" + key)[1:]
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("invalid storage key")
	}
	return filepath.Join(l.root, clean), nil
}

func (l *Local) Put(key string, content io.Reader) (string, error) {
	p, err := l.path(key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, content); err != nil {
		return "", err
	}
	return key, nil
}

func (l *Local) Open(key string) (io.ReadCloser, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (l *Local) Delete(key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *Local) Size(key string) (int64, error) {
	p, err := l.path(key)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
