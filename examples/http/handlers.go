package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/cexll/agentsdk-go/pkg/api"
	"github.com/cexll/agentsdk-go/pkg/model"
	"github.com/cexll/agentsdk-go/pkg/sandbox"
	"github.com/cexll/agentsdk-go/pkg/tool"
)

const (
	defaultMaxBodyBytes = int64(1 << 20) // 1 MiB
)

type exampleServer struct {
	baseOptions    api.Options
	toolExecutor   *tool.Executor
	defaultTimeout time.Duration
	maxTimeout     time.Duration
	maxBodyBytes   int64
}

func (s *exampleServer) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/run", s.handleRun)
	mux.HandleFunc("/v1/run/stream", s.handleStream)
	mux.HandleFunc("/v1/tools/execute", s.handleToolExecute)
}

func (s *exampleServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Code: "method_not_allowed", Message: http.StatusText(http.StatusMethodNotAllowed)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *exampleServer) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Code: "method_not_allowed", Message: "only POST is supported"})
		return
	}
	var req runRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: err.Error()})
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		s.writeJSON(w, http.StatusBadRequest, errorResponse{Code: "missing_prompt", Message: "prompt is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout(req.TimeoutMs))
	defer cancel()

	runtime, cleanup, err := s.newRuntime(ctx, req.Sandbox)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "runtime_start_failed", Message: err.Error()})
		return
	}
	defer cleanup()

	apiReq := req.toAPIRequest(s.baseOptions.Mode)
	resp, err := runtime.Run(ctx, apiReq)
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, errorResponse{Code: "agent_run_failed", Message: err.Error()})
		return
	}
	payload, err := buildRunResponse(resp, req.SessionID)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "response_encoding_failed", Message: err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *exampleServer) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Code: "method_not_allowed", Message: "only POST is supported"})
		return
	}
	var req runRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: err.Error()})
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		s.writeJSON(w, http.StatusBadRequest, errorResponse{Code: "missing_prompt", Message: "prompt is required"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "stream_unavailable", Message: "response writer does not support streaming"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout(req.TimeoutMs))
	defer cancel()

	runtime, cleanup, err := s.newRuntime(ctx, req.Sandbox)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "runtime_start_failed", Message: err.Error()})
		return
	}
	defer cleanup()

	events, err := runtime.RunStream(ctx, req.toAPIRequest(s.baseOptions.Mode))
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, errorResponse{Code: "agent_run_failed", Message: err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}

			eventBytes, err := json.Marshal(event)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, eventBytes)
			flusher.Flush()

		case <-ticker.C:
			fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
			flusher.Flush()

		case <-ctx.Done():
			return
		}
	}
}

func (s *exampleServer) handleToolExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Code: "method_not_allowed", Message: "only POST is supported"})
		return
	}
	if s.toolExecutor == nil {
		s.writeJSON(w, http.StatusInternalServerError, errorResponse{Code: "tool_executor_missing", Message: "tool executor is not initialised"})
		return
	}

	var req toolRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Message: err.Error()})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		s.writeJSON(w, http.StatusBadRequest, errorResponse{Code: "missing_name", Message: "tool name is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout(req.TimeoutMs))
	defer cancel()

	sandboxOpts := s.mergeSandbox(req.Sandbox)
	executor := s.toolExecutor.WithSandbox(buildSandboxManagerFromOptions(sandboxOpts, s.baseOptions.ProjectRoot))
	result, err := executor.Execute(ctx, tool.Call{
		Name:   req.Name,
		Params: cloneParams(req.Params),
		Path:   sandboxOpts.Root,
		Usage:  req.Usage.toUsage(),
	})
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, errorResponse{Code: "tool_execution_failed", Message: err.Error()})
		return
	}
	payload := toolResponse{
		Name:       req.Name,
		Success:    result != nil && result.Result != nil && result.Result.Success,
		Output:     extractOutput(result),
		DurationMs: result.Duration().Milliseconds(),
	}
	if result != nil && result.Result != nil {
		payload.Data = result.Result.Data
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *exampleServer) decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("request body is empty")
	}
	defer r.Body.Close()
	reader := io.LimitReader(r.Body, s.bodyLimit())
	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is empty")
		}
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func (s *exampleServer) bodyLimit() int64 {
	if s.maxBodyBytes > 0 {
		return s.maxBodyBytes
	}
	return defaultMaxBodyBytes
}

