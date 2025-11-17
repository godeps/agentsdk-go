package middleware

import (
	"context"
	"fmt"

	"github.com/cexll/agentsdk-go/pkg/memory"
	"github.com/cexll/agentsdk-go/pkg/model"
)

// AgentMemoryMiddleware injects agent persona content from AgentMemoryStore as a system message.
type AgentMemoryMiddleware struct {
	*BaseMiddleware
	store memory.AgentMemoryStore
}

// NewAgentMemoryMiddleware constructs the middleware with the provided store.
func NewAgentMemoryMiddleware(store memory.AgentMemoryStore) *AgentMemoryMiddleware {
	return &AgentMemoryMiddleware{
		BaseMiddleware: NewBaseMiddleware("agent_memory", 30),
		store:          store,
	}
}

// ExecuteModelCall prepends agent.md content (if present) into the message stream.
func (m *AgentMemoryMiddleware) ExecuteModelCall(ctx context.Context, req *ModelRequest, next ModelCallFunc) (*ModelResponse, error) {
	if next == nil {
		return nil, ErrMissingNext
	}
	if m.store == nil || req == nil {
		return next(ctx, req)
	}

	if !m.store.Exists(ctx) {
		return next(ctx, req)
	}

	content, err := m.store.Read(ctx)
	if err != nil {
		fmt.Printf("middleware: failed to read agent memory: %v\n", err)
		return next(ctx, req)
	}

	systemMsg := model.Message{
		Role:    "system",
		Content: "# Agent 配置\n\n" + content,
	}

	req.Messages = append([]model.Message{systemMsg}, req.Messages...)
	return next(ctx, req)
}
