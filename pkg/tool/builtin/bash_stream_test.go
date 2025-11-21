package toolbuiltin

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashToolStreamExecuteEmitsIncrementally(t *testing.T) {
	skipIfWindows(t)
	dir := cleanTempDir(t)
	script := writeScript(t, dir, "stream.sh", "#!/bin/sh\nsleep 0.1\necho first\nsleep 0.2\necho second 1>&2\n")

	tool := NewBashToolWithRoot(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	type chunk struct {
		text   string
		stderr bool
	}
	chunks := make(chan chunk, 4)

	done := make(chan struct{})
	var resultOutput string
	var execErr error
	go func() {
		res, err := tool.StreamExecute(ctx, map[string]any{
			"command": "./" + filepath.Base(script),
			"workdir": dir,
		}, func(text string, isStderr bool) {
			chunks <- chunk{text: text, stderr: isStderr}
		})
		if res != nil {
			resultOutput = strings.TrimSpace(res.Output)
		}
		execErr = err
		close(done)
	}()

	var first chunk
	select {
	case first = <-chunks:
	case <-time.After(3 * time.Second):
		t.Fatalf("did not receive streaming chunk before timeout")
	}
	if first.text != "first" || first.stderr {
		t.Fatalf("unexpected first chunk %+v", first)
	}

	<-done
	if execErr != nil {
		t.Fatalf("StreamExecute returned error: %v", execErr)
	}

	drained := []chunk{first}
	for len(chunks) > 0 {
		drained = append(drained, <-chunks)
	}
	if len(drained) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(drained))
	}
	if drained[1].text != "second" || !drained[1].stderr {
		t.Fatalf("unexpected second chunk %+v", drained[1])
	}
	if resultOutput != "first\nsecond" {
		t.Fatalf("unexpected final output %q", resultOutput)
	}
}

func TestBashToolStreamExecuteRespectsContextCancel(t *testing.T) {
	skipIfWindows(t)
	dir := cleanTempDir(t)
	tool := NewBashToolWithRoot(dir)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := tool.StreamExecute(ctx, map[string]any{
		"command": "sleep 2",
		"workdir": dir,
	}, nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestBashToolStreamExecuteOutputLimit(t *testing.T) {
	skipIfWindows(t)
	dir := cleanTempDir(t)
	tool := NewBashToolWithRoot(dir)

	res, err := tool.StreamExecute(context.Background(), map[string]any{
		"command": "printf '%.0sA' {1..40000}",
		"workdir": dir,
	}, nil)
	if err != nil {
		t.Fatalf("StreamExecute failed: %v", err)
	}
	if len(res.Output) != maxBashOutputLen {
		t.Fatalf("expected output length %d got %d", maxBashOutputLen, len(res.Output))
	}
}
