package parser

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type SQLParser struct{}

// sqlIdentPattern matches one possibly schema-qualified object name whose parts
// are bare words or quoted identifiers that may contain spaces: dbo.Orders,
// [dbo].[Order Items], "public"."Quoted Tbl", `tenant`.`items`.
const sqlIdentPart = `(?:\[[^\]\r\n]+\]|"(?:[^"\r\n]|"")+"|` + "`[^`\\r\\n]+`" + `|[A-Za-z0-9_][A-Za-z0-9_$]*)`
const sqlIdentPattern = sqlIdentPart + `(?:\.` + sqlIdentPart + `)*`

var (
	sqlCreateRoutineRe = regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+(?:ALTER|REPLACE)\s+)?(?:DEFINER\s*=\s*[^\s@]*\s*@?\s*[^\s]*\s+)?(?:PROCEDURE|PROC|FUNCTION)\s+(` + sqlIdentPattern + `)`)
	sqlCreateTableRe   = regexp.MustCompile(`(?is)\bCREATE\s+(?:TEMP(?:ORARY)?\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + sqlIdentPattern + `)`)
	sqlInsertStmtRe    = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+(?:ONLY\s+)?(` + sqlIdentPattern + `)`)
	sqlUpdateStmtRe    = regexp.MustCompile(`(?is)\bUPDATE\s+(?:(?:LOW_PRIORITY|IGNORE)\s+)*(?:TOP\s*\(\s*[^()\r\n]{1,64}\)\s*(?:PERCENT\s+)?)?(?:ONLY\s+)?(` + sqlIdentPattern + `)`)
	sqlDeleteStmtRe    = regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+(?:ONLY\s+)?(` + sqlIdentPattern + `)`)
	sqlMergeStmtRe     = regexp.MustCompile(`(?is)\bMERGE\s+INTO\s+(` + sqlIdentPattern + `)`)
	sqlFromStmtRe      = regexp.MustCompile(`(?is)\bFROM\s+(?:ONLY\s+)?(` + sqlIdentPattern + `)`)
	sqlJoinStmtRe      = regexp.MustCompile(`(?is)\bJOIN\s+(?:ONLY\s+)?(` + sqlIdentPattern + `)`)
	sqlGoBatchRe       = regexp.MustCompile(`(?im)^[ \t]*GO[ \t]*(?:\d+[ \t]*)?\r?$`)
	sqlDigitsRe        = regexp.MustCompile(`^\d+$`)
	// sqlSetAssignRe checks what follows the SET of an UPDATE: an assignment
	// (col = ..., t.col = ..., (a, b) = ...), not SET NOCOUNT ON. Quoted column
	// names are blank in the structural text.
	sqlSetAssignRe      = regexp.MustCompile(`^\s*(?:\(|(?:[A-Za-z_@#][A-Za-z0-9_@#$]*(?:\s*\.\s*[A-Za-z0-9_@#$]*)*)?\s*=)`)
	sqlBackslashQuoteRe = regexp.MustCompile(`\\'[A-Za-z]`)
)

// sqlFromSyntaxFunctions use FROM as an argument separator, not a table clause:
// EXTRACT(year FROM d), TRIM(BOTH ' ' FROM s), SUBSTRING(s FROM 2),
// OVERLAY(s PLACING x FROM 2), POSITION(a IN b).
var sqlFromSyntaxFunctions = map[string]bool{
	"extract":   true,
	"trim":      true,
	"substring": true,
	"overlay":   true,
	"position":  true,
}

// maxParenLookback bounds the backward scan for an enclosing parenthesis; the
// arguments of a FROM-syntax function are short.
const maxParenLookback = 4096

func NewSQLParser() *SQLParser {
	return &SQLParser{}
}

func (p *SQLParser) CanParse(filePath string) bool {
	return strings.EqualFold(filepath.Ext(filePath), ".sql")
}

// ParseFile recovers parser panics into a per-file failure.
func (p *SQLParser) ParseFile(filePath string, content []byte) ParsedFile {
	return parseGuarded(filePath, "sql", func() ParsedFile { return p.parseFile(filePath, content) })
}

type sqlRoutine struct {
	name       string
	start, end int // byte range of the routine within the file
}

