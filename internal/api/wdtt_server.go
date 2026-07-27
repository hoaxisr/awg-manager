package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

func (h *WdttHandler) SetNDMSQueries(q *ndmsquery.Queries) {
	h.queries = q
}

func (h *WdttHandler) resolveExternalIP(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var fallback testing.WANIPFallback
	var wanKernel string
	if h.queries != nil {
		fallback = h.queries.WANInterfaceAddress
		if h.queries.Routes != nil && h.queries.Interfaces != nil {
			if ndmsName, err := h.queries.Routes.GetDefaultGatewayInterface(ctx); err == nil && ndmsName != "" {
				wanKernel = h.queries.Interfaces.ResolveSystemName(ctx, ndmsName)
			}
		}
	}
	return testing.GetWANIPBound(ctx, wanKernel, fallback)
}

// UpdateServerConfig handles PUT /api/wdtt/server/config.
func (h *WdttHandler) UpdateServerConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var cfg wdtt.ServerConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	if err := h.svc.UpdateServerConfig(cfg); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, cfg)
}

// StartServer handles POST /api/wdtt/server/start.
func (h *WdttHandler) StartServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StartServer(); err != nil {
		response.Error(w, err.Error(), "WDTT_SERVER_START_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "server started"})
}

// StopServer handles POST /api/wdtt/server/stop.
func (h *WdttHandler) StopServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StopServer(); err != nil {
		response.Error(w, err.Error(), "WDTT_SERVER_STOP_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "server stopped"})
}

// WdttGenerateLinkRequest is the body for POST /api/wdtt/servers/{id}/link.
type WdttGenerateLinkRequest struct {
	Peer     string   `json:"peer,omitempty"`
	VKHashes []string `json:"vkHashes,omitempty"`
	Name     string   `json:"name,omitempty"`
}

// CreateServer handles POST /api/wdtt/servers.
func (h *WdttHandler) CreateServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var in wdtt.CreateServerInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && r.ContentLength > 0 {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	inst, err := h.svc.CreateServer(in)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, inst)
}

func (h *WdttHandler) ServeServers(w http.ResponseWriter, r *http.Request) {
	id, sub := parseInstancePath(r.URL.Path, "/api/wdtt/servers/")
	if id == "" {
		response.Error(w, "missing server id", "BAD_REQUEST")
		return
	}
	switch {
	case len(sub) == 0:
		h.serveServerByID(w, r, id)
	case len(sub) == 1 && sub[0] == "start":
		h.startServerInstance(w, r, id)
	case len(sub) == 1 && sub[0] == "stop":
		h.stopServerInstance(w, r, id)
	case len(sub) == 1 && sub[0] == "link":
		h.generateLinkForServer(w, r, id)
	default:
		response.ErrorWithStatus(w, http.StatusNotFound, "Not found", "NOT_FOUND")
	}
}

func (h *WdttHandler) serveServerByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		var cfg wdtt.ServerConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			response.Error(w, "invalid request body", "BAD_REQUEST")
			return
		}
		if err := h.svc.UpdateServerInstance(id, cfg); err != nil {
			response.Error(w, err.Error(), "WDTT_SERVER_UPDATE_FAILED")
			return
		}
		response.Success(w, map[string]any{"config": cfg})
	case http.MethodPatch:
		var req renameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, "invalid request body", "BAD_REQUEST")
			return
		}
		if err := h.svc.RenameServer(id, req.Name); err != nil {
			response.Error(w, err.Error(), "WDTT_SERVER_RENAME_FAILED")
			return
		}
		response.Success(w, map[string]string{"message": "renamed"})
	case http.MethodDelete:
		if err := h.svc.DeleteServer(id); err != nil {
			response.Error(w, err.Error(), "WDTT_SERVER_DELETE_FAILED")
			return
		}
		response.Success(w, map[string]string{"message": "deleted"})
	default:
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
	}
}

func (h *WdttHandler) startServerInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StartServerInstance(id); err != nil {
		response.Error(w, err.Error(), "WDTT_SERVER_START_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "server started"})
}

func (h *WdttHandler) stopServerInstance(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if err := h.svc.StopServerInstance(id); err != nil {
		response.Error(w, err.Error(), "WDTT_SERVER_STOP_FAILED")
		return
	}
	response.Success(w, map[string]string{"message": "server stopped"})
}

func (h *WdttHandler) generateLinkForServer(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req WdttGenerateLinkRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	h.generateLinkCore(w, r, id, req)
}

func (h *WdttHandler) generateLinkCore(w http.ResponseWriter, r *http.Request, serverID string, req WdttGenerateLinkRequest) {
	srvCfg, err := h.svc.ServerConfigForLink(serverID)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_SERVER_NOT_FOUND")
		return
	}
	if strings.TrimSpace(srvCfg.Password) == "" {
		response.Error(w, "укажите пароль сервера перед генерацией ссылки", "WDTT_SERVER_NO_PASSWORD")
		return
	}

	peer := strings.TrimSpace(req.Peer)
	if peer != "" {
		if !strings.Contains(peer, ":") {
			port := srvCfg.Listen
			if idx := strings.LastIndex(port, ":"); idx != -1 {
				port = port[idx+1:]
			}
			peer = peer + ":" + port
		}
	} else {
		ip, ipErr := h.resolveExternalIP(r.Context())
		if ipErr != nil {
			response.Error(w, "Не удалось определить внешний IP: "+ipErr.Error()+". Укажите peer вручную.", "WDTT_EXTERNAL_IP_FAILED")
			return
		}
		port := srvCfg.Listen
		if idx := strings.LastIndex(port, ":"); idx != -1 {
			port = port[idx+1:]
		}
		peer = ip + ":" + port
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Router WDTT"
	}

	link, err := wdtt.EncodeLink(peer, srvCfg.WgPort, srvCfg.Password, req.VKHashes, name)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_LINK_ENCODE_FAILED")
		return
	}
	qLink, qErr := wdtt.EncodeQwdttLink(peer, srvCfg.Password, req.VKHashes, name, 0, 0)
	if qErr != nil {
		response.Error(w, qErr.Error(), "WDTT_LINK_ENCODE_FAILED")
		return
	}
	response.Success(w, map[string]string{
		"link":      link,
		"linkQwdtt": qLink,
		"peer":      peer,
	})
}
