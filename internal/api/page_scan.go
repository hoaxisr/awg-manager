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
	Interface string `json:"interface,omitempty" example:"opkgtun11"`
	ProxyURL  string `json:"proxyUrl,omitempty" example:"socks5://127.0.0.1:1080"`
}

// PageScanInterfaceOptionDTO mirrors pagescan.InterfaceOption for OpenAPI schema generation.
type PageScanInterfaceOptionDTO struct {
	Name  string `json:"name" example:"opkgtun11"`
	Label string `json:"label" example:"VPN DE (opkgtun11)"`
}

// PageScanProxyOptionDTO mirrors pagescan.ProxyOption for OpenAPI schema generation.
type PageScanProxyOptionDTO struct {
	URL   string `json:"url" example:"http://127.0.0.1:1099"`
	Label string `json:"label" example:"FIRC inbound (127.0.0.1:1099)"`
}

// PageScanOptionsDTO mirrors pagescan.ScanOptionsResult for OpenAPI schema generation.
type PageScanOptionsDTO struct {
	DefaultProxy      string                       `json:"defaultProxy,omitempty" example:"http://127.0.0.1:1099"`
	DefaultProxyLabel string                       `json:"defaultProxyLabel,omitempty" example:"FIRC inbound (127.0.0.1:1099)"`
	Proxies           []PageScanProxyOptionDTO     `json:"proxies"`
	Interfaces        []PageScanInterfaceOptionDTO `json:"interfaces"`
}

// PageScanOptionsEnvelope is the response envelope for GET /diagnostics/page-scan/options.
type PageScanOptionsEnvelope struct {
	Success bool               `json:"success" example:"true"`
	Data    PageScanOptionsDTO `json:"data"`
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
	RouteUnknown  bool          `json:"routeUnknown,omitempty"`
}

// PageScanResultDTO mirrors pagescan.Result for OpenAPI schema generation.
type PageScanResultDTO struct {
	RequestedURL     string                `json:"requestedUrl" example:"https://www.themoviedb.org"`
	FinalURL         string                `json:"finalUrl" example:"https://www.themoviedb.org/"`
	StatusCode       int                   `json:"statusCode" example:"200"`
	ContentType      string                `json:"contentType,omitempty" example:"text/html; charset=utf-8"`
	FetchVia         string                `json:"fetchVia" example:"direct"`
	FetchTarget      string                `json:"fetchTarget,omitempty" example:"http://127.0.0.1:1099"`
	FetchTargetLabel string                `json:"fetchTargetLabel,omitempty" example:"FIRC inbound (127.0.0.1:1099)"`
	DefaultProxy     string                `json:"defaultProxy,omitempty" example:"http://127.0.0.1:1099"`
	ProxyAvailable   bool                  `json:"proxyAvailable" example:"true"`
	BodyBytes        int                   `json:"bodyBytes" example:"65536"`
	BodyTruncated    bool                  `json:"bodyTruncated,omitempty" example:"false"`
	TotalHosts       int                   `json:"totalHosts" example:"8"`
	HostsTruncated   bool                  `json:"hostsTruncated,omitempty" example:"false"`
	DurationMs       int64                 `json:"durationMs" example:"420"`
	Resources        []PageScanResourceDTO `json:"resources"`
}

// PageScanEnvelope is the response envelope for POST /diagnostics/page-scan.
type PageScanEnvelope struct {
	Success bool              `json:"success" example:"true"`
	Data    PageScanResultDTO `json:"data"`
}

// PageScanHandler handles GET /api/diagnostics/page-scan/options and POST /api/diagnostics/page-scan.
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

// GetOptions lists reachable local proxies and active router tunnel interfaces available for page scanning.
//
//	@Summary		List page scan egress options
//	@Description	Discovers active router tunnel interfaces and reachable local proxies available for on-demand page scanning.
//	@Tags			diagnostics
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	PageScanOptionsEnvelope
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/diagnostics/page-scan/options [get]
func (h *PageScanHandler) GetOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	response.Success(w, h.svc.Options(ctx))
}

// Post fetches a page on demand, extracts external resource hostnames, checks active routing, and resolves PTR/ASN metadata.
//
//	@Summary		Scan page resources
//	@Description	Fetches target HTML page on demand (SSRF-safe), extracts referenced hostnames, checks FIRC routing, and inspects DNS/PTR/ASN metadata.
//	@Tags			diagnostics
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		PageScanRequestDTO	true	"Target URL and optional fetch mode (auto, direct, proxy, interface) with interface/proxyUrl"
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

	res, err := h.svc.ScanWithOptions(ctx, pagescan.ScanRequest{
		URL:       req.URL,
		Mode:      mode,
		Interface: req.Interface,
		ProxyURL:  req.ProxyURL,
	})
	if err != nil {
		switch {
		case errors.Is(err, pagescan.ErrInvalidURL),
			errors.Is(err, pagescan.ErrBlockedAddress),
			errors.Is(err, pagescan.ErrInvalidFetchMode),
			errors.Is(err, pagescan.ErrInvalidInterface),
			errors.Is(err, pagescan.ErrInvalidProxy):
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
