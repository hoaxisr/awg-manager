package api

import (
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

// Start initiates DNS diagnostic check (server-side checks only).
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

// Probe — cross-origin endpoint hit by the client's DNS probe fetch.
// If the client's DNS resolves awgm-dnscheck.test to the router, this
// endpoint is reachable and responds with 200. NO auth required.
func (h *DnsCheckHandler) Probe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
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
