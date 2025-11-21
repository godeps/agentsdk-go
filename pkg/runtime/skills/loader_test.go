package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFromFS_Basic(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".claude", "skills", "alpha")
	skillBody := strings.Join([]string{
		"---",
		"name: alpha",
		"description: first skill",
		"---",
		"step 1",
		"step 2",
	}, "\n")
	mustWrite(t, filepath.Join(skillDir, "SKILL.md"), skillBody)

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}

	reg := regs[0]
	if reg.Definition.Name != "alpha" {
		t.Fatalf("unexpected name %q", reg.Definition.Name)
	}
	if reg.Definition.Description != "first skill" {
		t.Fatalf("unexpected description %q", reg.Definition.Description)
	}

	res, err := reg.Handler.Execute(context.Background(), ActivationContext{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	output, ok := res.Output.(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T", res.Output)
	}
	if output["body"] != "step 1\nstep 2" {
		t.Fatalf("unexpected body %q", output["body"])
	}
}

func TestLoadFromFS_Merge(t *testing.T) {
	projectRoot := t.TempDir()
	userHome := t.TempDir()

	projectSkill := filepath.Join(projectRoot, ".claude", "skills", "project-skill", "SKILL.md")
	userSkill := filepath.Join(userHome, ".claude", "skills", "user-skill", "SKILL.md")

	writeSkill(t, projectSkill, "project-skill", "project body")
	writeSkill(t, userSkill, "user-skill", "user body")

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: projectRoot, UserHome: userHome, EnableUser: true})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(regs) != 2 {
		t.Fatalf("expected 2 registrations, got %d", len(regs))
	}

	project := findRegistration(t, regs, "project-skill")
	user := findRegistration(t, regs, "user-skill")

	res, err := project.Handler.Execute(context.Background(), ActivationContext{})
	if err != nil {
		t.Fatalf("unexpected project result: %v %#v", err, res.Output)
	}
	projectOutput, ok := res.Output.(map[string]any)
	if !ok {
		t.Fatalf("project output should be map, got %T", res.Output)
	}
	if body, ok := projectOutput["body"].(string); !ok || body != "project body" {
		t.Fatalf("unexpected project result: %v %#v", err, res.Output)
	}
	res, err = user.Handler.Execute(context.Background(), ActivationContext{})
	if err != nil {
		t.Fatalf("unexpected user result: %v %#v", err, res.Output)
	}
	userOutput, ok := res.Output.(map[string]any)
	if !ok {
		t.Fatalf("user output should be map, got %T", res.Output)
	}
	if body, ok := userOutput["body"].(string); !ok || body != "user body" {
		t.Fatalf("unexpected user result: %v %#v", err, res.Output)
	}
}

func TestLoadFromFS_YAML(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "fmt")
	body := strings.Join([]string{
		"---",
		"name: fmt",
		"description: format code",
		"allowed-tools: gofmt,sed",
		"---",
		"run gofmt -w .",
	}, "\n")
	mustWrite(t, filepath.Join(dir, "SKILL.md"), body)

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}

	reg := regs[0]
	if reg.Definition.Description != "format code" {
		t.Fatalf("unexpected description %q", reg.Definition.Description)
	}
	if reg.Definition.Metadata["allowed-tools"] != "gofmt,sed" {
		t.Fatalf("missing allowed-tools metadata: %#v", reg.Definition.Metadata)
	}
	if reg.Definition.Metadata["source"] == "" {
		t.Fatalf("expected source metadata")
	}
}

func TestLoadFromFS_SupportFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "doc")

	writeSkill(t, filepath.Join(dir, "SKILL.md"), "doc", "body")
	mustWrite(t, filepath.Join(dir, "reference.md"), "reference")
	mustWrite(t, filepath.Join(dir, "examples.md"), "examples")
	mustWrite(t, filepath.Join(dir, "scripts", "generate.sh"), "script")
	mustWrite(t, filepath.Join(dir, "templates", "page.txt"), "template")

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}

	res, err := regs[0].Handler.Execute(context.Background(), ActivationContext{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	output, ok := res.Output.(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T", res.Output)
	}
	support, ok := output["support_files"].(map[string]string)
	if !ok {
		t.Fatalf("expected support files map, got %T", output["support_files"])
	}
	want := []string{"reference.md", "examples.md", "scripts/generate.sh", "templates/page.txt"}
	for _, key := range want {
		if _, ok := support[key]; !ok {
			t.Fatalf("missing support file %s in %v", key, support)
		}
	}
}

func TestLoadFromFS_Errors(t *testing.T) {
	projectRoot := t.TempDir()
	userHome := t.TempDir()

	// Valid skills
	writeSkill(t, filepath.Join(projectRoot, ".claude", "skills", "good", "SKILL.md"), "good", "ok")
	writeSkill(t, filepath.Join(projectRoot, ".claude", "skills", "unique", "SKILL.md"), "unique", "ok")

	// Duplicate across user/project
	writeSkill(t, filepath.Join(userHome, ".claude", "skills", "good", "SKILL.md"), "good", "user")

	// Invalid cases
	mustWrite(t, filepath.Join(projectRoot, ".claude", "skills", "broken", "SKILL.md"), "no frontmatter")
	mustWrite(t, filepath.Join(projectRoot, ".claude", "skills", "BAD", "SKILL.md"), strings.Join([]string{
		"---",
		"name: BAD",
		"description: bad name",
		"---",
		"body",
	}, "\n"))
	mustWrite(t, filepath.Join(projectRoot, ".claude", "skills", "malformed", "SKILL.md"), "---\nname: malformed\n")

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: projectRoot, UserHome: userHome, EnableUser: true})
	if len(regs) != 2 {
		t.Fatalf("expected 2 valid registrations, got %d", len(regs))
	}
	if len(errs) < 3 {
		t.Fatalf("expected aggregated errors, got %v", errs)
	}
	if !hasError(errs, "duplicate skill") {
		t.Fatalf("missing duplicate warning: %v", errs)
	}
	if !hasError(errs, "missing YAML frontmatter") {
		t.Fatalf("missing frontmatter error: %v", errs)
	}
	if !hasError(errs, "invalid name") {
		t.Fatalf("missing invalid name error: %v", errs)
	}
}

func writeSkill(t *testing.T, path, name, body string) {
	t.Helper()
	content := strings.Join([]string{
		"---",
		"name: " + name,
		"description: desc for " + name,
		"---",
		body,
	}, "\n")
	mustWrite(t, path, content)
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func findRegistration(t *testing.T, regs []SkillRegistration, name string) SkillRegistration {
	t.Helper()
	for _, reg := range regs {
		if reg.Definition.Name == name {
			return reg
		}
	}
	t.Fatalf("registration %s not found", name)
	return SkillRegistration{}
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
