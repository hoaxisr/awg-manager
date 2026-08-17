package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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
//
//	@Summary	Update the default WDTT server config (legacy single-instance route)
//	@Tags		wdtt
//	@Accept		json
//	@Param		request	body		wdtt.ServerConfig	true	"Server config"
//	@Success	200		{object}	APIEnvelope
//	@Failure	400		{object}	APIErrorEnvelope
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/wdtt/server/config [put]
//	@Router		/wdtt/server/config [post]
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
//
//	@Summary	Start the default WDTT server (legacy single-instance route)
//	@Tags		wdtt
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/server/start [post]
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
//
//	@Summary	Stop the default WDTT server (legacy single-instance route)
//	@Tags		wdtt
//	@Success	200	{object}	APIEnvelope
//	@Failure	500	{object}	APIErrorEnvelope
//	@Router		/wdtt/server/stop [post]
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
	Password string   `json:"password,omitempty"` // server client password from passwords.json; required — the server main password is never put into a link
}

// CreateServer handles POST /api/wdtt/servers.
//
//	@Summary	Create a WDTT server instance
//	@Description	wdtt-server owns the shared wdtt0 interface, so a second instance is rejected.
//	@Tags		wdtt
//	@Accept		json
//	@Param		request	body		wdtt.CreateServerInput	false	"Server instance"
//	@Success	200		{object}	APIEnvelope
//	@Failure	400		{object}	APIErrorEnvelope
//	@Failure	500		{object}	APIErrorEnvelope
//	@Router		/wdtt/servers [post]
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
	case len(sub) == 1 && sub[0] == "nat":
		h.setServerNATMode(w, r, id)
	case len(sub) == 1 && sub[0] == "policy":
		h.setServerPolicy(w, r, id)
	case len(sub) == 1 && sub[0] == "lan-segments":
		h.setServerLANSegments(w, r, id)
	case len(sub) >= 1 && sub[0] == "users":
		h.serveServerClients(w, r, id, sub[1:])
	default:
		response.ErrorWithStatus(w, http.StatusNotFound, "Not found", "NOT_FOUND")
	}
}

type wdttServerClientAddRequest struct {
	Password     string `json:"password,omitempty"`
	Comment      string `json:"comment,omitempty"`
	VkHash       string `json:"vkHash,omitempty"`
	MainPassword string `json:"mainPassword,omitempty"`
}

// serveServerClients handles GET/POST/DELETE /api/wdtt/servers/{id}/users[/{password}].
//
//	@Summary	List, add or delete WDTT server client passwords stored in passwords.json
//	@Tags		wdtt
//	@Accept		json
//	@Param		id			path		string						true	"Server instance id"
//	@Param		password	path		string						false	"Client password to delete"
//	@Param		request		body		wdttServerClientAddRequest	false	"New client password"
//	@Success	200			{object}	APIEnvelope
//	@Failure	400			{object}	APIErrorEnvelope
//	@Failure	500			{object}	APIErrorEnvelope
//	@Router		/wdtt/servers/{id}/users [get]
//	@Router		/wdtt/servers/{id}/users [post]
//	@Router		/wdtt/servers/{id}/users/{password} [delete]
func (h *WdttHandler) serveServerClients(w http.ResponseWriter, r *http.Request, serverID string, sub []string) {
	switch {
	case len(sub) == 0:
		switch r.Method {
		case http.MethodGet:
			st, err := h.svc.ListServerClients(serverID)
			if err != nil {
				response.Error(w, err.Error(), "WDTT_SERVER_CLIENTS_FAILED")
				return
			}
			response.Success(w, st)
		case http.MethodPost:
			var req wdttServerClientAddRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				response.Error(w, "invalid request body", "BAD_REQUEST")
				return
			}
			st, err := h.svc.AddServerClient(serverID, req.Password, req.Comment, req.VkHash, req.MainPassword)
			if err != nil {
				response.Error(w, err.Error(), "WDTT_SERVER_CLIENT_ADD_FAILED")
				return
			}
			response.Success(w, st)
		default:
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		}
	case len(sub) == 1:
		password := sub[0]
		if r.Method != http.MethodDelete {
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
			return
		}
		st, err := h.svc.RemoveServerClient(serverID, password)
		if err != nil {
			response.Error(w, err.Error(), "WDTT_SERVER_CLIENT_DELETE_FAILED")
			return
		}
		response.Success(w, st)
	default:
		response.ErrorWithStatus(w, http.StatusNotFound, "Not found", "NOT_FOUND")
	}
}

