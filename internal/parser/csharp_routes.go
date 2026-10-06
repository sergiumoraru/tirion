package parser

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// var g = app.MapGroup("/api"); RouteGroupBuilder g = parent.MapGroup("/v1"); g = ...
	csharpGroupAssignRe  = regexp.MustCompile(`^(?:[\w<>\.\?\[\]]+\s+)?([A-Za-z_]\w*)\s*=\s*([^=>].*)$`)
	csharpMapGroupRe     = regexp.MustCompile(`\bMapGroup\s*\(\s*("(?:[^"\\]|\\.)*")?`)
	csharpLeadingIdentRe = regexp.MustCompile(`^\s*(?:await\s+)?([A-Za-z_]\w*)`)
	csharpMapVerbCallRe  = regexp.MustCompile(`\bMap(?:Get|Post|Put|Delete|Patch|Head|Options|Methods)\s*\(`)
)

// csharpRouteGroups resolves minimal-API route prefixes introduced by MapGroup.
// Groups are tracked per method scope: a group variable only means something
// inside the method that assigned it, so declarations reset the table.
type csharpRouteGroups struct {
	prefixes map[string]string // group variable -> accumulated route prefix

	// A fluent chain may continue on following lines (".MapGet(...)"). Its
	// prefix state is folded one line at a time, so each line costs only its own
	// length however long the chain grows.
	chainOpen  bool            // the previous line left a statement unfinished
	chain      csharpExprState // state of the statement collected so far
	prior      csharpExprState // chain state before the current line was appended
	priorValid bool            // the current line continues a chain (prior is set)
	assignTo   string          // variable receiving the chain, if it is a group assignment
	assignRHS  csharpExprState // state of the right-hand side of that assignment
}

// csharpExprState is the route state of a fluent expression folded so far.
type csharpExprState struct {
	prefix  string // accumulated route prefix
	isGroup bool   // derives from a known group or calls MapGroup
	verb    bool   // contains a Map<Verb> call (a route builder, not a group)
}

func newCSharpRouteGroups() *csharpRouteGroups {
	return &csharpRouteGroups{prefixes: map[string]string{}}
}

// reset forgets group variables when a new method scope begins.
func (g *csharpRouteGroups) reset() {
	*g = csharpRouteGroups{prefixes: map[string]string{}}
}

// observe records MapGroup assignments and tracks fluent chains that span lines.
// Call it once per code line, before mapPrefix.
func (g *csharpRouteGroups) observe(line string) {
	trimmed := strings.TrimSpace(line)
	ended := strings.HasSuffix(trimmed, ";")
	g.priorValid = false
	if strings.HasPrefix(trimmed, ".") && g.chainOpen {
		g.prior, g.priorValid = g.chain, true
		g.chain = g.fold(g.chain, trimmed, false)
		if g.assignTo != "" {
			g.assignRHS = g.fold(g.assignRHS, trimmed, false)
		}
	} else {
		g.chain = g.fold(csharpExprState{}, trimmed, true)
		g.assignTo, g.assignRHS = "", csharpExprState{}
		if match := csharpGroupAssignRe.FindStringSubmatch(trimmed); match != nil {
			g.assignTo = match[1]
			g.assignRHS = g.fold(csharpExprState{}, match[2], true)
		}
	}
	if g.assignTo != "" {
		// "var g = app.MapGroup(...)" and aliases ("var v = g") define a group; a
		// verb call on the right makes the variable a route builder instead; any
		// other value forgets the variable.
		switch {
		case g.assignRHS.isGroup && !g.assignRHS.verb:
			g.prefixes[g.assignTo] = g.assignRHS.prefix
		case ended:
			delete(g.prefixes, g.assignTo)
		}
	}
	g.chainOpen = !ended
	if ended {
		g.assignTo, g.assignRHS = "", csharpExprState{}
	}
}

// mapPrefix returns the route prefix for the Map<Verb> call that starts at
// column idx of line, derived from the receiver expression before it.
func (g *csharpRouteGroups) mapPrefix(line string, idx int) string {
	trimmed := strings.TrimSpace(line)
	// idx is relative to line; translate it to the trimmed text.
	idx -= len(line) - len(strings.TrimLeft(line, " \t"))
	if idx < 0 {
		idx = 0
	}
	if idx > len(trimmed) {
		idx = len(trimmed)
	}
	receiver := trimmed[:idx]
	if g.priorValid && strings.HasPrefix(trimmed, ".") {
		return g.fold(g.prior, receiver, false).prefix
	}
	return g.fold(csharpExprState{}, receiver, true).prefix
}

// fold appends one segment of a fluent expression to st: on the first segment
// the group variable at the root of the expression, then every literal
// MapGroup(...) call. isGroup reports whether the expression derives from a
// known group or calls MapGroup at all. A MapGroup with a non-literal argument
// contributes nothing: its value is not in the source.
func (g *csharpRouteGroups) fold(st csharpExprState, seg string, first bool) csharpExprState {
	if first {
		if match := csharpLeadingIdentRe.FindStringSubmatch(seg); match != nil {
			if known, ok := g.prefixes[match[1]]; ok {
				st.prefix, st.isGroup = known, true
			}
		}
	}
	for _, match := range csharpMapGroupRe.FindAllStringSubmatch(seg, -1) {
		st.isGroup = true
		if match[1] == "" {
			continue
		}
		segment, err := strconv.Unquote(match[1])
		if err != nil {
			segment = strings.Trim(match[1], `"`)
		}
		st.prefix = joinRoutePrefix(st.prefix, segment)
	}
	if csharpMapVerbCallRe.MatchString(seg) {
		st.verb = true
	}
	return st
}

// joinRoutePrefix joins route segments into "/a/b" ("" when both are empty).
func joinRoutePrefix(prefix, segment string) string {
	return normalizeEndpointPath(strings.Trim(prefix, "/") + "/" + strings.Trim(segment, "/"))
}
