package parser

import (
	"regexp"
	"strings"
)

var csharpAccessorRe = regexp.MustCompile(`^\s*(?:(?:private|protected|internal)\s+)*(get|set|init)\b`)

// Accessors have callable bodies but no parameter-list declaration. Keep their
// ownership separate from methods that precede the property in the class.
func appendCSharpPropertyAccessors(result *ParsedFile, class, property string, source, masked, callLines []string, start int) {
	row, column, kind := csharpMethodBodyStart(masked, start)
	if kind != "{" {
		return
	}
	end, endColumn := csharpBlockEnd(masked, row, column)
	column++
	for row <= end {
		if column >= len(masked[row]) {
			row++
			column = 0
			continue
		}
		if row == end && column >= endColumn {
			break
		}
		part := masked[row][column:]
		match := csharpAccessorRe.FindStringSubmatchIndex(part)
		if match == nil {
			row++
			column = 0
			continue
		}
		name := class + "." + property + "." + part[match[2]:match[3]]
		declaration := row
		column += match[1]
		for row <= end && strings.TrimSpace(masked[row][column:]) == "" {
			row++
			column = 0
		}
		if row > end {
			break
		}
		column += len(masked[row][column:]) - len(strings.TrimLeft(masked[row][column:], " \t\r"))
		bodyRow, bodyColumn := row, column
		var last, lastColumn int
		switch {
		case strings.HasPrefix(masked[row][column:], "{"):
			last, lastColumn = csharpBlockEnd(masked, row, column)
			bodyColumn++
		case strings.HasPrefix(masked[row][column:], "=>"):
			bodyColumn += 2
			last, lastColumn = csharpExpressionEnd(masked, row, bodyColumn)
		default:
			column++
			continue
		}
		fn := ParsedFunction{Name: name, StartLine: declaration + 1, EndLine: last + 1, SourceCode: strings.Join(source[declaration:last+1], "\n")}
		result.Functions = append(result.Functions, fn)
		for i := bodyRow; i <= last; i++ {
			body := callLines[i]
			if i == last {
				body = body[:lastColumn]
			}
			if i == bodyRow {
				body = body[bodyColumn:]
			}
			result.FunctionCalls[name] = append(result.FunctionCalls[name], extractCSharpCalls(body, i+1)...)
		}
		row, column = last, lastColumn+1
	}
}

func csharpBlockEnd(lines []string, start, column int) (int, int) {
	depth := 0
	for row := start; row < len(lines); row++ {
		first := 0
		if row == start {
			first = column
		}
		for col := first; col < len(lines[row]); col++ {
			switch lines[row][col] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return row, col
				}
			}
		}
	}
	return start, len(lines[start])
}

// Mask literals and comments without moving offsets. Declaration/body boundaries
// must not depend on braces or semicolons inside strings.
func maskCSharpLiterals(source string) string {
	return maskCSharpSource(source, false)
}

// Call extraction keeps executable interpolation holes, but not literal text.
// Both views preserve byte offsets so structural boundaries can slice either.
func maskCSharpSource(source string, preserveExpressions bool) string {
	out := []byte(source)
	hide := func(start, end int) {
		for j := start; j < end; j++ {
			if out[j] != '\n' && out[j] != '\r' {
				out[j] = ' '
			}
		}
	}
	for i := 0; i < len(source); {
		start := i
		var expressions [][2]int
		switch {
		case strings.HasPrefix(source[i:], "//"):
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case strings.HasPrefix(source[i:], "/*"):
			i += 2
			for i < len(source) && !strings.HasPrefix(source[i:], "*/") {
				i++
			}
			if i < len(source) {
				i += 2
			}
		case source[i] == '"' || source[i] == '\'':
			i = csharpLiteralExpressions(source, i, func(start, end int) {
				if preserveExpressions {
					expressions = append(expressions, [2]int{start, end})
				}
			})
		default:
			i++
			continue
		}
		hide(start, i)
		for _, span := range expressions {
			body := source[span[0]:span[1]]
			end := csharpInterpolationCodeEnd(body)
			copy(out[span[0]:span[0]+end], maskCSharpSource(body[:end], true))
		}
	}
	return string(out)
}

// The format component after a top-level colon is text, not executable C#.
func csharpInterpolationCodeEnd(source string) int {
	masked := maskCSharpLiterals(source)
	depth, conditional := 0, 0
	for i := 0; i < len(masked); i++ {
		switch masked[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '?':
			if depth == 0 {
				if i+1 < len(masked) && strings.ContainsRune("?.[", rune(masked[i+1])) {
					if masked[i+1] == '?' {
						i++
					}
				} else {
					conditional++
				}
			}
		case ':':
			if i+1 < len(masked) && masked[i+1] == ':' {
				i++
			} else if depth == 0 {
				if conditional == 0 {
					return i
				}
				conditional--
			}
		}
	}
	return len(source)
}