// @Summary	Update config, rename or delete a WDTT server instance
// @Tags		wdtt
// @Accept		json
// @Param		id		path		string	true	"Server instance id"
// @Success	200		{object}	APIEnvelope
// @Failure	400		{object}	APIErrorEnvelope
// @Failure	500		{object}	APIErrorEnvelope
// @Router		/wdtt/servers/{id} [put]
// @Router		/wdtt/servers/{id} [patch]
// @Router		/wdtt/servers/{id} [delete]
func (h *WdttHandler) serveServerByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		var cfg wdtt.ServerConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			response.Error(w, "invalid request body", "BAD_REQUEST")
			return
		}
		saved, err := h.svc.UpdateServerInstance(id, cfg)
		if err != nil {
			response.Error(w, err.Error(), "WDTT_SERVER_UPDATE_FAILED")
			return
		}
		response.Success(w, map[string]any{"config": saved})
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

// @Summary	Start a WDTT server instance
// @Tags		wdtt
// @Param		id	path		string	true	"Server instance id"
// @Success	200	{object}	APIEnvelope
// @Failure	500	{object}	APIErrorEnvelope
// @Router		/wdtt/servers/{id}/start [post]
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

// @Summary	Stop a WDTT server instance
// @Tags		wdtt
// @Param		id	path		string	true	"Server instance id"
// @Success	200	{object}	APIEnvelope
// @Failure	500	{object}	APIErrorEnvelope
// @Router		/wdtt/servers/{id}/stop [post]
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

// @Summary	Generate a wdtt:// / qwdtt:// share link for a server instance
// @Tags		wdtt
// @Accept		json
// @Param		id		path		string					true	"Server instance id"
// @Param		request	body		WdttGenerateLinkRequest	false	"Link options"
// @Success	200		{object}	APIEnvelope
// @Failure	400		{object}	APIErrorEnvelope
// @Failure	500		{object}	APIErrorEnvelope
// @Router		/wdtt/servers/{id}/link [post]
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

// linkPasswordFor выбирает пароль ссылки. Главный пароль сервера — ключ
// администрирования (X-Admin-Password у admin-API форка), в ссылку он не
// попадает ни при каких условиях, поэтому пароль обязан принадлежать списку
// абонентов сервера.
//
// Членство считается по wdtt.UsableServerClients — ровно по тому предикату, по
// которому абоненты уезжают в passwords.json и по которому сервер собирает
// wrap-ключи. Проверка по всему cfg.Clients была бы мягче: ссылка на
// просроченного абонента собралась бы без единой жалобы и молча не
// подключилась. Своей копии правила здесь быть не имеет права — предикат один
// на всех потребителей.
func linkPasswordFor(req WdttGenerateLinkRequest, cfg wdtt.ServerConfig) (string, error) {
	usable := wdtt.UsableServerClients(cfg.Clients, cfg.Password, time.Now())
	if len(usable) == 0 {
		return "", errors.New("у сервера нет ни одного рабочего абонента: заведите абонента и повторите")
	}

	pass := strings.TrimSpace(req.Password)
	if pass == "" {
		return "", errors.New("выберите абонента: ссылка выдаётся на пароль абонента, а не на главный пароль сервера")
	}
	for _, c := range usable {
		// Пароль из UsableServerClients уже подрезан — трим тут не нужен.
		if c.Password == pass {
			return pass, nil
		}
	}

	// Пароль не рабочий. Причину спрашиваем у классификатора wdtt — того же, на
	// котором построен предикат. Выводить её исключением («не пустой, не
	// главный, значит просрочен») нельзя: вычитание исчерпывающе только для
	// сегодняшнего набора условий, а четвёртое дало бы уверенный ложный текст.
	known := false
	target := wdtt.ServerClient{Password: pass}
	for _, c := range cfg.Clients {
		if strings.TrimSpace(c.Password) == pass {
			target, known = c, true
			break
		}
	}
	return "", errors.New(linkRejectMessage(wdtt.ServerClientUnusableReason(target, cfg.Password, time.Now()), known))
}

