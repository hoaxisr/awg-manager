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

	"github.com/hoaxisr/awg-manager/internal/response"
	sysfiles "github.com/hoaxisr/awg-manager/internal/sys/files"
)

// Файловый менеджер: список, чтение и запись, права, скачивание и загрузка
// внутри разрешённых корней песочницы.
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
