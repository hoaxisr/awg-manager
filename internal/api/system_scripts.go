package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/response"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// Запуск скриптов и служб из файлового менеджера: статус процесса по пути
// и действия start/stop/restart/run.
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

	if (cleanDir == "/opt/etc/init.d" || strings.HasSuffix(cleanDir, "init.d")) && len(base) > 3 && (base[0] == 'S' || base[0] == 'K') && base[1] >= '0' && base[1] <= '9' {
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
