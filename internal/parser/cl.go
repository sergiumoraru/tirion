package parser

import (
	"path/filepath"
	"regexp"
	"strings"
)

type CLParser struct{}

type clFunctionFrame struct {
	Name          string
	StartLine     int
	FunctionIndex int
}

type clLogicalLine struct {
	StartLine int
	Text      string
}

var clSupportedExtensions = map[string]bool{
	".cl":   true,
	".clle": true,
	".clp":  true,
}

var (
	clProgramStartRe = regexp.MustCompile(`(?i)^\s*PGM\b`)
	clProgramEndRe   = regexp.MustCompile(`(?i)^\s*ENDPGM\b`)
	clSubrStartRe    = regexp.MustCompile(`(?i)^\s*SUBR\b(?:\s+SUBR\(\s*([^)]+)\s*\)|\s+([A-Za-z_#@$][A-Za-z0-9_#@$]*))?`)
	clSubrEndRe      = regexp.MustCompile(`(?i)^\s*ENDSUBR\b`)
	clCallProgramRe  = regexp.MustCompile(`(?i)\bCALL\b.*?\bPGM\(\s*([^)]+)\s*\)`)
	clCallPrcRe      = regexp.MustCompile(`(?i)\bCALLPRC\b.*?\bPRC\(\s*([^)]+)\s*\)`)
	clCallSubrRe     = regexp.MustCompile(`(?i)\bCALLSUBR\b.*?\bSUBR\(\s*([^)]+)\s*\)`)
	clDclfRe         = regexp.MustCompile(`(?i)\bDCLF\b.*?\bFILE\(\s*([^)]+)\s*\)`)
	clBindDirRe      = regexp.MustCompile(`(?i)\bBNDDIR\(\s*([^)]+)\s*\)`)
	clSrvpgmRe       = regexp.MustCompile(`(?i)\bSRVPGM\(\s*([^)]+)\s*\)`)
	clPgmObjectRe    = regexp.MustCompile(`(?i)\bPGM\(\s*([^)]+)\s*\)`)
	clSrcMbrRe       = regexp.MustCompile(`(?i)\bSRCMBR\(\s*([^)]+)\s*\)`)
	clObjTargetRe    = regexp.MustCompile(`(?i)\bOBJ\(\(\s*([^\s)]+)\s+\*([A-Za-z0-9]+)`)
	clAddBndDireRe   = regexp.MustCompile(`(?i)\bADDBNDDIRE\b`)
	clCrtSrvpgmRe    = regexp.MustCompile(`(?i)\bCRTSRVPGM\b`)
	clSndRcvfRe      = regexp.MustCompile(`(?i)\bSNDRCVF\b.*?\bRCDFMT\(\s*([^)]+)\s*\)`)
	clSndfRe         = regexp.MustCompile(`(?i)\bSNDF\b.*?\bRCDFMT\(\s*([^)]+)\s*\)`)
	clRcvfFmtRe      = regexp.MustCompile(`(?i)\bRCVF\b.*?\bRCDFMT\(\s*([^)]+)\s*\)`)
	clRcvfRe         = regexp.MustCompile(`(?i)\bRCVF\b`)
	clInlineComment  = regexp.MustCompile(`/\*.*?\*/`)
	clLabelPrefixRe  = regexp.MustCompile(`^\s*[A-Za-z_#@$][A-Za-z0-9_#@$]*\s*:\s*`)
)

func NewCLParser() *CLParser {
	return &CLParser{}
}

func (p *CLParser) CanParse(filePath string) bool {
	if clSupportedExtensions[strings.ToLower(filepath.Ext(filePath))] {
		return true
	}
	switch kind, ok := detectIBMISourceMemberKind(filePath); {
	case !ok:
		return false
	case kind == "cl", kind == "clle", kind == "clp":
		return true
	default:
		return false
	}
}

