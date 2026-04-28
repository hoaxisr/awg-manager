package api

import (
	"net/http"
	"regexp"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/signature"
)

// ── Response DTOs ────────────────────────────────────────────────

// SignaturePacketsDTO is the packets field in SignatureCaptureResult.
type SignaturePacketsDTO struct {
	I1 string `json:"i1" example:"0a1b2c3d"`
	I2 string `json:"i2" example:"4e5f6a7b"`
	I3 string `json:"i3" example:"8c9d0e1f"`
	I4 string `json:"i4" example:"2a3b4c5d"`
	I5 string `json:"i5" example:"6e7f8a9b"`
}

// SignatureCaptureData mirrors frontend SignatureCaptureResult.
type SignatureCaptureData struct {
	OK      bool                `json:"ok" example:"true"`
	Source  string              `json:"source" example:"Wireguard0"`
	Packets SignaturePacketsDTO `json:"packets"`
	Warning string              `json:"warning,omitempty" example:""`
}

// SignatureCaptureResponse is the envelope for GET /signature/capture.
type SignatureCaptureResponse struct {
	Success bool                `json:"success" example:"true"`
	Data    SignatureCaptureData `json:"data"`
}

var validDomain = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$`)

type SignatureHandler struct{}

func NewSignatureHandler() *SignatureHandler {
	return &SignatureHandler{}
}

// Capture runs TLS certificate capture for a domain.
//
//	@Summary		Signature capture
//	@Tags			signature
//	@Produce		json
//	@Security		CookieAuth
//	@Param			domain	query	string	true	"Domain name"
//	@Success		200	{object}	SignatureCaptureResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/signature/capture [get]
func (h *SignatureHandler) Capture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	domain := r.URL.Query().Get("domain")
	if domain == "" {
		response.Error(w, "Укажите домен", "MISSING_DOMAIN")
		return
	}

	domain = signature.NormalizeDomain(domain)

	if !validDomain.MatchString(domain) {
		response.Error(w, "Некорректный домен", "INVALID_DOMAIN")
		return
	}

	result := signature.Capture(domain)

	if result.Source == "error" {
		response.ErrorWithStatus(w, http.StatusBadGateway, result.Warning, "CAPTURE_FAILED")
		return
	}

	response.Success(w, result)
}
