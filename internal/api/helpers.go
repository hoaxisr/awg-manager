package api

import (
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// parseJSON guards method + reads the request body into T. On any
// error — wrong method, body-read failure, decode failure — it writes
// the canonical error response and returns (zero, false). Callers bail
// out immediately on false.
//
// Body size is capped to maxBodySize via http.MaxBytesReader so an
// oversized payload gets a clean 413 from the decoder rather than
// draining the whole body into memory.
func parseJSON[T any](w http.ResponseWriter, r *http.Request, method string) (T, bool) {
	var dst T
	if r.Method != method {
		response.MethodNotAllowed(w)
		return dst, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	if err := json.NewDecoder(r.Body).Decode(&dst); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON", "INVALID_JSON")
		return dst, false
	}
	return dst, true
}
