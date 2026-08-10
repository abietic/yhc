package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Marketplace represents a parsed plugin marketplace manifest.
type Marketplace struct {
	Name       string
	Owner      MarketplaceOwner
	Plugins    []MarketplacePlugin
	Metadata   MarketplaceMetadata
	SourcePath string
}

type MarketplaceOwner struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

type MarketplaceMetadata struct {
	PluginRoot  string `json:"pluginRoot,omitempty"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
}

type MarketplacePlugin struct {
	Name        string
	Version     string
	Description string
	Category    string
	Tags        []string
	Strict      bool
	Source      MarketplacePluginSource
}

type MarketplacePluginSource struct {
	Kind    string
	Path    string
	URL     string
	Repo    string
	Ref     string
	Package string
}

type marketplaceFile struct {
	Name     string              `json:"name"`
	Owner    MarketplaceOwner    `json:"owner"`
	Plugins  []marketplaceEntry  `json:"plugins"`
	Metadata MarketplaceMetadata `json:"metadata,omitempty"`
}

type marketplaceEntry struct {
	Name        string          `json:"name"`
	Version     string          `json:"version,omitempty"`
	Description string          `json:"description,omitempty"`
	Category    string          `json:"category,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	Source      json.RawMessage `json:"source"`
}

// LoadMarketplaceFile parses a reference-shaped marketplace.json file.
func LoadMarketplaceFile(path string) (*Marketplace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("plugins: read marketplace: %w", err)
	}
	var raw marketplaceFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("plugins: parse marketplace: %w", err)
	}
	if strings.TrimSpace(raw.Name) == "" {
		return nil, fmt.Errorf("plugins: marketplace name is required")
	}
	if strings.TrimSpace(raw.Owner.Name) == "" {
		return nil, fmt.Errorf("plugins: marketplace owner name is required")
	}
	if raw.Plugins == nil {
		return nil, fmt.Errorf("plugins: marketplace plugins list is required")
	}

	marketplace := &Marketplace{
		Name:       raw.Name,
		Owner:      raw.Owner,
		Metadata:   raw.Metadata,
		SourcePath: path,
		Plugins:    make([]MarketplacePlugin, 0, len(raw.Plugins)),
	}
	for _, entry := range raw.Plugins {
		plugin, err := parseMarketplacePlugin(entry)
		if err != nil {
			return nil, err
		}
		marketplace.Plugins = append(marketplace.Plugins, plugin)
	}
	sort.Slice(marketplace.Plugins, func(i, j int) bool {
		return marketplace.Plugins[i].Name < marketplace.Plugins[j].Name
	})
	return marketplace, nil
}

func parseMarketplacePlugin(entry marketplaceEntry) (MarketplacePlugin, error) {
	if strings.TrimSpace(entry.Name) == "" {
		return MarketplacePlugin{}, fmt.Errorf("plugins: marketplace plugin name is required")
	}
	if strings.Contains(entry.Name, " ") {
		return MarketplacePlugin{}, fmt.Errorf("plugins: marketplace plugin name %q cannot contain spaces", entry.Name)
	}
	source, err := parseMarketplacePluginSource(entry.Source)
	if err != nil {
		return MarketplacePlugin{}, fmt.Errorf("plugins: marketplace plugin %q: %w", entry.Name, err)
	}
	strict := true
	if entry.Strict != nil {
		strict = *entry.Strict
	}
	return MarketplacePlugin{
		Name:        entry.Name,
		Version:     entry.Version,
		Description: entry.Description,
		Category:    entry.Category,
		Tags:        append([]string(nil), entry.Tags...),
		Strict:      strict,
		Source:      source,
	}, nil
}

func parseMarketplacePluginSource(raw json.RawMessage) (MarketplacePluginSource, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return MarketplacePluginSource{}, fmt.Errorf("source is required")
	}
	var pathSource string
	if err := json.Unmarshal(raw, &pathSource); err == nil {
		if strings.TrimSpace(pathSource) == "" {
			return MarketplacePluginSource{}, fmt.Errorf("source path is empty")
		}
		return MarketplacePluginSource{Kind: "path", Path: pathSource}, nil
	}
	var obj struct {
		Source  string `json:"source"`
		Path    string `json:"path,omitempty"`
		URL     string `json:"url,omitempty"`
		Repo    string `json:"repo,omitempty"`
		Ref     string `json:"ref,omitempty"`
		Package string `json:"package,omitempty"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return MarketplacePluginSource{}, fmt.Errorf("invalid source")
	}
	switch obj.Source {
	case "github":
		if strings.TrimSpace(obj.Repo) == "" {
			return MarketplacePluginSource{}, fmt.Errorf("github source repo is required")
		}
	case "url", "git-subdir":
		if strings.TrimSpace(obj.URL) == "" {
			return MarketplacePluginSource{}, fmt.Errorf("%s source url is required", obj.Source)
		}
	case "npm", "pip":
		if strings.TrimSpace(obj.Package) == "" {
			return MarketplacePluginSource{}, fmt.Errorf("%s source package is required", obj.Source)
		}
	default:
		return MarketplacePluginSource{}, fmt.Errorf("unsupported source type %q", obj.Source)
	}
	return MarketplacePluginSource{
		Kind:    obj.Source,
		Path:    obj.Path,
		URL:     obj.URL,
		Repo:    obj.Repo,
		Ref:     obj.Ref,
		Package: obj.Package,
	}, nil
}

// DiscoverMarketplaces scans local marketplace files and cached marketplace
// directories. Directory sources may either contain marketplace.json directly or
// the reference .claude-plugin/marketplace.json shape.
func DiscoverMarketplaces(paths ...string) ([]*Marketplace, error) {
	var marketplaces []*Marketplace
	var failures []string
	for _, source := range paths {
		if strings.TrimSpace(source) == "" {
			continue
		}
		info, err := os.Stat(source)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", source, err))
			continue
		}
		candidates := []string{source}
		if info.IsDir() {
			candidates = []string{
				filepath.Join(source, "marketplace.json"),
				filepath.Join(source, ".claude-plugin", "marketplace.json"),
			}
		}
		loaded := false
		var lastErr error
		for _, candidate := range candidates {
			marketplace, err := LoadMarketplaceFile(candidate)
			if err != nil {
				lastErr = err
				continue
			}
			marketplaces = append(marketplaces, marketplace)
			loaded = true
			break
		}
		if !loaded && lastErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", source, lastErr))
		}
	}
	sort.Slice(marketplaces, func(i, j int) bool {
		return marketplaces[i].Name < marketplaces[j].Name
	})
	if len(failures) > 0 {
		return marketplaces, fmt.Errorf("plugins: marketplace discovery errors: %s", strings.Join(failures, "; "))
	}
	return marketplaces, nil
}