func (p *SQLParser) parseFile(filePath string, content []byte) ParsedFile {
	text := string(content)
	dialect := detectSQLDialect(text)
	searchable, dollarBodies, terminators := maskSQL(text, false, dialect)
	structural, _, _ := maskSQL(text, true, dialect)
	statements := newSQLStatementIndex(structural)
	lines := newSQLLineIndex(text)
	moduleFunction := ParsedFunction{
		Name:       "_module_",
		StartLine:  1,
		EndLine:    maxInt(1, lines.count()),
		SourceCode: text,
	}
	result := ParsedFile{
		Path:            filePath,
		Language:        "sql",
		Functions:       []ParsedFunction{moduleFunction},
		FunctionCalls:   map[string][]ParsedFunctionCall{},
		HttpCalls:       map[string][]ParsedHttpCall{},
		DataAccesses:    map[string][]ParsedDataAccess{},
		SqlStatements:   map[string][]ParsedSqlStatementCall{},
		SqsProducers:    map[string][]ParsedSqsProducer{},
		LocalVarTypes:   map[string]map[string]string{},
		ResourceAliases: nil,
	}

	routines := splitSQLRoutines(text, searchable, structural, dollarBodies, terminators, statements)
	for _, routine := range routines {
		startLine := lines.lineAt(routine.start)
		result.Functions = append(result.Functions, ParsedFunction{
			Name:       routine.name,
			StartLine:  startLine,
			EndLine:    maxInt(startLine, lines.lineAt(trimmedSQLEnd(text, routine.start, routine.end)-1)),
			SourceCode: text[routine.start:routine.end],
			IsExported: true,
		})
	}
	// Statements belong to the routine whose byte range holds them; anything
	// outside every routine (DDL between GO batches, seed data) is module-level.
	owner := func(offset int) string {
		// Routines are ordered and disjoint, so the candidate is the last one that
		// starts at or before the offset.
		i := sort.Search(len(routines), func(i int) bool { return routines[i].start > offset }) - 1
		if i >= 0 && offset < routines[i].end {
			return routines[i].name
		}
		return "_module_"
	}

	seen := make(map[string]bool)
	addAccesses := func(re *regexp.Regexp, access string) {
		for _, match := range re.FindAllStringSubmatchIndex(searchable, -1) {
			if len(match) < 4 {
				continue
			}
			// A string literal between the keyword and the name was masked to
			// spaces (COPY t FROM '/x.csv' WITH ...); the next word is not a table.
			if strings.ContainsAny(text[match[0]:match[2]], "'$") {
				continue
			}
			if !sqlMatchIsTableReference(re, structural, statements, match) {
				continue
			}
			entity := normalizeSQLIdentifier(text[match[2]:match[3]])
			if entity == "" {
				continue
			}
			line := lines.lineAt(match[0])
			target := owner(match[0])
			key := target + "|" + strings.ToLower(entity) + "|" + access + "|" + intString(line)
			if seen[key] {
				continue
			}
			seen[key] = true
			result.DataAccesses[target] = append(result.DataAccesses[target], ParsedDataAccess{
				EntityName: entity,
				Access:     access,
				LineNumber: line,
				Source:     "sql_file",
			})
		}
	}

	addAccesses(sqlCreateTableRe, "write")
	addAccesses(sqlInsertStmtRe, "write")
	addAccesses(sqlUpdateStmtRe, "write")
	addAccesses(sqlDeleteStmtRe, "write")
	addAccesses(sqlMergeStmtRe, "write")
	addAccesses(sqlFromStmtRe, "read")
	addAccesses(sqlJoinStmtRe, "read")
	return result
}

