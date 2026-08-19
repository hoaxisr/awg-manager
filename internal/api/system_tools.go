package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
	sysfiles "github.com/hoaxisr/awg-manager/internal/sys/files"
	"github.com/hoaxisr/awg-manager/internal/sys/opkg"
	sysports "github.com/hoaxisr/awg-manager/internal/sys/ports"
	"github.com/hoaxisr/awg-manager/internal/sys/procmon"
	"github.com/hoaxisr/awg-manager/internal/sys/services"
)

// SystemToolsHandler exposes Entware file manager, init.d services, opkg, port inspector, and process monitor.
type SystemToolsHandler struct {
	settings *storage.SettingsStore
	log      *logging.ScopedLogger
	files    *sysfiles.Sandbox
	services *services.Scanner
	opkg     *opkg.Client
	ports    *sysports.Scanner
	procmon  *procmon.Sampler
	bus      *events.Bus
}

func (h *SystemToolsHandler) SetEventBus(bus *events.Bus) {
	h.bus = bus
}

func (h *SystemToolsHandler) emitEvent(action, subject, details string) {
	h.log.Info(action, subject, details)
	if h.bus != nil {
		h.bus.Publish("system:tool-action", map[string]string{
			"type":    "system_tool_action",
			"action":  action,
			"subject": subject,
			"details": details,
		})
	}
}

// NewSystemToolsHandler creates the handler.
func NewSystemToolsHandler(settings *storage.SettingsStore, log logging.AppLogger) *SystemToolsHandler {
	return &SystemToolsHandler{
		settings: settings,
		log:      logging.NewScopedLogger(log, "system", "tools"),
		files:    sysfiles.NewSandbox(nil),
		services: services.NewScanner(),
		opkg:     opkg.NewClient(),
		ports:    sysports.NewScanner(),
		procmon:  procmon.NewSampler(),
	}
}

// ExpertOnly оборачивает хендлер проверкой usage level: вкладка «Система»
// целиком expert-only, и гейт стоит один раз на регистрации маршрута, а не
// копией в начале каждого из 32 хендлеров.
func (h *SystemToolsHandler) ExpertOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.requireExpert(w, r) {
			return
		}
		next(w, r)
	}
}

func (h *SystemToolsHandler) requireExpert(w http.ResponseWriter, r *http.Request) bool {
	if h.settings == nil {
		response.InternalError(w, "settings unavailable")
		return false
	}
	st, err := h.settings.Get()
	if err != nil {
		response.InternalError(w, "settings unavailable")
		return false
	}
	if storage.NormalizeUsageLevel(st.UsageLevel) != storage.UsageLevelExpert {
		response.ErrorWithStatus(w, http.StatusForbidden, "expert usage level required", "FORBIDDEN")
		return false
	}
	return true
}

// GET /api/system/files/roots
// @Summary FilesRoots (Expert only)
// @Description FilesRoots (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemFileRootsResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/roots [get]
func (h *SystemToolsHandler) FilesRoots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	roots := h.files.Roots()
	out := make([]SystemFileRootDTO, 0, len(roots))
	for _, root := range roots {
		out = append(out, SystemFileRootDTO{Path: root.Path, Label: root.Label, ReadOnly: root.ReadOnly})
	}
	response.Success(w, out)
}

// GET /api/system/files/list?path=
// @Summary FilesList (Expert only)
// @Description FilesList (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemFilesListResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/list [get]
func (h *SystemToolsHandler) FilesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	path := r.URL.Query().Get("path")
	entries, abs, err := h.files.ListDir(path)
	if err != nil {
		h.filesError(w, err)
		return
	}
	response.Success(w, SystemFilesListData{Path: abs, Entries: entries})
}

// GET /api/system/files/read?path=
// @Summary FilesRead (Expert only)
// @Description FilesRead (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemFileReadResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/read [get]
func (h *SystemToolsHandler) FilesRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	path := r.URL.Query().Get("path")
	content, info, err := h.files.ReadFile(path)
	if err != nil {
		h.filesError(w, err)
		return
	}
	response.Success(w, SystemFileReadData{Path: info.Path, Content: content, Info: info})
}

type filesWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// POST /api/system/files/write
// @Summary FilesWrite (Expert only)
// @Description FilesWrite (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOKResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/write [post]
func (h *SystemToolsHandler) FilesWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req filesWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		response.Error(w, "path required", "INVALID_PATH")
		return
	}
	if err := h.files.WriteFile(req.Path, req.Content); err != nil {
		h.filesError(w, err)
		return
	}
	h.emitEvent("write", req.Path, fmt.Sprintf("write %d bytes", len(req.Content)))
	response.Success(w, nil)
}

type filesPathRequest struct {
	Path string `json:"path"`
}

// POST /api/system/files/mkdir
// @Summary FilesMkdir (Expert only)
// @Description FilesMkdir (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOKResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/mkdir [post]
func (h *SystemToolsHandler) FilesMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req filesPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if err := h.files.Mkdir(req.Path); err != nil {
		h.filesError(w, err)
		return
	}
	h.emitEvent("mkdir", req.Path, "mkdir")
	response.Success(w, nil)
}

// POST /api/system/files/remove
// @Summary FilesRemove (Expert only)
// @Description FilesRemove (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOKResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/remove [post]
func (h *SystemToolsHandler) FilesRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req filesPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if err := h.files.Remove(req.Path); err != nil {
		h.filesError(w, err)
		return
	}
	h.emitEvent("remove", req.Path, "remove")
	response.Success(w, nil)
}

type filesRenameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// POST /api/system/files/rename
// @Summary FilesRename (Expert only)
// @Description FilesRename (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOKResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/rename [post]
func (h *SystemToolsHandler) FilesRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req filesRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if err := h.files.Rename(req.From, req.To); err != nil {
		h.filesError(w, err)
		return
	}
	h.emitEvent("rename", req.From, fmt.Sprintf("rename -> %s", req.To))
	response.Success(w, nil)
}

type filesCopyRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// POST /api/system/files/copy
// @Summary FilesCopy (Expert only)
// @Description FilesCopy (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOKResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/copy [post]
func (h *SystemToolsHandler) FilesCopy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req filesCopyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if err := h.files.Copy(req.From, req.To); err != nil {
		h.filesError(w, err)
		return
	}
	h.emitEvent("copy", req.From, fmt.Sprintf("copy -> %s", req.To))
	response.Success(w, nil)
}

type filesChmodRequest struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// POST /api/system/files/chmod
// @Summary FilesChmod (Expert only)
// @Description FilesChmod (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOKResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/chmod [post]
func (h *SystemToolsHandler) FilesChmod(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req filesChmodRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if err := h.files.Chmod(req.Path, req.Mode); err != nil {
		h.filesError(w, err)
		return
	}
	h.emitEvent("chmod", req.Path, req.Mode)
	response.Success(w, nil)
}

// GET /api/system/files/checksum?path=&algo=md5|sha256
// @Summary FilesChecksum (Expert only)
// @Description FilesChecksum (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemFileChecksumResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/checksum [get]
func (h *SystemToolsHandler) FilesChecksum(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	path := r.URL.Query().Get("path")
	algo := r.URL.Query().Get("algo")
	sum, info, err := h.files.Checksum(path, algo)
	if err != nil {
		h.filesError(w, err)
		return
	}
	response.Success(w, SystemFileChecksumData{Path: info.Path, Checksum: sum, Algo: algo, Info: info})
}

// GET /api/system/files/download?path=
// @Summary FilesDownload (Expert only)
// @Description FilesDownload (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Param path query string false "Path"
// @Security CookieAuth
// @Success 200 {file} binary "содержимое файла"
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/download [get]
func (h *SystemToolsHandler) FilesDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	path := r.URL.Query().Get("path")
	f, fi, err := h.files.OpenDownload(path)
	if err != nil {
		h.filesError(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fi.Name()))
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	_, _ = io.Copy(w, f)
}

