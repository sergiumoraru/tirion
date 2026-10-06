package config

import (
	"log"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
	"gopkg.in/yaml.v3"
)

// TraceConfig holds configuration for trace heuristics.
type TraceConfig struct {
	DefaultProfile string                     `yaml:"default_profile"`
	Profiles       map[string]TraceProfile    `yaml:"profiles"`
	NameBased      NameBasedConfig            `yaml:"name_based"`
	HTTP           HTTPConfig                 `yaml:"http"`
	SQS            SQSConfig                  `yaml:"sqs"`
	RepoOverrides  map[string]TraceRepoConfig `yaml:"repo_overrides"`
}

// TraceProfile defines defaults and tuning for trace execution.
type TraceProfile struct {
	Depth                 *int   `yaml:"depth"`
	MaxNodes              *int   `yaml:"max_nodes"`
	Resolve               *bool  `yaml:"resolve"`
	ResolveExpand         *bool  `yaml:"resolve_expand"`
	ResolveExpandMode     string `yaml:"resolve_expand_mode"`
	ResolveExpandMaxCalls *int   `yaml:"resolve_expand_max_calls"`
	DiMaxDepth            *int   `yaml:"di_max_depth"`
	DiMaxImpls            *int   `yaml:"di_max_impls"`
	UpstreamMaxNameCount  *int   `yaml:"upstream_max_name_count"`
}

// NameBasedConfig controls name-based (heuristic) matching.
type NameBasedConfig struct {
	MinNameLength int      `yaml:"min_name_length"`
	Exclude       []string `yaml:"exclude"`
	Include       []string `yaml:"include"`
}

// HTTPConfig controls HTTP endpoint matching.
type HTTPConfig struct {
	ContextPathPrefixes []string `yaml:"context_path_prefixes"`
}

// SQSConfig controls SQS queue matching.
type SQSConfig struct {
	QueuePrefixes []string `yaml:"queue_prefixes"`
}

// TraceRepoConfig holds per-repo overrides.
type TraceRepoConfig struct {
	NameBased NameBasedConfig `yaml:"name_based"`
	HTTP      HTTPConfig      `yaml:"http"`
	SQS       SQSConfig       `yaml:"sqs"`
}

var (
	traceConfigOnce sync.Once
	traceConfig     *TraceConfig
)

// DefaultTraceConfig returns defaults for trace heuristics.
func DefaultTraceConfig() *TraceConfig {
	return &TraceConfig{
		DefaultProfile: "fast",
		Profiles:       DefaultTraceProfiles(),
		NameBased: NameBasedConfig{
			MinNameLength: 3,
			Exclude:       append([]string{}, DefaultNameBasedExcludes...),
		},
		SQS: SQSConfig{
			QueuePrefixes: []string{},
		},
		RepoOverrides: map[string]TraceRepoConfig{},
	}
}

func DefaultTraceProfiles() map[string]TraceProfile {
	return map[string]TraceProfile{
		"fast": {
			Depth:                 intPtr(4),
			MaxNodes:              intPtr(2000),
			Resolve:               boolPtr(false),
			ResolveExpandMode:     "leaf",
			ResolveExpandMaxCalls: intPtr(0),
			DiMaxDepth:            intPtr(2),
			DiMaxImpls:            intPtr(3),
			UpstreamMaxNameCount:  intPtr(25),
		},
		"balanced": {
			Depth:                 intPtr(4),
			MaxNodes:              intPtr(2000),
			Resolve:               boolPtr(true),
			ResolveExpandMode:     "smart",
			ResolveExpandMaxCalls: intPtr(12),
			DiMaxDepth:            intPtr(2),
			DiMaxImpls:            intPtr(3),
			UpstreamMaxNameCount:  intPtr(25),
		},
		"deep": {
			Depth:                 intPtr(5),
			MaxNodes:              intPtr(5000),
			Resolve:               boolPtr(true),
			ResolveExpandMode:     "full",
			ResolveExpandMaxCalls: intPtr(0),
			DiMaxDepth:            intPtr(0),
			DiMaxImpls:            intPtr(0),
			UpstreamMaxNameCount:  intPtr(0),
		},
	}
}

