package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// ── Inspector DTOs ───────────────────────────────────────────────

// SingboxRouterInspectRequest is the body for POST /singbox/router/inspect.
type SingboxRouterInspectRequest struct {
	Domain   string `json:"domain" example:"google.com"`
	Port     int    `json:"port,omitempty" example:"443"`
	Protocol string `json:"protocol,omitempty" example:"tcp"`
}

// SingboxRouterInspectMatchDTO mirrors router.RuleMatchResult.
type SingboxRouterInspectMatchDTO struct {
	Index      int      `json:"index" example:"0"`
	Matched    bool     `json:"matched" example:"true"`
	Action     string   `json:"action" example:"route"`
	Outbound   string   `json:"outbound,omitempty" example:"vpn"`
	Conditions []string `json:"conditions,omitempty"`
	Reason     string   `json:"reason,omitempty" example:"совпало по: domain_suffix"`
}

// SingboxRouterInspectData mirrors router.InspectResult.
type SingboxRouterInspectData struct {
	Input       string                         `json:"input" example:"google.com"`
	InputType   string                         `json:"inputType" example:"domain"`
	Matches     []SingboxRouterInspectMatchDTO `json:"matches"`
	Destination string                         `json:"destination" example:"vpn"`
	MatchedRule int                            `json:"matchedRule" example:"0"`
	Final       string                         `json:"final" example:"direct"`
	Note        string                         `json:"note,omitempty"`
}

// SingboxRouterInspectResponse is the envelope for POST /singbox/router/inspect.
type SingboxRouterInspectResponse struct {
	Success bool                     `json:"success" example:"true"`
	Data    SingboxRouterInspectData `json:"data"`
}

// SingboxRouterInspectDNSRequest is the body for POST /singbox/router/inspect-dns.
type SingboxRouterInspectDNSRequest struct {
	Domain    string `json:"domain" example:"discord.com"`
	QueryType string `json:"queryType,omitempty" example:"A"`
	SourceIP  string `json:"sourceIP,omitempty" example:"192.168.1.70"`
}

// SingboxRouterInspectDNSMatchDTO mirrors router.DNSRuleMatchResult.
type SingboxRouterInspectDNSMatchDTO struct {
	Index      int      `json:"index" example:"0"`
	Matched    bool     `json:"matched" example:"true"`
	Server     string   `json:"server,omitempty" example:"fakeip"`
	Conditions []string `json:"conditions,omitempty"`
	Reason     string   `json:"reason,omitempty" example:"совпало по: query_type"`
}

// SingboxRouterInspectDNSData mirrors router.InspectDNSResult.
type SingboxRouterInspectDNSData struct {
	Input          string                            `json:"input" example:"discord.com"`
	InputType      string                            `json:"inputType" example:"domain"`
	Matches        []SingboxRouterInspectDNSMatchDTO `json:"matches"`
	MatchedRule    int                               `json:"matchedRule" example:"5"`
	Server         string                            `json:"server" example:"fakeip"`
	Classification string                            `json:"classification" example:"fakeip"`
	Pool           string                            `json:"pool,omitempty" example:"198.18.0.0/15"`
	Final          string                            `json:"final" example:"fakeip"`
	Note           string                            `json:"note,omitempty"`
}

// SingboxRouterInspectDNSResponse is the envelope for POST /singbox/router/inspect-dns.
type SingboxRouterInspectDNSResponse struct {
	Success bool                        `json:"success" example:"true"`
	Data    SingboxRouterInspectDNSData `json:"data"`
}

// SingboxRouterInspectProgressDTO mirrors router.InspectProgress.
type SingboxRouterInspectProgressDTO struct {
	Phase        string `json:"phase" example:"rule_set_match_start"`
	Message      string `json:"message" example:"Проверяем rule_set geosite-youtube через sing-box…"`
	RuleIndex    *int   `json:"ruleIndex,omitempty" example:"3"`
	RuleTotal    *int   `json:"ruleTotal,omitempty" example:"17"`
	RuleSetTag   string `json:"ruleSetTag,omitempty" example:"geosite-youtube"`
	RuleSetIndex *int   `json:"ruleSetIndex,omitempty" example:"0"`
	RuleSetTotal *int   `json:"ruleSetTotal,omitempty" example:"2"`
	Final        string `json:"final,omitempty" example:"direct"`
	UsingDraft   bool   `json:"usingDraft,omitempty" example:"true"`
}

// SingboxRouterInspectStreamEventDTO mirrors router.InspectStreamEvent.
type SingboxRouterInspectStreamEventDTO struct {
	Type     string                            `json:"type" example:"progress"`
	Progress *SingboxRouterInspectProgressDTO  `json:"progress,omitempty"`
	Result   *SingboxRouterInspectData         `json:"result,omitempty"`
	Error    string                            `json:"error,omitempty" example:"load router config: ..."`
}

