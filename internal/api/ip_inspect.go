package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ipinspect"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// IPInspectDTO mirrors ipinspect.Result for OpenAPI schema generation.
type IPInspectDTO struct {
	IP           string   `json:"ip" example:"1.1.1.1"`
	Family       string   `json:"family" example:"ipv4"`
	Scope        string   `json:"scope" example:"public"`
	PTR          string   `json:"ptr,omitempty" example:"one.one.one.one"`
	PTRs         []string `json:"ptrs,omitempty"`
	ASN          int      `json:"asn,omitempty" example:"13335"`
	Prefix       string   `json:"prefix,omitempty" example:"1.1.1.0/24"`
	Country      string   `json:"country,omitempty" example:"AU"`
	Registry     string   `json:"registry,omitempty" example:"apnic"`
	Organization string   `json:"organization,omitempty" example:"CLOUDFLARENET, US"`
	Cached       bool     `json:"cached,omitempty"`
}

// IPInspectEnvelope is the response envelope for GET /diagnostics/ip-inspect.
type IPInspectEnvelope struct {
	Success bool         `json:"success" example:"true"`
	Data    IPInspectDTO `json:"data"`
}

// IPInspectHandler handles GET /api/diagnostics/ip-inspect.
type IPInspectHandler struct {
	svc *ipinspect.Service
}

// NewIPInspectHandler constructs an IP inspection HTTP handler.
func NewIPInspectHandler(svc *ipinspect.Service) *IPInspectHandler {
	if svc == nil {
		svc = ipinspect.NewService()
	}
	return &IPInspectHandler{svc: svc}
}

// Get returns PTR and Team Cymru ASN/BGP prefix metadata for a target IP address.
//
//	@Summary		Inspect IP address
//	@Description	Classifies an IP address and resolves PTR + Team Cymru DNS ASN/BGP prefix metadata on demand.
//	@Tags			diagnostics
//	@Produce		json
//	@Security		CookieAuth
//	@Param			ip	query		string	true	"IPv4 or IPv6 address"
//	@Success		200	{object}	IPInspectEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/diagnostics/ip-inspect [get]
func (h *IPInspectHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip == "" {
		response.BadRequest(w, "invalid ip parameter")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	res, err := h.svc.Inspect(ctx, ip)
	if err != nil {
		if errors.Is(err, ipinspect.ErrInvalidIP) {
			response.BadRequest(w, "invalid ip parameter")
			return
		}
		response.InternalError(w, err.Error())
		return
	}

	response.Success(w, res)
}
