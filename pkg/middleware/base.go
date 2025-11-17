package middleware

import (
	"context"
	"errors"
)

// ErrMissingNext 表示中间件调用链缺失下游处理器。
var ErrMissingNext = errors.New("middleware: next handler is nil")

// BaseMiddleware 提供基础字段与默认实现，便于具体中间件嵌入或组合。
type BaseMiddleware struct {
	name     string
	priority int
}

// NewBaseMiddleware 构造一个具有名称和优先级的基础中间件。
func NewBaseMiddleware(name string, priority int) *BaseMiddleware {
	return &BaseMiddleware{name: name, priority: priority}
}

// Name 返回中间件名称。
func (m *BaseMiddleware) Name() string { return m.name }

// Priority 返回中间件优先级。
func (m *BaseMiddleware) Priority() int { return m.priority }

// ExecuteModelCall 默认直接透传。
func (m *BaseMiddleware) ExecuteModelCall(ctx context.Context, req *ModelRequest, next ModelCallFunc) (*ModelResponse, error) {
	if next == nil {
		return nil, ErrMissingNext
	}
	return next(ctx, req)
}

// ExecuteToolCall 默认直接透传。
func (m *BaseMiddleware) ExecuteToolCall(ctx context.Context, req *ToolCallRequest, next ToolCallFunc) (*ToolCallResponse, error) {
	if next == nil {
		return nil, ErrMissingNext
	}
	return next(ctx, req)
}

// OnStart 默认无操作。
func (m *BaseMiddleware) OnStart(ctx context.Context) error { return nil }

// OnStop 默认无操作。
func (m *BaseMiddleware) OnStop(ctx context.Context) error { return nil }
