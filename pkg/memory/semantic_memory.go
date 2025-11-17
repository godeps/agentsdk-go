package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// InMemorySemanticMemory offers a minimal vector-store backed by RAM for demos/tests.
type InMemorySemanticMemory struct {
	embedder Embedder
	mu       sync.RWMutex
	memories map[string][]Memory // namespace -> memories
}

// NewInMemorySemanticMemory constructs an in-memory semantic memory using the provided embedder.
func NewInMemorySemanticMemory(embedder Embedder) *InMemorySemanticMemory {
	return &InMemorySemanticMemory{
		embedder: embedder,
		memories: make(map[string][]Memory),
	}
}

// Store embeds the text then stores it under namespace.
func (s *InMemorySemanticMemory) Store(ctx context.Context, namespace, text string, metadata map[string]any) error {
	if s == nil {
		return errors.New("semantic memory is nil")
	}
	if s.embedder == nil {
		return errors.New("embedder is nil")
	}
	if namespace == "" {
		return errors.New("namespace is required")
	}

	vectors, err := s.embedder.Embed(ctx, []string{text})
	if err != nil {
		return err
	}
	if len(vectors) == 0 {
		return errors.New("embedder returned empty vector")
	}

	var metadataCopy map[string]any
	if metadata != nil {
		metadataCopy = make(map[string]any, len(metadata))
		for k, v := range metadata {
			metadataCopy[k] = v
		}
	}

	vector := append([]float64(nil), vectors[0]...)
	mem := Memory{
		ID:        generateID(),
		Content:   text,
		Embedding: vector,
		Metadata:  metadataCopy,
		Namespace: namespace,
		Provenance: &Provenance{
			Source:    "in-memory",
			Timestamp: time.Now().UTC(),
		},
	}

	s.mu.Lock()
	s.memories[namespace] = append(s.memories[namespace], mem)
	s.mu.Unlock()
	return nil
}

// Recall performs cosine similarity search in-memory.
func (s *InMemorySemanticMemory) Recall(ctx context.Context, namespace, query string, topK int) ([]Memory, error) {
	_ = ctx
	if s == nil {
		return nil, errors.New("semantic memory is nil")
	}
	if s.embedder == nil {
		return nil, errors.New("embedder is nil")
	}
	if namespace == "" {
		return nil, errors.New("namespace is required")
	}

	vectors, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return nil, errors.New("embedder returned empty vector")
	}
	queryVec := vectors[0]

	s.mu.RLock()
	candidates := append([]Memory(nil), s.memories[namespace]...) // copy
	s.mu.RUnlock()

	for i := range candidates {
		candidates[i].Score = cosineSimilarity(queryVec, candidates[i].Embedding)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	if topK > 0 && len(candidates) > topK {
		candidates = candidates[:topK]
	}
	return candidates, nil
}

// Delete removes all memories under a namespace.
func (s *InMemorySemanticMemory) Delete(ctx context.Context, namespace string) error {
	_ = ctx
	if s == nil {
		return errors.New("semantic memory is nil")
	}
	if namespace == "" {
		return errors.New("namespace is required")
	}

	s.mu.Lock()
	delete(s.memories, namespace)
	s.mu.Unlock()
	return nil
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func generateID() string {
	return fmt.Sprintf("mem_%d", time.Now().UTC().UnixNano())
}

// ----- test helpers -----

// StaticEmbedder is a helper for tests that returns deterministic vectors.
type StaticEmbedder struct{ Vector []float64 }

// Embed returns identical vectors for each input, useful for deterministic tests.
func (e *StaticEmbedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	_ = ctx
	vectors := make([][]float64, len(texts))
	for i := range texts {
		vectors[i] = append([]float64(nil), e.Vector...)
	}
	return vectors, nil
}
