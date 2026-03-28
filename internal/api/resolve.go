package api

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/response"
)

type ResolveHandler struct{}

func NewResolveHandler() *ResolveHandler {
	return &ResolveHandler{}
}

type resolveResponse struct {
	Domain string   `json:"domain"`
	IPs    []string `json:"ips"`
	Error  string   `json:"error,omitempty"`
}

func (h *ResolveHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	domain := r.URL.Query().Get("domain")
	if domain == "" {
		response.BadRequest(w, "Missing domain parameter")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	resolver := &net.Resolver{}
	addrs, err := resolver.LookupHost(ctx, domain)
	if err != nil {
		response.Success(w, resolveResponse{
			Domain: domain,
			IPs:    []string{},
			Error:  "Не удалось резолвить домен: " + err.Error(),
		})
		return
	}

	// Filter to IPv4 only
	var ipv4 []string
	for _, addr := range addrs {
		if ip := net.ParseIP(addr); ip != nil && ip.To4() != nil {
			ipv4 = append(ipv4, addr)
		}
	}
	if ipv4 == nil {
		ipv4 = []string{}
	}

	response.Success(w, resolveResponse{
		Domain: domain,
		IPs:    ipv4,
	})
}
