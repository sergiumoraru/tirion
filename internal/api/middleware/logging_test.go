package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatusRecorder_ImplementsFlushWhenUnderlyingWriterSupportsIt(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: rr, status: http.StatusOK}

	if _, ok := any(rec).(http.Flusher); !ok {
		t.Fatalf("expected statusRecorder to implement http.Flusher")
	}

	// Should not panic and should delegate.
	rec.Flush()
}
