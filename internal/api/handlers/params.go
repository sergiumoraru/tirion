package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// queryInt reads an optional integer query parameter. A missing/blank value
// yields def. Anything that is not an integer >= min is an error rather than a
// silent fallback, so typos like limit=abc or limit=-5 reach the caller. Values
// beyond the int range are clamped (they are well above any maximum anyway).
func queryInt(r *http.Request, key string, def, min int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) && errors.Is(numErr.Err, strconv.ErrRange) && !strings.HasPrefix(raw, "-") {
			return int(^uint(0) >> 1), nil
		}
		return 0, fmt.Errorf("%s must be an integer >= %d, got %q", key, min, raw)
	}
	if parsed < min {
		return 0, fmt.Errorf("%s must be an integer >= %d, got %q", key, min, raw)
	}
	return parsed, nil
}

// queryLimit reads a positive "limit"-style parameter clamped to max.
func queryLimit(r *http.Request, key string, def, max int) (int, error) {
	limit, err := queryInt(r, key, def, 1)
	if err != nil {
		return 0, err
	}
	if limit > max {
		limit = max
	}
	return limit, nil
}

// parsePage reads limit and offset. On a bad value it writes a 400
// VALIDATION_ERROR and returns ok=false.
func parsePage(w http.ResponseWriter, r *http.Request, defLimit, maxLimit int) (limit, offset int, ok bool) {
	limit, err := queryLimit(r, "limit", defLimit, maxLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), map[string]any{"parameter": "limit"})
		return 0, 0, false
	}
	offset, err = queryInt(r, "offset", 0, 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), map[string]any{"parameter": "offset"})
		return 0, 0, false
	}
	return limit, offset, true
}

// parseLimitOnly reads limit only (for endpoints that do not paginate).
func parseLimitOnly(w http.ResponseWriter, r *http.Request, defLimit, maxLimit int) (int, bool) {
	limit, err := queryLimit(r, "limit", defLimit, maxLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), map[string]any{"parameter": "limit"})
		return 0, false
	}
	return limit, true
}