func (s *exampleServer) requestTimeout(raw int) time.Duration {
	timeout := s.defaultTimeout
	if raw > 0 {
		timeout = time.Duration(raw) * time.Millisecond
	}
	if timeout <= 0 {
		timeout = s.defaultTimeout
	}
	if timeout > s.maxTimeout && s.maxTimeout > 0 {
		timeout = s.maxTimeout
	}
	return timeout
}

func (s *exampleServer) newRuntime(ctx context.Context, sb sandboxRequest) (*api.Runtime, func(), error) {
	opts := s.baseOptions
	opts.Sandbox = s.mergeSandbox(sb)
	runtime, err := api.New(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = runtime.Close() }
	return runtime, cleanup, nil
}

func (s *exampleServer) mergeSandbox(req sandboxRequest) api.SandboxOptions {
	base := s.baseOptions.Sandbox
	out := base
	if req.Root != "" {
		out.Root = normalizePath(req.Root)
	}
	if out.Root == "" {
		out.Root = normalizePath(s.baseOptions.ProjectRoot)
	}

	out.AllowedPaths = append(cloneStrings(base.AllowedPaths), cleanPaths(req.AllowedPaths, out.Root)...)
	if out.Root != "" {
		out.AllowedPaths = append(out.AllowedPaths, out.Root)
	}
	out.AllowedPaths = dedupStrings(out.AllowedPaths)

	out.NetworkAllow = dedupStrings(append(cloneStrings(base.NetworkAllow), normalizeHosts(req.NetworkAllow)...))

	if req.Resource != nil {
		out.ResourceLimit = req.Resource.toLimits(base.ResourceLimit)
	}
	return out
}

func buildRunResponse(resp *api.Response, sessionID string) (*runResponse, error) {
	if resp == nil || resp.Result == nil {
		return nil, errors.New("agent response is empty")
	}
	payload := &runResponse{
		SessionID:  sessionID,
		Output:     resp.Result.Output,
		StopReason: resp.Result.StopReason,
		Usage:      resp.Result.Usage,
		Tags:       resp.Tags,
		ToolCalls:  resp.Result.ToolCalls,
		Sandbox:    resp.SandboxSnapshot,
	}
	if payload.Tags == nil {
		payload.Tags = map[string]string{}
	}
	return payload, nil
}

func writeSSE(w http.ResponseWriter, payload streamPayload) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return err
	}
	data := strings.TrimSpace(buf.String())
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return nil
}

func buildSandboxManagerFromOptions(opts api.SandboxOptions, projectRoot string) *sandbox.Manager {
	root := opts.Root
	if root == "" {
		root = projectRoot
	}
	root = normalizePath(root)
	fs := sandbox.NewFileSystemAllowList(root)
	for _, allowed := range opts.AllowedPaths {
		fs.Allow(allowed)
	}
	nw := sandbox.NewDomainAllowList(opts.NetworkAllow...)
	limiter := sandbox.NewResourceLimiter(opts.ResourceLimit)
	return sandbox.NewManager(fs, nw, limiter)
}

func extractOutput(result *tool.CallResult) string {
	if result == nil || result.Result == nil {
		return ""
	}
	return result.Result.Output
}

func cloneParams(params map[string]any) map[string]any {
	if params == nil {
		return map[string]any{}
	}
	dup := make(map[string]any, len(params))
	for k, v := range params {
		dup[k] = v
	}
	return dup
}

func cloneStrings(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	out := make([]string, len(input))
	copy(out, input)
	return out
}

