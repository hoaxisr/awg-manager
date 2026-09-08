package api

import (
	"encoding/json"
	"errors"
	"io"
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
	Success bool                 `json:"success" example:"true"`
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

// ── Generate (POST /signature/generate) ──────────────────────────────────────

// maxGenerateBodyBytes caps the request body — the payload is a tiny JSON object.
const maxGenerateBodyBytes = 4 << 10

// SignatureGenerateRequest is the POST /signature/generate body.
type SignatureGenerateRequest struct {
	Protocol string `json:"protocol" example:"quic_initial"`
}

// SignatureGenerateData is the data field of SignatureGenerateResponse. It
// mirrors SignatureCaptureData with source="generated" plus the canonical
// protocol key (quic_initial, stun, dns, dtls, sip; anything else is rejected
// with UNKNOWN_PROTOCOL) and the summed I1–I5 byte size.
type SignatureGenerateData struct {
	OK       bool                `json:"ok" example:"true"`
	Source   string              `json:"source" example:"generated"`
	Protocol string              `json:"protocol" example:"quic_initial"`
	ByteSize int                 `json:"byteSize" example:"344"`
	Packets  SignaturePacketsDTO `json:"packets"`
}

// SignatureGenerateResponse is the envelope for POST /signature/generate.
type SignatureGenerateResponse struct {
	Success bool                  `json:"success" example:"true"`
	Data    SignatureGenerateData `json:"data"`
}

// Generate produces I1–I5 CPS signature packets for a protocol, server-side.
// It is the API counterpart of the GUI's "Протокол → Сгенерировать" button.
//
//	@Summary		Signature generate
//	@Tags			signature
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body	SignatureGenerateRequest	true	"Protocol — один из quic_initial, stun, dns, dtls, sip"
//	@Success		200	{object}	SignatureGenerateResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		422	{object}	APIErrorEnvelope
//	@Router			/signature/generate [post]
func (h *SignatureHandler) Generate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req SignatureGenerateRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxGenerateBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && err != io.EOF {
		response.Error(w, "Некорректное тело запроса", "INVALID_BODY")
		return
	}

	if req.Protocol == "" {
		response.Error(w, "Укажите протокол", "MISSING_PROTOCOL")
		return
	}

	res, err := signature.Generate(req.Protocol)
	if err != nil {
		switch {
		case errors.Is(err, signature.ErrUnknownProtocol):
			response.Error(w, "Неизвестный протокол", "UNKNOWN_PROTOCOL")
		case errors.Is(err, signature.ErrPacketsTooLarge):
			response.ErrorWithStatus(w, http.StatusUnprocessableEntity,
				"Не удалось уложить пакеты в лимит размера", "PACKETS_TOO_LARGE")
		default:
			response.InternalError(w, "Ошибка генерации сигнатурных пакетов")
		}
		return
	}

	response.Success(w, SignatureGenerateData{
		OK:       true,
		Source:   "generated",
		Protocol: res.Profile,
		ByteSize: res.ByteSize,
		Packets: SignaturePacketsDTO{
			I1: res.Packets.I1,
			I2: res.Packets.I2,
			I3: res.Packets.I3,
			I4: res.Packets.I4,
			I5: res.Packets.I5,
		},
	})
}