func (p *CLParser) ParseFile(filePath string, content []byte) (result ParsedFile) {
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

	rawLines := strings.Split(string(content), "\n")
	logicalLines := buildCLLogicalLines(rawLines)
	programName := p.programNameForPath(filePath)
	artifactName := NormalizeIBMiObjectName(programName)
	seenImports := make(map[string]bool)
	declaredFiles := make(map[string]bool)
	seenBindings := make(map[string]bool)

	var stack []clFunctionFrame
	mainlineStart := 0
	mainlineEnd := 0

	ensureProgram := func(line int) string {
		if programName == "" {
			programName = "CL_PROGRAM"
		}
		if mainlineStart == 0 || line < mainlineStart {
			mainlineStart = line
		}
		if line > mainlineEnd {
			mainlineEnd = line
		}
		return programName
	}

	currentFunctionName := func(line int) string {
		if len(stack) > 0 {
			return stack[len(stack)-1].Name
		}
		return ensureProgram(line)
	}

	appendFunction := func(name string, startLine int) {
		idx := len(result.Functions)
		result.Functions = append(result.Functions, ParsedFunction{
			Name:       name,
			StartLine:  startLine,
			EndLine:    startLine,
			IsExported: true,
		})
		stack = append(stack, clFunctionFrame{
			Name:          name,
			StartLine:     startLine,
			FunctionIndex: idx,
		})
	}

	closeFunction := func(line int) {
		if len(stack) == 0 {
			return
		}
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if frame.FunctionIndex >= 0 && frame.FunctionIndex < len(result.Functions) {
			result.Functions[frame.FunctionIndex].EndLine = line
		}
	}

	appendBinding := func(binding ParsedIBMiBinding) {
		if binding.BindingName == "" {
			return
		}
		key := strings.ToLower(binding.OwnerObject) + "|" + strings.ToLower(binding.BindingName) + "|" + strings.ToLower(binding.BindingType) + "|" + strings.ToLower(binding.TargetObject)
		if seenBindings[key] {
			return
		}
		seenBindings[key] = true
		result.IBMiBindings = append(result.IBMiBindings, binding)
	}

	for _, logical := range logicalLines {
		clean := stripCLLine(logical.Text)
		if clean == "" {
			continue
		}
		for _, binding := range extractCLIBMIBindings(clean, artifactName, logical.StartLine) {
			appendBinding(binding)
		}
		if importPath, ok := extractCLImport(clean); ok {
			key := strings.ToLower(importPath)
			if !seenImports[key] {
				seenImports[key] = true
				result.Imports = append(result.Imports, ParsedImport{Path: importPath})
			}
			declaredFiles[importPath] = true
		}

		switch {
		case clProgramStartRe.MatchString(clean):
			continue
		case clProgramEndRe.MatchString(clean):
			continue
		}

		if name, ok := parseCLSubroutineStart(clean); ok {
			appendFunction(name, logical.StartLine)
			continue
		}
		if clSubrEndRe.MatchString(clean) {
			closeFunction(logical.StartLine)
			continue
		}

		fnName := currentFunctionName(logical.StartLine)
		for _, call := range extractCLCalls(clean, logical.StartLine) {
			result.FunctionCalls[fnName] = append(result.FunctionCalls[fnName], call)
		}
		if access, ok := extractCLDataAccess(clean, logical.StartLine); ok {
			result.DataAccesses[fnName] = append(result.DataAccesses[fnName], access)
		}
		for _, access := range extractCLDisplayAccesses(clean, logical.StartLine, resolveSingleCLDeclaredFile(declaredFiles)) {
			result.DataAccesses[fnName] = append(result.DataAccesses[fnName], access)
		}
	}

	lastLine := len(rawLines)
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if frame.FunctionIndex >= 0 && frame.FunctionIndex < len(result.Functions) {
			result.Functions[frame.FunctionIndex].EndLine = lastLine
		}
	}

	if mainlineStart > 0 {
		mainFn := ParsedFunction{
			Name:       programName,
			StartLine:  mainlineStart,
			EndLine:    maxInt(mainlineEnd, mainlineStart),
			IsExported: true,
		}
		result.Functions = append([]ParsedFunction{mainFn}, result.Functions...)
		result.IBMiExports = append(result.IBMiExports, ParsedIBMiExport{
			FunctionName: programName,
			ExportName:   artifactName,
			ObjectName:   artifactName,
			ObjectType:   "program",
			SourceType:   "program_entry",
			LineNumber:   mainlineStart,
		})
	}

	for i := range result.Functions {
		start := clampLine(result.Functions[i].StartLine, len(rawLines))
		end := clampLine(result.Functions[i].EndLine, len(rawLines))
		if start <= 0 || end <= 0 || start > end {
			continue
		}
		source := strings.Join(rawLines[start-1:end], "\n")
		if len(source) > 4000 {
			source = source[:4000] + "..."
		}
		result.Functions[i].SourceCode = source
	}

	return result
}

