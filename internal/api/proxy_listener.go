package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/procport"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// ProxyListenerHandler exposes port→PID lookup and kill for proxy clients.
type ProxyListenerHandler struct {
	records ProxyRecordLister
}

func NewProxyListenerHandler(records ProxyRecordLister) *ProxyListenerHandler {
	return &ProxyListenerHandler{records: records}
}

type listenerKey struct {
	port  int
	proto procport.Proto
}

// ownsListener — убивать разрешено только процесс на порту нашего же инстанса.
// Демон работает от root, и без этой проверки один POST сносит ndm (:79),
// DNS (:53) или SSH. Хост в ключ не входит: он ничего не защищает (процесс на
// этом порту находится по любому адресу), зато расхождение 0.0.0.0 против
// 127.0.0.1 ломало бы легитимный сценарий «освободить свой порт».
func (h *ProxyListenerHandler) ownsListener(port int, proto procport.Proto) bool {
	owned := make(map[listenerKey]bool)
	add := func(listen string, proto procport.Proto) {
		if _, p, ok := procport.ParseListenHostPort(listen, "127.0.0.1"); ok {
			owned[listenerKey{port: p, proto: proto}] = true
		}
	}
	if h.records == nil {
		return false
	}
	st, err := h.records.Load()
	if err != nil {
		return false
	}
	for _, rec := range st.Records {
		switch {
		case rec.WdttClient != nil:
			add(rec.WdttClient.Listen, procport.ProtoUDP)
		case rec.FreeTurnClient != nil:
			add(rec.FreeTurnClient.Listen, procport.ProtoUDP)
		case rec.WdttServer != nil:
			// Паритет ServerListenAddrs старого мира: DTLS, raw (явный либо
			// DTLS+1) и внутренний WG на localhost.
			add(rec.WdttServer.Listen, procport.ProtoUDP)
			add(rec.WdttServer.EffectiveRawListen(), procport.ProtoUDP)
			if rec.WdttServer.WgPort > 0 {
				add(net.JoinHostPort("127.0.0.1", strconv.Itoa(rec.WdttServer.WgPort)), procport.ProtoUDP)
			}
		case rec.FreeTurnServer != nil:
			serverProto := procport.ProtoUDP
			if strings.EqualFold(strings.TrimSpace(rec.FreeTurnServer.Mode), "tcp") {
				serverProto = procport.ProtoTCP
			}
			add(rec.FreeTurnServer.Listen, serverProto)
		}
	}
	return owned[listenerKey{port: port, proto: proto}]
}

// GetListener handles GET /api/proxy/listener?host=127.0.0.1&port=9000&proto=udp
//
//	@Summary		Lookup local proxy listen port
//	@Tags			device-proxy
//	@Produce		json
//	@Security		CookieAuth
//	@Param			host	query		string	false	"Bind address (default 127.0.0.1)"
//	@Param			port	query		int		true	"Listen port"
//	@Param			proto	query		string	false	"tcp or udp (default udp)"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/proxy/listener [get]
func (h *ProxyListenerHandler) GetListener(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		host = "127.0.0.1"
	}
	port, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("port")))
	if err != nil || port <= 0 {
		response.Error(w, "port required", "BAD_REQUEST")
		return
	}
	proto := procport.NormalizeProto(r.URL.Query().Get("proto"))
	info, err := procport.LookupListener(host, port, proto)
	if err != nil {
		response.Error(w, err.Error(), "PROXY_LISTENER_LOOKUP_FAILED")
		return
	}
	response.Success(w, info)
}

type killListenerRequest struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Proto string `json:"proto"`
}

// KillListener handles POST /api/proxy/kill-listener
//
//	@Summary		Kill process bound to local proxy listen port
//	@Tags			device-proxy
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		killListenerRequest	true	"Host, port, proto"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		403		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/proxy/kill-listener [post]
func (h *ProxyListenerHandler) KillListener(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req killListenerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	if req.Port <= 0 {
		response.Error(w, "port required", "BAD_REQUEST")
		return
	}
	proto := procport.NormalizeProto(req.Proto)
	if !h.ownsListener(req.Port, proto) {
		response.ErrorWithStatus(w, http.StatusForbidden,
			"порт не принадлежит ни одному инстансу прокси-рантайма", "PROXY_LISTENER_NOT_OWNED")
		return
	}
	info, err := procport.KillListener(host, req.Port, proto)
	if err != nil {
		response.Error(w, err.Error(), "PROXY_LISTENER_KILL_FAILED")
		return
	}
	response.Success(w, map[string]any{
		"message": "процесс остановлен, порт освобождён",
		"pid":     info.PID,
		"comm":    info.Comm,
	})
}
