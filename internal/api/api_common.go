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

// OkData is the {ok: true} confirmation payload returned by mutation
// endpoints that have no entity to send back.
type OkData struct {
	Ok bool `json:"ok" example:"true"`
}

// OkResponse is the typed envelope for endpoints that return only a
// confirmation (success + ok). Use as @Success {object} OkResponse.
type OkResponse struct {
	Success bool   `json:"success" example:"true"`
	Data    OkData `json:"data"`
}
