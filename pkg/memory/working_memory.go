package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrInvalidScope indicates the provided scope lacks a thread identifier.
var ErrInvalidScope = errors.New("memory: scope requires thread_id")

// FileWorkingMemoryStore persists working memories to JSON blobs per scope.
type FileWorkingMemoryStore struct {
	dir string
	mu  sync.RWMutex
}

// NewFileWorkingMemoryStore prepares a store rooted at workDir/working_memory.
func NewFileWorkingMemoryStore(workDir string) *FileWorkingMemoryStore {
	return &FileWorkingMemoryStore{dir: filepath.Join(workDir, "working_memory")}
}

// Get loads working memory for the provided scope.
func (s *FileWorkingMemoryStore) Get(ctx context.Context, scope Scope) (*WorkingMemory, error) {
	_ = ctx
	if err := validateScope(scope); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.scopePath(scope))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var wm WorkingMemory
	if err := json.Unmarshal(data, &wm); err != nil {
		return nil, err
	}
	if wm.Data == nil {
		wm.Data = map[string]any{}
	}

	if wm.expired(time.Now()) {
		return nil, nil
	}
	return cloneWorkingMemory(&wm), nil
}

// Set persists working memory for the given scope.
func (s *FileWorkingMemoryStore) Set(ctx context.Context, scope Scope, memory *WorkingMemory) error {
	_ = ctx
	if err := validateScope(scope); err != nil {
		return err
	}
	if memory == nil {
		return errors.New("memory: working memory payload is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if memory.Data == nil {
		memory.Data = map[string]any{}
	}
	if err := validateAgainstSchema(memory); err != nil {
		return err
	}

	now := time.Now().UTC()
	if memory.CreatedAt.IsZero() {
		memory.CreatedAt = now
	}
	memory.UpdatedAt = now
	if memory.TTL < 0 {
		memory.TTL = 0
	}

	payload, err := json.MarshalIndent(memory, "", "  ")
	if err != nil {
		return err
	}

	path := s.scopePath(scope)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}

// Delete removes the working memory file for the scope if it exists.
func (s *FileWorkingMemoryStore) Delete(ctx context.Context, scope Scope) error {
	_ = ctx
	if err := validateScope(scope); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.scopePath(scope)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List enumerates all scopes with stored working memory files.
func (s *FileWorkingMemoryStore) List(ctx context.Context) ([]Scope, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	var scopes []Scope
	err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		scope, parseErr := s.parseScope(path)
		if parseErr != nil {
			return parseErr
		}
		scopes = append(scopes, scope)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].ThreadID == scopes[j].ThreadID {
			return scopes[i].ResourceID < scopes[j].ResourceID
		}
		return scopes[i].ThreadID < scopes[j].ThreadID
	})
	return scopes, nil
}

func (s *FileWorkingMemoryStore) scopePath(scope Scope) string {
	thread := sanitizeSegment(scope.ThreadID)
	resource := sanitizeSegment(scope.ResourceID)
	if resource == "" {
		resource = "default"
	}
	filename := resource + ".json"
	return filepath.Join(s.dir, thread, filename)
}

func (s *FileWorkingMemoryStore) parseScope(path string) (Scope, error) {
	rel, err := filepath.Rel(s.dir, path)
	if err != nil {
		return Scope{}, err
	}
	if strings.HasPrefix(rel, "..") {
		return Scope{}, fmt.Errorf("memory: invalid scope path %q", path)
	}
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return Scope{}, fmt.Errorf("memory: malformed scope path %q", rel)
	}
	threadID := parts[0]
	file := parts[len(parts)-1]
	resource := strings.TrimSuffix(file, filepath.Ext(file))
	if resource == "default" {
		resource = ""
	}
	if threadID == "" {
		return Scope{}, fmt.Errorf("memory: missing thread id in %q", path)
	}
	return Scope{ThreadID: threadID, ResourceID: resource}, nil
}

func validateScope(scope Scope) error {
	if strings.TrimSpace(scope.ThreadID) == "" {
		return ErrInvalidScope
	}
	return nil
}

func sanitizeSegment(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func validateAgainstSchema(memory *WorkingMemory) error {
	if memory == nil || memory.Schema == nil {
		return nil
	}
	for _, field := range memory.Schema.Required {
		if _, ok := memory.Data[field]; !ok {
			return fmt.Errorf("memory: missing required field %q", field)
		}
	}
	return nil
}

func cloneWorkingMemory(src *WorkingMemory) *WorkingMemory {
	if src == nil {
		return nil
	}
	cloned := *src
	cloned.Data = make(map[string]any, len(src.Data))
	for k, v := range src.Data {
		cloned.Data[k] = v
	}
	return &cloned
}

func (wm *WorkingMemory) expired(now time.Time) bool {
	if wm == nil || wm.TTL <= 0 {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	pivot := wm.UpdatedAt
	if pivot.IsZero() {
		pivot = wm.CreatedAt
	}
	if pivot.IsZero() {
		return false
	}
	return now.Sub(pivot) > wm.TTL
}