// splitSQLRoutines finds every CREATE PROCEDURE/FUNCTION and bounds it by the
// next routine, the next GO batch separator or client-side DELIMITER terminator,
// or - for a dollar-quoted body - the statement terminator after the closing tag,
// whichever comes first.
func splitSQLRoutines(text, searchable, structural string, dollarBodies []sqlSpan, terminators []int, statements sqlStatementIndex) []sqlRoutine {
	var starts []int
	var names []string
	for _, match := range sqlCreateRoutineRe.FindAllStringSubmatchIndex(searchable, -1) {
		name := normalizeSQLIdentifier(text[match[2]:match[3]])
		if name == "" {
			continue
		}
		starts = append(starts, match[0])
		names = append(names, name)
	}
	goOffsets := append(append([]int(nil), statements.goLines...), terminators...)
	sort.Ints(goOffsets)
	routines := make([]sqlRoutine, 0, len(starts))
	for i, start := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		if idx := sort.SearchInts(goOffsets, start); idx < len(goOffsets) && goOffsets[idx] < end {
			end = goOffsets[idx]
		}
		// The first code body after the header is the routine's body; its
		// statement ends at the next semicolon after the closing tag.
		if b := sort.Search(len(dollarBodies), func(b int) bool { return dollarBodies[b].open >= start }); b < len(dollarBodies) && dollarBodies[b].open < end {
			body := dollarBodies[b]
			if body.closeEnd <= end {
				if semi := strings.IndexByte(structural[body.closeEnd:end], ';'); semi >= 0 {
					end = body.closeEnd + semi + 1
				}
			}
		}
		routines = append(routines, sqlRoutine{name: names[i], start: start, end: end})
	}
	return routines
}

func trimmedSQLEnd(text string, start, end int) int {
	for end > start && strings.ContainsRune(" \t\r\n", rune(text[end-1])) {
		end--
	}
	return maxInt(end, start+1)
}

// sqlMatchIsTableReference rejects keyword matches that are not table names.
// structural has comments, strings and quoted identifiers blanked, so keyword and
// parenthesis scans cannot be fooled by their contents.
func sqlMatchIsTableReference(re *regexp.Regexp, structural string, statements sqlStatementIndex, match []int) bool {
	switch re {
	case sqlUpdateStmtRe:
		// UPDATE in a referential action, lock clause or trigger event is not a
		// DML statement; a real one assigns with SET after the table list.
		switch sqlWordBefore(structural, match[0]) {
		case "ON", "FOR", "KEY":
			return false
		}
		switch strings.ToUpper(strings.TrimSpace(structural[match[2]:match[3]])) {
		case "SET", "ON", "OF", "AS", "NO", "CASCADE", "RESTRICT", "STATISTICS":
			return false
		}
		return sqlUpdateHasSet(structural, match[3])
	case sqlFromStmtRe:
		return sqlFromIsTableClause(structural, statements, match)
	case sqlJoinStmtRe:
		return !sqlFollowedByCall(structural, match[3]) && !strings.EqualFold(strings.Trim(structural[match[2]:match[3]], " "), "lateral")
	}
	return true
}

func sqlFromIsTableClause(structural string, statements sqlStatementIndex, match []int) bool {
	fromAt := match[0]
	switch sqlWordBefore(structural, fromAt) {
	case "DELETE": // DELETE FROM t is a write; the DELETE pattern records it.
		return false
	case "DISTINCT": // a IS [NOT] DISTINCT FROM b
		return false
	}
	if sqlDigitsRe.MatchString(structural[match[2]:match[3]]) { // SUBSTRING(s FROM 2)
		return false
	}
	if sqlFollowedByCall(structural, match[3]) { // FROM generate_series(1, 10)
		return false
	}
	if statements.keywordAt(structural, fromAt) == "REVOKE" { // REVOKE ... ON t FROM role
		return false
	}
	if open := sqlEnclosingParen(structural, fromAt); open >= 0 {
		name := sqlWordBeforeRaw(structural, open)
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		if sqlFromSyntaxFunctions[strings.ToLower(name)] {
			return false
		}
	}
	return true
}

func sqlFollowedByCall(structural string, end int) bool {
	for end < len(structural) && (structural[end] == ' ' || structural[end] == '\t') {
		end++
	}
	return end < len(structural) && structural[end] == '('
}

// sqlWordBefore returns the upper-cased alphabetic word that ends just before
// offset, skipping whitespace.
func sqlWordBefore(source string, offset int) string {
	return strings.ToUpper(sqlWordBeforeRaw(source, offset))
}

func sqlWordBeforeRaw(source string, offset int) string {
	end := offset
	for end > 0 && strings.ContainsRune(" \t\r\n", rune(source[end-1])) {
		end--
	}
	begin := end
	for begin > 0 {
		c := source[begin-1]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' {
			begin--
			continue
		}
		break
	}
	return source[begin:end]
}

