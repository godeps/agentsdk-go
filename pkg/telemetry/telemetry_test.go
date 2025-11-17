package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

func TestFilterMasking(t *testing.T) {
	filter, err := NewFilter(FilterConfig{
		Mask:     "<safe>",
		Patterns: []string{`user\d+`},
	})
	if err != nil {
		t.Fatalf("new filter: %v", err)
	}
	raw := "token=sk-secret-123 user42 says hi"
	if got := filter.MaskText(raw); strings.Contains(got, "sk-secret") || strings.Contains(got, "user42") {
		t.Fatalf("expected sensitive segments masked, got %q", got)
	}
	attrs := filter.MaskAttributes(
		attribute.String("api_key", "sk-abcdef"),
		attribute.StringSlice("tokens", []string{"user1", "user2"}),
	)
	for _, attr := range attrs {
		switch attr.Key {
		case "api_key":
			if attr.Value.AsString() != "<safe>" {
				t.Fatalf("expected api key masked, got %q", attr.Value.AsString())
			}
		case "tokens":
			for _, v := range attr.Value.AsStringSlice() {
				if v != "<safe>" {
					t.Fatalf("expected token masked, got %q", v)
				}
			}
		}
	}
}

func TestManagerRecordsMetricsAndSpans(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)),
	)
	cfg := Config{
		ServiceName:    "unit-test-agent",
		ServiceVersion: "test",
		Environment:    "ci",
		MeterProvider:  mp,
		TracerProvider: tp,
		Filter: FilterConfig{
			Mask:     "<removed>",
			Patterns: []string{`demo`},
		},
	}
	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	SetDefault(mgr)
	t.Cleanup(func() {
		SetDefault(nil)
		_ = mgr.Shutdown(context.Background())
	})

	ctx := context.Background()
	ctx, span := StartSpan(ctx, "test.span", trace.WithSpanKind(trace.SpanKindServer))
	RecordRequest(ctx, RequestData{
		Kind:      "run",
		AgentName: "unit",
		SessionID: "sess-1",
		Input:     "demo payload sk-abc123",
		Duration:  25 * time.Millisecond,
	})
	RecordToolCall(ctx, ToolData{
		AgentName: "unit",
		Name:      "echo",
	})
	EndSpan(span, errors.New("boom"))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	reqMetric := findMetric(t, rm, "agent.requests.total")
	sum, ok := reqMetric.Data.(metricdata.Sum[int64])
	if !ok || len(sum.DataPoints) != 1 {
		t.Fatalf("unexpected request metric payload: %#v", reqMetric.Data)
	}
	if val, ok := sum.DataPoints[0].Attributes.Value(attrAgentInput); !ok || strings.Contains(val.AsString(), "sk-") {
		t.Fatalf("expected sanitized input attribute, got %v", val.AsString())
	}
	toolMetric := findMetric(t, rm, "tool.calls.total")
	if _, ok := toolMetric.Data.(metricdata.Sum[int64]); !ok {
		t.Fatalf("unexpected tool metric payload: %#v", toolMetric.Data)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name != "test.span" {
		t.Fatalf("unexpected span name %q", spans[0].Name)
	}
	if spans[0].Status.Code != codes.Error {
		t.Fatalf("expected error status, got %v", spans[0].Status.Code)
	}
}

func TestSanitizeAttributes(t *testing.T) {
	filter, err := NewFilter(FilterConfig{})
	if err != nil {
		t.Fatalf("new filter: %v", err)
	}
	mgr := &Manager{
		filter:  filter,
		metrics: &metrics{},
	}
	SetDefault(mgr)
	defer SetDefault(nil)

	masked := SanitizeAttributes(attribute.String("auth", "Bearer sk-secret-42"))
	if len(masked) != 1 || strings.Contains(masked[0].Value.AsString(), "sk-secret") {
		t.Fatalf("expected masked attribute, got %+v", masked)
	}
	if got := MaskText("token=sk-secret"); strings.Contains(got, "sk-secret") {
		t.Fatalf("expected masked text, got %q", got)
	}
}

func findMetric(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == name {
				return metric
			}
		}
	}
	t.Fatalf("metric %q not found", name)
	return metricdata.Metrics{}
}

func TestBuildResourceDefaults(t *testing.T) {
	res, err := buildResource(Config{ServiceVersion: "v1.2.3", Environment: "staging"})
	if err != nil {
		t.Fatalf("build resource: %v", err)
	}
	attrs := res.Attributes()
	vals := map[attribute.Key]string{}
	for _, attr := range attrs {
		vals[attr.Key] = attr.Value.AsString()
	}
	if vals[semconv.ServiceNameKey] != "agentsdk-go" {
		t.Fatalf("expected default service name, got %q", vals[semconv.ServiceNameKey])
	}
	if vals[semconv.ServiceVersionKey] != "v1.2.3" {
		t.Fatalf("version missing: %+v", vals)
	}
	if vals[semconv.DeploymentEnvironmentKey] != "staging" {
		t.Fatalf("environment missing: %+v", vals)
	}
}

