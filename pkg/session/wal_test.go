package session

import (
	"path/filepath"
	"testing"

	"github.com/cexll/agentsdk-go/pkg/wal"
)

func TestChannelWALIsolation(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir, wal.WithDisabledSync())
	if err != nil {
		t.Fatalf("new wal: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	p1, err := w.Append(ChannelProgress, wal.Entry{Type: "progress", Data: []byte("p1")})
	if err != nil {
		t.Fatalf("append progress: %v", err)
	}
	if _, err := w.Append(ChannelControl, wal.Entry{Type: "control", Data: []byte("c1")}); err != nil {
		t.Fatalf("append control: %v", err)
	}

	var progress []string
	if err := w.ReadSince(ChannelProgress, p1, func(e wal.Entry) error {
		progress = append(progress, string(e.Data))
		return nil
	}); err != nil {
		t.Fatalf("read progress: %v", err)
	}
	if len(progress) != 1 || progress[0] != "p1" {
		t.Fatalf("unexpected progress entries: %+v", progress)
	}

	var control []string
	if err := w.ReadSince(ChannelControl, 0, func(e wal.Entry) error {
		control = append(control, string(e.Data))
		return nil
	}); err != nil {
		t.Fatalf("read control: %v", err)
	}
	if len(control) != 1 || control[0] != "c1" {
		t.Fatalf("unexpected control entries %+v", control)
	}

	if err := w.Rotate(ChannelControl); err != nil {
		t.Fatalf("rotate control: %v", err)
	}
	cursors := w.Snapshot()
	if cursors[ChannelProgress] != p1 {
		t.Fatalf("snapshot progress cursor = %d want %d", cursors[ChannelProgress], p1)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "control", "segment-*.wal"))
	if len(files) == 0 {
		t.Fatalf("expected control channel segments after rotate")
	}
}
