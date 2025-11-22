package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// PluginMCPServerConfig describes MCP server configuration within a plugin.
// This mirrors config.MCPServerConfig to avoid import cycles.
type PluginMCPServerConfig struct {
	Type           string            `json:"type"`              // stdio/http/sse
	Command        string            `json:"command,omitempty"` // for stdio
	Args           []string          `json:"args,omitempty"`
	URL            string            `json:"url,omitempty"` // for http/sse
	Env            map[string]string `json:"env,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
}

const manifestFileName = "plugin.json"

var (
	// ErrManifestNotFound indicates that the plugin directory is missing a manifest file.
	ErrManifestNotFound = errors.New("plugin manifest not found")

	pluginNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)
)

// Manifest models .claude-plugin/plugin.json metadata. Signature fields remain
// to preserve the existing trust-store workflow; the digest now protects the
// canonical manifest payload instead of an entrypoint file.
type Manifest struct {
	Name        string                           `json:"name"`
	Version     string                           `json:"version"`
	Description string                           `json:"description"`
	Author      string                           `json:"author"`
	Commands    []string                         `json:"commands"`
	Agents      []string                         `json:"agents"`
	Skills      []string                         `json:"skills"`
	Hooks       map[string][]string              `json:"hooks"`
	MCPServers  map[string]PluginMCPServerConfig `json:"mcpServers,omitempty"`
	Digest      string                           `json:"digest,omitempty"`
	Signer      string                           `json:"signer,omitempty"`
	Signature   string                           `json:"signature,omitempty"`

	ManifestPath string `json:"-"`
	PluginDir    string `json:"-"`
	Trusted      bool   `json:"-"`
}

// ManifestOption mutates manifest loading behaviour.
type ManifestOption func(*manifestOptions)

type manifestOptions struct {
	trust *TrustStore
	root  string
}

// WithTrustStore requests signature validation.
func WithTrustStore(store *TrustStore) ManifestOption {
	return func(opts *manifestOptions) {
		opts.trust = store
	}
}

// WithRoot constrains manifests to live under the provided root.
func WithRoot(root string) ManifestOption {
	return func(opts *manifestOptions) {
		opts.root = root
	}
}

// LoadManifest parses and validates a .claude-plugin/plugin.json file.
func LoadManifest(path string, opts ...ManifestOption) (*Manifest, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("manifest path %s is a directory", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var opt manifestOptions
	for _, fn := range opts {
		fn(&opt)
	}

	var mf Manifest
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}

	manifestDir := filepath.Dir(path)
	pluginDir := manifestDir
	if filepath.Base(manifestDir) == ".claude-plugin" {
		pluginDir = filepath.Dir(manifestDir)
	}
	if opt.root == "" {
		opt.root = pluginDir
	}
	rootAbs, err := filepath.Abs(opt.root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	pluginDirAbs, err := filepath.Abs(pluginDir)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin dir: %w", err)
	}
	if !strings.HasPrefix(pluginDirAbs, rootAbs) {
		return nil, fmt.Errorf("manifest outside trusted root: %s", pluginDirAbs)
	}

	normalizeManifest(&mf)
	if err := validateManifestFields(&mf); err != nil {
		return nil, err
	}

	computedDigest, err := computeManifestDigest(&mf)
	if err != nil {
		return nil, err
	}
	if mf.Digest != "" && !strings.EqualFold(mf.Digest, computedDigest) {
		return nil, fmt.Errorf("manifest digest mismatch: want %s computed %s", mf.Digest, computedDigest)
	}
	mf.Digest = computedDigest

	payload, err := CanonicalManifestBytes(&mf)
	if err != nil {
		return nil, err
	}
	if opt.trust != nil {
		if err := opt.trust.Verify(&mf, payload); err != nil {
			return nil, err
		}
		mf.Trusted = true
	}
	if opt.trust == nil {
		mf.Trusted = true
	}

	manifestAbs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	mf.ManifestPath = manifestAbs
	mf.PluginDir = pluginDirAbs

	return &mf, nil
}

// DiscoverManifests walks a directory and loads every child manifest it can find.
func DiscoverManifests(dir string, store *TrustStore) ([]*Manifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var manifests []*Manifest
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath, err := FindManifest(filepath.Join(dir, entry.Name()))
		if err != nil {
			if errors.Is(err, ErrManifestNotFound) {
				continue
			}
			return nil, err
		}
		mf, err := LoadManifest(manifestPath, WithRoot(filepath.Join(dir, entry.Name())), WithTrustStore(store))
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, mf)
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].Name < manifests[j].Name
	})
	return manifests, nil
}

// FindManifest returns the manifest file path for a plugin directory.
func FindManifest(dir string) (string, error) {
	primary := filepath.Join(dir, ".claude-plugin", manifestFileName)
	if info, err := os.Stat(primary); err == nil && !info.IsDir() {
		return primary, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	alt := filepath.Join(dir, manifestFileName)
	if info, err := os.Stat(alt); err == nil && !info.IsDir() {
		return alt, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return "", fmt.Errorf("%w in %s", ErrManifestNotFound, dir)
}

func validateManifestFields(m *Manifest) error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	if !pluginNamePattern.MatchString(m.Name) {
		return fmt.Errorf("invalid plugin name %q", m.Name)
	}
	if !IsSemVer(m.Version) {
		return fmt.Errorf("invalid semver %q", m.Version)
	}
	for i, cmd := range m.Commands {
		if strings.TrimSpace(cmd) == "" {
			return fmt.Errorf("commands[%d] is empty", i)
		}
	}
	for i, agent := range m.Agents {
		if strings.TrimSpace(agent) == "" {
			return fmt.Errorf("agents[%d] is empty", i)
		}
	}
	for i, skill := range m.Skills {
		if strings.TrimSpace(skill) == "" {
			return fmt.Errorf("skills[%d] is empty", i)
		}
	}
	if m.Digest != "" {
		if len(m.Digest) != 64 {
			return errors.New("digest must be a sha256 hex")
		}
		if _, err := hex.DecodeString(m.Digest); err != nil {
			return fmt.Errorf("invalid digest: %w", err)
		}
	}
	return nil
}

func computeManifestDigest(m *Manifest) (string, error) {
	if m == nil {
		return "", errors.New("manifest is nil")
	}
	payload := struct {
		Name        string                           `json:"name"`
		Version     string                           `json:"version"`
		Description string                           `json:"description"`
		Author      string                           `json:"author"`
		Commands    []string                         `json:"commands,omitempty"`
		Agents      []string                         `json:"agents,omitempty"`
		Skills      []string                         `json:"skills,omitempty"`
		Hooks       map[string][]string              `json:"hooks,omitempty"`
		MCPServers  map[string]PluginMCPServerConfig `json:"mcpServers,omitempty"`
	}{
		Name:        m.Name,
		Version:     m.Version,
		Description: m.Description,
		Author:      m.Author,
		Commands:    m.Commands,
		Agents:      m.Agents,
		Skills:      m.Skills,
		Hooks:       m.Hooks,
		MCPServers:  m.MCPServers,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func normalizeManifest(m *Manifest) {
	if m == nil {
		return
	}
	m.Name = strings.TrimSpace(m.Name)
	m.Version = strings.TrimSpace(m.Version)
	m.Description = strings.TrimSpace(m.Description)
	m.Author = strings.TrimSpace(m.Author)
	m.Commands = normalizeList(m.Commands)
	m.Agents = normalizeList(m.Agents)
	m.Skills = normalizeList(m.Skills)
	m.Hooks = normalizeHookMap(m.Hooks)
}

func normalizeList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	uniq := make(map[string]struct{}, len(values))
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			continue
		}
		uniq[trimmed] = struct{}{}
	}
	result := make([]string, 0, len(uniq))
	for k := range uniq {
		result = append(result, k)
	}
	sort.Strings(result)
	return result
}

func normalizeHookMap(src map[string][]string) map[string][]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string][]string, len(src))
	for key, vals := range src {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		normVals := normalizeList(vals)
		if len(normVals) == 0 {
			continue
		}
		out[trimmedKey] = normVals
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// IsSemVer validates a minimal SemVer string.
func IsSemVer(version string) bool {
	if version == "" {
		return false
	}
	normalized := version
	if !strings.HasPrefix(normalized, "v") {
		normalized = "v" + normalized
	}
	return semver.IsValid(normalized)
}