// POST /api/system/files/upload  multipart: file + path (target directory)
// @Summary FilesUpload (Expert only)
// @Description FilesUpload (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Param path query string false "Path"
// @Security CookieAuth
// @Success 200 {object} SystemUploadResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/upload [post]
func (h *SystemToolsHandler) FilesUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	const maxMem = 12 << 20 // 12 MB
	// Protect against memory exhaustion DoS
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxUploadBytes())+1<<20)

	if err := r.ParseMultipartForm(maxMem); err != nil {
		response.Error(w, "invalid multipart form", "INVALID_FORM")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	dir := strings.TrimSpace(r.FormValue("path"))
	file, header, err := r.FormFile("file")
	if err != nil {
		response.Error(w, "file required", "INVALID_FILE")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxUploadBytes())+1))
	if err != nil {
		response.Error(w, err.Error(), "READ_ERROR")
		return
	}
	if len(data) > maxUploadBytes() {
		response.Error(w, fmt.Sprintf("file too large (max %d bytes)", maxUploadBytes()), "FILE_TOO_LARGE")
		return
	}
	saved, err := h.files.SaveUpload(dir, filepath.Base(header.Filename), data)
	if err != nil {
		h.filesError(w, err)
		return
	}
	h.emitEvent("upload", saved, fmt.Sprintf("%d bytes", len(data)))
	response.Success(w, SystemUploadData{Path: saved})
}

func maxUploadBytes() int { return 10 * 1024 * 1024 }

func (h *SystemToolsHandler) filesError(w http.ResponseWriter, err error) {
	if errors.Is(err, sysfiles.ErrPathDenied) {
		response.ErrorWithStatus(w, http.StatusForbidden, err.Error(), "FORBIDDEN")
		return
	}
	if os.IsNotExist(err) {
		response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
		return
	}
	response.Error(w, err.Error(), "FILES_ERROR")
}

// GET /api/system/services/list
// @Summary ServicesList (Expert only)
// @Description ServicesList (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemServicesListResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/list [get]
func (h *SystemToolsHandler) ServicesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	items, err := h.services.List()
	if err != nil {
		response.Error(w, err.Error(), "SERVICES_ERROR")
		return
	}
	response.Success(w, items)
}

type serviceActionRequest struct {
	Script string `json:"script"`
	Action string `json:"action"`
}

// POST /api/system/services/action
// @Summary ServicesAction (Expert only)
// @Description ServicesAction (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemServiceActionResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/action [post]
func (h *SystemToolsHandler) ServicesAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req serviceActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	out, err := h.services.RunAction(req.Script, req.Action)
	h.emitEvent(req.Action, req.Script, out)
	if err != nil {
		response.Success(w, SystemServiceActionData{Output: out, OK: false, Error: err.Error()})
		return
	}
	response.Success(w, SystemServiceActionData{Output: out, OK: true})
}

// GET /api/system/services/get?script=/opt/etc/init.d/S90name
// @Summary ServicesGetScript (Expert only)
// @Description ServicesGetScript (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemServiceScriptResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/get [get]
func (h *SystemToolsHandler) ServicesGetScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	script := r.URL.Query().Get("script")
	if script == "" {
		response.Error(w, "missing script parameter", "INVALID_PARAMS")
		return
	}
	content, err := h.services.ReadScript(script)
	if err != nil {
		response.Error(w, err.Error(), "READ_ERROR")
		return
	}
	response.Success(w, SystemServiceScriptData{Script: script, Content: content})
}

type serviceSaveRequest struct {
	ScriptName string `json:"scriptName"` // e.g. "S90my-daemon"
	Content    string `json:"content"`    // shell script body
}

// POST /api/system/services/save
// @Summary ServicesSaveScript (Expert only)
// @Description ServicesSaveScript (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemServiceSavedResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/save [post]
func (h *SystemToolsHandler) ServicesSaveScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req serviceSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.ScriptName == "" {
		response.Error(w, "scriptName is required", "INVALID_PARAMS")
		return
	}
	if req.Content == "" {
		response.Error(w, "content cannot be empty", "INVALID_PARAMS")
		return
	}

	fullPath, err := h.services.SaveScript(req.ScriptName, req.Content)
	if err != nil {
		response.Error(w, err.Error(), "SAVE_ERROR")
		return
	}

	h.emitEvent("save", req.ScriptName, fullPath)
	response.Success(w, SystemServiceSavedData{OK: true, Script: fullPath})
}

