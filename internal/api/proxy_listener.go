package api

import (
	"encoding/json"
	"net"
	"net/http"
	"slices"
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

// Шов для тестов: настоящий KillListener шлёт сигналы от root.
var killListener = procport.KillListener

// ownedBindHosts — адреса, на которые инстансы прокси-рантайма делают bind на
// (port, proto). Убивать разрешено только процесс, мешающий такому bind'у:
// демон работает от root, и без этой проверки один POST сносит ndm (:79), DNS
// (:53) или SSH. Хост берётся из конфига, а не из запроса: procport ищет
// сокеты, конфликтующие с переданным адресом (для 0.0.0.0 — любой адрес на
// порту), и хост клиента позволял бы снести процесс на чужом IP с тем же
// номером порта.
//
// Хост без адреса (":56000") бинарь поднимает на всех адресах, поэтому у
// серверов он 0.0.0.0. Клиенты слушают только 127.0.0.1 (localListen).
func (h *ProxyListenerHandler) ownedBindHosts(port int, proto procport.Proto) []string {
	owned := make(map[listenerKey][]string)
	add := func(listen, defaultHost string, proto procport.Proto) {
		if host, p, ok := procport.ParseListenHostPort(listen, defaultHost); ok {
			k := listenerKey{port: p, proto: proto}
			host = canonicalBindHost(host)
			if !slices.Contains(owned[k], host) {
				owned[k] = append(owned[k], host)
			}
		}
	}
	if h.records == nil {
		return nil
	}
	st, err := h.records.Load()
	if err != nil {
		return nil
	}
	for _, rec := range st.Records {
		switch {
		case rec.WdttClient != nil:
			add(rec.WdttClient.Listen, "127.0.0.1", procport.ProtoUDP)
		case rec.FreeTurnClient != nil:
			add(rec.FreeTurnClient.Listen, "127.0.0.1", procport.ProtoUDP)
		case rec.WdttServer != nil:
			// Паритет ServerListenAddrs старого мира: DTLS, raw (явный либо
			// DTLS+1), direct (если отличается от DTLS) и внутренний WG.
			add(rec.WdttServer.Listen, "0.0.0.0", procport.ProtoUDP)
			add(rec.WdttServer.EffectiveRawListen(), "0.0.0.0", procport.ProtoUDP)
			if d := strings.TrimSpace(rec.WdttServer.DirectListen); d != "" && d != rec.WdttServer.Listen {
				add(d, "0.0.0.0", procport.ProtoUDP)
			}
			// wireguard-go слушает wg-port на ВСЕХ адресах (см. validatePorts).
			if rec.WdttServer.WgPort > 0 {
				add(net.JoinHostPort("0.0.0.0", strconv.Itoa(rec.WdttServer.WgPort)), "0.0.0.0", procport.ProtoUDP)
			}
		case rec.FreeTurnServer != nil:
			serverProto := procport.ProtoUDP
			if strings.EqualFold(strings.TrimSpace(rec.FreeTurnServer.Mode), "tcp") {
				serverProto = procport.ProtoTCP
			}
			add(rec.FreeTurnServer.Listen, "0.0.0.0", serverProto)
		}
	}
	return owned[listenerKey{port: port, proto: proto}]
}

// killBindHost — адрес bind'а, конфликт с которым разрешено снять. Хост из
// запроса ничего не расширяет: он лишь выбирает один из адресов конфига, когда
// их на этом порту несколько. Единственный адрес конфига берётся как есть —
// UI шлёт хост, нормализованный по-своему (":port" у него 127.0.0.1).
func (h *ProxyListenerHandler) killBindHost(reqHost string, port int, proto procport.Proto) (string, bool) {
	hosts := h.ownedBindHosts(port, proto)
	if len(hosts) == 1 {
		return hosts[0], true
	}
	reqHost = canonicalBindHost(reqHost)
	if slices.Contains(hosts, reqHost) {
		return reqHost, true
	}
	return "", false
}

func canonicalBindHost(host string) string {
	host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(host), "["), "]")
	if host == "" || host == "localhost" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
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
	bindHost, ok := h.killBindHost(host, req.Port, proto)
	if !ok {
		response.ErrorWithStatus(w, http.StatusForbidden,
			"порт не принадлежит ни одному инстансу прокси-рантайма", "PROXY_LISTENER_NOT_OWNED")
		return
	}
	info, err := killListener(bindHost, req.Port, proto)
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
