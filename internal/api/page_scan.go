package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/pagescan"
	"github.com/hoaxisr/awg-manager/internal/response"
)

const maxPageScanReqBytes = 4096

// PageScanRequestDTO describes POST /diagnostics/page-scan request payload.
type PageScanRequestDTO struct {
	URL       string `json:"url" example:"https://www.themoviedb.org"`
	FetchMode string `json:"fetchMode,omitempty" example:"auto"`
}

// PageScanResourceDTO mirrors pagescan.Resource for OpenAPI schema generation.
type PageScanResourceDTO struct {
	Host          string        `json:"host" example:"www.themoviedb.org"`
	Sources       []string      `json:"sources" example:"document,script"`
	IP            string        `json:"ip,omitempty" example:"143.204.238.90"`
	FakeIP        string        `json:"fakeIp,omitempty" example:"198.18.0.12"`
	Routed        bool          `json:"routed" example:"true"`
	GroupID       string        `json:"groupId,omitempty" example:"media"`
	GroupName     string        `json:"groupName,omitempty" example:"Media"`
	Outbound      string        `json:"outbound,omitempty" example:"tunvless0"`
	OutboundLabel string        `json:"outboundLabel,omitempty" example:"Netherlands"`
	Inspect       *IPInspectDTO `json:"inspect,omitempty"`
	ResolveError  string        `json:"resolveError,omitempty"`
}

// PageScanResultDTO mirrors pagescan.Result for OpenAPI schema generation.
type PageScanResultDTO struct {
	RequestedURL   string                `json:"requestedUrl" example:"https://www.themoviedb.org"`
	FinalURL       string                `json:"finalUrl" example:"https://www.themoviedb.org/"`
	StatusCode     int                   `json:"statusCode" example:"200"`
	ContentType    string                `json:"contentType,omitempty" example:"text/html; charset=utf-8"`
	FetchVia       string                `json:"fetchVia" example:"direct"`
	ProxyAvailable bool                  `json:"proxyAvailable" example:"true"`
	BodyBytes      int                   `json:"bodyBytes" example:"65536"`
	BodyTruncated  bool                  `json:"bodyTruncated,omitempty" example:"false"`
	TotalHosts     int                   `json:"totalHosts" example:"8"`
	HostsTruncated bool                  `json:"hostsTruncated,omitempty" example:"false"`
	DurationMs     int64                 `json:"durationMs" example:"420"`
	Resources      []PageScanResourceDTO `json:"resources"`
}

// PageScanEnvelope is the response envelope for POST /diagnostics/page-scan.
type PageScanEnvelope struct {
	Success bool              `json:"success" example:"true"`
	Data    PageScanResultDTO `json:"data"`
}

// PageScanHandler handles POST /api/diagnostics/page-scan.
type PageScanHandler struct {
	svc *pagescan.Service
}

// NewPageScanHandler constructs a page resource scanner HTTP handler.
func NewPageScanHandler(svc *pagescan.Service) *PageScanHandler {
	if svc == nil {
		svc = pagescan.NewService(pagescan.Options{})
	}
	return &PageScanHandler{svc: svc}
}

// Post fetches a page on demand, extracts external resource hostnames, checks active routing, and resolves PTR/ASN metadata.
//
//	@Summary		Scan page resources
//	@Description	Fetches target HTML page on demand (SSRF-safe), extracts referenced hostnames, checks FIRC routing, and inspects DNS/PTR/ASN metadata.
//	@Tags			diagnostics
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		PageScanRequestDTO	true	"Target URL and optional fetch mode (auto, direct, proxy)"
//	@Success		200		{object}	PageScanEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		502		{object}	APIErrorEnvelope
//	@Failure		503		{object}	APIErrorEnvelope
//	@Router			/diagnostics/page-scan [post]
func (h *PageScanHandler) Post(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPageScanReqBytes+1))
	if err != nil {
		response.BadRequest(w, "failed to read request body")
		return
	}
	if len(body) > maxPageScanReqBytes {
		response.BadRequest(w, "request body too large")
		return
	}

	var req PageScanRequestDTO
	if err := json.Unmarshal(body, &req); err != nil {
		response.BadRequest(w, "invalid JSON payload")
		return
	}

	mode := pagescan.FetchMode(strings.ToLower(strings.TrimSpace(req.FetchMode)))
	ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
	defer cancel()

	res, err := h.svc.Scan(ctx, req.URL, mode)
	if err != nil {
		switch {
		case errors.Is(err, pagescan.ErrInvalidURL),
			errors.Is(err, pagescan.ErrBlockedAddress),
			errors.Is(err, pagescan.ErrInvalidFetchMode):
			response.BadRequest(w, err.Error())
		case errors.Is(err, pagescan.ErrProxyUnavailable):
			response.ErrorWithStatus(w, http.StatusServiceUnavailable, err.Error(), "PROXY_UNAVAILABLE")
		default:
			response.ErrorWithStatus(w, http.StatusBadGateway, err.Error(), "UPSTREAM_ERROR")
		}
		return
	}

	response.Success(w, res)
}
