package trace

import (
	"regexp"
	"strings"
)

var lowSignalUtilityMethods = map[string]struct{}{
	"gethostname":      {},
	"gethostaddresses": {},
	"islocalurl":       {},
	"stringify":        {},
}

var lowSignalContextualMethods = map[string]struct{}{
	"indexof":         {},
	"substring":       {},
	"parse":           {},
	"tryparse":        {},
	"firstordefault":  {},
	"first":           {},
	"singleordefault": {},
	"single":          {},
	"tolist":          {},
	"toarray":         {},
	"tostring":        {},
	"trim":            {},
	"trimspace":       {},
	"split":           {},
	"join":            {},
	"replace":         {},
	"aslist":          {},
	"requirenonnull":  {},
	"keys":            {},
	"values":          {},
	"entries":         {},
	"hashcode":        {},
	"equals":          {},
	"valueof":         {},
	"ofnullable":      {},
}

var lowSignalPrimitiveNames = map[string]struct{}{
	"string":  {},
	"int":     {},
	"long":    {},
	"double":  {},
	"float":   {},
	"decimal": {},
	"bool":    {},
	"boolean": {},
}

var lowSignalTypeSuffixPattern = regexp.MustCompile(`(?i)(Handler|Client|Model|InputModel|ViewModel|Event|Exception|Claim|Token|Response|Request|Identity|Principal|Result|Options|Dto|Entity|Builder)$`)
var lowSignalPascalIdentifierPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)
var lowSignalQualifierSuffixPattern = regexp.MustCompile(`(?i)(Util|Utils|Helper|Formatter|Parser|Mapper|Converter)$`)
var lowSignalBusinessQualifierPattern = regexp.MustCompile(`(?i)(Service|Repository|Repo|Controller|Manager|Handler|Validator|Factory|Gateway|Provider|Processor)$`)

var lowSignalBusinessVerbs = []string{
	"Get",
	"Set",
	"Update",
	"Create",
	"Delete",
	"Save",
	"Load",
	"Find",
	"Search",
	"Validate",
	"Log",
	"Raise",
	"Redirect",
	"Make",
	"Build",
	"Send",
	"Sync",
	"Change",
	"Check",
	"Handle",
	"Process",
	"Resolve",
	"Generate",
	"Sign",
	"Add",
	"Remove",
	"Import",
	"Export",
}

// AnnotateLowSignal marks low-signal leaf nodes conservatively so views can
// collapse noise without dropping boundary hops or business branches.
func AnnotateLowSignal(nodes []*TreeNode) {
	var walk func(parent, node *TreeNode)
	walk = func(parent, node *TreeNode) {
		if node == nil {
			return
		}
		node.LowSignal = IsLowSignalNode(parent, node)
		for _, child := range node.Children {
			walk(node, child)
		}
	}
	for _, node := range nodes {
		walk(nil, node)
	}
}

// IsLowSignalNode identifies nodes that are likely runtime/framework noise.
// It is intentionally conservative: only leaf nodes are eligible, and
// cross-service / HTTP / SQS / EventBridge boundaries are always preserved.
func IsLowSignalNode(parent, node *TreeNode) bool {
	if node == nil {
		return false
	}
	if len(node.Children) > 0 {
		return false
	}
	if node.IsCrossService || node.IsSqs || node.HttpTarget != "" {
		return false
	}
	switch node.EdgeType {
	case edgeTypeHTTP, edgeTypeSQS, edgeTypeEventBridge:
		return false
	}

	name := strings.TrimSpace(node.Name)
	if name == "" || strings.HasPrefix(name, "[") || strings.HasPrefix(name, "→ ") {
		return false
	}

	qualifier, finalSegment := traceNameParts(name)
	finalLower := strings.ToLower(finalSegment)
	if _, ok := lowSignalUtilityMethods[finalLower]; ok {
		return true
	}
	if _, ok := lowSignalContextualMethods[finalLower]; ok && (qualifierLooksLowSignal(qualifier) || qualifierLooksLikeLocalValue(qualifier)) {
		return true
	}
	if looksLikeSameClassPseudoCallWithoutLocation(parent, node, qualifier, finalSegment, finalLower) {
		return true
	}

	if parent == nil || !sameNodeLocation(parent, node) {
		return false
	}

	if _, ok := lowSignalContextualMethods[finalLower]; ok {
		return true
	}
	if _, ok := lowSignalPrimitiveNames[finalLower]; ok {
		return true
	}
	if lowSignalTypeSuffixPattern.MatchString(finalSegment) {
		return true
	}
	if !hasBusinessVerbPrefix(finalSegment) && lowSignalPascalIdentifierPattern.MatchString(finalSegment) {
		return true
	}

	return false
}