type serviceDeleteRequest struct {
	Script string `json:"script"`
}

// POST /api/system/services/delete
// @Summary ServicesDeleteScript (Expert only)
// @Description ServicesDeleteScript (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOKFlagResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/delete [post]
func (h *SystemToolsHandler) ServicesDeleteScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req serviceDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.Script == "" {
		response.Error(w, "script is required", "INVALID_PARAMS")
		return
	}

	if err := h.services.DeleteScript(req.Script); err != nil {
		response.Error(w, err.Error(), "DELETE_ERROR")
		return
	}

	h.emitEvent("delete", req.Script, "service deleted")
	response.Success(w, SystemOKFlagData{OK: true})
}

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
	h.emitEvent("update", "", "opkg update")
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
	h.emitEvent("upgrade", strings.Join(req.Packages, ","), "opkg upgrade")
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
	h.emitEvent("install", strings.Join(req.Packages, ","), "opkg install")
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
	h.emitEvent("remove", strings.Join(req.Packages, ","), "opkg remove")
	if err != nil {
		response.Error(w, out+"\n"+err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, SystemOutputData{Output: out})
}

// GET /api/system/ports/list
// @Summary PortsList (Expert only)
// @Description PortsList (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemPortsListResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/ports/list [get]
func (h *SystemToolsHandler) PortsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	items, err := h.ports.List()
	if err != nil {
		response.Error(w, err.Error(), "PORTS_ERROR")
		return
	}
	response.Success(w, items)
}

// GET /api/system/ports/inspect?port=&proto=
// @Summary PortsInspect (Expert only)
// @Description PortsInspect (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemPortInspectResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/ports/inspect [get]
func (h *SystemToolsHandler) PortsInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	portStr := r.URL.Query().Get("port")
	if strings.TrimSpace(portStr) == "" {
		response.Error(w, "port parameter required", "INVALID_PORT")
		return
	}
	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil || port <= 0 || port > 65535 {
		response.Error(w, "invalid port number (1-65535)", "INVALID_PORT")
		return
	}
	proto := r.URL.Query().Get("proto")
	items, err := h.ports.InspectPort(port, proto)
	if err != nil {
		response.Error(w, err.Error(), "PORTS_ERROR")
		return
	}
	response.Success(w, SystemPortInspectData{Port: port, Proto: proto, Bindings: items, Occupied: len(items) > 0})
}

type portKillRequest struct {
	PID    int    `json:"pid"`
	Signal string `json:"signal,omitempty"` // "SIGTERM" or "SIGKILL"
	Port   int    `json:"port,omitempty"`
	Proto  string `json:"proto,omitempty"`
}

// POST /api/system/ports/kill
// @Summary PortsKill (Expert only)
// @Description PortsKill (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemKillResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/ports/kill [post]
func (h *SystemToolsHandler) PortsKill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req portKillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.PID <= 0 {
		response.Error(w, "valid PID required", "INVALID_PID")
		return
	}
	sig := req.Signal
	if strings.TrimSpace(sig) == "" {
		sig = "SIGTERM"
	}
	if err := h.ports.KillProcess(req.PID, sig); err != nil {
		h.log.Error("kill_process", fmt.Sprintf("PID %d (port %d)", req.PID, req.Port), err.Error())
		response.Error(w, err.Error(), "KILL_ERROR")
		return
	}
	h.emitEvent("kill_process", fmt.Sprintf("PID %d (signal %s, port %d)", req.PID, sig, req.Port), "process terminated")
	response.Success(w, SystemKillData{PID: req.PID, Signal: sig, OK: true})
}