// LoadTraceConfig loads trace config from ~/.tirion/trace.yaml.
// Returns nil if file doesn't exist (not an error).
func LoadTraceConfig() (*TraceConfig, error) {
	configPath, err := runtimeconfig.UserPath("trace.yaml")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfg TraceConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// GetEffectiveTraceConfig returns merged defaults + custom overrides.
func GetEffectiveTraceConfig() *TraceConfig {
	traceConfigOnce.Do(func() {
		custom, err := LoadTraceConfig()
		if err != nil {
			log.Printf("trace config: failed to load: %v", err)
			custom = nil
		} else if warnings := ValidateTraceConfig(custom); len(warnings) > 0 {
			log.Printf("trace config warnings: %s", strings.Join(warnings, "; "))
		}
		traceConfig = MergeTraceConfig(custom)
	})
	return traceConfig
}

// MergeTraceConfig merges defaults with custom config.
func MergeTraceConfig(custom *TraceConfig) *TraceConfig {
	merged := DefaultTraceConfig()
	if custom == nil {
		return merged
	}
	if strings.TrimSpace(custom.DefaultProfile) != "" {
		merged.DefaultProfile = strings.ToLower(strings.TrimSpace(custom.DefaultProfile))
	}
	if custom.Profiles != nil {
		for name, profile := range custom.Profiles {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" {
				continue
			}
			if base, ok := merged.Profiles[key]; ok {
				merged.Profiles[key] = mergeTraceProfile(base, profile)
			} else {
				merged.Profiles[key] = profile
			}
		}
	}
	if custom.NameBased.MinNameLength > 0 {
		merged.NameBased.MinNameLength = custom.NameBased.MinNameLength
	}
	merged.NameBased.Exclude = append(merged.NameBased.Exclude, custom.NameBased.Exclude...)
	merged.NameBased.Include = append(merged.NameBased.Include, custom.NameBased.Include...)
	merged.HTTP.ContextPathPrefixes = append(merged.HTTP.ContextPathPrefixes, custom.HTTP.ContextPathPrefixes...)
	merged.SQS.QueuePrefixes = append(merged.SQS.QueuePrefixes, custom.SQS.QueuePrefixes...)

	if custom.RepoOverrides != nil {
		for repo, override := range custom.RepoOverrides {
			if merged.RepoOverrides == nil {
				merged.RepoOverrides = make(map[string]TraceRepoConfig)
			}
			merged.RepoOverrides[strings.ToLower(repo)] = override
		}
	}
	return merged
}

// ValidateTraceConfig returns warnings for invalid or suspicious settings.
func ValidateTraceConfig(cfg *TraceConfig) []string {
	if cfg == nil {
		return nil
	}
	var warnings []string
	if strings.TrimSpace(cfg.DefaultProfile) == "" {
		warnings = append(warnings, "default_profile is empty; falling back to 'fast'")
	} else if cfg.Profiles != nil {
		if _, ok := cfg.Profiles[strings.ToLower(cfg.DefaultProfile)]; !ok {
			warnings = append(warnings, "default_profile '"+cfg.DefaultProfile+"' not found in profiles")
		}
	}
	for name, profile := range cfg.Profiles {
		scope := "profiles." + strings.ToLower(name)
		warnings = append(warnings, validateTraceProfile(profile, scope)...)
	}
	warnings = append(warnings, validateNameBasedConfig(cfg.NameBased, "name_based")...)
	warnings = append(warnings, validateHTTPPrefixes(cfg.HTTP.ContextPathPrefixes, "http.context_path_prefixes")...)
	warnings = append(warnings, validateQueuePrefixes(cfg.SQS.QueuePrefixes, "sqs.queue_prefixes")...)

	for repo, override := range cfg.RepoOverrides {
		repoKey := strings.TrimSpace(repo)
		if repoKey == "" {
			warnings = append(warnings, "repo_overrides contains an empty repo name")
			continue
		}
		scope := "repo_overrides." + repoKey
		warnings = append(warnings, validateNameBasedConfig(override.NameBased, scope+".name_based")...)
		warnings = append(warnings, validateHTTPPrefixes(override.HTTP.ContextPathPrefixes, scope+".http.context_path_prefixes")...)
		warnings = append(warnings, validateQueuePrefixes(override.SQS.QueuePrefixes, scope+".sqs.queue_prefixes")...)
	}

	return warnings
}

func validateNameBasedConfig(cfg NameBasedConfig, scope string) []string {
	var warnings []string
	if cfg.MinNameLength < 0 {
		warnings = append(warnings, scope+": min_name_length must be >= 0")
	}
	dupes := make(map[string]bool)
	for _, name := range cfg.Include {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			warnings = append(warnings, scope+": include contains an empty entry")
			continue
		}
		dupes[key] = true
	}
	for _, name := range cfg.Exclude {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			warnings = append(warnings, scope+": exclude contains an empty entry")
			continue
		}
		if dupes[key] {
			warnings = append(warnings, scope+": "+key+" is in both include and exclude")
		}
	}
	return warnings
}

func validateHTTPPrefixes(prefixes []string, scope string) []string {
	var warnings []string
	for _, prefix := range prefixes {
		trimmed := strings.TrimSpace(prefix)
		if trimmed == "" {
			warnings = append(warnings, scope+": contains an empty entry")
			continue
		}
		if !strings.HasPrefix(trimmed, "/") {
			warnings = append(warnings, scope+": "+trimmed+" should start with '/'")
		}
	}
	return warnings
}

func validateQueuePrefixes(prefixes []string, scope string) []string {
	var warnings []string
	for _, prefix := range prefixes {
		trimmed := strings.TrimSpace(prefix)
		if trimmed == "" {
			warnings = append(warnings, scope+": contains an empty entry")
		}
	}
	return warnings
}

