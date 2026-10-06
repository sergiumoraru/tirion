package parser

import (
	"bytes"
	"strings"
)

// vueTag is a parsed start tag of a top-level SFC block.
type vueTag struct {
	name        string
	attrs       map[string]string
	bodyStart   int // offset just after the closing '>' of the start tag
	selfClosing bool
}

// parseVueStartTag parses the start tag whose name begins at content[open+1].
// Attribute values may be quoted and contain '>' (for example
// generic="T extends Record<string, any>"). It reports ok=false when the tag is
// not terminated.
func parseVueStartTag(content []byte, open int) (tag vueTag, ok bool) {
	i := open + 1
	for i < len(content) && isVueTagNameByte(content[i]) {
		i++
	}
	tag.name = strings.ToLower(string(content[open+1 : i]))
	tag.attrs = map[string]string{}
	for i < len(content) {
		switch c := content[i]; {
		case c == '>':
			tag.bodyStart = i + 1
			return tag, true
		case c == '/' && i+1 < len(content) && content[i+1] == '>':
			tag.bodyStart = i + 2
			tag.selfClosing = true
			return tag, true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '/':
			i++
		default:
			start := i
			for i < len(content) && !strings.ContainsRune(" \t\r\n/>=", rune(content[i])) {
				i++
			}
			name := strings.ToLower(string(content[start:i]))
			j := i
			for j < len(content) && (content[j] == ' ' || content[j] == '\t' || content[j] == '\n' || content[j] == '\r') {
				j++
			}
			value := ""
			if j < len(content) && content[j] == '=' {
				j++
				for j < len(content) && (content[j] == ' ' || content[j] == '\t' || content[j] == '\n' || content[j] == '\r') {
					j++
				}
				if j < len(content) && (content[j] == '"' || content[j] == '\'') {
					end := bytes.IndexByte(content[j+1:], content[j])
					if end < 0 {
						return tag, false
					}
					value = string(content[j+1 : j+1+end])
					j += end + 2
				} else {
					start := j
					for j < len(content) && !strings.ContainsRune(" \t\r\n>", rune(content[j])) {
						j++
					}
					value = string(content[start:j])
				}
				i = j
			}
			if name != "" {
				tag.attrs[name] = value
			} else {
				i++ // stray '=' or similar; make progress
			}
		}
	}
	return tag, false
}

func isVueTagNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':' || c == '.'
}

// indexFoldClosingTag finds "</name" followed by optional whitespace and '>'
// at or after from, case-insensitively, and returns its start and the offset
// just past '>'.
func indexFoldClosingTag(content []byte, from int, name string) (start, end int) {
	needle := "</" + name
	for i := from; i+len(needle) <= len(content); i++ {
		if content[i] != '<' || !strings.EqualFold(string(content[i:i+len(needle)]), needle) {
			continue
		}
		j := i + len(needle)
		for j < len(content) && (content[j] == ' ' || content[j] == '\t' || content[j] == '\n' || content[j] == '\r') {
			j++
		}
		if j < len(content) && content[j] == '>' {
			return i, j + 1
		}
	}
	return -1, -1
}

// hasFoldPrefix reports whether b begins with prefix, ignoring ASCII case.
func hasFoldPrefix(b []byte, prefix string) bool {
	return len(b) >= len(prefix) && strings.EqualFold(string(b[:len(prefix)]), prefix)
}

// skipVueTemplate returns the offset after the </template> matching a
// <template> start tag, counting nested <template> elements (v-slot).
func skipVueTemplate(content []byte, from int) int {
	depth := 1
	for i := from; i < len(content); i++ {
		if content[i] != '<' {
			continue
		}
		rest := content[i:]
		switch {
		case bytes.HasPrefix(rest, []byte("<!--")):
			end := bytes.Index(rest[4:], []byte("-->"))
			if end < 0 {
				return len(content)
			}
			i += 4 + end + 2
		case hasFoldPrefix(rest, "<template") && len(rest) > 9 && strings.ContainsRune(" \t\r\n>", rune(rest[9])):
			depth++
		case hasFoldPrefix(rest, "</template>"):
			depth--
			if depth == 0 {
				return i + len("</template>")
			}
		}
	}
	return len(content)
}

// extractVueScript returns the contents of every top-level <script> block of a
// single-file component, with everything else removed except newlines, so a
// line in the result is the same line in the .vue file. Both <script> and
// <script setup> are kept. Blocks are located the way the SFC compiler does it:
// start tags are parsed with quoted attribute values, comments are skipped, and
// the bodies of <template>, <style> and custom blocks are never searched for
// script tags.
//
// lang is the grammar the blocks need: "tsx" when any block declares
// lang="tsx" or "jsx", otherwise "ts". It is empty when there is no script.
func extractVueScript(content []byte) (script []byte, lang string) {
	var out bytes.Buffer
	out.Grow(len(content))
	last := 0 // everything before last is already accounted for in out
	found := false
	pad := func(to int) {
		out.Write(bytes.Repeat([]byte{'\n'}, bytes.Count(content[last:to], []byte{'\n'})))
		last = to
	}

	for i := 0; i < len(content); {
		open := bytes.IndexByte(content[i:], '<')
		if open < 0 {
			break
		}
		i += open
		if bytes.HasPrefix(content[i:], []byte("<!--")) {
			end := bytes.Index(content[i+4:], []byte("-->"))
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		if i+1 >= len(content) || !(content[i+1] >= 'a' && content[i+1] <= 'z' || content[i+1] >= 'A' && content[i+1] <= 'Z') {
			i++
			continue
		}
		tag, ok := parseVueStartTag(content, i)
		if !ok {
			break
		}
		if tag.selfClosing {
			i = tag.bodyStart
			continue
		}
		var bodyEnd, next int
		if tag.name == "template" {
			next = skipVueTemplate(content, tag.bodyStart)
			i = next
			continue
		}
		closeStart, closeEnd := indexFoldClosingTag(content, tag.bodyStart, tag.name)
		if closeStart < 0 {
			break
		}
		bodyEnd, next = closeStart, closeEnd
		if tag.name == "script" {
			pad(tag.bodyStart)
			if out.Len() > 0 && out.Bytes()[out.Len()-1] != '\n' {
				out.WriteByte(';') // two blocks on one line must not fuse into one token
			}
			out.Write(content[tag.bodyStart:bodyEnd])
			last = bodyEnd
			found = true
			switch strings.ToLower(tag.attrs["lang"]) {
			case "tsx", "jsx":
				lang = "tsx"
			case "ts":
				if lang == "" {
					lang = "ts"
				}
			}
		}
		i = next
	}
	if !found {
		return []byte{}, ""
	}
	pad(len(content))
	if lang == "" {
		lang = "ts"
	}
	return out.Bytes(), lang
}
