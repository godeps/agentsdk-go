package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cexll/agentsdk-go/pkg/memory"
	"github.com/cexll/agentsdk-go/pkg/model"
)

const (
	threadMetadataKey   = "thread_id"
	resourceMetadataKey = "resource_id"
)

// WorkingMemoryMiddleware loads scoped working memory into the LLM system prompt and injects scope metadata.
type WorkingMemoryMiddleware struct {
	*BaseMiddleware
	store memory.WorkingMemoryStore
}

// NewWorkingMemoryMiddleware builds the middleware pointing at the provided store.
func NewWorkingMemoryMiddleware(store memory.WorkingMemoryStore) *WorkingMemoryMiddleware {
	return &WorkingMemoryMiddleware{
		BaseMiddleware: NewBaseMiddleware("working_memory", 40),
		store:          store,
	}
}

// ExecuteModelCall injects the serialized working memory (if any) ahead of the conversation.
func (m *WorkingMemoryMiddleware) ExecuteModelCall(ctx context.Context, req *ModelRequest, next ModelCallFunc) (*ModelResponse, error) {
	if next == nil {
		return nil, ErrMissingNext
	}
	if req == nil || m.store == nil {
		return next(ctx, req)
	}

	scope, ok := deriveScope(req.Metadata, req.SessionID)
	if !ok {
		return next(ctx, req)
	}

	if req.Metadata == nil {
		req.Metadata = map[string]any{}
	}
	decorateMetadata(req.Metadata, scope)

	wm, err := m.store.Get(ctx, scope)
	if err != nil {
		fmt.Printf("middleware: working memory load failed: %v\n", err)
		return next(ctx, req)
	}
	if wm == nil || len(wm.Data) == 0 {
		return next(ctx, req)
	}

	payload, err := json.MarshalIndent(wm.Data, "", "  ")
	if err != nil {
		return next(ctx, req)
	}

	systemMsg := model.Message{
		Role:    "system",
		Content: fmt.Sprintf("# 工作记忆\n\n```json\n%s\n```", string(payload)),
	}

	req.Messages = append([]model.Message{systemMsg}, req.Messages...)
	return next(ctx, req)
}

// ExecuteToolCall injects missing scope parameters so tools can operate without redundant boilerplate.
func (m *WorkingMemoryMiddleware) ExecuteToolCall(ctx context.Context, req *ToolCallRequest, next ToolCallFunc) (*ToolCallResponse, error) {
	if next == nil {
		return nil, ErrMissingNext
	}
	if req == nil {
		return next(ctx, req)
	}

	scope, ok := deriveScope(req.Metadata, req.SessionID)
	if ok {
		if req.Arguments == nil {
			req.Arguments = map[string]any{}
		}
		if _, exists := req.Arguments[threadMetadataKey]; !exists || asString(req.Arguments[threadMetadataKey]) == "" {
			req.Arguments[threadMetadataKey] = scope.ThreadID
		}
		if scope.ResourceID != "" {
			if _, exists := req.Arguments[resourceMetadataKey]; !exists || asString(req.Arguments[resourceMetadataKey]) == "" {
				req.Arguments[resourceMetadataKey] = scope.ResourceID
			}
		}
	}

	return next(ctx, req)
}

func deriveScope(metadata map[string]any, fallback string) (memory.Scope, bool) {
	threadID := asString(metadataValue(metadata, threadMetadataKey))
	if threadID == "" {
		threadID = strings.TrimSpace(fallback)
	}
	resourceID := asString(metadataValue(metadata, resourceMetadataKey))
	if threadID == "" {
		return memory.Scope{}, false
	}
	return memory.Scope{ThreadID: threadID, ResourceID: resourceID}, true
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func metadataValue(metadata map[string]any, key string) any {
	if metadata == nil {
		return nil
	}
	return metadata[key]
}

func decorateMetadata(metadata map[string]any, scope memory.Scope) {
	if metadata == nil || scope.ThreadID == "" {
		return
	}
	metadata[threadMetadataKey] = scope.ThreadID
	if scope.ResourceID != "" {
		metadata[resourceMetadataKey] = scope.ResourceID
	}
}