// Interpolation holes contain C# expressions, including their own quoted literals.
// Their quotes must not terminate the containing string or consume later methods.
func csharpLiteralEnd(source string, start int) int {
	return csharpLiteralExpressions(source, start, nil)
}

func csharpLiteralExpressions(source string, start int, expression func(int, int)) int {
	quote := source[start]
	verbatim := quote == '"' && (start > 0 && source[start-1] == '@' || start > 1 && source[start-2:start] == "@$")
	interpolated := quote == '"' && (start > 0 && source[start-1] == '$' || start > 1 && source[start-2:start] == "$@")
	width := 1
	if quote == '"' && !verbatim {
		for start+width < len(source) && source[start+width] == '"' {
			width++
		}
	}
	if width >= 3 {
		dollars := 0
		for j := start - 1; j >= 0 && source[j] == '$'; j-- {
			dollars++
		}
		for i := start + width; i < len(source); {
			if strings.HasPrefix(source[i:], strings.Repeat("\"", width)) {
				return i + width
			}
			if dollars > 0 && strings.HasPrefix(source[i:], strings.Repeat("{", dollars)) {
				for i+dollars < len(source) && source[i+dollars] == '{' {
					i++
				}
				body := i + dollars
				end := csharpInterpolationEnd(source, body)
				if end > body && source[end-1] == '}' {
					if expression != nil {
						expression(body, end-1)
					}
					i = end
					for n := 1; n < dollars && i < len(source) && source[i] == '}'; n++ {
						i++
					}
				} else {
					i = end
				}
				continue
			}
			i++
		}
		return len(source)
	}
	for i := start + 1; i < len(source); {
		switch {
		case source[i] == quote:
			i++
			if verbatim && i < len(source) && source[i] == quote {
				i++
				continue
			}
			return i
		case !verbatim && source[i] == '\\' && i+1 < len(source):
			i += 2
		case interpolated && strings.HasPrefix(source[i:], "{{"):
			i += 2
		case interpolated && source[i] == '{':
			body := i + 1
			i = csharpInterpolationEnd(source, body)
			if expression != nil && i > body && source[i-1] == '}' {
				expression(body, i-1)
			}
		default:
			i++
		}
	}
	return len(source)
}

func csharpInterpolationEnd(source string, start int) int {
	depth := 1
	for i := start; i < len(source); {
		switch {
		case strings.HasPrefix(source[i:], "//"):
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case strings.HasPrefix(source[i:], "/*"):
			end := strings.Index(source[i+2:], "*/")
			if end < 0 {
				return len(source)
			}
			i += end + 4
		case source[i] == '"' || source[i] == '\'':
			i = csharpLiteralEnd(source, i)
		case source[i] == '{':
			depth++
			i++
		case source[i] == '}':
			depth--
			i++
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(source)
}

func csharpMethodBodyStart(lines []string, start int) (int, int, string) {
	parens, brackets := 0, 0
	for row := start; row < len(lines); row++ {
		for col := 0; col < len(lines[row]); col++ {
			ch := lines[row][col]
			if parens == 0 && brackets == 0 {
				if ch == '=' && col+1 < len(lines[row]) && lines[row][col+1] == '>' {
					return row, col, "=>"
				}
				if ch == '{' || ch == ';' {
					return row, col, string(ch)
				}
				if ch == '}' {
					return start, 0, ""
				}
			}
			switch ch {
			case '(':
				parens++
			case ')':
				if parens > 0 {
					parens--
				}
			case '[':
				brackets++
			case ']':
				if brackets > 0 {
					brackets--
				}
			}
		}
	}
	return start, 0, ""
}

func csharpExpressionEnd(lines []string, start, column int) (int, int) {
	parens, brackets, braces := 0, 0, 0
	for row := start; row < len(lines); row++ {
		col := 0
		if row == start {
			col = column
		}
		for ; col < len(lines[row]); col++ {
			switch lines[row][col] {
			case '(':
				parens++
			case ')':
				parens--
			case '[':
				brackets++
			case ']':
				brackets--
			case '{':
				braces++
			case '}':
				if braces == 0 {
					return row, col
				}
				braces--
			case ';':
				if parens == 0 && brackets == 0 && braces == 0 {
					return row, col
				}
			}
		}
	}
	last := len(lines) - 1
	return last, len(lines[last])
}
