package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestLoadFromFS_IgnoresUserDir(t *testing.T) {
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
	if len(regs) != 1 {
		t.Fatalf("expected only project registrations, got %d", len(regs))
	}

	project := findRegistration(t, regs, "project-skill")
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
	for _, reg := range regs {
		if reg.Definition.Name == "user-skill" {
			t.Fatalf("user skills should be ignored")
		}
	}
}

func TestLoadFromFS_NoProjectDir(t *testing.T) {
	projectRoot := t.TempDir()
	userHome := t.TempDir()

	writeSkill(t, filepath.Join(userHome, ".claude", "skills", "user", "SKILL.md"), "user", "body")

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: projectRoot, UserHome: userHome, EnableUser: true})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(regs) != 0 {
		t.Fatalf("expected no registrations, got %d", len(regs))
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
	mustWrite(t, filepath.Join(dir, "scripts", "generate.sh"), "script")
	mustWrite(t, filepath.Join(dir, "references", "api.md"), "reference")
	mustWrite(t, filepath.Join(dir, "assets", "logo.png"), "asset")

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
	support, ok := output["support_files"].(map[string][]string)
	if !ok {
		t.Fatalf("expected support files map, got %T", output["support_files"])
	}
	if got := support["scripts"]; len(got) != 1 || got[0] != "generate.sh" {
		t.Fatalf("unexpected scripts index: %v", got)
	}
	if got := support["references"]; len(got) != 1 || got[0] != "api.md" {
		t.Fatalf("unexpected references index: %v", got)
	}
	if got := support["assets"]; len(got) != 1 || got[0] != "logo.png" {
		t.Fatalf("unexpected assets index: %v", got)
	}
}

func TestLoadFromFS_ProjectPathNotDirectory(t *testing.T) {
	root := t.TempDir()
	skillsPath := filepath.Join(root, ".claude", "skills")
	if err := os.MkdirAll(filepath.Dir(skillsPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(skillsPath, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(regs) != 0 {
		t.Fatalf("expected no registrations, got %d", len(regs))
	}
	if !hasError(errs, "not a directory") {
		t.Fatalf("expected not a directory error, got %v", errs)
	}
}

func TestLoadFromFS_SupportDirError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "broken-support")

	writeSkill(t, filepath.Join(dir, "SKILL.md"), "broken-support", "body")
	mustWrite(t, filepath.Join(dir, "scripts"), "not a directory")

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: root})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors during load: %v", errs)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}

	_, err := regs[0].Handler.Execute(context.Background(), ActivationContext{})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected support dir error, got %v", err)
	}
}

func TestLoadFromFS_Errors(t *testing.T) {
	projectRoot := t.TempDir()

	// Valid skills
	writeSkill(t, filepath.Join(projectRoot, ".claude", "skills", "good", "SKILL.md"), "good", "ok")
	writeSkill(t, filepath.Join(projectRoot, ".claude", "skills", "unique", "SKILL.md"), "unique", "ok")

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

	regs, errs := LoadFromFS(LoaderOptions{ProjectRoot: projectRoot})
	if len(regs) != 2 {
		t.Fatalf("expected 2 valid registrations, got %d", len(regs))
	}
	if len(errs) < 2 {
		t.Fatalf("expected aggregated errors, got %v", errs)
	}
	if !hasError(errs, "missing YAML frontmatter") {
		t.Fatalf("missing frontmatter error: %v", errs)
	}
	if !hasError(errs, "invalid name") {
		t.Fatalf("missing invalid name error: %v", errs)
	}
}

func TestSkillName_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"a", true},
		{"ab", true},
		{"-abc", false},
		{"abc-", false},
		{"abc_def", false},
		{"ABC", false},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
	}

	for _, tt := range tests {
		got := isValidSkillName(tt.name)
		if got != tt.valid {
			t.Fatalf("name %q: expected valid=%v, got %v", tt.name, tt.valid, got)
		}
	}
}

func TestCompatibility_MaxLength(t *testing.T) {
	ok := SkillMetadata{
		Name:          "ok",
		Description:   "desc",
		Compatibility: strings.Repeat("a", 500),
	}
	if err := validateMetadata(ok); err != nil {
		t.Fatalf("expected 500 chars to pass, got %v", err)
	}

	tooLong := ok
	tooLong.Compatibility = strings.Repeat("a", 501)
	if err := validateMetadata(tooLong); err == nil || !strings.Contains(err.Error(), "compatibility") {
		t.Fatalf("expected compatibility length error, got %v", err)
	}
}