// sqlEnclosingParen returns the offset of the unmatched "(" that encloses
// offset within the same statement, or -1.
func sqlEnclosingParen(source string, offset int) int {
	depth := 0
	limit := offset - maxParenLookback
	if limit < 0 {
		limit = 0
	}
	for i := offset - 1; i >= limit; i-- {
		switch source[i] {
		case ')':
			depth++
		case '(':
			if depth == 0 {
				return i
			}
			depth--
		case ';':
			if depth == 0 {
				return -1
			}
		}
	}
	return -1
}

// sqlStatementIndex holds the offsets where statements begin (after each ";" and
// each GO line) so the statement around an offset is found without rescanning.
type sqlStatementIndex struct {
	starts  []int
	goLines []int // offsets of the GO batch separator lines
}

func newSQLStatementIndex(structural string) sqlStatementIndex {
	var starts []int
	for i := 0; i < len(structural); i++ {
		if structural[i] == ';' {
			starts = append(starts, i+1)
		}
	}
	var goLines []int
	for _, match := range sqlGoBatchRe.FindAllStringIndex(structural, -1) {
		starts = append(starts, match[1])
		goLines = append(goLines, match[0])
	}
	sort.Ints(starts)
	return sqlStatementIndex{starts: starts, goLines: goLines}
}

// keywordAt returns the first word of the statement containing offset.
func (x sqlStatementIndex) keywordAt(source string, offset int) string {
	begin := 0
	if i := sort.SearchInts(x.starts, offset+1) - 1; i >= 0 {
		begin = x.starts[i]
	}
	for begin < offset && strings.ContainsRune(" \t\r\n", rune(source[begin])) {
		begin++
	}
	end := begin
	for end < offset && ((source[end] >= 'a' && source[end] <= 'z') || (source[end] >= 'A' && source[end] <= 'Z')) {
		end++
	}
	return strings.ToUpper(source[begin:end])
}

// sqlUpdateEndKeywords start a new statement; reaching one before SET means the
// UPDATE keyword was not the head of an UPDATE ... SET statement.
var sqlUpdateEndKeywords = map[string]bool{
	"SELECT": true, "INSERT": true, "DELETE": true, "UPDATE": true, "MERGE": true, "TRUNCATE": true,
	"FROM": true, "WHERE": true, "VALUES": true, "BEGIN": true, "END": true, "CREATE": true,
	"DROP": true, "ALTER": true, "GRANT": true, "REVOKE": true, "EXEC": true, "EXECUTE": true,
	"DECLARE": true, "IF": true, "RETURN": true, "GO": true, "WHEN": true, "THEN": true, "ELSE": true,
}

// sqlUpdateHasSet reports whether the table list that starts at offset (alias,
// WITH (hints), JOIN ... ON ..., comma-separated tables) is followed by SET and
// an assignment.
func sqlUpdateHasSet(structural string, offset int) bool {
	limit := offset + maxParenLookback
	if limit > len(structural) {
		limit = len(structural)
	}
	depth := 0
	for i := offset; i < limit; {
		c := structural[i]
		switch {
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			if depth < 0 {
				return false
			}
			i++
		case c == ';':
			return false
		case sqlIsWordByte(c):
			end := i
			for end < len(structural) && sqlIsWordByte(structural[end]) {
				end++
			}
			if depth == 0 {
				word := strings.ToUpper(structural[i:end])
				if word == "SET" {
					return sqlSetAssignRe.MatchString(structural[end:min(len(structural), end+256)])
				}
				if sqlUpdateEndKeywords[word] {
					return false
				}
			}
			i = end
		default:
			i++
		}
	}
	return false
}

func sqlIsWordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$' || c == '@' || c == '#'
}

// normalizeSQLIdentifier removes quoting from each part of a (possibly
// qualified) name: [dbo].[Order Items] -> dbo.Order Items, "a"."b" -> a.b.
func normalizeSQLIdentifier(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimRight(value, ";,)")
	var parts []string
	var part strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch c {
		case '"', '`', '[':
			closer := c
			if c == '[' {
				closer = ']'
			}
			i++
			for i < len(value) {
				if value[i] == closer {
					if i+1 < len(value) && value[i+1] == closer { // "" and ]] escape the closer
						part.WriteByte(closer)
						i += 2
						continue
					}
					break
				}
				part.WriteByte(value[i])
				i++
			}
		case '.':
			parts = append(parts, part.String())
			part.Reset()
		case '\'':
			// Stray string quote around a name.
		default:
			part.WriteByte(c)
		}
	}
	parts = append(parts, part.String())
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return strings.Trim(strings.Join(parts, "."), ".")
}