// ScriptStatusDTO describes execution and runtime process status of a script/service.
type ScriptStatusDTO struct {
	Path        string `json:"path"`
	IsScript    bool   `json:"isScript"`
	Running     bool   `json:"running"`
	PIDs        []int  `json:"pids"`
	IsService   bool   `json:"isService"`
	ServiceName string `json:"serviceName,omitempty"`
	StatusText  string `json:"statusText,omitempty"`
	CanExecute  bool   `json:"canExecute"`
}

func isScriptOrService(path string, fi os.FileInfo) (isService bool, isScript bool, serviceName string) {
	base := filepath.Base(path)
	cleanDir := filepath.Clean(filepath.Dir(path))

	if (cleanDir == "/opt/etc/init.d" || strings.HasSuffix(cleanDir, "init.d")) && len(base) > 3 && base[0] == 'S' && base[1] >= '0' && base[1] <= '9' {
		return true, true, base[3:]
	}

	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, ".sh") || strings.HasSuffix(lower, ".bash") || strings.HasSuffix(lower, ".py") {
		return false, true, ""
	}

	if fi != nil && (fi.Mode()&0111 != 0) {
		return false, true, ""
	}

	return false, false, ""
}

func findPIDsForPath(targetPath string) []int {
	var pids []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return pids
	}
	cleanTarget := filepath.Clean(targetPath)

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}

		exeLink, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if exeLink == cleanTarget {
			pids = append(pids, pid)
			continue
		}

		cmdBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err == nil && len(cmdBytes) > 0 {
			cmdStr := string(cmdBytes)
			parts := strings.Split(cmdStr, "\x00")
			matched := false
			for _, part := range parts {
				if part == cleanTarget || filepath.Clean(part) == cleanTarget {
					matched = true
					break
				}
			}
			if matched {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

// GET /api/system/files/script-status?path=
// @Summary FilesScriptStatus (Expert only)
// @Description FilesScriptStatus (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemScriptStatusResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/script-status [get]
func (h *SystemToolsHandler) FilesScriptStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		response.Error(w, "path is required", "INVALID_PATH")
		return
	}

	abs, _, err := h.files.Resolve(path)
	if err != nil {
		response.Error(w, "access denied", "ACCESS_DENIED")
		return
	}

	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		response.Success(w, ScriptStatusDTO{Path: path, IsScript: false})
		return
	}

	isService, isScript, svcName := isScriptOrService(abs, fi)
	if !isScript && !isService {
		response.Success(w, ScriptStatusDTO{Path: path, IsScript: false})
		return
	}

	dto := ScriptStatusDTO{
		Path:        path,
		IsScript:    true,
		IsService:   isService,
		ServiceName: svcName,
		CanExecute:  true,
	}

	if isService {
		items, err := h.services.List()
		if err == nil {
			for _, item := range items {
				if item.Script == abs || item.Name == svcName {
					dto.Running = item.Running
					dto.StatusText = item.StatusText
					break
				}
			}
		}
	}

	pids := findPIDsForPath(abs)
	dto.PIDs = pids
	if len(pids) > 0 {
		dto.Running = true
		if dto.StatusText == "" {
			dto.StatusText = fmt.Sprintf("running (PID %v)", pids)
		}
	} else if !isService {
		dto.StatusText = "stopped"
	}

	response.Success(w, dto)
}

type scriptActionRequest struct {
	Path   string `json:"path"`
	Action string `json:"action"` // "start", "stop", "restart", "run"
	// Аргументы намеренно не принимаются: они уходили бы в exec от root.
}

