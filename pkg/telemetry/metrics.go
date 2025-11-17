package telemetry

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	maxInputSample = 256
)

var (
	attrAgentName  = attribute.Key("agent.name")
	attrAgentKind  = attribute.Key("agent.kind")
	attrSessionID  = attribute.Key("agent.session_id")
	attrAgentInput = attribute.Key("agent.input")
	attrToolName   = attribute.Key("tool.name")
	attrToolError  = attribute.Key("tool.error")
	attrRequestErr = attribute.Key("agent.request.error")
)

type metrics struct {
	requests  metric.Int64Counter
	latency   metric.Float64Histogram
	errors    metric.Float64Histogram
	toolCalls metric.Int64Counter
}

// RequestData captures the metadata recorded for each agent entry point.
type RequestData struct {
	Kind      string
	AgentName string
	SessionID string
	Input     string
	Duration  time.Duration
	Error     error
}

// ToolData captures metrics related to tool execution.
type ToolData struct {
	AgentName string
	Name      string
	Error     error
}

func newMetrics(m meterProvider) (*metrics, error) {
	if m == nil {
		return &metrics{}, nil
	}
	requests, err := m.Int64Counter("agent.requests.total", metric.WithDescription("Total number of agent Run/RunStream invocations."))
	if err != nil {
		return nil, err
	}
	latency, err := m.Float64Histogram("agent.latency.ms", metric.WithDescription("Agent end-to-end latency in milliseconds."), metric.WithUnit("ms"))
	if err != nil {
		return nil, err
	}
	errorRate, err := m.Float64Histogram("agent.errors.rate", metric.WithDescription("Per-request error indicator (0 or 1)."), metric.WithUnit("1"))
	if err != nil {
		return nil, err
	}
	toolCalls, err := m.Int64Counter("tool.calls.total", metric.WithDescription("Total number of tool executions."))
	if err != nil {
		return nil, err
	}
	return &metrics{
		requests:  requests,
		latency:   latency,
		errors:    errorRate,
		toolCalls: toolCalls,
	}, nil
}

func (m *metrics) RecordRequest(ctx context.Context, data RequestData) {
	if m == nil || m.requests == nil {
		return
	}
	attrs := make([]attribute.KeyValue, 0, 5)
	if data.Kind != "" {
		attrs = append(attrs, attrAgentKind.String(data.Kind))
	}
	if data.AgentName != "" {
		attrs = append(attrs, attrAgentName.String(data.AgentName))
	}
	if data.SessionID != "" {
		attrs = append(attrs, attrSessionID.String(data.SessionID))
	}
	if input := sanitizeSample(data.Input); input != "" {
		attrs = append(attrs, attrAgentInput.String(input))
	}
	errFlag := data.Error != nil
	attrs = append(attrs, attrRequestErr.Bool(errFlag))

	m.requests.Add(ctx, 1, metric.WithAttributes(attrs...))
	if data.Duration > 0 && m.latency != nil {
		m.latency.Record(ctx, float64(data.Duration.Milliseconds()), metric.WithAttributes(attrs...))
	}
	if m.errors != nil {
		if errFlag {
			m.errors.Record(ctx, 1, metric.WithAttributes(attrs...))
		} else {
			m.errors.Record(ctx, 0, metric.WithAttributes(attrs...))
		}
	}
}

func (m *metrics) RecordToolCall(ctx context.Context, data ToolData) {
	if m == nil || m.toolCalls == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attrToolName.String(strings.TrimSpace(data.Name)),
		attrToolError.Bool(data.Error != nil),
	}
	if data.AgentName != "" {
		attrs = append(attrs, attrAgentName.String(data.AgentName))
	}
	m.toolCalls.Add(ctx, 1, metric.WithAttributes(attrs...))
}

func sanitizeSample(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= maxInputSample {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxInputSample])
}

// meterProvider is the subset of metric.Meter we rely on, which makes
// dependency injection straightforward in tests.
type meterProvider interface {
	Int64Counter(name string, opts ...metric.Int64CounterOption) (metric.Int64Counter, error)
	Float64Histogram(name string, opts ...metric.Float64HistogramOption) (metric.Float64Histogram, error)
}
