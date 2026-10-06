package graph

import "strings"

// RepoFilterPattern converts a user-supplied repository filter into an ILIKE
// pattern. A bare name matches the repository name exactly (case-insensitive);
// a trailing "*" matches by prefix. Every other character is literal, so "_" and
// "%" in repository names never act as wildcards.
func RepoFilterPattern(filter string) string {
	filter = strings.TrimSpace(filter)
	if strings.HasSuffix(filter, "*") {
		return EscapeLike(strings.TrimSuffix(filter, "*")) + "%"
	}
	return EscapeLike(filter)
}

func repoFilterPatterns(filters []string) []string {
	out := make([]string, 0, len(filters))
	for _, filter := range filters {
		out = append(out, RepoFilterPattern(filter))
	}
	return out
}

// SearchFetchCap bounds the rows a single capped search source may return.
// Callers request SearchFetchCap+1 rows so they can tell "exactly the cap"
// from "more than the cap" and report an inexact total.
const SearchFetchCap = 500

// RepoFilterMatches is the in-memory twin of RepoFilterPattern. Keeping both in
// one place guarantees existence checks, SQL filters and post-filters agree.
func RepoFilterMatches(repoName, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(repoName))
	want := strings.ToLower(filter)
	if strings.HasSuffix(want, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(want, "*"))
	}
	return name == want
}