func intString(value int) string {
	return strconv.Itoa(value)
}

// sqlLineIndex maps byte offsets to 1-based line numbers.
type sqlLineIndex struct {
	starts []int // byte offset where each line begins
}

func newSQLLineIndex(text string) sqlLineIndex {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return sqlLineIndex{starts: starts}
}

func (l sqlLineIndex) count() int { return len(l.starts) }

func (l sqlLineIndex) lineAt(offset int) int {
	if offset < 0 {
		offset = 0
	}
	return sort.SearchInts(l.starts, offset+1)
}

// sqlSpan is a dollar-quoted body whose text is SQL code.
type sqlSpan struct {
	open     int // offset of the opening tag
	bodyFrom int
	bodyTo   int
	closeEnd int // offset just after the closing tag
}

// sqlDialect selects lexical rules that differ between SQL families.
type sqlDialect struct {
	mysql     bool // # comments, executable comments, "..." strings
	backslash bool // \' is an escaped quote inside a string
}

// detectSQLDialect enables the optional rules only for scripts that show
// MySQL-family constructs, so T-SQL #temp tables and the literal backslash of
// 'C:\' keep working elsewhere. A backslash-escaped quote followed by a letter
// ('Christie\'s') cannot be valid in standard SQL, so it also switches escapes on.
func detectSQLDialect(text string) sqlDialect {
	mysql := sqlHasMySQLMarker(text) && !strings.Contains(text, "ANSI_QUOTES") && !strings.Contains(text, "ansi_quotes")
	return sqlDialect{mysql: mysql, backslash: mysql || sqlBackslashQuoteRe.MatchString(text)}
}

// sqlHasMySQLMarker looks for dump and client constructs that only MySQL-family
// scripts contain: table options, DELIMITER directives, /*!NNNNN executable
// comments, # comment lines and backtick-quoted table names. It uses substring
// searches because it runs over whole files.
func sqlHasMySQLMarker(text string) bool {
	for _, token := range []string{"AUTO_INCREMENT", "auto_increment", "ENGINE=", "ENGINE =", "engine=", "engine ="} {
		if strings.Contains(text, token) {
			return true
		}
	}
	for _, token := range []string{"DELIMITER", "delimiter"} {
		if sqlAnyIndex(text, token, func(at int) bool { return sqlDelimiterLineAt(text, at) }) {
			return true
		}
	}
	if sqlAnyIndex(text, "/*!", func(at int) bool { return at+3 < len(text) && text[at+3] >= '0' && text[at+3] <= '9' }) {
		return true
	}
	if sqlAnyIndex(text, "#", func(at int) bool {
		if at+1 >= len(text) || !strings.ContainsRune(" \t#=-", rune(text[at+1])) {
			return false
		}
		for b := at - 1; b >= 0 && text[b] != '\n'; b-- {
			if text[b] != ' ' && text[b] != '\t' {
				return false
			}
		}
		return true
	}) {
		return true
	}
	return sqlAnyIndex(text, "`", func(at int) bool {
		switch sqlWordBefore(text, at) {
		case "FROM", "JOIN", "INTO", "UPDATE", "TABLE", "EXISTS":
			return true
		}
		return false
	})
}

// sqlAnyIndex reports whether accept holds for any occurrence of needle.
func sqlAnyIndex(text, needle string, accept func(offset int) bool) bool {
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], needle)
		if i < 0 {
			return false
		}
		if accept(from + i) {
			return true
		}
		from += i + len(needle)
	}
	return false
}