func validateTraceProfile(profile TraceProfile, scope string) []string {
	var warnings []string
	if profile.Depth != nil && *profile.Depth < 1 {
		warnings = append(warnings, scope+": depth must be >= 1")
	}
	if profile.MaxNodes != nil && *profile.MaxNodes < 1 {
		warnings = append(warnings, scope+": max_nodes must be >= 1")
	}
	if profile.DiMaxDepth != nil && *profile.DiMaxDepth < 0 {
		warnings = append(warnings, scope+": di_max_depth must be >= 0")
	}
	if profile.DiMaxImpls != nil && *profile.DiMaxImpls < 0 {
		warnings = append(warnings, scope+": di_max_impls must be >= 0")
	}
	if profile.UpstreamMaxNameCount != nil && *profile.UpstreamMaxNameCount < 0 {
		warnings = append(warnings, scope+": upstream_max_name_count must be >= 0")
	}
	if profile.ResolveExpandMaxCalls != nil && *profile.ResolveExpandMaxCalls < 0 {
		warnings = append(warnings, scope+": resolve_expand_max_calls must be >= 0")
	}
	if profile.ResolveExpandMode != "" {
		mode := strings.ToLower(strings.TrimSpace(profile.ResolveExpandMode))
		if mode != "leaf" && mode != "smart" && mode != "full" {
			warnings = append(warnings, scope+": resolve_expand_mode must be one of leaf|smart|full")
		}
	}
	return warnings
}

func mergeTraceProfile(base TraceProfile, override TraceProfile) TraceProfile {
	if override.Depth != nil {
		base.Depth = override.Depth
	}
	if override.MaxNodes != nil {
		base.MaxNodes = override.MaxNodes
	}
	if override.Resolve != nil {
		base.Resolve = override.Resolve
	}
	if override.ResolveExpand != nil {
		base.ResolveExpand = override.ResolveExpand
	}
	if strings.TrimSpace(override.ResolveExpandMode) != "" {
		base.ResolveExpandMode = override.ResolveExpandMode
	}
	if override.ResolveExpandMaxCalls != nil {
		base.ResolveExpandMaxCalls = override.ResolveExpandMaxCalls
	}
	if override.DiMaxDepth != nil {
		base.DiMaxDepth = override.DiMaxDepth
	}
	if override.DiMaxImpls != nil {
		base.DiMaxImpls = override.DiMaxImpls
	}
	if override.UpstreamMaxNameCount != nil {
		base.UpstreamMaxNameCount = override.UpstreamMaxNameCount
	}
	return base
}

// NameBasedForRepo returns the merged name-based config for a repo.
func (c *TraceConfig) NameBasedForRepo(repo string) NameBasedConfig {
	if c == nil {
		return NameBasedConfig{}
	}
	base := c.NameBased
	base.Exclude = slices.Clone(base.Exclude)
	base.Include = slices.Clone(base.Include)
	if c.RepoOverrides == nil {
		return base
	}
	override, ok := c.RepoOverrides[strings.ToLower(repo)]
	if !ok {
		return base
	}
	if override.NameBased.MinNameLength > 0 {
		base.MinNameLength = override.NameBased.MinNameLength
	}
	base.Exclude = append(base.Exclude, override.NameBased.Exclude...)
	base.Include = append(base.Include, override.NameBased.Include...)
	return base
}

// ResolveProfile returns the merged profile and the resolved profile name.
func (c *TraceConfig) ResolveProfile(name string) (TraceProfile, string) {
	if c == nil {
		return TraceProfile{}, ""
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(c.DefaultProfile))
	}
	if key != "" {
		if profile, ok := c.Profiles[key]; ok {
			return profile, key
		}
	}
	fallback := strings.ToLower(strings.TrimSpace(c.DefaultProfile))
	if profile, ok := c.Profiles[fallback]; ok {
		return profile, fallback
	}
	if profile, ok := c.Profiles["fast"]; ok {
		return profile, "fast"
	}
	return TraceProfile{}, key
}

func boolPtr(v bool) *bool {
	return &v
}

func intPtr(v int) *int {
	return &v
}

// HTTPForRepo returns the merged HTTP config for a repo.
func (c *TraceConfig) HTTPForRepo(repo string) HTTPConfig {
	if c == nil {
		return HTTPConfig{}
	}
	base := c.HTTP
	base.ContextPathPrefixes = slices.Clone(base.ContextPathPrefixes)
	if c.RepoOverrides == nil {
		return base
	}
	override, ok := c.RepoOverrides[strings.ToLower(repo)]
	if !ok {
		return base
	}
	base.ContextPathPrefixes = append(base.ContextPathPrefixes, override.HTTP.ContextPathPrefixes...)
	return base
}

// SQSForRepo returns the merged SQS config for a repo.
func (c *TraceConfig) SQSForRepo(repo string) SQSConfig {
	if c == nil {
		return SQSConfig{}
	}
	base := c.SQS
	base.QueuePrefixes = slices.Clone(base.QueuePrefixes)
	if c.RepoOverrides == nil {
		return base
	}
	override, ok := c.RepoOverrides[strings.ToLower(repo)]
	if !ok {
		return base
	}
	base.QueuePrefixes = append(base.QueuePrefixes, override.SQS.QueuePrefixes...)
	return base
}
