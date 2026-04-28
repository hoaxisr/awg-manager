package api

import (
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/connections"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// ── Response DTOs ────────────────────────────────────────────────

// ConnectionProtocolsDTO holds per-protocol connection counts.
type ConnectionProtocolsDTO struct {
	TCP  int `json:"tcp" example:"28"`
	UDP  int `json:"udp" example:"12"`
	ICMP int `json:"icmp" example:"2"`
}

// ConnectionStatsDTO mirrors frontend ConnectionStats.
type ConnectionStatsDTO struct {
	Total     int                    `json:"total" example:"42"`
	Direct    int                    `json:"direct" example:"30"`
	Tunneled  int                    `json:"tunneled" example:"12"`
	Protocols ConnectionProtocolsDTO `json:"protocols"`
}

// ConntrackConnectionDTO mirrors frontend ConntrackConnection.
type ConntrackConnectionDTO struct {
	Protocol   string `json:"protocol" example:"tcp"`
	Src        string `json:"src" example:"192.168.1.100"`
	Dst        string `json:"dst" example:"8.8.8.8"`
	SrcPort    int    `json:"srcPort" example:"54321"`
	DstPort    int    `json:"dstPort" example:"443"`
	State      string `json:"state" example:"ESTABLISHED"`
	Packets    int    `json:"packets" example:"15"`
	Bytes      int    `json:"bytes" example:"4096"`
	Interface  string `json:"interface" example:"nwg0"`
	TunnelId   string `json:"tunnelId" example:"tun_abc123"`
	TunnelName string `json:"tunnelName" example:"My VPN"`
	ClientMac  string `json:"clientMac" example:"aa:bb:cc:dd:ee:ff"`
	ClientName string `json:"clientName" example:"My Phone"`
}

// ConnectionsPaginationDTO mirrors frontend ConnectionsPagination.
type ConnectionsPaginationDTO struct {
	Total    int `json:"total" example:"42"`
	Offset   int `json:"offset" example:"0"`
	Limit    int `json:"limit" example:"50"`
	Returned int `json:"returned" example:"42"`
}

// TunnelConnectionInfoDTO mirrors frontend TunnelConnectionInfo.
type TunnelConnectionInfoDTO struct {
	Name      string `json:"name" example:"My VPN"`
	Interface string `json:"interface" example:"nwg0"`
	Count     int    `json:"count" example:"12"`
}

// ConnectionsData mirrors frontend ConnectionsResponse.
type ConnectionsData struct {
	Stats      ConnectionStatsDTO               `json:"stats"`
	Tunnels    map[string]TunnelConnectionInfoDTO `json:"tunnels" swaggertype:"object"`
	Connections []ConntrackConnectionDTO         `json:"connections"`
	Pagination ConnectionsPaginationDTO          `json:"pagination"`
	FetchedAt  string                           `json:"fetchedAt" example:"2024-01-15T10:30:00Z"`
}

// ConnectionsResponseEnvelope is the envelope for GET /connections.
type ConnectionsResponseEnvelope struct {
	Success bool            `json:"success" example:"true"`
	Data    ConnectionsData `json:"data"`
}

// ConnectionsHandler handles GET /api/connections.
type ConnectionsHandler struct {
	svc *connections.Service
}

// NewConnectionsHandler creates a new connections handler.
func NewConnectionsHandler(svc *connections.Service) *ConnectionsHandler {
	return &ConnectionsHandler{svc: svc}
}

// List returns filtered and paginated conntrack connections.
//
//	@Summary		Connections list
//	@Tags			connections
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ConnectionsResponseEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/connections [get]
func (h *ConnectionsHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	q := r.URL.Query()
	params := connections.ListParams{
		Tunnel:   q.Get("tunnel"),
		Protocol: q.Get("protocol"),
		Search:   q.Get("search"),
		SortBy:   q.Get("sortBy"),
		SortDir:  q.Get("sortDir"),
	}

	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			response.BadRequest(w, "invalid offset parameter")
			return
		}
		params.Offset = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			response.BadRequest(w, "invalid limit parameter")
			return
		}
		params.Limit = n
	}

	resp, err := h.svc.List(r.Context(), params)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Conntrack not available", "CONNTRACK_UNAVAILABLE")
		return
	}

	response.Success(w, resp)
}
