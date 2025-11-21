package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoaderOptions controls how skills are discovered from the filesystem.
type LoaderOptions struct {
	ProjectRoot string
	// UserHome overrides the OS home directory when EnableUser is true.
	UserHome string
	// EnableUser toggles scanning ~/.claude/skills.
	EnableUser bool
}

// SkillFile captures an on-disk SKILL.md plus its support files.
type SkillFile struct {
	Name         string
	Path         string
	Metadata     SkillMetadata
	Body         string
	SupportFiles map[string]string
}

// SkillMetadata mirrors the YAML frontmatter fields inside SKILL.md.
type SkillMetadata struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	AllowedTools string `yaml:"allowed-tools"`
}

// SkillRegistration wires a definition to its handler.
type SkillRegistration struct {
	Definition Definition
	Handler    Handler
}

var skillNameRegexp = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// LoadFromFS loads skills from the filesystem. Errors are aggregated so one
// broken file will not block others. Duplicate names are skipped with a
// warning entry in the error list.
func LoadFromFS(opts LoaderOptions) ([]SkillRegistration, []error) {
	var (
		registrations []SkillRegistration
		errs          []error
		allFiles      []SkillFile
	)

	if opts.EnableUser {
		home := opts.UserHome
		if home == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				errs = append(errs, fmt.Errorf("skills: resolve user home: %w", err))
			} else {
				home = h
			}
		}
		if home != "" {
			userDir := filepath.Join(home, ".claude", "skills")
			files, loadErrs := loadSkillDir(userDir)
			errs = append(errs, loadErrs...)
			allFiles = append(allFiles, files...)
		}
	}

	projectDir := filepath.Join(opts.ProjectRoot, ".claude", "skills")
	files, loadErrs := loadSkillDir(projectDir)
	errs = append(errs, loadErrs...)
	allFiles = append(allFiles, files...)

	if len(allFiles) == 0 {
		return nil, errs
	}

	sort.Slice(allFiles, func(i, j int) bool {
		if allFiles[i].Metadata.Name != allFiles[j].Metadata.Name {
			return allFiles[i].Metadata.Name < allFiles[j].Metadata.Name
		}
		return allFiles[i].Path < allFiles[j].Path
	})

	seen := map[string]string{}
	for _, file := range allFiles {
		if prev, ok := seen[file.Metadata.Name]; ok {
			errs = append(errs, fmt.Errorf("skills: duplicate skill %q at %s (already from %s)", file.Metadata.Name, file.Path, prev))
			continue
		}
		seen[file.Metadata.Name] = file.Path

		def := Definition{
			Name:        file.Metadata.Name,
			Description: file.Metadata.Description,
			Metadata:    buildDefinitionMetadata(file),
		}
		reg := SkillRegistration{
			Definition: def,
			Handler:    buildHandler(file),
		}
		registrations = append(registrations, reg)
	}

	return registrations, errs
}

func loadSkillDir(root string) ([]SkillFile, []error) {
	var (
		results []SkillFile
		errs    []error
	)

	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("skills: stat %s: %w", root, err)}
	}
	if !info.IsDir() {
		return nil, []error{fmt.Errorf("skills: path %s is not a directory", root)}
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			errs = append(errs, fmt.Errorf("skills: walk %s: %w", path, walkErr))
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != "SKILL.md" {
			return nil
		}

		dirName := filepath.Base(filepath.Dir(path))
		file, parseErr := parseSkillFile(path, dirName)
		if parseErr != nil {
			errs = append(errs, parseErr)
			return nil
		}

		support, supportErrs := loadSupportFiles(filepath.Dir(path))
		errs = append(errs, supportErrs...)
		file.SupportFiles = support

		results = append(results, file)
		return nil
	})
	if walkErr != nil {
		errs = append(errs, walkErr)
	}
	return results, errs
}

