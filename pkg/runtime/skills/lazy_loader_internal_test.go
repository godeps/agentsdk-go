package skills

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests live in the skills package to get coverage on lazy-loading internals.

func TestHandlerLazyLoadsOnFirstExecute(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "lazy")

	writeSkill(t, filepath.Join(dir, "SKILL.md"), "lazy", "lazy body")
	mustWrite(t, filepath.Join(dir, "scripts", "setup.sh"), "echo hi")

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 reg, got %d", len(regs))
	}

	lazy := requireLazyHandler(t, regs[0].Handler)
	callCount := trackLoaderCalls(lazy)
	if got := callCount(); got != 0 {
		t.Fatalf("expected zero loader calls before execute, got %d", got)
	}

	res, err := regs[0].Handler.Execute(context.Background(), ActivationContext{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	output, ok := res.Output.(map[string]any)
	require.True(t, ok)
	if output["body"] != "lazy body" {
		t.Fatalf("unexpected body: %#v", output["body"])
	}
	support, ok := output["support_files"].(map[string][]string)
	require.True(t, ok)
	require.Equal(t, []string{"setup.sh"}, support["scripts"])

	if got := callCount(); got != 1 {
		t.Fatalf("expected single loader invocation, got %d", got)
	}
}

func TestHandlerCachesLoadResult(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "cache")
	writeSkill(t, filepath.Join(dir, "SKILL.md"), "cache", "cache body")

	regs, _ := LoadFromFS(LoaderOptions{ProjectRoot: root})
	lazy := requireLazyHandler(t, regs[0].Handler)
	callCount := trackLoaderCalls(lazy)

	if _, err := lazy.Execute(context.Background(), ActivationContext{}); err != nil {
		t.Fatalf("first execute failed: %v", err)
	}
	if _, err := lazy.Execute(context.Background(), ActivationContext{}); err != nil {
		t.Fatalf("second execute failed: %v", err)
	}

	if got := callCount(); got != 1 {
		t.Fatalf("expected single loader execution, got %d", got)
	}
}

func TestHandlerConcurrentExecuteSingleLoad(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "concurrent")
	writeSkill(t, filepath.Join(dir, "SKILL.md"), "concurrent", "body")

	regs, _ := LoadFromFS(LoaderOptions{ProjectRoot: root})
	lazy := requireLazyHandler(t, regs[0].Handler)
	callCount := trackLoaderCalls(lazy)
	handler := Handler(lazy)

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if _, err := handler.Execute(context.Background(), ActivationContext{}); err != nil {
				t.Errorf("execute error: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := callCount(); got != 1 {
		t.Fatalf("expected single loader execution under concurrency, got %d", got)
	}
}

func TestHandlerLoadErrorIsCached(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "fail")
	writeSkill(t, filepath.Join(dir, "SKILL.md"), "fail", "body")

	regs, _ := LoadFromFS(LoaderOptions{ProjectRoot: root})
	lazy := requireLazyHandler(t, regs[0].Handler)

	var (
		mu    sync.Mutex
		calls int
	)
	lazy.loader = func() (Result, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return Result{}, errors.New("boom")
	}

	handler := Handler(lazy)

	if _, err := handler.Execute(context.Background(), ActivationContext{}); err == nil {
		t.Fatalf("expected error on load")
	}
	if _, err := handler.Execute(context.Background(), ActivationContext{}); err == nil {
		t.Fatalf("expected cached error on second execute")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected single load attempt, got %d", calls)
	}
}

func TestHandlerBodyLengthProbe(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "probe")
	body := "probe body"
	writeSkill(t, filepath.Join(dir, "SKILL.md"), "probe", body)

	regs, _ := LoadFromFS(LoaderOptions{ProjectRoot: root})
	handler := regs[0].Handler

	sizer, ok := handler.(interface {
		BodyLength() (int, bool)
	})
	if !ok {
		t.Fatalf("handler does not expose BodyLength")
	}
	if size, loaded := sizer.BodyLength(); loaded || size != 0 {
		t.Fatalf("expected unloaded body length probe to be zero, got size=%d loaded=%t", size, loaded)
	}

	if _, err := handler.Execute(context.Background(), ActivationContext{}); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if size, loaded := sizer.BodyLength(); !loaded || size != len(body) {
		t.Fatalf("expected loaded body length=%d loaded=%t, got %d %t", len(body), true, size, loaded)
	}
}

func requireLazyHandler(t *testing.T, handler Handler) *lazySkillHandler {
	t.Helper()
	lazy, ok := handler.(*lazySkillHandler)
	if !ok {
		t.Fatalf("expected *lazySkillHandler, got %T", handler)
	}
	return lazy
}

func trackLoaderCalls(lazy *lazySkillHandler) func() int {
	var (
		mu    sync.Mutex
		calls int
	)
	original := lazy.loader
	lazy.loader = func() (Result, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return original()
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}
