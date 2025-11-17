package telemetry

import (
	"fmt"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/attribute"
)

// FilterConfig declares how sensitive data should be sanitized before it is
// attached to spans or metrics.
type FilterConfig struct {
	// Mask is the replacement string applied whenever a pattern matches.
	Mask string
	// Patterns augments the default regular expressions used to detect
	// credentials or other sensitive payloads.
	Patterns []string
}

// Filter masks strings that should never reach telemetry backends.
type Filter struct {
	mask     string
	patterns []*regexp.Regexp
}

var defaultPatterns = []string{
	`(?i)sk-[a-z0-9]{6,}`,
	`(?i)(api[_-]?key|token|secret|bearer)[\s:=]+[a-z0-9\-_.]{8,}`,
	`(?i)(?:access|secret)[\s_-]*(?:key|token)[\s:=]+[a-z0-9\-_/]{8,}`,
}

// NewFilter compiles the configured mask and regex patterns.
func NewFilter(cfg FilterConfig) (*Filter, error) {
	mask := strings.TrimSpace(cfg.Mask)
	if mask == "" {
		mask = "[redacted]"
	}
	patterns := make([]string, 0, len(defaultPatterns)+len(cfg.Patterns))
	patterns = append(patterns, defaultPatterns...)
	patterns = append(patterns, cfg.Patterns...)

	seen := map[string]struct{}{}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, raw := range patterns {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		re, err := regexp.Compile(raw)
		if err != nil {
			return nil, fmt.Errorf("telemetry: compile filter %q: %w", raw, err)
		}
		compiled = append(compiled, re)
		seen[raw] = struct{}{}
	}
	return &Filter{
		mask:     mask,
		patterns: compiled,
	}, nil
}

// MaskText replaces all matching segments in value.
func (f *Filter) MaskText(value string) string {
	if f == nil || value == "" || len(f.patterns) == 0 {
		return value
	}
	masked := value
	for _, re := range f.patterns {
		masked = re.ReplaceAllString(masked, f.mask)
	}
	return masked
}

// MaskAttributes returns a sanitized copy of attrs.
func (f *Filter) MaskAttributes(attrs ...attribute.KeyValue) []attribute.KeyValue {
	if f == nil || len(attrs) == 0 {
		return attrs
	}
	clean := make([]attribute.KeyValue, len(attrs))
	for i, attr := range attrs {
		clean[i] = f.maskAttribute(attr)
	}
	return clean
}

func (f *Filter) maskAttribute(attr attribute.KeyValue) attribute.KeyValue {
	if f == nil {
		return attr
	}
	switch attr.Value.Type() {
	case attribute.STRING:
		return attribute.String(string(attr.Key), f.MaskText(attr.Value.AsString()))
	case attribute.STRINGSLICE:
		values := attr.Value.AsStringSlice()
		masked := make([]string, len(values))
		for i, v := range values {
			masked[i] = f.MaskText(v)
		}
		return attribute.StringSlice(string(attr.Key), masked)
	default:
		return attr
	}
}
