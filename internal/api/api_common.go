package api

// APIEnvelope describes the common successful API response shape.
// Most handlers return: { success: true, data: ... }.
type APIEnvelope struct {
	Success bool   `json:"success" example:"true"`
	Data    any    `json:"data,omitempty" swaggertype:"object"`
	Message string `json:"message,omitempty" example:"ok"`
}

// APIErrorEnvelope describes the common error response shape.
// Most handlers return: { error: true, message: "...", code: "..." }.
type APIErrorEnvelope struct {
	Error   bool   `json:"error" example:"true"`
	Message string `json:"message" example:"invalid request"`
	Code    string `json:"code" example:"BAD_REQUEST"`
}
