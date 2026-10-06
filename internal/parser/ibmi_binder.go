package parser

import (
	"path/filepath"
	"regexp"
	"strings"
)

type IBMiBinderParser struct{}

var ibmiBinderSupportedExtensions = map[string]bool{
	".bnd":    true,
	".bnddir": true,
}

var (
	ibmiBinderSignatureRe = regexp.MustCompile(`(?i)\bSTRPGMEXP\b.*?\bSIGNATURE\(\s*['"]?([^'")\s]+)['"]?\s*\)`)
	ibmiBinderExportRe    = regexp.MustCompile(`(?i)\bEXPORT\b.*?\bSYMBOL\(\s*['"]?([^'")\s]+)['"]?\s*\)`)
)

func NewIBMiBinderParser() *IBMiBinderParser {
	return &IBMiBinderParser{}
}

func (p *IBMiBinderParser) CanParse(filePath string) bool {
	if ibmiBinderSupportedExtensions[strings.ToLower(filepath.Ext(filePath))] {
		return true
	}
	kind, ok := detectIBMISourceMemberKind(filePath)
	return ok && kind == "binder"
}

func (p *IBMiBinderParser) ParseFile(filePath string, content []byte) (result ParsedFile) {
	result = ParsedFile{
		Path:          filePath,
		Language:      "ibmi_binder",
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
	artifactName := ibmiArtifactNameForPath(filePath)
	signature := ""
	for i, rawLine := range lines {
		lineNum := i + 1
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if match := ibmiBinderSignatureRe.FindStringSubmatch(line); len(match) == 2 {
			sig := NormalizeIBMiObjectName(match[1])
			if sig != "" {
				signature = sig
			}
			continue
		}
		if match := ibmiBinderExportRe.FindStringSubmatch(line); len(match) == 2 {
			exportName := NormalizeIBMiObjectName(match[1])
			if exportName == "" {
				continue
			}
			objectName := binderObjectName(artifactName, signature)
			result.IBMiExports = append(result.IBMiExports, ParsedIBMiExport{
				ExportName: exportName,
				ObjectName: objectName,
				ObjectType: "service_program",
				SourceType: "binder_export",
				LineNumber: lineNum,
			})
		}
	}

	return result
}

func binderObjectName(artifactName, signature string) string {
	artifactName = NormalizeIBMiObjectName(artifactName)
	signature = NormalizeIBMiObjectName(signature)
	if signature != "" {
		if strings.EqualFold(signature, artifactName) {
			return signature
		}
		if strings.HasSuffix(strings.ToUpper(artifactName), "D") {
			base := artifactName[:len(artifactName)-1]
			if strings.EqualFold(signature, base) {
				return signature
			}
		}
	}
	return artifactName
}