func looksLikeSameClassPseudoCallWithoutLocation(parent, node *TreeNode, qualifier, finalSegment, finalLower string) bool {
	if parent == nil {
		return false
	}
	if strings.TrimSpace(node.Repo) != "" || strings.TrimSpace(node.File) != "" {
		return false
	}
	parentQualifier, _ := traceNameParts(parent.Name)
	parentQualifier = strings.TrimSpace(parentQualifier)
	qualifier = strings.TrimSpace(qualifier)
	if parentQualifier == "" || qualifier == "" || qualifier != parentQualifier {
		return false
	}
	if _, ok := lowSignalContextualMethods[finalLower]; ok {
		return true
	}
	if _, ok := lowSignalPrimitiveNames[finalLower]; ok {
		return true
	}
	if lowSignalTypeSuffixPattern.MatchString(finalSegment) {
		return true
	}
	return false
}

func traceNameParts(name string) (string, string) {
	clean := strings.TrimSpace(strings.TrimSuffix(name, " (impl)"))
	if idx := strings.LastIndex(clean, "."); idx >= 0 {
		return clean[:idx], clean[idx+1:]
	}
	return "", clean
}

func sameNodeLocation(parent, node *TreeNode) bool {
	parentRepo := strings.TrimSpace(parent.Repo)
	parentFile := strings.TrimSpace(parent.File)
	childRepo := strings.TrimSpace(node.Repo)
	childFile := strings.TrimSpace(node.File)
	return parentRepo != "" && parentFile != "" && parentRepo == childRepo && parentFile == childFile
}

func hasBusinessVerbPrefix(name string) bool {
	for _, verb := range lowSignalBusinessVerbs {
		if strings.HasPrefix(name, verb) {
			return true
		}
	}
	return false
}

func qualifierLooksLowSignal(qualifier string) bool {
	qualifier = strings.TrimSpace(qualifier)
	if qualifier == "" {
		return false
	}
	if strings.ContainsAny(qualifier, "()[]'\" ") {
		return true
	}
	parts := strings.Split(qualifier, ".")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch part {
		case "String", "Objects", "Collections", "Arrays", "Math", "System", "Thread",
			"Logger", "BufferedReader", "InputStreamReader", "OutputStreamWriter",
			"HttpURLConnection", "URLConnection", "URL", "JsonMapper", "FastHashMap",
			"Map", "List", "Set", "Dns", "Convert", "DateTimeOffset", "Guid", "Uri",
			"Enumerable", "Queryable", "Task", "HttpContext", "Request", "Response",
			"Url", "JSON", "Object", "Promise":
			return true
		}
		if lowSignalQualifierSuffixPattern.MatchString(part) {
			return true
		}
	}
	return false
}

func qualifierLooksLikeLocalValue(qualifier string) bool {
	qualifier = strings.TrimSpace(qualifier)
	if qualifier == "" {
		return false
	}
	if strings.ContainsAny(qualifier, ".()[]'\" ") {
		return false
	}
	trimmed := strings.TrimLeft(qualifier, "_")
	if trimmed == "" {
		return false
	}
	if lowSignalBusinessQualifierPattern.MatchString(trimmed) {
		return false
	}
	first := rune(trimmed[0])
	return first >= 'a' && first <= 'z'
}
