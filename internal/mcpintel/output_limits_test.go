package mcpintel

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLimitedOutputRetainsPrefixAcrossWrites(t *testing.T) {
	out := limitedOutput{limit: 5}
	for _, chunk := range []string{"abc", "def", "ghi"} {
		n, err := out.Write([]byte(chunk))
		if err != nil || n != len(chunk) {
			t.Fatalf("writer must drain each chunk: n=%d err=%v", n, err)
		}
	}
	if out.buffer.String() != "abcde" || !out.truncated {
		t.Fatalf("unexpected retained output %q, truncated=%v", out.buffer.String(), out.truncated)
	}
}

func TestLimitedOutputExactLimitIsNotTruncated(t *testing.T) {
	out := limitedOutput{limit: 3}
	out.Write([]byte("abc"))
	if out.truncated {
		t.Fatal("exact-limit output was marked truncated")
	}
}

func TestDecodeAPIResponseRejectsOversizedJSON(t *testing.T) {
	body := io.MultiReader(strings.NewReader(`{"value":"`), io.LimitReader(repeatingByteReader{}, 20*1024*1024), strings.NewReader(`"}`))
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}
	var result map[string]any
	if err := decodeAPIResponse(resp, &result); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected explicit size error, got %v", err)
	}
}

type repeatingByteReader struct{}

func (repeatingByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
