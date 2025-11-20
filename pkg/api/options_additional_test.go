package api

import (
	"path/filepath"
	"testing"
)

func TestWithMaxSessionsRespectsPositiveOnly(t *testing.T) {
	opts := Options{MaxSessions: 5}
	WithMaxSessions(42)(&opts)
	if opts.MaxSessions != 42 {
		t.Fatalf("expected max sessions updated, got %d", opts.MaxSessions)
	}
	WithMaxSessions(0)(&opts)
	if opts.MaxSessions != 42 {
		t.Fatalf("non-positive override should be ignored, got %d", opts.MaxSessions)
	}
}

func TestOptionsWithDefaultsPopulatesMissingFields(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTSDK_PROJECT_ROOT", root)

	raw := Options{ProjectRoot: "", SettingsPath: "  settings.json  "}
	applied := raw.withDefaults()
	if applied.EntryPoint != defaultEntrypoint || applied.Mode.EntryPoint != defaultEntrypoint {
		t.Fatalf("entrypoint defaults not applied: %+v", applied)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval symlink: %v", err)
	}
	if wantRoot == "" {
		wantRoot = root
	}
	if applied.ProjectRoot != wantRoot {
		t.Fatalf("project root not resolved: %s (want %s)", applied.ProjectRoot, wantRoot)
	}
	if applied.Sandbox.Root != applied.ProjectRoot {
		t.Fatalf("sandbox root should mirror project root, got %s", applied.Sandbox.Root)
	}
	if applied.MaxSessions != defaultMaxSessions {
		t.Fatalf("expected default max sessions, got %d", applied.MaxSessions)
	}
	if len(applied.Sandbox.NetworkAllow) == 0 {
		t.Fatalf("network allow list not defaulted")
	}
	if !filepath.IsAbs(applied.SettingsPath) {
		t.Fatalf("settings path not absolutised: %s", applied.SettingsPath)
	}
}
