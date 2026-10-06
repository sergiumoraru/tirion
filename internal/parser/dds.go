package parser

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type DDSParser struct{}

type ddsRecordFormat struct {
	Name      string
	StartLine int
	EndLine   int
	Fields    []ParsedField
}

var ddsSupportedExtensions = map[string]bool{
	".dspf": true,
	".pf":   true,
	".lf":   true,
	".prtf": true,
}

var (
	ddsIdentifierRe = regexp.MustCompile(`^[A-Za-z_#@$][A-Za-z0-9_#@$]*$`)
	ddsRecordRe     = regexp.MustCompile(`(?i)^\s*A(?:\s+[A-Z0-9N]+)?\s+R\s+([A-Za-z_#@$][A-Za-z0-9_#@$]*)\b`)
)

func NewDDSParser() *DDSParser {
	return &DDSParser{}
}

func (p *DDSParser) CanParse(filePath string) bool {
	if ddsSupportedExtensions[strings.ToLower(filepath.Ext(filePath))] {
		return true
	}
	switch kind, ok := detectIBMISourceMemberKind(filePath); {
	case !ok:
		return false
	case kind == "dds", kind == "dspf", kind == "pf", kind == "lf", kind == "prtf":
		return true
	default:
		return false
	}
}

func (p *DDSParser) ParseFile(filePath string, content []byte) (result ParsedFile) {
	result = ParsedFile{
		Path:          filePath,
		Language:      p.languageForPath(filePath),
		Functions:     []ParsedFunction{},
		Classes:       []ParsedClass{},
		Imports:       []ParsedImport{},
		FunctionCalls: make(map[string][]ParsedFunctionCall),
		Endpoints:     []ParsedEndpoint{},
		HttpCalls:     make(map[string][]ParsedHttpCall),
		DataAccesses:  make(map[string][]ParsedDataAccess),
	}
	defer recoverParsePanic(&result)

	lines := strings.Split(string(content), "\n")
	fileName := p.artifactNameForPath(filePath)
	if fileName == "" {
		fileName = "DDS_ARTIFACT"
	}

	var records []ddsRecordFormat
	var current *ddsRecordFormat
	fileFields := make(map[string]ParsedField)

	for i, rawLine := range lines {
		lineNum := i + 1
		line := strings.TrimRight(rawLine, "\r")
		if name, ok := parseDDSRecordName(line); ok {
			if current != nil {
				current.EndLine = lineNum - 1
				records = append(records, *current)
			}
			current = &ddsRecordFormat{
				Name:      name,
				StartLine: lineNum,
				EndLine:   lineNum,
			}
			continue
		}

		field, ok := parseDDSField(line, lineNum)
		if !ok {
			continue
		}
		if current != nil {
			current.Fields = append(current.Fields, field)
			current.EndLine = lineNum
		}
		key := strings.ToLower(field.Name)
		if _, exists := fileFields[key]; !exists {
			fileFields[key] = field
		}
	}

	if current != nil {
		if current.EndLine < current.StartLine {
			current.EndLine = current.StartLine
		}
		records = append(records, *current)
	}

	allFields := make([]ParsedField, 0, len(fileFields))
	for _, field := range fileFields {
		allFields = append(allFields, field)
	}
	sort.Slice(allFields, func(i, j int) bool {
		if allFields[i].StartLine == allFields[j].StartLine {
			return strings.ToLower(allFields[i].Name) < strings.ToLower(allFields[j].Name)
		}
		return allFields[i].StartLine < allFields[j].StartLine
	})

	result.Classes = append(result.Classes, ParsedClass{
		Name:       fileName,
		StartLine:  1,
		EndLine:    maxInt(len(lines), 1),
		IsExported: true,
		Fields:     allFields,
		Modifiers:  []string{p.languageForPath(filePath)},
	})

	for _, record := range records {
		result.Classes = append(result.Classes, ParsedClass{
			Name:         fileName + "." + record.Name,
			StartLine:    record.StartLine,
			EndLine:      maxInt(record.EndLine, record.StartLine),
			IsExported:   true,
			Fields:       record.Fields,
			ExtendsClass: fileName,
			Modifiers:    []string{p.languageForPath(filePath), "record_format"},
		})
	}

	return result
}

