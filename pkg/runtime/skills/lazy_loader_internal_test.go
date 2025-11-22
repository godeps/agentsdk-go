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
	mustWrite(t, filepath.Join(dir, "reference.md"), "ref")

	calls := map[string]int{}
	var mu sync.Mutex
	original := readFile
	restore := SetReadFileForTest(func(path string) ([]byte, error) {
		mu.Lock()
		calls[path]++
		mu.Unlock()
		return original(path)
	})
	defer restore()

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 reg, got %d", len(regs))
	}

	// Startup should not have touched the body or support files.
	if len(calls) != 0 {
		t.Fatalf("expected no readFile calls before execute, got %v", calls)
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

	mu.Lock()
	defer mu.Unlock()
	if calls[filepath.Join(dir, "SKILL.md")] != 1 {
		t.Fatalf("expected SKILL.md to be read once, got %d", calls[filepath.Join(dir, "SKILL.md")])
	}
	if calls[filepath.Join(dir, "reference.md")] != 1 {
		t.Fatalf("expected reference.md to be read once, got %d", calls[filepath.Join(dir, "reference.md")])
	}
}

func TestHandlerCachesLoadResult(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "cache")
	writeSkill(t, filepath.Join(dir, "SKILL.md"), "cache", "cache body")

	var mu sync.Mutex
	calls := map[string]int{}
	original := readFile
	restore := SetReadFileForTest(func(path string) ([]byte, error) {
		mu.Lock()
		calls[path]++
		mu.Unlock()
		return original(path)
	})
	defer restore()

	regs, _ := LoadFromFS(LoaderOptions{ProjectRoot: root})
	res := regs[0].Handler
	if _, err := res.Execute(context.Background(), ActivationContext{}); err != nil {
		t.Fatalf("first execute failed: %v", err)
	}
	if _, err := res.Execute(context.Background(), ActivationContext{}); err != nil {
		t.Fatalf("second execute failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls[filepath.Join(dir, "SKILL.md")] != 1 {
		t.Fatalf("expected single SKILL.md read, got %d", calls[filepath.Join(dir, "SKILL.md")])
	}
}

func TestHandlerConcurrentExecuteSingleLoad(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "concurrent")
	writeSkill(t, filepath.Join(dir, "SKILL.md"), "concurrent", "body")

	var mu sync.Mutex
	calls := map[string]int{}
	original := readFile
	restore := SetReadFileForTest(func(path string) ([]byte, error) {
		mu.Lock()
		calls[path]++
		mu.Unlock()
		return original(path)
	})
	defer restore()

	regs, _ := LoadFromFS(LoaderOptions{ProjectRoot: root})
	handler := regs[0].Handler

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

	mu.Lock()
	defer mu.Unlock()
	if calls[filepath.Join(dir, "SKILL.md")] != 1 {
		t.Fatalf("expected single SKILL.md read under concurrency, got %d", calls[filepath.Join(dir, "SKILL.md")])
	}
}

func TestHandlerLoadErrorIsCached(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "fail")
	writeSkill(t, filepath.Join(dir, "SKILL.md"), "fail", "body")

	var mu sync.Mutex
	calls := map[string]int{}
	original := readFile
	restore := SetReadFileForTest(func(path string) ([]byte, error) {
		mu.Lock()
		calls[path]++
		mu.Unlock()
		if filepath.Base(path) == "SKILL.md" {
			return nil, errors.New("boom")
		}
		return original(path)
	})
	defer restore()

	regs, _ := LoadFromFS(LoaderOptions{ProjectRoot: root})
	handler := regs[0].Handler

	if _, err := handler.Execute(context.Background(), ActivationContext{}); err == nil {
		t.Fatalf("expected error on load")
	}
	if _, err := handler.Execute(context.Background(), ActivationContext{}); err == nil {
		t.Fatalf("expected cached error on second execute")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls[filepath.Join(dir, "SKILL.md")] != 1 {
		t.Fatalf("expected single load attempt, got %d", calls[filepath.Join(dir, "SKILL.md")])
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
