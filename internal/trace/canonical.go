package trace

import (
	"fmt"
	"sort"
	"strings"
)

// functionIDCandidate captures the minimum metadata required to deterministically
// select a function identity when multiple rows match the same caller_id.
type functionIDCandidate struct {
	id        int64
	startLine int
	endLine   int
}

// pickDeterministicFunctionID applies a stable tie-break policy:
// 1) lower start line first
// 2) lower end line first
// 3) lower function id first
func pickDeterministicFunctionID(candidates []functionIDCandidate) (int64, bool) {
	if len(candidates) == 0 {
		return 0, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].startLine != candidates[j].startLine {
			return candidates[i].startLine < candidates[j].startLine
		}
		if candidates[i].endLine != candidates[j].endLine {
			return candidates[i].endLine < candidates[j].endLine
		}
		return candidates[i].id < candidates[j].id
	})
	return candidates[0].id, true
}

func normalizeCallerIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func pickUniqueCallerID(ids []string) string {
	normalized := normalizeCallerIDs(ids)
	if len(normalized) != 1 {
		return ""
	}
	return normalized[0]
}

func normalizeInterfaceImpls(impls []InterfaceImpl) []InterfaceImpl {
	if len(impls) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(impls))
	out := make([]InterfaceImpl, 0, len(impls))
	for _, impl := range impls {
		if impl.ClassName == "" {
			continue
		}
		key := fmt.Sprintf("%d|%s|%s|%s", impl.ClassID, strings.ToLower(impl.Repo), strings.ToLower(impl.File), strings.ToLower(impl.ClassName))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, impl)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].ClassName != out[j].ClassName {
			return out[i].ClassName < out[j].ClassName
		}
		return out[i].ClassID < out[j].ClassID
	})
	return out
}
