package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// DHCPPoolLister is the read surface the fakeip-segments handler needs:
// enumerate the router's DHCP pools (name, subnet, dns-server). Satisfied
// by *query.DHCPPoolStore; an interface keeps the handler testable.
type DHCPPoolLister interface {
	List(ctx context.Context) ([]query.DHCPPool, error)
}

// SegmentDNSMutator is the write surface the per-segment DNS-delivery toggle
// needs: set or clear a single DHCP pool's advertised DNS server. Satisfied
// by *command.DHCPCommands; an interface keeps the handler testable.
type SegmentDNSMutator interface {
	SetPoolDNS(ctx context.Context, pool string, servers []string) error
	ClearPoolDNS(ctx context.Context, pool string) error
}

// FakeIPSegmentDTO is one DHCP pool projected as a fakeip "segment" for the
// per-segment DNS-delivery UI. InFakeip is true when the pool already hands
// clients the fakeip-tun DNS address (the .2 of the tun /30).
type FakeIPSegmentDTO struct {
	Pool      string `json:"pool" example:"_WEBADMIN"`
	Subnet    string `json:"subnet" example:"192.168.0.1/24"`
	DNSServer string `json:"dnsServer,omitempty" example:"172.18.0.2"`
	InFakeip  bool   `json:"inFakeip" example:"false"`
}

// FakeIPSegmentsData is the payload of GET /singbox/fakeip/segments: the
// fakeip-tun gateway addresses (read-only, engine-managed — surfaced for the
// Inbounds "tun-in" card so the FE does not hardcode magic IPs) plus the
// DHCP-pool→segment projection. The tun addresses come from the same
// DefaultFakeIPTunParams the enable path uses (single source of truth).
type FakeIPSegmentsData struct {
	// TunAddr4 is the tun gateway IPv4 CIDR, e.g. "172.18.0.1/30".
	TunAddr4 string `json:"tunAddr4" example:"172.18.0.1/30"`
	// TunAddr6 is the tun gateway IPv6 CIDR, e.g. "fdfe:dcba:9876::1/126"
	// (empty when v6 is disabled).
	TunAddr6 string `json:"tunAddr6,omitempty" example:"fdfe:dcba:9876::1/126"`
	// TunDNS is the fakeip-tun DNS handed to clients (the .2 of the tun /30),
	// the same address provisioning advertises via DHCP.
	TunDNS string `json:"tunDns,omitempty" example:"172.18.0.2"`
	// Segments is the DHCP-pool→fakeip-segment projection (always non-null).
	Segments []FakeIPSegmentDTO `json:"segments"`
}

// FakeIPSegmentsListResponse is the envelope for GET /singbox/fakeip/segments.
type FakeIPSegmentsListResponse struct {
	Success bool               `json:"success" example:"true"`
	Data    FakeIPSegmentsData `json:"data"`
}

// FakeIPSegmentToggleRequest is the body of POST /singbox/fakeip/segments: set
// (inFakeip=true) or clear (false) the DHCP DNS-delivery for one pool.
type FakeIPSegmentToggleRequest struct {
	Pool     string `json:"pool" example:"_WEBADMIN"`
	InFakeip bool   `json:"inFakeip" example:"true"`
}

// FakeIPSegmentToggleResult is the data payload returned after a successful
// toggle: the pool and its resulting delivery state.
type FakeIPSegmentToggleResult struct {
	Pool     string `json:"pool" example:"_WEBADMIN"`
	InFakeip bool   `json:"inFakeip" example:"true"`
}

// FakeIPSegmentToggleResponse is the envelope for POST /singbox/fakeip/segments.
type FakeIPSegmentToggleResponse struct {
	Success bool                      `json:"success" example:"true"`
	Data    FakeIPSegmentToggleResult `json:"data"`
}

// FakeIPSegmentsHandler serves the DHCP-pool→fakeip-segment projection (GET)
// and the per-segment DNS-delivery toggle (POST) used by the frontend.
type FakeIPSegmentsHandler struct {
	pools DHCPPoolLister
	dns   SegmentDNSMutator
}

// NewFakeIPSegmentsHandler wires the handler over a DHCP-pool lister (read)
// and a per-pool DNS mutator (write).
func NewFakeIPSegmentsHandler(pools DHCPPoolLister, dns SegmentDNSMutator) *FakeIPSegmentsHandler {
	return &FakeIPSegmentsHandler{pools: pools, dns: dns}
}

