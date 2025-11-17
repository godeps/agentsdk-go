package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/cexll/agentsdk-go/telemetry"

// Config drives how telemetry is initialized.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	Resource       *resource.Resource
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	Filter         FilterConfig
}

// Manager coordinates tracing, metrics and sensitive-data filtering.
type Manager struct {
	tracer trace.Tracer

	metrics        *metrics
	filter         *Filter
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
}

var globalManager atomic.Pointer[Manager]

// NewManager builds a fully wired telemetry manager.
func NewManager(cfg Config) (*Manager, error) {
	filter, err := NewFilter(cfg.Filter)
	if err != nil {
		return nil, err
	}
	tp := cfg.TracerProvider
	if tp == nil {
		res := cfg.Resource
		if res == nil {
			res, err = buildResource(cfg)
			if err != nil {
				return nil, err
			}
		}
		tp = sdktrace.NewTracerProvider(sdktrace.WithResource(res))
	}
	mp := cfg.MeterProvider
	if mp == nil {
		mp = sdkmetric.NewMeterProvider()
	}
	meter := mp.Meter(instrumentationName)
	recorder, err := newMetrics(meter)
	if err != nil {
		return nil, err
	}
	return &Manager{
		tracer:         tp.Tracer(instrumentationName),
		metrics:        recorder,
		filter:         filter,
		tracerProvider: tp,
		meterProvider:  mp,
	}, nil
}

// StartSpan proxies trace creation through the configured tracer.
func (m *Manager) StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if m == nil || m.tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return m.tracer.Start(ctx, name, opts...)
}

// RecordRequest forwards per-request metrics.
func (m *Manager) RecordRequest(ctx context.Context, data RequestData) {
	if m == nil || m.metrics == nil {
		return
	}
	if m.filter != nil {
		data.Input = m.filter.MaskText(data.Input)
	}
	m.metrics.RecordRequest(ctx, data)
}

// RecordToolCall increments tool-level counters when telemetry is enabled.
func (m *Manager) RecordToolCall(ctx context.Context, data ToolData) {
	if m == nil || m.metrics == nil {
		return
	}
	m.metrics.RecordToolCall(ctx, data)
}

// SanitizeAttributes masks any sensitive fields before they reach OTEL.
func (m *Manager) SanitizeAttributes(attrs ...attribute.KeyValue) []attribute.KeyValue {
	if m == nil || m.filter == nil {
		return attrs
	}
	return m.filter.MaskAttributes(attrs...)
}

// MaskText removes sensitive content from the provided value.
func (m *Manager) MaskText(value string) string {
	if m == nil || m.filter == nil {
		return value
	}
	return m.filter.MaskText(value)
}

// Shutdown gracefully stops the configured providers.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	var result error
	if closer, ok := m.tracerProvider.(interface {
		Shutdown(context.Context) error
	}); ok && closer != nil {
		if err := closer.Shutdown(ctx); err != nil {
			result = errors.Join(result, err)
		}
	}
	if closer, ok := m.meterProvider.(interface {
		Shutdown(context.Context) error
	}); ok && closer != nil {
		if err := closer.Shutdown(ctx); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

// SetDefault swaps the global telemetry manager used by helper functions.
func SetDefault(mgr *Manager) {
	globalManager.Store(mgr)
}

// Default returns the process-wide telemetry manager when registered.
func Default() *Manager {
	return globalManager.Load()
}

// StartSpan starts a span using the global manager when available.
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if mgr := Default(); mgr != nil {
		return mgr.StartSpan(ctx, name, opts...)
	}
	return ctx, trace.SpanFromContext(ctx)
}

// RecordRequest publishes request metrics through the global manager.
func RecordRequest(ctx context.Context, data RequestData) {
	if mgr := Default(); mgr != nil {
		mgr.RecordRequest(ctx, data)
	}
}

// RecordToolCall publishes tool metrics through the global manager.
func RecordToolCall(ctx context.Context, data ToolData) {
	if mgr := Default(); mgr != nil {
		mgr.RecordToolCall(ctx, data)
	}
}

// SanitizeAttributes exposes the global filtering helper.
func SanitizeAttributes(attrs ...attribute.KeyValue) []attribute.KeyValue {
	if mgr := Default(); mgr != nil {
		return mgr.SanitizeAttributes(attrs...)
	}
	return attrs
}

// MaskText exposes global masking for user-supplied content.
func MaskText(value string) string {
	if mgr := Default(); mgr != nil {
		return mgr.MaskText(value)
	}
	return value
}

// EndSpan finalizes span state while standardizing error recording.
func EndSpan(span trace.Span, err error) {
	if span == nil {
		return
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "ok")
	}
	span.End()
}

func buildResource(cfg Config) (*resource.Resource, error) {
	service := strings.TrimSpace(cfg.ServiceName)
	if service == "" {
		service = "agentsdk-go"
	}
	attrs := []attribute.KeyValue{semconv.ServiceName(service)}
	if version := strings.TrimSpace(cfg.ServiceVersion); version != "" {
		attrs = append(attrs, semconv.ServiceVersion(version))
	}
	if env := strings.TrimSpace(cfg.Environment); env != "" {
		attrs = append(attrs, semconv.DeploymentEnvironment(env))
	}
	base := resource.Default()
	schema := base.SchemaURL()
	if schema == "" {
		schema = semconv.SchemaURL
	}
	custom := resource.NewWithAttributes(schema, attrs...)
	return resource.Merge(base, custom)
}
