package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"

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
	Singbox   int                    `json:"singbox" example:"3"`
	Local     int                    `json:"local" example:"5"`
	Protocols ConnectionProtocolsDTO `json:"protocols"`
}

// ConntrackConnectionDTO mirrors frontend ConntrackConnection.
type ConntrackConnectionDTO struct {
	Protocol   string                 `json:"protocol" example:"tcp"`
	Src        string                 `json:"src" example:"192.168.1.100"`
	Dst        string                 `json:"dst" example:"8.8.8.8"`
	SrcPort    int                    `json:"srcPort" example:"54321"`
	DstPort    int                    `json:"dstPort" example:"443"`
	State      string                 `json:"state" example:"ESTABLISHED"`
	Packets    int                    `json:"packets" example:"15"`
	Bytes      int                    `json:"bytes" example:"4096"`
	BytesIn    int                    `json:"bytesIn" example:"3072"`
	BytesOut   int                    `json:"bytesOut" example:"1024"`
	TTL        int                    `json:"ttl" example:"1183"`
	RouteClass string                 `json:"routeClass" example:"tunnel"`
	Interface  string                 `json:"interface" example:"nwg0"`
	TunnelId   string                 `json:"tunnelId" example:"tun_abc123"`
	TunnelName string                 `json:"tunnelName" example:"My VPN"`
	ClientMac  string                 `json:"clientMac" example:"aa:bb:cc:dd:ee:ff"`
	ClientName string                 `json:"clientName" example:"My Phone"`
	Rules      []ConnectionRuleHitDTO `json:"rules,omitempty"`
}

// ConnectionRuleHitDTO mirrors connections.RuleHit.
type ConnectionRuleHitDTO struct {
	ListID   string `json:"listId" example:"list_6"`
	ListName string `json:"listName,omitempty" example:"YouTube"`
	FQDN     string `json:"fqdn,omitempty" example:"m.youtube.com"`
	Pattern  string `json:"pattern,omitempty" example:"youtube.com"`
}

// ConnectionBucketDTO mirrors connections.Bucket.
type ConnectionBucketDTO struct {
	Key      string `json:"key" example:"@direct"`
	Label    string `json:"label,omitempty" example:"Напрямую"`
	Count    int    `json:"count" example:"12"`
	BytesIn  int    `json:"bytesIn" example:"4096"`
	BytesOut int    `json:"bytesOut" example:"1024"`
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
	Stats ConnectionStatsDTO `json:"stats"`
	// Tunnels: сводка по маршрутам; ключи — tunnelID и псевдо-классы
	// "@direct" / "@singbox" / "@local".
	Tunnels     map[string]TunnelConnectionInfoDTO `json:"tunnels"`
	ByTunnel    []ConnectionBucketDTO              `json:"byTunnel"`
	ByClient    []ConnectionBucketDTO              `json:"byClient"`
	ByDst       []ConnectionBucketDTO              `json:"byDst"`
	Connections []ConntrackConnectionDTO           `json:"connections"`
	Pagination  ConnectionsPaginationDTO           `json:"pagination"`
	FetchedAt   string                             `json:"fetchedAt" example:"2024-01-15T10:30:00Z"`
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
		State:    q.Get("state"),
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

// ConnectionKillData is the payload for DELETE /connections.
type ConnectionKillData struct {
	OK bool `json:"ok" example:"true"`
}

// ConnectionKillEnvelope is the envelope for DELETE /connections.
type ConnectionKillEnvelope struct {
	Success bool               `json:"success" example:"true"`
	Data    ConnectionKillData `json:"data"`
}

// Kill deletes one conntrack entry by its 5-tuple.
//
//	@Summary		Kill connection
//	@Tags			connections
//	@Produce		json
//	@Security		CookieAuth
//	@Param			src		query	string	true	"source IP"
//	@Param			dst		query	string	true	"destination IP"
//	@Param			srcPort	query	int		true	"source port"
//	@Param			dstPort	query	int		true	"destination port"
//	@Param			protocol	query	string	true	"tcp | udp"
//	@Success		200	{object}	ConnectionKillEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/connections [delete]
func (h *ConnectionsHandler) Kill(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := connections.KillParams{
		Src:      q.Get("src"),
		Dst:      q.Get("dst"),
		Protocol: strings.ToLower(q.Get("protocol")),
	}
	srcPort, err1 := strconv.Atoi(q.Get("srcPort"))
	dstPort, err2 := strconv.Atoi(q.Get("dstPort"))
	if err1 != nil || err2 != nil ||
		srcPort < 0 || srcPort > 65535 || dstPort < 0 || dstPort > 65535 ||
		net.ParseIP(p.Src) == nil || net.ParseIP(p.Dst) == nil {
		response.BadRequest(w, "invalid kill parameters")
		return
	}
	if p.Protocol != "tcp" && p.Protocol != "udp" {
		response.BadRequest(w, "protocol must be tcp or udp")
		return
	}
	p.SrcPort, p.DstPort = srcPort, dstPort

	if err := connections.Kill(p); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError,
			"Failed to kill connection: "+err.Error(), "CONNTRACK_KILL_FAILED")
		return
	}
	// Иначе следующий запрос списка (в пределах TTL снимка) вернёт убитое
	// соединение как живое.
	h.svc.InvalidateSnapshot()
	response.Success(w, ConnectionKillData{OK: true})
}