func (p *CLParser) languageForPath(filePath string) string {
	if kind, ok := detectIBMISourceMemberKind(filePath); ok {
		switch kind {
		case "clle", "clp", "cl":
			return kind
		}
	}
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".clle":
		return "clle"
	case ".clp":
		return "clp"
	default:
		return "cl"
	}
}

func (p *CLParser) programNameForPath(filePath string) string {
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	base = strings.TrimSuffix(base, ext)
	base = strings.TrimSpace(base)
	if base == "" {
		return "CL_PROGRAM"
	}
	return base
}

func buildCLLogicalLines(rawLines []string) []clLogicalLine {
	var logical []clLogicalLine
	var current strings.Builder
	startLine := 0

	flush := func() {
		text := strings.TrimSpace(current.String())
		if text != "" {
			logical = append(logical, clLogicalLine{StartLine: startLine, Text: text})
		}
		current.Reset()
		startLine = 0
	}

	for i, raw := range rawLines {
		line := stripCLComments(raw)
		line = strings.TrimRight(line, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if current.Len() > 0 {
				flush()
			}
			continue
		}
		if startLine == 0 {
			startLine = i + 1
		}
		continued := strings.HasSuffix(trimmed, "+")
		segment := strings.TrimSpace(strings.TrimSuffix(trimmed, "+"))
		if segment != "" {
			if current.Len() > 0 {
				current.WriteByte(' ')
			}
			current.WriteString(segment)
		}
		if !continued {
			flush()
		}
	}

	if current.Len() > 0 {
		flush()
	}
	return logical
}

func extractCLIBMIBindings(cleanLine, ownerObject string, lineNumber int) []ParsedIBMiBinding {
	var bindings []ParsedIBMiBinding
	if matches := clBindDirRe.FindAllStringSubmatch(cleanLine, -1); len(matches) > 0 {
		for _, match := range matches {
			if len(match) != 2 {
				continue
			}
			for _, name := range SplitIBMIBindingNames(match[1]) {
				bindings = append(bindings, ParsedIBMiBinding{
					OwnerObject: ownerObject,
					BindingName: name,
					BindingType: "bnddir_ref",
					LineNumber:  lineNumber,
				})
			}
		}
	}

	if clAddBndDireRe.MatchString(cleanLine) {
		bindingName := firstNormalizedSubmatch(clBindDirRe, cleanLine)
		targetObject, targetType := parseCLObjectTarget(cleanLine)
		if bindingName != "" && targetObject != "" {
			bindings = append(bindings, ParsedIBMiBinding{
				OwnerObject:      ownerObject,
				BindingName:      bindingName,
				BindingType:      "bnddir_entry",
				TargetObject:     targetObject,
				TargetObjectType: targetType,
				LineNumber:       lineNumber,
			})
		}
	}

	if clCrtSrvpgmRe.MatchString(cleanLine) {
		serviceProgram := firstNormalizedSubmatch(clSrvpgmRe, cleanLine)
		sourceMember := firstNormalizedSubmatch(clSrcMbrRe, cleanLine)
		if serviceProgram != "" && sourceMember != "" {
			bindings = append(bindings, ParsedIBMiBinding{
				OwnerObject:      serviceProgram,
				BindingName:      sourceMember,
				BindingType:      "binder_source",
				TargetObject:     serviceProgram,
				TargetObjectType: "srvpgm",
				LineNumber:       lineNumber,
			})
		}
	}

	if strings.Contains(strings.ToUpper(cleanLine), "CRTPGM") {
		owner := firstNormalizedSubmatch(clPgmObjectRe, cleanLine)
		if owner != "" {
			for _, match := range clBindDirRe.FindAllStringSubmatch(cleanLine, -1) {
				if len(match) != 2 {
					continue
				}
				for _, name := range SplitIBMIBindingNames(match[1]) {
					bindings = append(bindings, ParsedIBMiBinding{
						OwnerObject: owner,
						BindingName: name,
						BindingType: "bnddir_ref",
						LineNumber:  lineNumber,
					})
				}
			}
		}
	}

	return bindings
}