// Inspect simulates which router rule would match the given domain/IP.
//
//	@Summary		Inspect router routing decision
//	@Description	Simulates which router rule would match the given domain or IP, returning the would-be outbound. Matchers are evaluated in Go; rule_set matchers are additionally checked via sing-box rule-set match when the binary and rule-set files are available. If unavailable, rule_set degrades to no-match with an explanatory note.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterInspectRequest	true	"Domain or IP to test, plus optional port/protocol"
//	@Success		200		{object}	SingboxRouterInspectResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/inspect [post]
func (h *SingboxRouterHandler) Inspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req SingboxRouterInspectRequest
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if req.Domain == "" {
		response.Error(w, "domain обязателен", "MISSING_DOMAIN")
		return
	}
	if err := validateInspectParams(req.Port, req.Protocol); err != nil {
		response.Error(w, err.message, err.code)
		return
	}
	res, err := h.svc.Inspect(r.Context(), router.InspectInput{
		Domain:   req.Domain,
		Port:     req.Port,
		Protocol: req.Protocol,
	})
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, res)
}

// InspectDNS simulates which DNS rule would match the given domain and how
// the resolved DNS server classifies the resolution (fakeip/real/local).
//
//	@Summary		Inspect DNS-resolution decision
//	@Description	Simulates the DNS-resolution branch of the inspector: which dns.rule matches the domain, which DNS server answers, and whether the domain gets a fake-ip from a pool (→ tunnel), a real upstream IP, or a local (router) resolution. Mirrors the route inspector but over dns.rules. rule_set matchers are checked via sing-box rule-set match when available.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterInspectDNSRequest	true	"Domain to test, plus optional query type and source IP"
//	@Success		200		{object}	SingboxRouterInspectDNSResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/inspect-dns [post]
func (h *SingboxRouterHandler) InspectDNS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req SingboxRouterInspectDNSRequest
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if req.Domain == "" {
		response.Error(w, "domain обязателен", "MISSING_DOMAIN")
		return
	}
	res, err := h.svc.InspectDNS(r.Context(), router.InspectDNSInput{
		Domain:    req.Domain,
		QueryType: req.QueryType,
		SourceIP:  req.SourceIP,
	})
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, res)
}

// InspectStream streams route-inspector progress events via SSE.
//
// Event names:
//   - progress
//   - result
//   - inspect-error
//
// Each SSE event `data` is a JSON object matching SingboxRouterInspectStreamEventDTO.
//
//	@Summary		Inspect router routing decision (SSE stream)
//	@Description	Streams route-inspector progress via Server-Sent Events. Event names: progress, result, inspect-error. Data payload for each event is JSON shaped as SingboxRouterInspectStreamEventDTO.
//	@Tags			singbox-router
//	@Produce		text/event-stream
//	@Security		CookieAuth
//	@Param			domain		query		string	true	"Domain or IP to test"
//	@Param			port		query		int		false	"Destination port, 0-65535"
//	@Param			protocol	query		string	false	"Protocol (tcp/udp)"
//	@Success		200			{object}	SingboxRouterInspectStreamEventDTO	"SSE event data payload"
//	@Failure		400			{object}	APIErrorEnvelope
//	@Failure		500			{object}	APIErrorEnvelope
//	@Router			/singbox/router/inspect/stream [get]
func (h *SingboxRouterHandler) InspectStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		response.Error(w, "streaming not supported", "SSE_NOT_SUPPORTED")
		return
	}
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		response.Error(w, "domain обязателен", "MISSING_DOMAIN")
		return
	}
	port := 0
	if raw := r.URL.Query().Get("port"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			response.Error(w, "port должен быть числом от 0 до 65535", "INVALID_PORT")
			return
		}
		port = p
	}
	protocol := r.URL.Query().Get("protocol")
	if err := validateInspectParams(port, protocol); err != nil {
		response.Error(w, err.message, err.code)
		return
	}
	ch, err := h.svc.InspectStream(r.Context(), router.InspectInput{
		Domain: domain, Port: port, Protocol: protocol,
	})
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		}
	}
}

type inspectValidationError struct {
	message string
	code    string
}

func validateInspectParams(port int, protocol string) *inspectValidationError {
	if port < 0 || port > 65535 {
		return &inspectValidationError{
			message: "port должен быть числом от 0 до 65535",
			code:    "INVALID_PORT",
		}
	}
	if protocol != "" && protocol != "tcp" && protocol != "udp" {
		return &inspectValidationError{
			message: "protocol должен быть tcp или udp",
			code:    "INVALID_PROTOCOL",
		}
	}
	return nil
}
