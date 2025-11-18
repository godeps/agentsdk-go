package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cexll/agentsdk-go/pkg/api"
	modelpkg "github.com/cexll/agentsdk-go/pkg/model"
	"github.com/cexll/agentsdk-go/pkg/sandbox"
	"github.com/cexll/agentsdk-go/pkg/tool"
	toolbuiltin "github.com/cexll/agentsdk-go/pkg/tool/builtin"
)

func main() {
	cfg, err := loadAppConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	server, err := newExampleServer(cfg.Server)
	if err != nil {
		log.Fatalf("init server: %v", err)
	}

	mux := http.NewServeMux()
	server.registerRoutes(mux)

	httpServer := &http.Server{
		Addr:              cfg.Address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("HTTP agent server listening on %s", cfg.Address)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server stopped unexpectedly: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("graceful shutdown failed: %v", err)
	}
	log.Println("server exited cleanly")
}

type appConfig struct {
	Address string
	Server  serverConfig
}

type serverConfig struct {
	ProjectRoot    string
	SandboxRoot    string
	ModelFactory   api.ModelFactory
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	NetworkAllow   []string
	ResourceLimit  sandbox.ResourceLimits
	MaxBodyBytes   int64
	MaxSessions    int
}

func newExampleServer(cfg serverConfig) (*exampleServer, error) {
	if cfg.ModelFactory == nil {
		return nil, errors.New("model factory is required")
	}
	projectRoot := normalizePath(cfg.ProjectRoot)
	sandboxRoot := normalizePath(cfg.SandboxRoot)
	if sandboxRoot == "" {
		sandboxRoot = projectRoot
	}
	tools, executor, err := buildToolExecutor(projectRoot)
	if err != nil {
		return nil, err
	}

	maxSessions := cfg.MaxSessions
	if maxSessions <= 0 {
		maxSessions = 500
	}

	allowed := dedupStrings([]string{projectRoot, sandboxRoot})
	baseOptions := api.Options{
		EntryPoint:  api.EntryPointPlatform,
		ProjectRoot: projectRoot,
		Mode: api.ModeContext{
			EntryPoint: api.EntryPointPlatform,
			Platform: &api.PlatformContext{
				Organization: "agentsdk-go",
				Project:      "http-example",
				Environment:  "dev",
			},
		},
		ModelFactory: cfg.ModelFactory,
		Tools:        tools,
		MaxSessions:  maxSessions,
		Sandbox: api.SandboxOptions{
			Root:          sandboxRoot,
			AllowedPaths:  allowed,
			NetworkAllow:  dedupStrings(cfg.NetworkAllow),
			ResourceLimit: cfg.ResourceLimit,
		},
	}

	defaultTimeout := cfg.DefaultTimeout
	if defaultTimeout <= 0 {
		defaultTimeout = 45 * time.Second
	}
	maxTimeout := cfg.MaxTimeout
	if maxTimeout <= 0 {
		maxTimeout = 2 * time.Minute
	}
	if maxTimeout < defaultTimeout {
		maxTimeout = defaultTimeout
	}

	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}

	return &exampleServer{
		baseOptions:    baseOptions,
		toolExecutor:   executor,
		defaultTimeout: defaultTimeout,
		maxTimeout:     maxTimeout,
		maxBodyBytes:   maxBody,
	}, nil
}

func buildToolExecutor(projectRoot string) ([]tool.Tool, *tool.Executor, error) {
	registry := tool.NewRegistry()
	toolImpls := []tool.Tool{
		toolbuiltin.NewBashToolWithRoot(projectRoot),
		toolbuiltin.NewFileToolWithRoot(projectRoot),
	}
	for _, impl := range toolImpls {
		if err := registry.Register(impl); err != nil {
			return nil, nil, err
		}
	}
	return toolImpls, tool.NewExecutor(registry, nil), nil
}

func loadAppConfig() (appConfig, error) {
	addr := strings.TrimSpace(envDefault("AGENTSDK_HTTP_ADDR", ":8080"))
	projectRoot := envPath("AGENTSDK_PROJECT_ROOT", ".")
	sandboxRoot := envPath("AGENTSDK_SANDBOX_ROOT", projectRoot)
	modelName := strings.TrimSpace(envDefault("AGENTSDK_MODEL", "claude-3-5-sonnet-20241022"))
	defaultTimeout := envDuration("AGENTSDK_DEFAULT_TIMEOUT", 45*time.Second)
	maxTimeout := envDuration("AGENTSDK_MAX_TIMEOUT", 2*time.Minute)
	cpuLimit := envFloat("AGENTSDK_RESOURCE_CPU_PERCENT", 85)
	memLimit := envMegabytes("AGENTSDK_RESOURCE_MEMORY_MB", 1536)
	diskLimit := envMegabytes("AGENTSDK_RESOURCE_DISK_MB", 2048)
	maxBody := int64(envUint("AGENTSDK_MAX_BODY_BYTES", uint64(defaultMaxBodyBytes)))
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}
	maxSessions := envUint("AGENTSDK_MAX_SESSIONS", 500)
	if maxSessions == 0 {
		maxSessions = 500
	}
	provider := &modelpkg.AnthropicProvider{
		ModelName: modelName,
		CacheTTL:  5 * time.Minute,
	}
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
		log.Println("warning: ANTHROPIC_API_KEY is not set; requests will fail until it is configured")
	}

	cfg := appConfig{
		Address: addr,
		Server: serverConfig{
			ProjectRoot:    projectRoot,
			SandboxRoot:    sandboxRoot,
			ModelFactory:   provider,
			DefaultTimeout: defaultTimeout,
			MaxTimeout:     maxTimeout,
			NetworkAllow:   envList("AGENTSDK_NETWORK_ALLOW", []string{"api.anthropic.com"}),
			ResourceLimit: sandbox.ResourceLimits{
				MaxCPUPercent:  cpuLimit,
				MaxMemoryBytes: memLimit * 1024 * 1024,
				MaxDiskBytes:   diskLimit * 1024 * 1024,
			},
			MaxBodyBytes: maxBody,
			MaxSessions:  int(maxSessions),
		},
	}
	return cfg, nil
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envPath(key, fallback string) string {
	value := envDefault(key, fallback)
	return normalizePath(value)
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if dur, err := time.ParseDuration(raw); err == nil {
		return dur
	}
	if ms, err := strconv.Atoi(raw); err == nil {
		return time.Duration(ms) * time.Millisecond
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if val, err := strconv.ParseFloat(raw, 64); err == nil {
		return val
	}
	return fallback
}

func envUint(key string, fallback uint64) uint64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if val, err := strconv.ParseUint(raw, 10, 64); err == nil {
		return val
	}
	return fallback
}

func envMegabytes(key string, fallback uint64) uint64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value := strings.ToLower(raw)
	value = strings.TrimSuffix(value, "mb")
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if val, err := strconv.ParseUint(value, 10, 64); err == nil {
		return val
	}
	return fallback
}

func envList(key string, fallback []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
