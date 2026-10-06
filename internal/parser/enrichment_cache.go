package parser

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/sergiumoraru/tirion/internal/buildinfo"
	"github.com/sergiumoraru/tirion/internal/config"
)

// EnrichmentParser shares raw parse results from indexing through one candidate's
// helper sequence, before module-specific additions. It never replaces sourceindex.Read: callers still verify the
// indexed bytes before consulting it. Cache errors fall back to ordinary parsing.
type EnrichmentParser struct {
	parse               func(string, []byte) ParsedFile
	directory, identity string
}

func (p *JavaParser) ForEnrichment() *EnrichmentParser {
	return newEnrichmentParser("java", p.config, p.ParseFile)
}

func (p *JavaScriptParser) ForEnrichment() *EnrichmentParser {
	return newEnrichmentParser("javascript", p.config, p.ParseFile)
}

func newEnrichmentParser(kind string, cfg *config.PatternsConfig, parse func(string, []byte) ParsedFile) *EnrichmentParser {
	p := &EnrichmentParser{parse: parse}
	data, err := json.Marshal(cfg)
	if err == nil && cfg != nil && cfg.Err() == nil {
		p.directory = os.Getenv("TIRION_ENRICHMENT_CACHE_DIR")
		// This wire version must change if the shared ParsedFile contract changes.
		p.identity = fmt.Sprintf("enrichment-v1\x00%s\x00%s\x00%x", buildinfo.Version, kind, sha256.Sum256(data))
	}
	return p
}

func (p *EnrichmentParser) ParseFile(path string, content []byte) ParsedFile {
	if p.directory == "" || !utf8.Valid(content) || !utf8.ValidString(path) {
		return p.parse(path, content)
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00", p.identity, path)
	hash.Write(content)
	key := fmt.Sprintf("%x", hash.Sum(nil))
	file := filepath.Join(p.directory, key+".json")
	type record struct {
		Key    string
		Result ParsedFile
	}
	// JSON preserves empty versus nil slices used by the persisted metadata.
	// Parser annotation values are source strings, booleans and arrays of those.
	if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() && info.Size() <= 32<<20 {
		if data, err := os.ReadFile(file); err == nil {
			var cached record
			if json.Unmarshal(data, &cached) == nil && cached.Key == key && cached.Result.Path == path {
				return cached.Result
			}
		}
	}
	result := p.parse(path, content)
	// A retry must be allowed to recover from a timeout or other parser failure.
	if result.ParseDiagnostics.Failed() {
		return result
	}
	data, err := json.Marshal(record{Key: key, Result: result})
	if err != nil || len(data) > 32<<20 {
		return result
	}
	temp, err := os.CreateTemp(p.directory, "write-")
	if err != nil {
		return result
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(data)
	closeErr := temp.Close()
	if writeErr == nil && closeErr == nil {
		// Readers must never observe a partially written result.
		_ = os.Rename(temp.Name(), file)
	}
	return result
}
