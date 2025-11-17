package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileAgentMemoryStore persists agent.md onto the filesystem.
type FileAgentMemoryStore struct {
	filePath string
	mu       sync.RWMutex
}

// NewFileAgentMemoryStore creates a FileAgentMemoryStore rooted at workDir.
func NewFileAgentMemoryStore(workDir string) *FileAgentMemoryStore {
	return &FileAgentMemoryStore{filePath: filepath.Join(workDir, "agent.md")}
}

// Read loads the agent persona file content.
func (s *FileAgentMemoryStore) Read(ctx context.Context) (string, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("agent.md not found: %w", err)
		}
		return "", err
	}
	return string(data), nil
}

// Write overwrites agent.md with provided content.
func (s *FileAgentMemoryStore) Write(ctx context.Context, content string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.filePath, []byte(content), 0o644)
}

// Exists reports whether agent.md exists at the configured location.
func (s *FileAgentMemoryStore) Exists(ctx context.Context) bool {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, err := os.Stat(s.filePath)
	return err == nil
}