func (p *DDSParser) languageForPath(filePath string) string {
	if kind, ok := detectIBMISourceMemberKind(filePath); ok {
		switch kind {
		case "dspf", "pf", "lf", "prtf", "dds":
			return kind
		}
	}
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".dspf":
		return "dspf"
	case ".pf":
		return "pf"
	case ".lf":
		return "lf"
	case ".prtf":
		return "prtf"
	default:
		return "dds"
	}
}

func (p *DDSParser) artifactNameForPath(filePath string) string {
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	return strings.TrimSpace(strings.TrimSuffix(base, ext))
}

func parseDDSRecordName(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || isDDSCommentLine(trimmed) {
		return "", false
	}
	match := ddsRecordRe.FindStringSubmatch(trimmed)
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

func parseDDSField(line string, lineNum int) (ParsedField, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || isDDSCommentLine(trimmed) {
		return ParsedField{}, false
	}
	tokens := strings.Fields(trimmed)
	if len(tokens) < 2 {
		return ParsedField{}, false
	}
	if !strings.EqualFold(tokens[0], "A") {
		return ParsedField{}, false
	}

	idx := 1
	skippedIndicator := false
	if idx < len(tokens) && isDDSConditionToken(tokens[idx]) {
		idx++
		skippedIndicator = true
	}
	if idx >= len(tokens) {
		return ParsedField{}, false
	}
	if strings.EqualFold(tokens[idx], "R") {
		return ParsedField{}, false
	}

	name := tokens[idx]
	if !ddsIdentifierRe.MatchString(name) {
		return ParsedField{}, false
	}
	rest := tokens[idx+1:]
	fieldType := parseDDSFieldType(rest)
	if fieldType == "" {
		if skippedIndicator || len(rest) != 1 || !looksLikeDDSSpecialFieldKeyword(rest[0]) {
			return ParsedField{}, false
		}
	}

	return ParsedField{
		Name:      name,
		FieldType: fieldType,
		StartLine: lineNum,
	}, true
}

func parseDDSFieldType(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	first := strings.TrimSpace(tokens[0])
	if !looksLikeDDSLengthType(first) {
		return ""
	}
	if len(tokens) > 1 {
		second := strings.TrimSpace(tokens[1])
		if looksLikeDDSLengthType(second) {
			return first + " " + second
		}
	}
	return first
}

func isDDSCommentLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "A*") || strings.HasPrefix(trimmed, "*")
}

func isDDSConditionToken(token string) bool {
	token = strings.TrimSpace(strings.ToUpper(token))
	if token == "" {
		return false
	}
	if token == "N" {
		return true
	}
	if strings.HasPrefix(token, "N") && len(token) <= 3 && digitsOnly(token[1:]) {
		return true
	}
	return len(token) <= 2 && digitsOnly(token)
}

func looksLikeDDSLengthType(token string) bool {
	token = strings.TrimSpace(strings.ToUpper(token))
	if token == "" {
		return false
	}
	if strings.ContainsAny(token, "()'\"") {
		return false
	}
	hasDigit := false
	for _, r := range token {
		if r >= '0' && r <= '9' {
			hasDigit = true
			continue
		}
		if (r >= 'A' && r <= 'Z') || r == '-' {
			continue
		}
		return false
	}
	return hasDigit
}

func looksLikeDDSSpecialFieldKeyword(token string) bool {
	token = strings.TrimSpace(strings.ToUpper(token))
	if token == "" {
		return false
	}
	if strings.Contains(token, "(") {
		return true
	}
	return strings.HasPrefix(token, "SFL") || strings.HasPrefix(token, "MSG")
}

func digitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