// linkRejectMessage переводит причину непригодности в текст отказа.
// «Пароля нет в списке» проверяется ПОСЛЕ главного пароля: главный в списке
// абонентов не лежит, а сказать про него надо именно про него.
func linkRejectMessage(reason wdtt.ServerClientReason, knownClient bool) string {
	switch {
	case reason == wdtt.ServerClientMainPassword:
		return "это главный пароль сервера: он остаётся ключом администрирования, ссылка выдаётся на пароль абонента"
	case !knownClient:
		return "пароль не принадлежит ни одному абоненту сервера"
	case reason == wdtt.ServerClientExpired:
		return "абонент просрочен, ссылка не будет работать: заведите нового абонента"
	default:
		// Причина, которой у текстов ещё нет: новое условие пригодности в
		// wdtt. Общий отказ честнее уверенного «просрочен».
		return "абонент непригоден для ссылки: заведите нового абонента"
	}
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

	linkPassword, err := linkPasswordFor(req, srvCfg)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_LINK_NO_CLIENT")
		return
	}

	peer := strings.TrimSpace(req.Peer)
	linkPort := srvCfg.LinkListenPort()
	if peer != "" {
		if !strings.Contains(peer, ":") {
			peer = peer + ":" + strconv.Itoa(linkPort)
		}
	} else {
		ip, ipErr := h.resolveExternalIP(r.Context())
		if ipErr != nil {
			response.Error(w, "Не удалось определить внешний IP: "+ipErr.Error()+". Укажите peer вручную.", "WDTT_EXTERNAL_IP_FAILED")
			return
		}
		peer = ip + ":" + strconv.Itoa(linkPort)
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Router WDTT"
	}

	link, err := wdtt.EncodeLink(peer, srvCfg.WgPort, linkPassword, req.VKHashes, name)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_LINK_ENCODE_FAILED")
		return
	}
	qLink, qErr := wdtt.EncodeQwdttLink(peer, linkPassword, req.VKHashes, name, 0, 0, srvCfg.RelayMode)
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

type wdttSetNATModeRequest struct {
	Mode string `json:"mode"`
}

type wdttSetPolicyRequest struct {
	Policy string `json:"policy"`
}

type wdttSetLANSegmentsRequest struct {
	Segments []string `json:"segments"`
}

// @Summary	Set WDTT server NAT mode
// @Tags		wdtt
// @Accept		json
// @Param		id		path		string					true	"Server instance id"
// @Param		body	body		wdttSetNATModeRequest	true	"NAT mode"
// @Success	200		{object}	APIEnvelope
// @Router		/wdtt/servers/{id}/nat [post]
func (h *WdttHandler) setServerNATMode(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req wdttSetNATModeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	saved, err := h.svc.SetServerNATMode(r.Context(), id, req.Mode)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_SERVER_NAT_FAILED")
		return
	}
	response.Success(w, map[string]any{"config": saved})
}

// @Summary	Set WDTT server IP policy
// @Tags		wdtt
// @Accept		json
// @Param		id		path		string				true	"Server instance id"
// @Param		body	body		wdttSetPolicyRequest	true	"Policy name or none"
// @Success	200		{object}	APIEnvelope
// @Router		/wdtt/servers/{id}/policy [post]
func (h *WdttHandler) setServerPolicy(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req wdttSetPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	saved, err := h.svc.SetServerPolicy(r.Context(), id, req.Policy)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_SERVER_POLICY_FAILED")
		return
	}
	response.Success(w, map[string]any{"config": saved})
}

// @Summary	Set WDTT server LAN segments
// @Tags		wdtt
// @Accept		json
// @Param		id		path		string						true	"Server instance id"
// @Param		body	body		wdttSetLANSegmentsRequest	true	"LAN bridge names"
// @Success	200		{object}	APIEnvelope
// @Router		/wdtt/servers/{id}/lan-segments [post]
func (h *WdttHandler) setServerLANSegments(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req wdttSetLANSegmentsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	saved, err := h.svc.SetServerLANSegments(r.Context(), id, req.Segments)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_SERVER_LAN_FAILED")
		return
	}
	response.Success(w, map[string]any{"config": saved})
}