func TestSupportFiles_OnlyReturnsIndex(t *testing.T) {
	dir := t.TempDir()

	mustWrite(t, filepath.Join(dir, "scripts", "setup.sh"), "echo hi")
	mustWrite(t, filepath.Join(dir, "references", "api.md"), "secret")
	mustWrite(t, filepath.Join(dir, "assets", "logo.png"), "pngdata")

	support, errs := loadSupportFiles(dir)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if support == nil {
		t.Fatalf("expected support index map")
	}

	if got := support["scripts"]; len(got) != 1 || got[0] != "setup.sh" {
		t.Fatalf("unexpected scripts index: %v", support["scripts"])
	}
	if got := support["references"]; len(got) != 1 || got[0] != "api.md" {
		t.Fatalf("unexpected references index: %v", support["references"])
	}
	if got := support["assets"]; len(got) != 1 || got[0] != "logo.png" {
		t.Fatalf("unexpected assets index: %v", support["assets"])
	}

	for _, list := range support {
		for _, entry := range list {
			if entry == "echo hi" || entry == "secret" || entry == "pngdata" {
				t.Fatalf("support files must be an index, got content %q", entry)
			}
		}
	}
}

func TestToolList_UnmarshalYAML(t *testing.T) {
	type wrapper struct {
		Allowed ToolList `yaml:"allowed-tools"`
	}

	var scalar wrapper
	if err := yaml.Unmarshal([]byte("allowed-tools: gofmt, grep ,gofmt"), &scalar); err != nil {
		t.Fatalf("unmarshal scalar: %v", err)
	}
	if got := []string(scalar.Allowed); strings.Join(got, ",") != "gofmt,grep" {
		t.Fatalf("unexpected scalar tools: %v", got)
	}

	var seq wrapper
	if err := yaml.Unmarshal([]byte("allowed-tools:\n  - gofmt\n  - grep\n  - gofmt\n"), &seq); err != nil {
		t.Fatalf("unmarshal sequence: %v", err)
	}
	if got := []string(seq.Allowed); strings.Join(got, ",") != "gofmt,grep" {
		t.Fatalf("unexpected sequence tools: %v", got)
	}

	var null wrapper
	if err := yaml.Unmarshal([]byte("allowed-tools: null\n"), &null); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	if null.Allowed != nil {
		t.Fatalf("expected nil tools for null, got %v", null.Allowed)
	}

	var bad wrapper
	if err := yaml.Unmarshal([]byte("allowed-tools:\n  k: v\n"), &bad); err == nil {
		t.Fatalf("expected error for mapping allowed-tools")
	}
}

func TestBuildDefinitionMetadata_MergesAndNormalizes(t *testing.T) {
	if got := buildDefinitionMetadata(SkillFile{}); got != nil {
		t.Fatalf("expected nil metadata for empty file, got %v", got)
	}

	file := SkillFile{
		Path: "/tmp/skill/SKILL.md",
		Metadata: SkillMetadata{
			AllowedTools:  ToolList{"grep", "file_read"},
			License:       " MIT ",
			Compatibility: " go>=1.22 ",
			Metadata: map[string]string{
				"owner":  "examples",
				"source": "fake",
			},
		},
	}
	got := buildDefinitionMetadata(file)
	if got["owner"] != "examples" {
		t.Fatalf("expected owner metadata, got %v", got)
	}
	if got["allowed-tools"] != "grep,file_read" {
		t.Fatalf("unexpected allowed-tools: %v", got["allowed-tools"])
	}
	if got["license"] != "MIT" {
		t.Fatalf("unexpected license: %v", got["license"])
	}
	if got["compatibility"] != "go>=1.22" {
		t.Fatalf("unexpected compatibility: %v", got["compatibility"])
	}
	if got["source"] != file.Path {
		t.Fatalf("expected source override, got %v", got["source"])
	}
}

func TestLoadSkillDir_OnlyScansOneLevel(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, ".claude", "skills")

	writeSkill(t, filepath.Join(skillsRoot, "good", "SKILL.md"), "good", "body")
	if err := os.MkdirAll(filepath.Join(skillsRoot, "empty"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSkill(t, filepath.Join(skillsRoot, "outer", "inner", "SKILL.md"), "inner", "body")

	files, errs := loadSkillDir(skillsRoot, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(files) != 1 || files[0].Metadata.Name != "good" {
		t.Fatalf("expected only good to load, got %v", files)
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
