package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrProfileNotFound  = errors.New("profile not found")
	ErrProfileExists    = errors.New("profile already exists")
	ErrProfileAmbiguous = errors.New("profile name is ambiguous")
)

// Document is one encoded profile definition.
type Document struct {
	Format string
	Data   []byte
}

// Store is the narrow persistence boundary used by profile managers.
type Store interface {
	Load(context.Context, string) (Document, error)
	Create(context.Context, string, Document) error
}

// MemoryStore is an in-memory profile store intended for tests and embedding.
type MemoryStore struct {
	mu   sync.RWMutex
	docs map[string]Document
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{docs: make(map[string]Document)} }

func (s *MemoryStore) Load(_ context.Context, name string) (Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.docs[name]
	if !ok {
		return Document{}, fmt.Errorf("%w: %s", ErrProfileNotFound, name)
	}
	return cloneDocument(doc), nil
}

func (s *MemoryStore) Create(_ context.Context, name string, doc Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.docs[name]; ok {
		return fmt.Errorf("%w: %s", ErrProfileExists, name)
	}
	s.docs[name] = cloneDocument(doc)
	return nil
}

// Put replaces a memory-store definition. It represents an external profile
// edit and is deliberately not part of Store.
func (s *MemoryStore) Put(name string, doc Document) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[name] = cloneDocument(doc)
}

func cloneDocument(doc Document) Document {
	return Document{Format: doc.Format, Data: append([]byte(nil), doc.Data...)}
}

// FileStore stores named JSON or YAML profiles in one directory.
type FileStore struct{ directory string }

func NewFileStore(directory string) *FileStore { return &FileStore{directory: directory} }

func (s *FileStore) Load(_ context.Context, name string) (Document, error) {
	matches, err := s.matches(name)
	if err != nil {
		return Document{}, err
	}
	if len(matches) == 0 {
		return Document{}, fmt.Errorf("%w: %s", ErrProfileNotFound, name)
	}
	if len(matches) > 1 {
		return Document{}, fmt.Errorf("%w: %s", ErrProfileAmbiguous, name)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return Document{}, fmt.Errorf("read profile %q: %w", name, err)
	}
	return Document{Format: filepath.Ext(matches[0]), Data: data}, nil
}

func (s *FileStore) Create(_ context.Context, name string, doc Document) error {
	matches, err := s.matches(name)
	if err != nil {
		return err
	}
	if len(matches) != 0 {
		return fmt.Errorf("%w: %s", ErrProfileExists, name)
	}
	extension, err := normalizeFormat(doc.Format)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.directory, 0o755); err != nil {
		return fmt.Errorf("create profile directory: %w", err)
	}
	path := filepath.Join(s.directory, name+extension)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrProfileExists, name)
		}
		return fmt.Errorf("create profile %q: %w", name, err)
	}
	if _, err := file.Write(doc.Data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write profile %q: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close profile %q: %w", name, err)
	}
	return nil
}

func (s *FileStore) matches(name string) ([]string, error) {
	if !validProfileName(name) {
		return nil, fmt.Errorf("invalid profile name %q", name)
	}
	var matches []string
	for _, extension := range []string{".json", ".yaml", ".yml"} {
		path := filepath.Join(s.directory, name+extension)
		_, err := os.Stat(path)
		switch {
		case err == nil:
			matches = append(matches, path)
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, fmt.Errorf("inspect profile %q: %w", name, err)
		}
	}
	return matches, nil
}

func validProfileName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}

func normalizeFormat(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", ".json":
		return ".json", nil
	case "yaml", ".yaml":
		return ".yaml", nil
	case "yml", ".yml":
		return ".yml", nil
	default:
		return "", fmt.Errorf("unsupported profile format %q", format)
	}
}

// Clone copies a definition to a new independent profile identity.
func Clone(ctx context.Context, store Store, source, destination string) error {
	doc, err := store.Load(ctx, source)
	if err != nil {
		return err
	}
	return store.Create(ctx, destination, doc)
}
