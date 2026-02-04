package hooks

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cexll/agentsdk-go/pkg/core/events"
)

func TestExecutorWithWorkDirAndClose(t *testing.T) {
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != "" {
		dir = resolved
	}
	exec := NewExecutor(WithWorkDir(dir))
	exec.Register(ShellHook{Event: events.Notification, Command: "pwd"})

	results, err := exec.Execute(context.Background(), events.Event{Type: events.Notification})
	if err != nil || len(results) == 0 {
		t.Fatalf("execute failed: %v", err)
	}
	if got := strings.TrimSpace(results[0].Stdout); got != dir {
		t.Fatalf("expected workdir %q, got %q", dir, got)
	}

	exec.Close()
}