// maskSQL blanks comments and string values without changing byte offsets or
// line numbers. Quoted identifiers stay visible for declaration/access extraction
// unless hideIdentifiers is set (used for keyword and parenthesis scans).
//
// Dollar-quoted bodies are the body of a routine (CREATE FUNCTION ... AS $$ ...
// $$, DO $$ ... $$) or a string literal. A body that follows AS or DO is SQL and
// stays visible with its own comments and strings masked; any other body is a
// string value and is blanked. The code bodies are returned so routine
// boundaries can end at the statement that closes them.
//
// A string literal that itself starts with a statement keyword is dynamic SQL
// (EXEC('SELECT ...'), sp_executesql N'UPDATE ...') and is masked as SQL in place.
//
// The dialect enables MySQL-family lexical rules: backslash escapes in strings,
// "..." strings, # line comments and /*! ... */ executable comments, whose body is
// code. A client "DELIMITER x" line is blanked, and each later x becomes a ";"
// (the offsets of those terminators are returned) so "$$" is not read as a dollar
// quote.
func maskSQL(source string, hideIdentifiers bool, dialect sqlDialect) (string, []sqlSpan, []int) {
	out := []byte(source)
	var bodies []sqlSpan
	var terminators []int
	delimiter := ""
	executableComments := 0
	hide := func(start, end int) {
		for j := start; j < end; j++ {
			if out[j] != '\n' && out[j] != '\r' {
				out[j] = ' '
			}
		}
	}
	for i := 0; i < len(source); {
		start := i
		switch {
		case (source[i] == 'D' || source[i] == 'd') && sqlDelimiterLineAt(source, i):
			for i < len(source) && source[i] != '\n' {
				i++
			}
			delimiter = strings.Fields(source[start+len("DELIMITER") : i])[0]
			if delimiter == ";" {
				delimiter = ""
			}
			hide(start, i)
		case strings.HasPrefix(source[i:], "--"):
			for i < len(source) && source[i] != '\n' {
				i++
			}
			hide(start, i)
		case source[i] == '#' && dialect.mysql:
			for i < len(source) && source[i] != '\n' {
				i++
			}
			hide(start, i)
		case dialect.mysql && strings.HasPrefix(source[i:], "/*!"):
			// Executable comment: only the markers are blanked.
			i += 3
			for i < len(source) && source[i] >= '0' && source[i] <= '9' {
				i++
			}
			executableComments++
			hide(start, i)
		case executableComments > 0 && strings.HasPrefix(source[i:], "*/"):
			executableComments--
			i += 2
			hide(start, i)
		case strings.HasPrefix(source[i:], "/*"):
			i += 2
			depth := 1
			for i < len(source) && depth > 0 {
				if strings.HasPrefix(source[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(source[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			hide(start, i)
		case source[i] == '\'' || (dialect.mysql && source[i] == '"'):
			// MySQL also reads "..." as a string.
			quote := source[i]
			escapeString := dialect.backslash || (i > 0 && (source[i-1] == 'E' || source[i-1] == 'e') && (i < 2 || !((source[i-2] >= 'a' && source[i-2] <= 'z') || (source[i-2] >= 'A' && source[i-2] <= 'Z') || source[i-2] == '_')))
			closed := false
			i++
			for i < len(source) {
				if source[i] == quote {
					i++
					if i < len(source) && source[i] == quote {
						i++
						continue
					}
					closed = true
					break
				}
				if escapeString && source[i] == '\\' && i+1 < len(source) {
					i += 2
				} else {
					i++
				}
			}
			hide(start, i)
			contentEnd := i
			if closed {
				contentEnd--
			}
			if contentEnd < start+1 {
				contentEnd = start + 1
			}
			if content := source[start+1 : contentEnd]; sqlLiteralIsStatement(content) {
				inner, _, _ := maskSQL(sqlUnquoteLiteral(content, quote), hideIdentifiers, dialect)
				copy(out[start+1:contentEnd], inner)
			}
		case delimiter != "" && strings.HasPrefix(source[i:], delimiter):
			terminators = append(terminators, i)
			if !strings.Contains(delimiter, ";") {
				out[i] = ';'
				hide(i+1, i+len(delimiter))
			}
			i += len(delimiter)
		case source[i] == '$':
			tagEnd, ok := sqlDollarTagEnd(source, i)
			if !ok {
				i++
				break
			}
			tag := source[i:tagEnd]
			closeAt := strings.Index(source[tagEnd:], tag)
			if closeAt < 0 {
				// Unterminated: treat the rest of the file as the body.
				closeAt = len(source) - tagEnd
			}
			bodyFrom, bodyTo := tagEnd, tagEnd+closeAt
			closeEnd := bodyTo + len(tag)
			if closeEnd > len(source) {
				closeEnd = len(source)
			}
			if sqlDollarBodyIsCode(string(out[maxInt(0, i-64):i])) {
				inner, _, _ := maskSQL(source[bodyFrom:bodyTo], hideIdentifiers, dialect)
				copy(out[bodyFrom:bodyTo], inner)
				bodies = append(bodies, sqlSpan{open: i, bodyFrom: bodyFrom, bodyTo: bodyTo, closeEnd: closeEnd})
			} else {
				hide(bodyFrom, bodyTo)
			}
			i = closeEnd
		case source[i] == '"' || source[i] == '`' || source[i] == '[':
			closer := source[i]
			if closer == '[' {
				closer = ']'
			}
			i++
			for i < len(source) {
				if source[i] == closer {
					i++
					if i < len(source) && source[i] == closer {
						i++
						continue
					}
					break
				}
				i++
			}
			if hideIdentifiers {
				hide(start, i)
			}
		default:
			i++
		}
	}
	return string(out), bodies, terminators
}

// sqlDelimiterLineAt reports whether a client-side "DELIMITER <punctuation>"
// directive starts at offset (first word on its line). A column named delimiter
// is not one: the argument must be pure punctuation.
func sqlDelimiterLineAt(source string, offset int) bool {
	const word = "DELIMITER"
	if len(source)-offset < len(word)+2 || !strings.EqualFold(source[offset:offset+len(word)], word) {
		return false
	}
	for b := offset - 1; b >= 0 && source[b] != '\n'; b-- {
		if source[b] != ' ' && source[b] != '\t' {
			return false
		}
	}
	rest := source[offset+len(word):]
	if rest[0] != ' ' && rest[0] != '\t' {
		return false
	}
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	fields := strings.Fields(rest)
	return len(fields) > 0 && strings.Trim(fields[0], ";$/|@#%&!^~\\:*+=<>?.-") == ""
}

// sqlStatementKeywords are the words that open a statement whose text is worth
// analysing when it appears inside a string literal.
var sqlStatementKeywords = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"MERGE": true, "WITH": true, "TRUNCATE": true,
}

// sqlLiteralIsStatement reports whether a string literal's text begins (after
// whitespace) with a statement keyword, i.e. it is dynamic SQL rather than data.
func sqlLiteralIsStatement(content string) bool {
	begin := 0
	for begin < len(content) && strings.ContainsRune(" \t\r\n", rune(content[begin])) {
		begin++
	}
	end := begin
	for end < len(content) && ((content[end] >= 'a' && content[end] <= 'z') || (content[end] >= 'A' && content[end] <= 'Z')) {
		end++
	}
	if end == begin || (end < len(content) && sqlIsWordByte(content[end])) {
		return false
	}
	return sqlStatementKeywords[strings.ToUpper(content[begin:end])]
}

// sqlUnquoteLiteral turns the doubled quote characters of a string literal's
// content into real ones without moving any byte: each pair becomes one quote
// followed by a space, so a nested literal stays delimited.
func sqlUnquoteLiteral(content string, quote byte) string {
	out := []byte(content)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == quote && out[i+1] == quote {
			out[i+1] = ' '
			i++
		}
	}
	return string(out)
}

// sqlDollarTagEnd reports the end of a dollar-quote opening tag ($$ or $tag$)
// starting at offset. A tag cannot start with a digit, so positional parameters
// ($1) never open a body, and a "$" inside an identifier (my$var$) does not
// either.
func sqlDollarTagEnd(source string, offset int) (int, bool) {
	if offset > 0 {
		c := source[offset-1]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$' {
			return 0, false
		}
	}
	i := offset + 1
	for i < len(source) {
		c := source[i]
		if c == '$' {
			return i + 1, true
		}
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c >= 0x80
		isDigit := c >= '0' && c <= '9'
		if !isLetter && !(isDigit && i > offset+1) {
			return 0, false
		}
		i++
	}
	return 0, false
}

// sqlDollarBodyIsCode reports whether a dollar-quoted body is executable SQL,
// given the already-masked text before its opening tag: the keyword before it is
// AS (CREATE FUNCTION ... AS $$) or DO (anonymous block). After anything else
// ("IS $$comment$$", "= $$text$$", VALUES (...)) it is an ordinary string.
func sqlDollarBodyIsCode(before string) bool {
	switch sqlWordBefore(before, len(before)) {
	case "AS", "DO":
		return true
	}
	return false
}
