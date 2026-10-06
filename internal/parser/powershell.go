package parser

import (
	"path/filepath"
	"regexp"
	"strings"
)

type PowerShellParser struct{}

var (
	powershellFunctionRe = regexp.MustCompile(`(?i)^\s*function\s+([A-Za-z_][A-Za-z0-9_-]*)\b`)
	powershellCommandRe  = regexp.MustCompile(`^\s*(?:&\s*)?([A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z_][A-Za-z0-9_-]*)?)\b`)
)

var powershellIgnoredCommands = map[string]bool{
	"begin": true, "break": true, "catch": true, "class": true, "continue": true,
	"data": true, "do": true, "dynamicparam": true, "else": true, "elseif": true,
	"end": true, "exit": true, "filter": true, "finally": true, "for": true,
	"foreach": true, "from": true, "if": true, "in": true, "param": true,
	"process": true, "return": true, "switch": true, "throw": true, "trap": true,
	"try": true, "until": true, "using": true, "while": true,
}

func NewPowerShellParser() *PowerShellParser {
	return &PowerShellParser{}
}

func (p *PowerShellParser) CanParse(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	return ext == ".ps1" || ext == ".psm1" || ext == ".psd1"
}

// ParseFile recovers parser panics into a per-file failure.
func (p *PowerShellParser) ParseFile(filePath string, content []byte) ParsedFile {
	return parseGuarded(filePath, "powershell", func() ParsedFile { return p.parseFile(filePath, content) })
}

func (p *PowerShellParser) parseFile(filePath string, content []byte) ParsedFile {
	text := string(content)
	lines := strings.Split(text, "\n")
	result := ParsedFile{
		Language:      "powershell",
		Functions:     []ParsedFunction{{Name: "_module_", StartLine: 1, EndLine: maxInt(1, len(lines)), SourceCode: text}},
		FunctionCalls: map[string][]ParsedFunctionCall{},
		HttpCalls:     map[string][]ParsedHttpCall{},
		DataAccesses:  map[string][]ParsedDataAccess{},
		SqlStatements: map[string][]ParsedSqlStatementCall{},
		SqsProducers:  map[string][]ParsedSqsProducer{},
		LocalVarTypes: map[string]map[string]string{},
	}

	currentFunction := "_module_"
	functionStart := 0
	braceDepth := 0
	for i, rawLine := range lines {
		lineNumber := i + 1
		line := stripPowerShellComment(rawLine)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if match := powershellFunctionRe.FindStringSubmatch(trimmed); len(match) == 2 {
			currentFunction = match[1]
			functionStart = lineNumber
			braceDepth = countPowerShellBraceDelta(line)
			result.Functions = append(result.Functions, ParsedFunction{
				Name:       currentFunction,
				StartLine:  lineNumber,
				EndLine:    lineNumber,
				IsExported: true,
			})
			continue
		}

		if call := parsePowerShellCommand(trimmed, lineNumber); call != nil {
			result.FunctionCalls[currentFunction] = append(result.FunctionCalls[currentFunction], *call)
			if httpCall := parsePowerShellHTTPCall(trimmed, lineNumber); httpCall != nil {
				result.HttpCalls[currentFunction] = append(result.HttpCalls[currentFunction], *httpCall)
			}
		}

		if currentFunction != "_module_" {
			braceDepth += countPowerShellBraceDelta(line)
			if braceDepth <= 0 && functionStart > 0 {
				for idx := range result.Functions {
					if result.Functions[idx].Name == currentFunction && result.Functions[idx].StartLine == functionStart {
						result.Functions[idx].EndLine = lineNumber
						break
					}
				}
				currentFunction = "_module_"
				functionStart = 0
			}
		}
	}
	return result
}

func parsePowerShellCommand(line string, lineNumber int) *ParsedFunctionCall {
	if strings.HasPrefix(line, "#") {
		return nil
	}
	if strings.HasPrefix(line, "$") {
		if idx := strings.Index(line, "="); idx >= 0 && idx < len(line)-1 {
			line = strings.TrimSpace(line[idx+1:])
		}
	}
	match := powershellCommandRe.FindStringSubmatch(line)
	if len(match) != 2 {
		return nil
	}
	name := strings.TrimSpace(match[1])
	if name == "" || powershellIgnoredCommands[strings.ToLower(name)] {
		return nil
	}
	return &ParsedFunctionCall{
		CalleeName: name,
		MethodName: name,
		LineNumber: lineNumber,
	}
}

func parsePowerShellHTTPCall(line string, lineNumber int) *ParsedHttpCall {
	lower := strings.ToLower(line)
	if !strings.HasPrefix(lower, "invoke-restmethod") && !strings.HasPrefix(lower, "invoke-webrequest") {
		return nil
	}
	url := firstPowerShellNamedArg(line, "Uri")
	if url == "" {
		url = firstPowerShellNamedArg(line, "Url")
	}
	if url == "" {
		return nil
	}
	method := strings.ToUpper(firstPowerShellNamedArg(line, "Method"))
	if method == "" {
		method = "GET"
	}
	return &ParsedHttpCall{
		HttpMethod: method,
		UrlPattern: normalizePowerShellURL(url),
		LineNumber: lineNumber,
		ClientType: "powershell",
	}
}

func firstPowerShellNamedArg(line, name string) string {
	re := regexp.MustCompile(`(?i)-` + regexp.QuoteMeta(name) + `\s+(?:"([^"]+)"|'([^']+)'|([^\s]+))`)
	match := re.FindStringSubmatch(line)
	if len(match) == 0 {
		return ""
	}
	for _, part := range match[1:] {
		if part != "" {
			return strings.Trim(part, `"'`)
		}
	}
	return ""
}

func normalizePowerShellURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	if strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}

func stripPowerShellComment(line string) string {
	inSingle := false
	inDouble := false
	for i, ch := range line {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

func countPowerShellBraceDelta(line string) int {
	delta := 0
	inSingle := false
	inDouble := false
	for _, ch := range line {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '{':
			if !inSingle && !inDouble {
				delta++
			}
		case '}':
			if !inSingle && !inDouble {
				delta--
			}
		}
	}
	return delta
}
