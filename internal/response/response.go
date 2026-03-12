package response

import (
	"encoding/json"
	"net/http"
)

// APIResponse is the envelope format expected by frontend client.ts
// Success: {success: true, data: ...}
// Error: {error: true, message: "...", code: "..."}
type APIResponse struct {
	Success bool        `json:"success,omitempty"`
	Error   bool        `json:"error,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Message string      `json:"message,omitempty"`
	Code    string      `json:"code,omitempty"`
}

// Success writes a success response with optional data.
// Format: {success: true} or {success: true, data: ...}
func Success(w http.ResponseWriter, data interface{}) {
	resp := APIResponse{Success: true}
	if data != nil {
		resp.Data = data
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Error writes an error response with message and code.
// Format: {error: true, message: "...", code: "..."}
func Error(w http.ResponseWriter, message, code string) {
	resp := APIResponse{
		Error:   true,
		Message: message,
		Code:    code,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(resp)
}

// InternalError writes a 500 error response.
func InternalError(w http.ResponseWriter, message string) {
	resp := APIResponse{
		Error:   true,
		Message: message,
		Code:    "INTERNAL_ERROR",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(resp)
}

// Write encodes any value as JSON response.
func Write(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// JSON writes any value as JSON response (alias for Write).
func JSON(w http.ResponseWriter, v interface{}) {
	Write(w, v)
}

// BadRequest writes a 400 error response.
func BadRequest(w http.ResponseWriter, message string) {
	resp := APIResponse{
		Error:   true,
		Message: message,
		Code:    "BAD_REQUEST",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(resp)
}

// MethodNotAllowed writes a 405 error response.
func MethodNotAllowed(w http.ResponseWriter) {
	resp := APIResponse{
		Error:   true,
		Message: "method not allowed",
		Code:    "METHOD_NOT_ALLOWED",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusMethodNotAllowed)
	json.NewEncoder(w).Encode(resp)
}

// ErrorWithStatus writes an error response with custom HTTP status.
func ErrorWithStatus(w http.ResponseWriter, status int, message, code string) {
	resp := APIResponse{
		Error:   true,
		Message: message,
		Code:    code,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

// MustNotNil ensures a slice is not nil (returns empty slice if nil).
func MustNotNil[T any](slice []T) []T {
	if slice == nil {
		return []T{}
	}
	return slice
}
