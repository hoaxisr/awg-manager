package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	sysfiles "github.com/hoaxisr/awg-manager/internal/sys/files"
	"github.com/hoaxisr/awg-manager/internal/sys/opkg"
	"github.com/hoaxisr/awg-manager/internal/sys/services"
)

// SystemToolsHandler exposes Entware file manager, init.d services and opkg.
type SystemToolsHandler struct {
	settings *storage.SettingsStore
	log      *logging.ScopedLogger
	files    *sysfiles.Sandbox
	services *services.Scanner
	opkg     *opkg.Client
}

// NewSystemToolsHandler creates the handler.
func NewSystemToolsHandler(settings *storage.SettingsStore, log logging.AppLogger) *SystemToolsHandler {
	return &SystemToolsHandler{
		settings: settings,
		log:      logging.NewScopedLogger(log, "system", "tools"),
		files:    sysfiles.NewSandbox(nil),
		services: services.NewScanner(),
		opkg:     opkg.NewClient(),
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
func (h *SystemToolsHandler) FilesRoots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
		return
	}
	type rootDTO struct {
		Path     string `json:"path"`
		Label    string `json:"label"`
		ReadOnly bool   `json:"readOnly"`
	}
	roots := h.files.Roots()
	out := make([]rootDTO, 0, len(roots))
	for _, root := range roots {
		out = append(out, rootDTO{Path: root.Path, Label: root.Label, ReadOnly: root.ReadOnly})
	}
	response.Success(w, out)
}

// GET /api/system/files/list?path=
func (h *SystemToolsHandler) FilesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
		return
	}
	path := r.URL.Query().Get("path")
	entries, abs, err := h.files.ListDir(path)
	if err != nil {
		h.filesError(w, err)
		return
	}
	response.Success(w, map[string]interface{}{
		"path":    abs,
		"entries": entries,
	})
}

// GET /api/system/files/read?path=
func (h *SystemToolsHandler) FilesRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
		return
	}
	path := r.URL.Query().Get("path")
	content, info, err := h.files.ReadFile(path)
	if err != nil {
		h.filesError(w, err)
		return
	}
	response.Success(w, map[string]interface{}{
		"path":    info.Path,
		"content": content,
		"info":    info,
	})
}

type filesWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// POST /api/system/files/write
func (h *SystemToolsHandler) FilesWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
	h.log.Info("write", req.Path, fmt.Sprintf("write %d bytes", len(req.Content)))
	response.Success(w, nil)
}

type filesPathRequest struct {
	Path string `json:"path"`
}

// POST /api/system/files/mkdir
func (h *SystemToolsHandler) FilesMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
	h.log.Info("mkdir", req.Path, "mkdir")
	response.Success(w, nil)
}

// POST /api/system/files/remove
func (h *SystemToolsHandler) FilesRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
	h.log.Info("remove", req.Path, "remove")
	response.Success(w, nil)
}

type filesRenameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// POST /api/system/files/rename
func (h *SystemToolsHandler) FilesRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
	h.log.Info("rename", req.From, fmt.Sprintf("rename -> %s", req.To))
	response.Success(w, nil)
}

// GET /api/system/files/download?path=
func (h *SystemToolsHandler) FilesDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
func (h *SystemToolsHandler) FilesUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
		return
	}
	const maxMem = 12 << 20
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
	h.log.Info("upload", saved, fmt.Sprintf("%d bytes", len(data)))
	response.Success(w, map[string]string{"path": saved})
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
func (h *SystemToolsHandler) ServicesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
func (h *SystemToolsHandler) ServicesAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
		return
	}
	var req serviceActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	out, err := h.services.RunAction(req.Script, req.Action)
	h.log.Info(req.Action, req.Script, out)
	if err != nil {
		response.Success(w, map[string]interface{}{
			"output": out,
			"ok":     false,
			"error":  err.Error(),
		})
		return
	}
	response.Success(w, map[string]interface{}{
		"output": out,
		"ok":     true,
	})
}

// GET /api/system/opkg/installed
func (h *SystemToolsHandler) OpkgInstalled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
func (h *SystemToolsHandler) OpkgUpgradable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
func (h *SystemToolsHandler) OpkgSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
func (h *SystemToolsHandler) OpkgUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
		return
	}
	out, err := h.opkg.Update()
	h.log.Info("update", "", "opkg update")
	if err != nil {
		response.Error(w, out+"\n"+err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, map[string]string{"output": out})
}

// POST /api/system/opkg/upgrade
func (h *SystemToolsHandler) OpkgUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
	h.log.Info("upgrade", strings.Join(req.Packages, ","), "opkg upgrade")
	if err != nil {
		response.Error(w, out+"\n"+err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, map[string]string{"output": out})
}

// POST /api/system/opkg/install
func (h *SystemToolsHandler) OpkgInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
		return
	}
	var req opkgPackagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	out, err := h.opkg.Install(req.Packages)
	h.log.Info("install", strings.Join(req.Packages, ","), "opkg install")
	if err != nil {
		response.Error(w, out+"\n"+err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, map[string]string{"output": out})
}

// GET /api/system/opkg/available?q=&offset=&limit=
func (h *SystemToolsHandler) OpkgAvailable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
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
	response.Success(w, map[string]interface{}{
		"items":  items,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

// POST /api/system/opkg/remove
func (h *SystemToolsHandler) OpkgRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.requireExpert(w, r) {
		return
	}
	var req opkgPackagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	out, err := h.opkg.Remove(req.Packages)
	h.log.Info("remove", strings.Join(req.Packages, ","), "opkg remove")
	if err != nil {
		response.Error(w, out+"\n"+err.Error(), "OPKG_ERROR")
		return
	}
	response.Success(w, map[string]string{"output": out})
}
