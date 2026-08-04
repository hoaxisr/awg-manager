package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/procport"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// ProxyListenerHandler exposes port→PID lookup and kill for proxy clients.
type ProxyListenerHandler struct{}

func NewProxyListenerHandler() *ProxyListenerHandler { return &ProxyListenerHandler{} }

// GetListener handles GET /api/proxy/listener?host=127.0.0.1&port=9000&proto=udp
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