// Serve dispatches one path by method: GET→ListSegments, POST→ToggleSegment.
// Any other method is 405. Registered on /singbox/fakeip/segments.
func (h *FakeIPSegmentsHandler) Serve(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.ListSegments(w, r)
	case http.MethodPost:
		h.ToggleSegment(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}

// ListSegments returns the router's DHCP pools as fakeip segments.
//
//	@Summary		List fakeip segments
//	@Description	Returns the router's DHCP pools projected as fakeip "segments" for the per-segment DNS-delivery toggles. Each segment reports its pool name, subnet CIDR, the DHCP-advertised DNS server, and whether that DNS is already the fakeip-tun DNS (inFakeip). Always a JSON array, never null.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	FakeIPSegmentsListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/segments [get]
func (h *FakeIPSegmentsHandler) ListSegments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	pools, err := h.pools.List(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	// Tun gateway params come from the same default params the enable path
	// uses (single source of truth) — the FE's read-only "tun-in" Inbounds
	// card surfaces these instead of hardcoding magic IPs.
	params := router.DefaultFakeIPTunParams()
	// expected = the fakeip-tun DNS address handed to clients (.2 of the tun
	// /30). Same derivation as provisioning, so the inFakeip flag stays
	// consistent with what provisioning would set.
	expected, _ := router.DeriveTunDNS(params.TunAddr4)

	segments := make([]FakeIPSegmentDTO, 0, len(pools))
	for _, p := range pools {
		segments = append(segments, FakeIPSegmentDTO{
			Pool:      p.Name,
			Subnet:    p.Network,
			DNSServer: p.DNSServer,
			InFakeip:  expected != "" && p.DNSServer == expected,
		})
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].Pool < segments[j].Pool })
	response.Success(w, FakeIPSegmentsData{
		TunAddr4: params.TunAddr4,
		TunAddr6: params.TunAddr6,
		TunDNS:   expected,
		Segments: segments,
	})
}

// ToggleSegment sets (inFakeip=true) or clears (false) the DHCP-advertised DNS
// for one pool, so its clients are (or stop being) handed the fakeip-tun DNS
// (.2 of the tun /30, the SAME address the enable path derives).
//
// Atomicity: exactly ONE pool is toggled per call via a single NDMS
// SetPoolDNS/ClearPoolDNS — atomic per pool. A failure affects only that pool
// and returns an error naming it; there is no partial multi-pool state.
//
//	@Summary		Toggle fakeip DNS delivery for one segment
//	@Description	Sets (inFakeip=true) or clears (false) the DHCP-advertised DNS for one pool. true hands that pool's clients the fakeip-tun DNS (the .2 of the tun /30, the same address the enable path derives); false drops the custom dns-server so the pool falls back to default. Exactly one pool is toggled per call (atomic per pool). On an NDMS failure returns 500 with the pool name in the message; no partial multi-pool state.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		FakeIPSegmentToggleRequest	true	"Pool + target delivery state"
//	@Success		200		{object}	FakeIPSegmentToggleResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/fakeip/segments [post]
func (h *FakeIPSegmentsHandler) ToggleSegment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req FakeIPSegmentToggleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body: "+err.Error())
		return
	}
	if req.Pool == "" {
		response.BadRequest(w, "pool is required")
		return
	}

	if req.InFakeip {
		// .2 of the tun /30 — the fakeip-tun DNS handed to clients. Derived from
		// the same default params the enable path uses, so this toggle and
		// provisioning agree on the address.
		dns, err := router.DeriveTunDNS(router.DefaultFakeIPTunParams().TunAddr4)
		if err != nil {
			response.InternalError(w, fmt.Sprintf("derive fakeip DNS for pool %s: %v", req.Pool, err))
			return
		}
		if err := h.dns.SetPoolDNS(r.Context(), req.Pool, []string{dns}); err != nil {
			response.InternalError(w, fmt.Sprintf("set DNS delivery for pool %s: %v", req.Pool, err))
			return
		}
	} else {
		if err := h.dns.ClearPoolDNS(r.Context(), req.Pool); err != nil {
			response.InternalError(w, fmt.Sprintf("clear DNS delivery for pool %s: %v", req.Pool, err))
			return
		}
	}

	response.Success(w, FakeIPSegmentToggleResult{Pool: req.Pool, InFakeip: req.InFakeip})
}
