package transport

import (
	"encoding/json"
	"fmt"
)

// HTTPError is returned by Client.Get / GetRaw / Post when NDMS replies
// with a non-2xx status. Typed so callers can match on Status — e.g.
// a 404 on /show/interface/<name>/wireguard/peer means "no peers",
// not a real error.
type HTTPError struct {
	Method string
	Path   string
	Status int
	Body   []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("rci %s %s: status %d", e.Method, e.Path, e.Status)
}

// NDMSAppError represents an application-level NDMS failure: HTTP 200 but
// the body contains the NDMS error envelope. The wire was fine — the
// command was rejected by NDMS.
type NDMSAppError struct {
	Method  string
	Path    string
	Message string
	Body    []byte
}

func (e *NDMSAppError) Error() string {
	return fmt.Sprintf("rci %s %s: ndms-error: %s", e.Method, e.Path, e.Message)
}

// BatchElementError describes one failed element of a PostBatch.
type BatchElementError struct {
	Index   int
	Message string
}

// BatchError aggregates per-element NDMS application errors from PostBatch.
type BatchError struct {
	Failures []BatchElementError
	Total    int
	Body     json.RawMessage
}

func (e *BatchError) Error() string {
	if len(e.Failures) == 1 {
		return fmt.Sprintf("rci batch: element %d failed: %s",
			e.Failures[0].Index, e.Failures[0].Message)
	}
	return fmt.Sprintf("rci batch: %d/%d elements failed",
		len(e.Failures), e.Total)
}

// ExtractError parses the NDMS error envelope from a response body.
// Returns the message if the body indicates an application error,
// "" otherwise. NDMS shape: {"status":"error","message":"…"}.
func ExtractError(body []byte) string {
	var envelope struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	if envelope.Status == "error" {
		return envelope.Message
	}
	return ""
}
