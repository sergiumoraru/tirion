package sourceindex

import (
	"bytes"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// TextNormalization records what NormalizeText had to change, so callers can
// report it. Indexing hashes the original bytes; only parser input is normalized.
type TextNormalization struct {
	Encoding    string // "utf-16le" or "utf-16be" when a BOM selected a decode; empty otherwise
	UTF8BOM     bool   // a leading UTF-8 byte-order mark was dropped
	NULsRemoved int
	InvalidUTF8 bool
}

// Changed reports whether the returned text differs from the input bytes.
func (n TextNormalization) Changed() bool {
	return n.Encoding != "" || n.UTF8BOM || n.NULsRemoved > 0 || n.InvalidUTF8
}

// NormalizeText converts source bytes to text that parsers and PostgreSQL text
// columns accept:
//   - UTF-16 with a byte-order mark (Windows PowerShell 5.1, SSMS) is decoded to
//     UTF-8. Without a BOM the encoding is not guessed.
//   - A UTF-8 BOM is dropped.
//   - NUL bytes are removed. PostgreSQL rejects NUL in text and jsonb, and a
//     BOM-less ASCII UTF-16 file degrades to its readable ASCII text.
//   - Invalid UTF-8 is replaced with U+FFFD; unknown legacy code pages are not
//     guessed.
//
// Every reader of indexed bytes (cmd/parse and the extract helpers) must apply
// this to the same input so that line numbers and facts agree.
func NormalizeText(data []byte) ([]byte, TextNormalization) {
	var info TextNormalization
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		data = decodeUTF16(data[2:], false)
		info.Encoding = "utf-16le"
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		data = decodeUTF16(data[2:], true)
		info.Encoding = "utf-16be"
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		// A UTF-8 BOM is not source text; it would defeat line-anchored patterns.
		data = data[3:]
		info.UTF8BOM = true
	}
	if n := bytes.Count(data, []byte{0}); n > 0 {
		data = bytes.ReplaceAll(data, []byte{0}, nil)
		info.NULsRemoved = n
	}
	if !utf8.Valid(data) {
		data = []byte(strings.ToValidUTF8(string(data), "�"))
		info.InvalidUTF8 = true
	}
	return data, info
}

func decodeUTF16(data []byte, bigEndian bool) []byte {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if bigEndian {
			units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
		} else {
			units = append(units, uint16(data[i+1])<<8|uint16(data[i]))
		}
	}
	return []byte(string(utf16.Decode(units)))
}
