package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// Пакеты opkg: списки, поиск, установка, удаление и обновление.
// GET /api/system/opkg/installed
// @Summary OpkgInstalled (Expert only)
// @Description OpkgInstalled (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOpkgPackagesResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/opkg/installed [get]
func (h *SystemToolsHandler) OpkgInstalled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	pkgs, err := h.opkg.ListInstalled()
	if err != nil {
		response.Error(w, err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, pkgs)
}

// GET /api/system/opkg/upgradable
// @Summary OpkgUpgradable (Expert only)
// @Description OpkgUpgradable (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOpkgPackagesResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/opkg/upgradable [get]
func (h *SystemToolsHandler) OpkgUpgradable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	pkgs, err := h.opkg.ListUpgradable()
	if err != nil {
		response.Error(w, err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, pkgs)
}

// GET /api/system/opkg/search?q=
// @Summary OpkgSearch (Expert only)
// @Description OpkgSearch (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOpkgPackagesResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/opkg/search [get]
func (h *SystemToolsHandler) OpkgSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	q := r.URL.Query().Get("q")
	pkgs, err := h.opkg.Search(q)
	if err != nil {
		response.Error(w, err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, pkgs)
}

// opkgOutcome помечает исход в журнале: SSE-подписчиков у события нет,
// строка в app-логе — единственный след операции, и она не должна врать,
// будто пакет установлен, когда opkg упал.
func opkgOutcome(cmd string, err error) string {
	if err != nil {
		return cmd + ": ошибка"
	}
	return cmd
}

type opkgPackagesRequest struct {
	Packages []string `json:"packages"`
}

// POST /api/system/opkg/update
// @Summary OpkgUpdate (Expert only)
// @Description OpkgUpdate (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOutputResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/opkg/update [post]
func (h *SystemToolsHandler) OpkgUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	out, err := h.opkg.Update()
	h.emitEvent("update", "", opkgOutcome("opkg update", err))
	if err != nil {
		response.Error(w, out+"\n"+err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, SystemOutputData{Output: out})
}

// POST /api/system/opkg/upgrade
// @Summary OpkgUpgrade (Expert only)
// @Description OpkgUpgrade (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOutputResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/opkg/upgrade [post]
func (h *SystemToolsHandler) OpkgUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req opkgPackagesRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	var (
		out string
		err error
	)
	if len(req.Packages) == 0 {
		out, err = h.opkg.Upgrade()
	} else {
		out, err = h.opkg.UpgradePackages(req.Packages)
	}
	h.emitEvent("upgrade", strings.Join(req.Packages, ","), opkgOutcome("opkg upgrade", err))
	if err != nil {
		response.Error(w, out+"\n"+err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, SystemOutputData{Output: out})
}

// POST /api/system/opkg/install
// @Summary OpkgInstall (Expert only)
// @Description OpkgInstall (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOutputResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/opkg/install [post]
func (h *SystemToolsHandler) OpkgInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req opkgPackagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	out, err := h.opkg.Install(req.Packages)
	h.emitEvent("install", strings.Join(req.Packages, ","), opkgOutcome("opkg install", err))
	if err != nil {
		response.Error(w, out+"\n"+err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, SystemOutputData{Output: out})
}

// GET /api/system/opkg/available?q=&offset=&limit=
// @Summary OpkgAvailable (Expert only)
// @Description OpkgAvailable (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOpkgAvailableResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/opkg/available [get]
func (h *SystemToolsHandler) OpkgAvailable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	q := r.URL.Query().Get("q")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, total, err := h.opkg.ListAvailable(q, offset, limit)
	if err != nil {
		response.Error(w, err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, SystemOpkgAvailableData{Items: items, Total: total, Offset: offset, Limit: limit})
}

// POST /api/system/opkg/remove
// @Summary OpkgRemove (Expert only)
// @Description OpkgRemove (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOutputResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/opkg/remove [post]
func (h *SystemToolsHandler) OpkgRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req opkgPackagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	out, err := h.opkg.Remove(req.Packages)
	h.emitEvent("remove", strings.Join(req.Packages, ","), opkgOutcome("opkg remove", err))
	if err != nil {
		response.Error(w, out+"\n"+err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, SystemOutputData{Output: out})
}
