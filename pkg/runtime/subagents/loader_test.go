package subagents

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadFromFS_Basic(t *testing.T) {
	root := t.TempDir()
	content := strings.Join([]string{
		"---",
		"name: helper",
		"description: basic helper",
		"---",
		"System prompt body",
	}, "\n")
	mustWrite(t, root, ".claude/agents/helper.md", content)

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}

	reg := findRegistration(t, regs, "helper")
	if reg.Definition.Description != "basic helper" {
		t.Fatalf("unexpected description: %+v", reg.Definition)
	}
	res, err := reg.Handler.Handle(context.Background(), Context{}, Request{Instruction: "run"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.Output != "System prompt body" {
		t.Fatalf("unexpected output %q", res.Output)
	}
}

func TestLoadFromFS_Priority(t *testing.T) {
	projectRoot := t.TempDir()
	userHome := t.TempDir()

	mustWrite(t, userHome, ".claude/agents/shared.md", strings.Join([]string{
		"---",
		"name: shared",
		"description: user def",
		"---",
		"user prompt",
	}, "\n"))
	mustWrite(t, userHome, ".claude/agents/user-only.md", strings.Join([]string{
		"---",
		"name: user-only",
		"description: only user",
		"---",
		"user only prompt",
	}, "\n"))
	mustWrite(t, projectRoot, ".claude/agents/shared.md", strings.Join([]string{
		"---",
		"name: shared",
		"description: project def",
		"---",
		"project prompt",
	}, "\n"))

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: projectRoot, UserHome: userHome, EnableUser: true})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(regs) != 2 {
		t.Fatalf("expected 2 registrations, got %d", len(regs))
	}

	shared := findRegistration(t, regs, "shared")
	res, err := shared.Handler.Handle(context.Background(), Context{}, Request{Instruction: "go"})
	if err != nil || res.Output != "project prompt" {
		t.Fatalf("expected project prompt, got %v %q", err, res.Output)
	}

	userOnly := findRegistration(t, regs, "user-only")
	res, err = userOnly.Handler.Handle(context.Background(), Context{}, Request{Instruction: "go"})
	if err != nil || res.Output != "user only prompt" {
		t.Fatalf("unexpected user-only output: %v %q", err, res.Output)
	}
}

func TestLoadFromFS_YAML(t *testing.T) {
	root := t.TempDir()
	body := strings.Join([]string{
		"---",
		"name: custom-agent",
		"description: greeting agent",
		"tools: read, write",
		"model: haiku",
		"permissionMode: plan",
		"skills: docs, go ",
		"---",
		"## prompt body",
	}, "\n")
	path := mustWrite(t, root, ".claude/agents/ignored.md", body)

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}

	reg := regs[0]
	if reg.Definition.Name != "custom-agent" || reg.Definition.Description != "greeting agent" {
		t.Fatalf("unexpected definition: %+v", reg.Definition)
	}
	if !reflect.DeepEqual(reg.Definition.BaseContext.ToolWhitelist, []string{"read", "write"}) {
		t.Fatalf("unexpected whitelist: %+v", reg.Definition.BaseContext.ToolWhitelist)
	}
	if reg.Definition.BaseContext.Model != "haiku" || reg.Definition.DefaultModel != "haiku" {
		t.Fatalf("model not propagated: %+v", reg.Definition)
	}

	res, err := reg.Handler.Handle(context.Background(), Context{}, Request{Instruction: "run"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.Output != "## prompt body" {
		t.Fatalf("unexpected output %q", res.Output)
	}
	if res.Metadata == nil {
		t.Fatalf("expected metadata")
	}
	if src, ok := res.Metadata["source"]; !ok || src != path {
		t.Fatalf("missing source metadata: %#v", res.Metadata)
	}
	if pm := res.Metadata["permission-mode"]; pm != "plan" {
		t.Fatalf("permission-mode mismatch: %#v", res.Metadata)
	}
	if tools, ok := res.Metadata["tools"].([]string); !ok || !reflect.DeepEqual(tools, []string{"read", "write"}) {
		t.Fatalf("tools metadata mismatch: %#v", res.Metadata)
	}
	if skills, ok := res.Metadata["skills"].([]string); !ok || !reflect.DeepEqual(skills, []string{"docs", "go"}) {
		t.Fatalf("skills metadata mismatch: %#v", res.Metadata)
	}
}

func TestLoadFromFS_MetadataParsing(t *testing.T) {
	root := t.TempDir()
	body := strings.Join([]string{
		"---",
		"name: worker",
		"description: with lists",
		"tools: read, read, bash",
		"skills: Go, docs, go",
		"model: inherit",
		"permissionMode: default",
		"---",
		"list body",
	}, "\n")
	mustWrite(t, root, ".claude/agents/worker.md", body)

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}
	reg := regs[0]
	if !reflect.DeepEqual(reg.Definition.BaseContext.ToolWhitelist, []string{"bash", "read"}) {
		t.Fatalf("tool whitelist mismatch: %+v", reg.Definition.BaseContext.ToolWhitelist)
	}
	if reg.Definition.BaseContext.Model != "" || reg.Definition.DefaultModel != "" {
		t.Fatalf("inherit model should be empty, got %+v", reg.Definition)
	}
	res, err := reg.Handler.Handle(context.Background(), Context{}, Request{Instruction: "go"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if pm := res.Metadata["permission-mode"]; pm != "default" {
		t.Fatalf("expected permission-mode default, got %#v", res.Metadata)
	}
	if skills, ok := res.Metadata["skills"].([]string); !ok || !reflect.DeepEqual(skills, []string{"docs", "go"}) {
		t.Fatalf("skills metadata mismatch: %#v", res.Metadata)
	}
}

func TestLoadFromFS_Errors(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, ".claude/agents/bad name.md", strings.Join([]string{
		"---",
		"description: missing name uses fallback",
		"---",
		"body",
	}, "\n"))
	mustWrite(t, root, ".claude/agents/broken.md", "---\nname: ok\n")
	mustWrite(t, root, ".claude/agents/good.md", strings.Join([]string{
		"---",
		"name: good",
		"description: ok",
		"---",
		"good body",
	}, "\n"))

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(regs) != 1 {
		t.Fatalf("expected only good file loaded, got %d", len(regs))
	}
	if len(errs) < 2 {
		t.Fatalf("expected aggregated errors, got %v", errs)
	}
	if !hasError(errs, "invalid name") {
		t.Fatalf("missing invalid name error: %v", errs)
	}
	if !hasError(errs, "missing closing frontmatter") && !hasError(errs, "decode YAML") {
		t.Fatalf("missing frontmatter error: %v", errs)
	}
}

func mustWrite(t *testing.T, root, relative, content string) string {
	t.Helper()
	path := join(root, relative)
	if err := makeDirs(path); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return path
}

func makeDirs(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0o755)
}

func join(parts ...string) string {
	return filepath.Join(parts...)
}

func findRegistration(t *testing.T, regs []SubagentRegistration, name string) SubagentRegistration {
	t.Helper()
	for _, reg := range regs {
		if reg.Definition.Name == name {
			return reg
		}
	}
	t.Fatalf("registration %s not found", name)
	return SubagentRegistration{}
}

func hasError(errs []error, substr string) bool {
	for _, err := range errs {
		if err == nil {
			continue
		}
		if strings.Contains(err.Error(), substr) {
			return true
		}
	}
	return false
}