func parseSkillFile(path, dirName string) (SkillFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SkillFile{}, fmt.Errorf("skills: read %s: %w", path, err)
	}
	meta, body, err := parseFrontMatter(string(data))
	if err != nil {
		return SkillFile{}, fmt.Errorf("skills: parse %s: %w", path, err)
	}
	if meta.Name != "" && dirName != "" && meta.Name != dirName {
		return SkillFile{}, fmt.Errorf("skills: name %q does not match directory %q in %s", meta.Name, dirName, path)
	}
	if err := validateMetadata(meta); err != nil {
		return SkillFile{}, fmt.Errorf("skills: validate %s: %w", path, err)
	}

	return SkillFile{
		Name:     meta.Name,
		Path:     path,
		Metadata: meta,
		Body:     body,
	}, nil
}

func parseFrontMatter(content string) (SkillMetadata, string, error) {
	trimmed := strings.TrimPrefix(content, "\uFEFF") // drop BOM if present
	lines := strings.Split(trimmed, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return SkillMetadata{}, "", errors.New("missing YAML frontmatter")
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return SkillMetadata{}, "", errors.New("missing closing frontmatter separator")
	}

	metaText := strings.Join(lines[1:end], "\n")
	var meta SkillMetadata
	if err := yaml.Unmarshal([]byte(metaText), &meta); err != nil {
		return SkillMetadata{}, "", fmt.Errorf("decode YAML: %w", err)
	}

	body := strings.Join(lines[end+1:], "\n")
	body = strings.TrimPrefix(body, "\n")

	return meta, body, nil
}

func validateMetadata(meta SkillMetadata) error {
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		return errors.New("name is required")
	}
	if !skillNameRegexp.MatchString(name) {
		return fmt.Errorf("invalid name %q", meta.Name)
	}
	desc := strings.TrimSpace(meta.Description)
	if desc == "" {
		return errors.New("description is required")
	}
	if len(desc) > 1024 {
		return errors.New("description exceeds 1024 characters")
	}
	return nil
}

func loadSupportFiles(dir string) (map[string]string, []error) {
	out := map[string]string{}
	var errs []error

	readOptional := func(name string) {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("skills: read %s: %w", path, err))
			}
			return
		}
		out[name] = string(data)
	}

	for _, file := range []string{"reference.md", "examples.md"} {
		readOptional(file)
	}

	for _, sub := range []string{"scripts", "templates"} {
		root := filepath.Join(dir, sub)
		info, err := os.Stat(root)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("skills: stat %s: %w", root, err))
			}
			continue
		}
		if !info.IsDir() {
			errs = append(errs, fmt.Errorf("skills: %s is not a directory", root))
			continue
		}
		if walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				errs = append(errs, fmt.Errorf("skills: walk %s: %w", path, walkErr))
				return nil
			}
			if d.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				errs = append(errs, fmt.Errorf("skills: read %s: %w", path, err))
				return nil
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				rel = d.Name()
			}
			out[filepath.ToSlash(rel)] = string(data)
			return nil
		}); walkErr != nil {
			errs = append(errs, fmt.Errorf("skills: walk %s: %w", root, walkErr))
		}
	}

	if len(out) == 0 {
		return nil, errs
	}
	return out, errs
}

func buildDefinitionMetadata(file SkillFile) map[string]string {
	meta := map[string]string{}
	if file.Metadata.AllowedTools != "" {
		meta["allowed-tools"] = strings.TrimSpace(file.Metadata.AllowedTools)
	}
	if file.Path != "" {
		meta["source"] = file.Path
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func buildHandler(file SkillFile) Handler {
	output := map[string]any{
		"body": file.Body,
	}
	if len(file.SupportFiles) > 0 {
		output["support_files"] = file.SupportFiles
	}

	return HandlerFunc(func(_ context.Context, _ ActivationContext) (Result, error) {
		res := Result{
			Skill:  file.Metadata.Name,
			Output: output,
		}
		meta := map[string]any{}
		if file.Metadata.AllowedTools != "" {
			meta["allowed-tools"] = strings.TrimSpace(file.Metadata.AllowedTools)
		}
		meta["source"] = file.Path
		if len(file.SupportFiles) > 0 {
			meta["support-file-count"] = len(file.SupportFiles)
		}
		if len(meta) > 0 {
			res.Metadata = meta
		}
		return res, nil
	})
}
