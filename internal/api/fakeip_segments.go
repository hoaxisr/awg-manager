package api

import (
	"context"
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

// FakeIPSegmentDTO is one DHCP pool projected as a fakeip "segment" for the
// per-segment DNS-delivery UI. InFakeip is true when the pool already hands
// clients the fakeip-tun DNS address (the .2 of the tun /30).
type FakeIPSegmentDTO struct {
	Pool      string `json:"pool" example:"_WEBADMIN"`
	Subnet    string `json:"subnet" example:"192.168.0.1/24"`
	DNSServer string `json:"dnsServer,omitempty" example:"172.18.0.2"`
	InFakeip  bool   `json:"inFakeip" example:"false"`
}

// FakeIPSegmentsListResponse is the envelope for GET /singbox/fakeip/segments.
type FakeIPSegmentsListResponse struct {
	Success bool               `json:"success" example:"true"`
	Data    []FakeIPSegmentDTO `json:"data"`
}

// FakeIPSegmentsHandler serves the DHCP-pool→fakeip-segment projection used
// by the per-segment DNS-delivery toggles in the frontend.
type FakeIPSegmentsHandler struct {
	pools DHCPPoolLister
}

// NewFakeIPSegmentsHandler wires the handler over a DHCP-pool lister.
func NewFakeIPSegmentsHandler(pools DHCPPoolLister) *FakeIPSegmentsHandler {
	return &FakeIPSegmentsHandler{pools: pools}
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

	// expected = the fakeip-tun DNS address handed to clients (.2 of the tun
	// /30). Derived from the same default params the enable path uses, so the
	// inFakeip flag stays consistent with what provisioning would set.
	expected, _ := router.DeriveTunDNS(router.DefaultFakeIPTunParams().TunAddr4)

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
	response.Success(w, segments)
}