func dedupStrings(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(input))
	out := make([]string, 0, len(input))
	for _, v := range input {
		key := strings.TrimSpace(v)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func cleanPaths(paths []string, fallback string) []string {
	if len(paths) == 0 {
		return nil
	}
	var out []string
	for _, p := range paths {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		if !filepath.IsAbs(trimmed) && fallback != "" {
			trimmed = filepath.Join(fallback, trimmed)
		}
		out = append(out, normalizePath(trimmed))
	}
	return out
}

func normalizeHosts(hosts []string) []string {
	var out []string
	for _, h := range hosts {
		trimmed := strings.TrimSpace(strings.ToLower(h))
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func normalizePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return filepath.Clean(trimmed)
	}
	return filepath.Clean(abs)
}

// ---------------------- request / response payloads ----------------------

type runRequest struct {
	Prompt        string            `json:"prompt"`
	SessionID     string            `json:"session_id"`
	TimeoutMs     int               `json:"timeout_ms"`
	Tags          map[string]string `json:"tags"`
	Traits        []string          `json:"traits"`
	Channels      []string          `json:"channels"`
	Metadata      map[string]any    `json:"metadata"`
	ToolWhitelist []string          `json:"tool_whitelist"`
	Sandbox       sandboxRequest    `json:"sandbox"`
	Platform      platformRequest   `json:"platform"`
}

func (r runRequest) toAPIRequest(base api.ModeContext) api.Request {
	req := api.Request{
		Prompt:        r.Prompt,
		SessionID:     r.SessionID,
		Traits:        cloneStrings(r.Traits),
		Channels:      cloneStrings(r.Channels),
		Tags:          cloneMap(r.Tags),
		Metadata:      cloneAnyMap(r.Metadata),
		ToolWhitelist: cloneStrings(r.ToolWhitelist),
	}
	req.Mode = r.buildMode(base)
	return req
}

func (r runRequest) buildMode(base api.ModeContext) api.ModeContext {
	mode := base
	if mode.Platform == nil {
		mode.Platform = &api.PlatformContext{}
	}
	if r.Platform.Organization != "" {
		mode.Platform.Organization = r.Platform.Organization
	}
	if r.Platform.Project != "" {
		mode.Platform.Project = r.Platform.Project
	}
	if r.Platform.Environment != "" {
		mode.Platform.Environment = r.Platform.Environment
	}
	return mode
}

type platformRequest struct {
	Organization string `json:"organization"`
	Project      string `json:"project"`
	Environment  string `json:"environment"`
}

type sandboxRequest struct {
	Root         string                `json:"root"`
	AllowedPaths []string              `json:"allowed_paths"`
	NetworkAllow []string              `json:"network_allow"`
	Resource     *resourceLimitRequest `json:"resource"`
}

type resourceLimitRequest struct {
	MaxCPUPercent float64 `json:"max_cpu_percent"`
	MaxMemoryMB   int64   `json:"max_memory_mb"`
	MaxDiskMB     int64   `json:"max_disk_mb"`
}

func (r *resourceLimitRequest) toLimits(base sandbox.ResourceLimits) sandbox.ResourceLimits {
	if r == nil {
		return base
	}
	out := base
	if r.MaxCPUPercent > 0 {
		out.MaxCPUPercent = r.MaxCPUPercent
	}
	if r.MaxMemoryMB > 0 {
		out.MaxMemoryBytes = uint64(r.MaxMemoryMB) * 1024 * 1024
	}
	if r.MaxDiskMB > 0 {
		out.MaxDiskBytes = uint64(r.MaxDiskMB) * 1024 * 1024
	}
	return out
}

type toolRequest struct {
	Name      string           `json:"name"`
	Params    map[string]any   `json:"params"`
	TimeoutMs int              `json:"timeout_ms"`
	Sandbox   sandboxRequest   `json:"sandbox"`
	Usage     toolUsageRequest `json:"usage"`
}

type toolUsageRequest struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemoryMB   int64   `json:"memory_mb"`
	DiskMB     int64   `json:"disk_mb"`
}

func (t toolUsageRequest) toUsage() sandbox.ResourceUsage {
	usage := sandbox.ResourceUsage{}
	if t.CPUPercent > 0 {
		usage.CPUPercent = t.CPUPercent
	}
	if t.MemoryMB > 0 {
		usage.MemoryBytes = uint64(t.MemoryMB) * 1024 * 1024
	}
	if t.DiskMB > 0 {
		usage.DiskBytes = uint64(t.DiskMB) * 1024 * 1024
	}
	return usage
}

type runResponse struct {
	SessionID  string            `json:"session_id"`
	Output     string            `json:"output"`
	StopReason string            `json:"stop_reason"`
	Usage      model.Usage       `json:"usage"`
	Tags       map[string]string `json:"tags"`
	ToolCalls  []model.ToolCall  `json:"tool_calls"`
	Sandbox    api.SandboxReport `json:"sandbox"`
}

type toolResponse struct {
	Name       string      `json:"name"`
	Success    bool        `json:"success"`
	Output     string      `json:"output"`
	Data       interface{} `json:"data,omitempty"`
	DurationMs int64       `json:"duration_ms"`
}

type streamPayload struct {
	Type     string       `json:"type"`
	Message  string       `json:"message,omitempty"`
	Error    string       `json:"error,omitempty"`
	Response *runResponse `json:"response,omitempty"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (s *exampleServer) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func cloneMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}
