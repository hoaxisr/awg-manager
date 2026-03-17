package api

import (
	"net/http"
	"regexp"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/signature"
)

var validDomain = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$`)

type SignatureHandler struct{}

func NewSignatureHandler() *SignatureHandler {
	return &SignatureHandler{}
}

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
