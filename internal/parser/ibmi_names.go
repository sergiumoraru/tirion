package parser

import (
	"path/filepath"
	"regexp"
	"strings"
)

var ibmiBindingTokenRe = regexp.MustCompile(`'([^']+)'|"([^"]+)"|([A-Za-z0-9_&*$./-]+)`)

func NormalizeIBMiObjectName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.Trim(trimmed, `"'`)
	if idx := strings.LastIndexAny(trimmed, "/."); idx >= 0 && idx+1 < len(trimmed) {
		trimmed = trimmed[idx+1:]
	}
	trimmed = strings.TrimPrefix(trimmed, "*LIBL/")
	trimmed = strings.TrimPrefix(trimmed, "*CURLIB/")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return ""
	}
	return strings.ToUpper(trimmed)
}

func ibmiArtifactNameForPath(filePath string) string {
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	return NormalizeIBMiObjectName(strings.TrimSuffix(base, ext))
}

func SplitIBMIBindingNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	raw = strings.ReplaceAll(raw, ":", " ")
	matches := ibmiBindingTokenRe.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, match := range matches {
		token := ""
		for i := 1; i < len(match); i++ {
			if strings.TrimSpace(match[i]) != "" {
				token = match[i]
				break
			}
		}
		token = NormalizeIBMiObjectName(token)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

func ExtractRPGPrototypeAliases(content []byte) map[string]string {
	lines := strings.Split(string(content), "\n")
	aliases := make(map[string]string)
	for _, rawLine := range lines {
		if localName, externalName, ok := parseRPGPrototypeAlias(rawLine); ok {
			aliases[strings.ToLower(localName)] = externalName
		}
	}
	return aliases
}

func ExtractRPGCopyImports(content []byte) []string {
	lines := strings.Split(string(content), "\n")
	var out []string
	seen := make(map[string]bool)
	for _, rawLine := range lines {
		if path, ok := parseRPGCopyDirective(rawLine); ok {
			key := strings.ToLower(path)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, path)
		}
	}
	return out
}