func TestManagerShutdownClosesProviders(t *testing.T) {
	tracer := newClosingTracerProvider()
	meter := newClosingMeterProvider()
	mgr, err := NewManager(Config{
		ServiceName:    "test",
		ServiceVersion: "v",
		TracerProvider: tracer,
		MeterProvider:  meter,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if !tracer.closed || !meter.closed {
		t.Fatalf("expected providers to close tracer=%v meter=%v", tracer.closed, meter.closed)
	}
}

func TestNewMetricsPropagatesErrors(t *testing.T) {
	meter := &failingMeter{}
	if _, err := newMetrics(meter); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestSanitizeSampleTruncates(t *testing.T) {
	long := strings.Repeat("🙂", maxInputSample+5)
	got := sanitizeSample("  " + long + "  ")
	if utf8.RuneCountInString(got) != maxInputSample {
		t.Fatalf("expected truncation to %d runes, got %d", maxInputSample, utf8.RuneCountInString(got))
	}
	short := sanitizeSample("  hi  ")
	if short != "hi" {
		t.Fatalf("expected trimmed short sample, got %q", short)
	}
}

type closingTracerProvider struct {
	*sdktrace.TracerProvider
	closed bool
}

func newClosingTracerProvider() *closingTracerProvider {
	return &closingTracerProvider{TracerProvider: sdktrace.NewTracerProvider()}
}

func (c *closingTracerProvider) Shutdown(ctx context.Context) error {
	c.closed = true
	return c.TracerProvider.Shutdown(ctx)
}

type closingMeterProvider struct {
	*sdkmetric.MeterProvider
	closed bool
}

func newClosingMeterProvider() *closingMeterProvider {
	return &closingMeterProvider{MeterProvider: sdkmetric.NewMeterProvider()}
}

func (c *closingMeterProvider) Shutdown(ctx context.Context) error {
	c.closed = true
	return c.MeterProvider.Shutdown(ctx)
}

type failingMeter struct{}

func (f *failingMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return nil, errors.New("boom")
}

func (f *failingMeter) Float64Histogram(string, ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return nil, nil
}

func TestNewManagerBuildsDefaults(t *testing.T) {
	cfg := Config{}
	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, span := mgr.StartSpan(context.Background(), "op")
	mgr.RecordRequest(ctx, RequestData{
		Kind:      "run",
		AgentName: "demo",
		SessionID: "sess",
		Input:     "secret sk-123",
		Duration:  5 * time.Millisecond,
	})
	mgr.RecordToolCall(ctx, ToolData{Name: "tool"})
	attrs := mgr.SanitizeAttributes(attribute.String("token", "sk-secret-123456"))
	if len(attrs) != 1 || strings.Contains(attrs[0].Value.AsString(), "secret") {
		t.Fatalf("expected sanitized attribute, got %+v", attrs)
	}
	if masked := mgr.MaskText("sk-secret-123456"); strings.Contains(masked, "secret") {
		t.Fatalf("expected sanitized text, got %q", masked)
	}
	EndSpan(span, nil)
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestNewManagerFilterError(t *testing.T) {
	_, err := NewManager(Config{Filter: FilterConfig{Patterns: []string{"("}}})
	if err == nil {
		t.Fatal("expected filter compile error")
	}
}

func TestGlobalHelpersWithoutManager(t *testing.T) {
	SetDefault(nil)
	ctx := context.Background()
	ctx, span := StartSpan(ctx, "noop")
	RecordRequest(ctx, RequestData{})
	RecordToolCall(ctx, ToolData{})
	out := SanitizeAttributes(attribute.String("token", "raw"))
	if out[0].Value.AsString() != "raw" {
		t.Fatalf("unexpected sanitation without manager: %+v", out)
	}
	if MaskText("raw") != "raw" {
		t.Fatal("mask should be no-op without manager")
	}
	EndSpan(span, nil)
}

func TestNewMetricsNilMeter(t *testing.T) {
	m, err := newMetrics(nil)
	if err != nil {
		t.Fatalf("new metrics: %v", err)
	}
	m.RecordRequest(context.Background(), RequestData{})
	m.RecordToolCall(context.Background(), ToolData{})
}

func TestManagerStartSpanWithoutTracer(t *testing.T) {
	mgr := &Manager{}
	ctx, span := mgr.StartSpan(context.Background(), "noop")
	if span == nil {
		t.Fatal("expected span even without tracer")
	}
	mgr.RecordToolCall(ctx, ToolData{Name: "noop"})
	mgr.RecordRequest(ctx, RequestData{})
	EndSpan(span, nil)
}

func TestManagerSanitizeWithoutFilter(t *testing.T) {
	mgr := &Manager{}
	out := mgr.SanitizeAttributes(attribute.String("foo", "bar"))
	if len(out) != 1 || out[0].Value.AsString() != "bar" {
		t.Fatalf("expected passthrough attrs %+v", out)
	}
	if txt := mgr.MaskText("baz"); txt != "baz" {
		t.Fatalf("expected passthrough text, got %s", txt)
	}
}

func TestManagerShutdownNil(t *testing.T) {
	var mgr *Manager
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected nil shutdown to succeed: %v", err)
	}
}
