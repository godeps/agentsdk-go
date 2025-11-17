package middleware

import (
	"context"
	"sort"
	"sync"
)

// Stack 维护洋葱模型的中间件执行链，优先级大者越靠外层。
type Stack struct {
	mu          sync.RWMutex
	middlewares []Middleware
}

// NewStack 创建一个空的中间件栈。
func NewStack() *Stack {
	return &Stack{middlewares: make([]Middleware, 0)}
}

// Use 注册一个中间件并按优先级（升序）保持有序。
func (s *Stack) Use(mw Middleware) {
	if mw == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.middlewares = append(s.middlewares, mw)
	s.sortLocked()
}

// Remove 通过名称移除一个中间件，存在则返回 true。
func (s *Stack) Remove(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, mw := range s.middlewares {
		if mw.Name() == name {
			s.middlewares = append(s.middlewares[:i], s.middlewares[i+1:]...)
			return true
		}
	}
	return false
}

// List 返回按执行顺序（高优先级至低优先级）的中间件副本。
func (s *Stack) List() []Middleware {
	result := s.snapshot()
	reverse(result)
	return result
}

// ExecuteModelCall 构建模型调用链并运行。
func (s *Stack) ExecuteModelCall(ctx context.Context, req *ModelRequest, finalHandler ModelCallFunc) (*ModelResponse, error) {
	if finalHandler == nil {
		return nil, ErrMissingNext
	}

	middlewares := s.snapshot()
	handler := finalHandler
	for i := 0; i < len(middlewares); i++ {
		mw := middlewares[i]
		next := handler
		handler = func(ctx context.Context, req *ModelRequest) (*ModelResponse, error) {
			return mw.ExecuteModelCall(ctx, req, next)
		}
	}

	return handler(ctx, req)
}

// ExecuteToolCall 构建工具调用链并运行。
func (s *Stack) ExecuteToolCall(ctx context.Context, req *ToolCallRequest, finalHandler ToolCallFunc) (*ToolCallResponse, error) {
	if finalHandler == nil {
		return nil, ErrMissingNext
	}

	middlewares := s.snapshot()
	handler := finalHandler
	for i := 0; i < len(middlewares); i++ {
		mw := middlewares[i]
		next := handler
		handler = func(ctx context.Context, req *ToolCallRequest) (*ToolCallResponse, error) {
			return mw.ExecuteToolCall(ctx, req, next)
		}
	}

	return handler(ctx, req)
}

// Start 依次调用所有中间件的 OnStart（低优先级先启动）。
func (s *Stack) Start(ctx context.Context) error {
	middlewares := s.snapshot()
	for _, mw := range middlewares {
		if err := mw.OnStart(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Stop 逆序调用所有中间件的 OnStop（高优先级先停止）。
func (s *Stack) Stop(ctx context.Context) error {
	middlewares := s.snapshot()
	for i := len(middlewares) - 1; i >= 0; i-- {
		if err := middlewares[i].OnStop(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stack) snapshot() []Middleware {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cloned := make([]Middleware, len(s.middlewares))
	copy(cloned, s.middlewares)
	return cloned
}

func (s *Stack) sortLocked() {
	sort.SliceStable(s.middlewares, func(i, j int) bool {
		return s.middlewares[i].Priority() < s.middlewares[j].Priority()
	})
}

func reverse[T any](items []T) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}
