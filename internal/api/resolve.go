package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/response"
)

type ResolveHandler struct {
	lookupHost func(context.Context, string) ([]string, error)
	lookupIP   func(context.Context, string, string) ([]net.IP, error)
}

func NewResolveHandler() *ResolveHandler {
	resolver := &net.Resolver{}
	return &ResolveHandler{lookupHost: resolver.LookupHost, lookupIP: resolver.LookupIP}
}

// ResolveResponse is the JSON body for GET /api/routing/resolve.
type ResolveResponse struct {
	Domain string   `json:"domain"`
	IPs    []string `json:"ips"`
	Error  string   `json:"error,omitempty"`
}

// lookupResolveAllFamilies resolves A and AAAA independently. LookupHost can
// return partial addresses alongside a family error, which is not fail-closed
// enough for routing coverage checks. IsNotFound is the system resolver's
// NODATA result; SERVFAIL and transport failures remain errors.
func lookupResolveAllFamilies(ctx context.Context, domain string, lookupIP func(context.Context, string, string) ([]net.IP, error)) ([]string, error) {
	families := make([][]string, 2)
	total := 0
	var noDataErrs []error
	for i, network := range []string{"ip4", "ip6"} {
		ips, err := lookupIP(ctx, network, domain)
		if err != nil {
			var dnsErr *net.DNSError
			if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
				noDataErrs = append(noDataErrs, err)
				continue
			}
			return nil, fmt.Errorf("%s lookup: %w", network, err)
		}
		for _, ip := range ips {
			if parsed := net.ParseIP(ip.String()); parsed != nil {
				families[i] = append(families[i], parsed.String())
				total++
			}
		}
	}
	if total == 0 {
		if len(noDataErrs) > 0 {
			return nil, errors.Join(noDataErrs...)
		}
		return nil, errors.New("DNS returned no addresses")
	}
	addresses := make([]string, 0, total)
	for i := 0; len(addresses) < total; i++ {
		for _, family := range families {
			if i < len(family) {
				addresses = append(addresses, family[i])
			}
		}
	}
	return addresses, nil
}

// Resolve performs a DNS lookup for routing search (IPv4 only by default).
//
//	@Summary		Resolve domain
//	@Tags			routing
//	@Produce		json
//	@Security		CookieAuth
//	@Param			domain	query	string	true	"Hostname to resolve"
//	@Param			family	query	string	false	"Address family: all returns IPv4 and IPv6 (default IPv4 only)"
//	@Success		200	{object}	APIEnvelope{data=ResolveResponse}
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/routing/resolve [get]
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

	resolveError := func(err error) {
		response.Success(w, ResolveResponse{
			Domain: domain,
			IPs:    []string{},
			Error:  "Не удалось резолвить домен: " + err.Error(),
		})
	}
	allFamilies := r.URL.Query().Get("family") == "all"
	if allFamilies {
		lookupIP := h.lookupIP
		if lookupIP == nil {
			resolver := &net.Resolver{}
			lookupIP = resolver.LookupIP
		}
		ips, err := lookupResolveAllFamilies(ctx, domain, lookupIP)
		if err != nil {
			resolveError(err)
			return
		}
		response.Success(w, ResolveResponse{Domain: domain, IPs: ips})
		return
	}

	lookupHost := h.lookupHost
	if lookupHost == nil {
		resolver := &net.Resolver{}
		lookupHost = resolver.LookupHost
	}
	addrs, err := lookupHost(ctx, domain)
	if err != nil {
		resolveError(err)
		return
	}

	var ips []string
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && ip.To4() != nil {
			ips = append(ips, addr)
		}
	}
	if ips == nil {
		ips = []string{}
	}

	response.Success(w, ResolveResponse{
		Domain: domain,
		IPs:    ips,
	})
}