// POST /api/system/files/script-action
// @Summary FilesScriptAction (Expert only)
// @Description FilesScriptAction (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemScriptActionResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/files/script-action [post]
func (h *SystemToolsHandler) FilesScriptAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req scriptActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		response.Error(w, "path is required", "INVALID_PATH")
		return
	}

	abs, _, err := h.files.Resolve(req.Path)
	if err != nil {
		response.Error(w, "access denied", "ACCESS_DENIED")
		return
	}

	fi, err := os.Stat(abs)
	if err != nil {
		response.Error(w, "file not found", "NOT_FOUND")
		return
	}

	isService, isScript, svcName := isScriptOrService(abs, fi)
	if !isScript && !isService {
		response.Error(w, "file is not an executable script or service", "NOT_SCRIPT")
		return
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "run"
	}

	var output string
	var runErr error

	if isService {
		output, runErr = h.services.RunAction(filepath.Base(abs), action)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		switch action {
		case "start", "run":
			var cmd *sysexec.Result
			if strings.HasSuffix(abs, ".sh") || strings.HasSuffix(abs, ".bash") {
				cmd, runErr = sysexec.Run(ctx, "/bin/sh", abs)
			} else {
				cmd, runErr = sysexec.Run(ctx, abs)
			}
			if cmd != nil {
				output = cmd.Stdout
				if output == "" {
					output = cmd.Stderr
				}
			}
		case "stop":
			pids := findPIDsForPath(abs)
			if len(pids) == 0 {
				output = "No running processes found"
			} else {
				for _, pid := range pids {
					_ = h.ports.KillProcess(pid, "SIGTERM")
				}
				output = fmt.Sprintf("Stopped PIDs: %v", pids)
			}
		case "restart":
			pids := findPIDsForPath(abs)
			for _, pid := range pids {
				_ = h.ports.KillProcess(pid, "SIGTERM")
			}
			time.Sleep(300 * time.Millisecond)
			var cmd *sysexec.Result
			if strings.HasSuffix(abs, ".sh") || strings.HasSuffix(abs, ".bash") {
				cmd, runErr = sysexec.Run(ctx, "/bin/sh", abs)
			} else {
				cmd, runErr = sysexec.Run(ctx, abs)
			}
			if cmd != nil {
				output = cmd.Stdout
				if output == "" {
					output = cmd.Stderr
				}
			}
		default:
			response.Error(w, "unsupported action", "INVALID_ACTION")
			return
		}
	}

	time.Sleep(200 * time.Millisecond)
	pids := findPIDsForPath(req.Path)
	running := len(pids) > 0
	if isService && !running {
		items, _ := h.services.List()
		for _, item := range items {
			if item.Script == req.Path || item.Name == svcName {
				running = item.Running
				break
			}
		}
	}

	h.emitEvent("script_action", fmt.Sprintf("%s (%s)", req.Path, action), output)

	errStr := ""
	if runErr != nil {
		errStr = runErr.Error()
	}

	response.Success(w, SystemScriptActionData{
		OK:      runErr == nil,
		Output:  strings.TrimSpace(output),
		Running: running,
		PIDs:    pids,
		Error:   errStr,
	})
}

// ProcSnapshot returns current CPU, RAM, and process top list.
// @Summary ProcSnapshot (Expert only)
// @Description ProcSnapshot (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemProcSnapshotResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/proc/snapshot [get]
func (h *SystemToolsHandler) ProcSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	snap, err := h.procmon.Snapshot()
	if err != nil {
		response.InternalError(w, fmt.Sprintf("proc snapshot failed: %v", err))
		return
	}

	response.Success(w, snap)
}

// ProcKill terminates a process by PID with signal.
// @Summary ProcKill (Expert only)
// @Description ProcKill (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemKillResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/proc/kill [post]
func (h *SystemToolsHandler) ProcKill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req struct {
		PID    int    `json:"pid"`
		Signal string `json:"signal"` // "SIGTERM" or "SIGKILL"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid JSON payload")
		return
	}
	if req.PID <= 1 {
		response.BadRequest(w, "invalid process PID")
		return
	}
	if req.Signal == "" {
		req.Signal = "SIGTERM"
	}

	if err := h.procmon.KillProcess(req.PID, req.Signal); err != nil {
		response.Error(w, err.Error(), "KILL_FAILED")
		return
	}

	h.emitEvent("proc_kill", strconv.Itoa(req.PID), fmt.Sprintf("Killed PID %d with %s", req.PID, req.Signal))
	response.Success(w, SystemKillData{PID: req.PID, Signal: req.Signal, OK: true})
}
