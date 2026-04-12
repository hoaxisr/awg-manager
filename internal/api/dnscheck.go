package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/dnscheck"
	"github.com/hoaxisr/awg-manager/internal/response"
)

type DnsCheckHandler struct {
	svc *dnscheck.Service
}

func NewDnsCheckHandler(svc *dnscheck.Service) *DnsCheckHandler {
	return &DnsCheckHandler{svc: svc}
}

// Start initiates DNS diagnostic check.
func (h *DnsCheckHandler) Start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	clientIP := extractClientIP(r)
	result, err := h.svc.Start(r.Context(), clientIP)
	if err != nil {
		response.Error(w, err.Error(), "DNSCHECK_START_ERROR")
		return
	}
	response.Success(w, result)
}

// Probe — cross-origin endpoint that client's DNS fetch hits. CORS required. NO auth.
func (h *DnsCheckHandler) Probe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	// Extract token from path: /api/dns-check/probe/{token}
	parts := strings.Split(r.URL.Path, "/")
	token := parts[len(parts)-1]
	if token == "" || token == "probe" {
		response.Error(w, "Missing token", "MISSING_TOKEN")
		return
	}

	h.svc.MarkReached(token)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// Complete finalizes the DNS check.
func (h *DnsCheckHandler) Complete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req dnscheck.CompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid JSON", "INVALID_JSON")
		return
	}
	result, err := h.svc.Complete(r.Context(), req.Token, req.DNSReached)
	if err != nil {
		response.Error(w, err.Error(), "DNSCHECK_COMPLETE_ERROR")
		return
	}
	response.Success(w, result)
}

func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
