package parser

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode"
)

type ResourceConfigParser struct{}

func NewResourceConfigParser() *ResourceConfigParser {
	return &ResourceConfigParser{}
}

func (p *ResourceConfigParser) CanParse(filePath string) bool {
	base := strings.ToLower(filepath.Base(filePath))
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env") {
		return true
	}
	if !strings.EqualFold(filepath.Ext(filePath), ".json") {
		return false
	}
	if strings.HasSuffix(base, ".parameters.json") {
		return true
	}
	switch base {
	case "function.json", "host.json", "package.json", "package-lock.json", "tsconfig.json", "jsconfig.json":
		return false
	}
	normalized := strings.ToLower(filepath.ToSlash(filePath))
	return strings.Contains(base, "config") ||
		strings.Contains(base, "settings") ||
		strings.Contains(base, ".env") ||
		strings.Contains(normalized, "/config/") ||
		strings.Contains(normalized, "/configs/")
}

// ParseFile recovers parser panics into a per-file failure.
func (p *ResourceConfigParser) ParseFile(filePath string, content []byte) ParsedFile {
	return parseGuarded(filePath, "json-config", func() ParsedFile { return p.parseFile(filePath, content) })
}

func (p *ResourceConfigParser) parseFile(filePath string, content []byte) ParsedFile {
	result := ParsedFile{
		Path:            filePath,
		Language:        "json-config",
		Functions:       []ParsedFunction{},
		Classes:         []ParsedClass{},
		Imports:         []ParsedImport{},
		Endpoints:       []ParsedEndpoint{},
		FunctionCalls:   make(map[string][]ParsedFunctionCall),
		HttpCalls:       make(map[string][]ParsedHttpCall),
		ResourceAliases: []ParsedResourceAlias{},
	}

	if resourceConfigIsEnvFile(filePath) {
		result.Language = "env-config"
		result.ResourceAliases = parseEnvResourceAliases(string(content))
		return result
	}

	var root any
	if err := json.Unmarshal(content, &root); err != nil {
		result.ParseDiagnostics = ParseDiagnostics{
			FailureKind: ParseFailureSyntaxUnsupported,
			Message:     err.Error(),
		}
		return result
	}

	contentStr := string(content)
	seen := make(map[string]bool)
	var walk func(prefix string, value any)
	walk = func(prefix string, value any) {
		obj, ok := value.(map[string]any)
		if !ok {
			return
		}
		for key, child := range obj {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			fullKey := key
			if prefix != "" {
				fullKey = prefix + "." + key
			}
			if nested, ok := child.(map[string]any); ok {
				walk(fullKey, nested)
				continue
			}
			value, ok := child.(string)
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			alias := resourceAliasKey(key, fullKey)
			if alias == "" || !looksLikeResourceAliasValue(value) {
				continue
			}
			dedupe := strings.ToLower(alias) + "\x00" + strings.ToLower(value)
			if seen[dedupe] {
				continue
			}
			seen[dedupe] = true
			result.ResourceAliases = append(result.ResourceAliases, ParsedResourceAlias{
				Alias:      alias,
				Value:      value,
				Kind:       "config",
				LineNumber: lineNumberForJSONKey(contentStr, key),
			})
		}
	}
	walk("", root)

	return result
}

func resourceConfigIsEnvFile(filePath string) bool {
	base := strings.ToLower(filepath.Base(filePath))
	return strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env")
}

func parseEnvResourceAliases(content string) []ParsedResourceAlias {
	lines := strings.Split(content, "\n")
	out := make([]ParsedResourceAlias, 0)
	seen := make(map[string]bool)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		trimmed = strings.TrimPrefix(trimmed, "export ")
		eq := strings.Index(trimmed, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		value := strings.TrimSpace(trimmed[eq+1:])
		value = strings.Trim(value, `"'`)
		alias := resourceAliasKey(key, key)
		if alias == "" || !looksLikeResourceAliasValue(value) {
			continue
		}
		dedupe := strings.ToLower(alias) + "\x00" + strings.ToLower(value)
		if seen[dedupe] {
			continue
		}
		seen[dedupe] = true
		out = append(out, ParsedResourceAlias{
			Alias:      alias,
			Value:      value,
			Kind:       "env",
			LineNumber: i + 1,
		})
	}
	return out
}

func resourceAliasKey(key, fullKey string) string {
	key = strings.TrimSpace(key)
	fullKey = strings.TrimSpace(fullKey)
	if strings.EqualFold(key, "value") || strings.EqualFold(key, "defaultValue") {
		parts := strings.Split(fullKey, ".")
		if len(parts) >= 2 {
			parent := strings.TrimSpace(parts[len(parts)-2])
			if looksLikeResourceAliasKey(parent) {
				return parent
			}
		}
	}
	if looksLikeResourceAliasKey(key) {
		return key
	}
	if looksLikeResourceAliasKey(fullKey) {
		return fullKey
	}
	return ""
}

func looksLikeResourceAliasKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	lower := strings.ToLower(key)
	if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") {
		return false
	}
	resourceWord := strings.Contains(lower, "queue") ||
		strings.Contains(lower, "topic") ||
		strings.Contains(lower, "subscription") ||
		strings.Contains(lower, "container") ||
		strings.Contains(lower, "blob")
	if !resourceWord {
		return false
	}
	hasUpper := false
	for _, r := range key {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r), unicode.IsDigit(r), r == '_', r == '.', r == '-', r == ':':
		default:
			return false
		}
	}
	return hasUpper || strings.ContainsAny(key, "_.-:")
}

func looksLikeResourceAliasValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 160 {
		return false
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "password") || strings.Contains(lower, "secret") {
		return false
	}
	if strings.Contains(value, ";") || strings.Contains(value, "=") || strings.Contains(value, "://") {
		return false
	}
	hasLetter := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r), r == '_', r == '-', r == '.', r == '/', r == ':':
		default:
			return false
		}
	}
	return hasLetter
}

func lineNumberForJSONKey(content, key string) int {
	if strings.TrimSpace(key) == "" {
		return 1
	}
	needle := `"` + key + `"`
	idx := strings.Index(content, needle)
	if idx < 0 {
		return 1
	}
	return 1 + strings.Count(content[:idx], "\n")
}
