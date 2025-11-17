package middleware

import (
	"context"
	"reflect"
	"testing"

	"github.com/cexll/agentsdk-go/pkg/model"
)

func TestStackListOrdersByPriority(t *testing.T) {
	stack := NewStack()
	stack.Use(newTestMiddleware("low", 10, nil, nil))
	stack.Use(newTestMiddleware("high", 90, nil, nil))
	stack.Use(newTestMiddleware("mid", 50, nil, nil))

	order := names(stack.List())
	want := []string{"high", "mid", "low"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("List() order mismatch: got %v want %v", order, want)
	}

	if !stack.Remove("mid") {
		t.Fatalf("expected Remove to delete existing middleware")
	}
	if stack.Remove("missing") {
		t.Fatalf("Remove should return false for unknown middleware")
	}
}

func TestStackExecuteModelCallOrder(t *testing.T) {
	ctx := context.Background()
	stack := NewStack()
	var order []string

	high := newTestMiddleware("high", 90, func() { order = append(order, "high") }, nil)
	mid := newTestMiddleware("mid", 50, func() { order = append(order, "mid") }, nil)
	low := newTestMiddleware("low", 10, func() { order = append(order, "low") }, nil)

	stack.Use(low)
	stack.Use(high)
	stack.Use(mid)

	final := func(ctx context.Context, req *ModelRequest) (*ModelResponse, error) {
		order = append(order, "final")
		return &ModelResponse{Message: model.Message{}, Usage: model.TokenUsage{}}, nil
	}

	if _, err := stack.ExecuteModelCall(ctx, &ModelRequest{}, final); err != nil {
		t.Fatalf("ExecuteModelCall failed: %v", err)
	}

	want := []string{"high", "mid", "low", "final"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("unexpected execution order: got %v want %v", order, want)
	}
}

func TestStackExecuteToolCallOrder(t *testing.T) {
	ctx := context.Background()
	stack := NewStack()
	var order []string

	low := newTestMiddleware("low", 5, nil, func() { order = append(order, "low") })
	high := newTestMiddleware("high", 80, nil, func() { order = append(order, "high") })
	stack.Use(low)
	stack.Use(high)

	final := func(ctx context.Context, req *ToolCallRequest) (*ToolCallResponse, error) {
		order = append(order, "final")
		return &ToolCallResponse{}, nil
	}

	if _, err := stack.ExecuteToolCall(ctx, &ToolCallRequest{Name: "noop"}, final); err != nil {
		t.Fatalf("ExecuteToolCall failed: %v", err)
	}

	want := []string{"high", "low", "final"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("unexpected tool execution order: got %v want %v", order, want)
	}
}

type testMiddleware struct {
	name     string
	priority int
	modelFn  func()
	toolFn   func()
}

func newTestMiddleware(name string, priority int, modelFn, toolFn func()) *testMiddleware {
	return &testMiddleware{name: name, priority: priority, modelFn: modelFn, toolFn: toolFn}
}

func (m *testMiddleware) Name() string  { return m.name }
func (m *testMiddleware) Priority() int { return m.priority }

func (m *testMiddleware) ExecuteModelCall(ctx context.Context, req *ModelRequest, next ModelCallFunc) (*ModelResponse, error) {
	if m.modelFn != nil {
		m.modelFn()
	}
	return next(ctx, req)
}

func (m *testMiddleware) ExecuteToolCall(ctx context.Context, req *ToolCallRequest, next ToolCallFunc) (*ToolCallResponse, error) {
	if m.toolFn != nil {
		m.toolFn()
	}
	return next(ctx, req)
}

func (m *testMiddleware) OnStart(ctx context.Context) error { return nil }
func (m *testMiddleware) OnStop(ctx context.Context) error  { return nil }

func names(list []Middleware) []string {
	result := make([]string, len(list))
	for i, mw := range list {
		result[i] = mw.Name()
	}
	return result
}
