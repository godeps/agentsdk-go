package middleware

import (
	"context"

	"github.com/cexll/agentsdk-go/pkg/model"
)

// Middleware 定义模型和工具调用的拦截接口。
type Middleware interface {
	// Priority 返回优先级（越大越靠外层）。
	Priority() int

	// Name 返回中间件名称。
	Name() string

	// ExecuteModelCall 拦截模型调用。
	ExecuteModelCall(ctx context.Context, req *ModelRequest, next ModelCallFunc) (*ModelResponse, error)

	// ExecuteToolCall 拦截工具调用。
	ExecuteToolCall(ctx context.Context, req *ToolCallRequest, next ToolCallFunc) (*ToolCallResponse, error)

	// OnStart 在 Agent 启动时调用（可选）。
	OnStart(ctx context.Context) error

	// OnStop 在 Agent 停止时调用（可选）。
	OnStop(ctx context.Context) error
}

// ModelRequest 模型调用请求。
type ModelRequest struct {
	Messages  []model.Message  // 消息历史
	Tools     []map[string]any // 工具定义
	SessionID string           // 会话 ID
	Metadata  map[string]any   // 元数据
}

// ModelResponse 模型调用响应。
type ModelResponse struct {
	Message  model.Message    // 模型响应
	Usage    model.TokenUsage // Token 使用情况
	Metadata map[string]any   // 元数据
}

// ToolCallRequest 工具调用请求。
type ToolCallRequest struct {
	Name      string         // 工具名称
	Arguments map[string]any // 工具参数
	SessionID string         // 会话 ID
	Metadata  map[string]any // 元数据
}

// ToolCallResponse 工具调用响应。
type ToolCallResponse struct {
	Output   string         // 工具输出
	Data     any            // 结构化数据
	Error    error          // 错误（如有）
	Metadata map[string]any // 元数据
}

// ModelCallFunc 模型调用函数类型。
type ModelCallFunc func(ctx context.Context, req *ModelRequest) (*ModelResponse, error)

// ToolCallFunc 工具调用函数类型。
type ToolCallFunc func(ctx context.Context, req *ToolCallRequest) (*ToolCallResponse, error)