func firstNormalizedSubmatch(re *regexp.Regexp, input string) string {
	match := re.FindStringSubmatch(input)
	if len(match) != 2 {
		return ""
	}
	names := SplitIBMIBindingNames(match[1])
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func parseCLObjectTarget(input string) (string, string) {
	match := clObjTargetRe.FindStringSubmatch(input)
	if len(match) != 3 {
		return "", ""
	}
	return NormalizeIBMiObjectName(match[1]), strings.ToLower(strings.TrimSpace(match[2]))
}

func stripCLComments(line string) string {
	if line == "" {
		return ""
	}
	return clInlineComment.ReplaceAllString(line, "")
}

func stripCLLine(line string) string {
	line = stripCLComments(line)
	line = clLabelPrefixRe.ReplaceAllString(line, "")
	return strings.TrimSpace(line)
}

func parseCLSubroutineStart(line string) (string, bool) {
	match := clSubrStartRe.FindStringSubmatch(line)
	if len(match) < 2 {
		return "", false
	}
	for _, candidate := range match[1:] {
		name := normalizeCLTarget(candidate)
		if name != "" {
			return name, true
		}
	}
	return "", false
}

func extractCLCalls(line string, lineNum int) []ParsedFunctionCall {
	var calls []ParsedFunctionCall
	seen := make(map[string]bool)
	appendCall := func(name string) {
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if seen[key] {
			return
		}
		seen[key] = true
		calls = append(calls, ParsedFunctionCall{
			CalleeName: name,
			MethodName: name,
			LineNumber: lineNum,
		})
	}

	for _, match := range clCallProgramRe.FindAllStringSubmatch(line, -1) {
		if len(match) == 2 {
			appendCall(normalizeCLTarget(match[1]))
		}
	}
	for _, match := range clCallPrcRe.FindAllStringSubmatch(line, -1) {
		if len(match) == 2 {
			appendCall(normalizeCLTarget(match[1]))
		}
	}
	for _, match := range clCallSubrRe.FindAllStringSubmatch(line, -1) {
		if len(match) == 2 {
			appendCall(normalizeCLTarget(match[1]))
		}
	}
	return calls
}

func extractCLDataAccess(line string, lineNum int) (ParsedDataAccess, bool) {
	match := clDclfRe.FindStringSubmatch(line)
	if len(match) != 2 {
		return ParsedDataAccess{}, false
	}
	entity := normalizeCLTarget(match[1])
	if entity == "" || strings.HasPrefix(entity, "&") {
		return ParsedDataAccess{}, false
	}
	return ParsedDataAccess{
		EntityName: entity,
		Access:     "read",
		LineNumber: lineNum,
		Source:     "cl_dclf",
	}, true
}

func extractCLImport(line string) (string, bool) {
	match := clDclfRe.FindStringSubmatch(line)
	if len(match) != 2 {
		return "", false
	}
	entity := normalizeCLTarget(match[1])
	if entity == "" || strings.HasPrefix(entity, "&") {
		return "", false
	}
	return entity, true
}

func extractCLDisplayAccesses(line string, lineNum int, declaredFile string) []ParsedDataAccess {
	recordFormat := ""
	for _, re := range []*regexp.Regexp{clSndRcvfRe, clSndfRe, clRcvfFmtRe} {
		match := re.FindStringSubmatch(line)
		if len(match) == 2 {
			recordFormat = normalizeCLTarget(match[1])
			break
		}
	}

	if recordFormat == "" && !clRcvfRe.MatchString(line) {
		return nil
	}

	var accesses []ParsedDataAccess
	if recordFormat != "" {
		accesses = append(accesses, ParsedDataAccess{
			EntityName: recordFormat,
			Access:     "display",
			LineNumber: lineNum,
			Source:     "cl_display_command",
		})
	}
	if declaredFile != "" {
		accesses = append(accesses, ParsedDataAccess{
			EntityName: declaredFile,
			Access:     "display",
			LineNumber: lineNum,
			Source:     "cl_display_file",
		})
	}
	return accesses
}

func resolveSingleCLDeclaredFile(files map[string]bool) string {
	if len(files) != 1 {
		return ""
	}
	for name := range files {
		return name
	}
	return ""
}

func normalizeCLTarget(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	value = strings.TrimSuffix(value, ")")
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if idx := strings.LastIndex(value, "/"); idx >= 0 {
		value = value[idx+1:]
	}
	if strings.HasPrefix(value, "*") {
		return strings.TrimSpace(value)
	}
	return strings.ToUpper(strings.TrimSpace(value))
}
