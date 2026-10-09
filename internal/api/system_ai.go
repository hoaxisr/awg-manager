package api

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/ports"
	"github.com/hoaxisr/awg-manager/internal/sys/services"
)

const assistantFileReadLimit = 32 * 1024

var (
	assistantSensitiveLine = regexp.MustCompile(`(?i)(password|passwd|passphrase|secret|token|api[_-]?key|private[_-]?key|pre[_-]?shared[_-]?key|psk|authorization)\s*[:=]`)
	assistantURLUserInfo   = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
	assistantBearer        = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]+=*`)
)

// AssistantSystemSnapshot returns a reduced snapshot without process command
// lines, which may contain credentials. It uses the same sampler as the UI.
func (h *SystemToolsHandler) AssistantSystemSnapshot() (any, error) {
	snapshot, err := h.procmon.Snapshot()
	if err != nil {
		return nil, err
	}
	processes := make([]map[string]any, 0, len(snapshot.Processes))
	for _, process := range snapshot.Processes {
		processes = append(processes, map[string]any{
			"pid": process.PID, "ppid": process.PPID, "user": process.User,
			"state": process.State, "cpuPercent": process.CPUPercent,
			"memoryRss": process.MemoryRSS, "memoryPercent": process.MemoryPercent,
			"name": process.Name, "service": process.Service,
			"critical": process.IsCritical,
		})
	}
	return map[string]any{
		"timestamp": snapshot.Timestamp, "uptimeSeconds": snapshot.UptimeSeconds,
		"loadAvg": snapshot.LoadAvg, "cpuModel": snapshot.CPUModel,
		"architecture": snapshot.CPUArchitecture, "cpuCount": snapshot.CPUCount,
		"cores": snapshot.Cores, "memory": snapshot.Memory,
		"processSummary": snapshot.ProcessSummary, "processes": processes,
	}, nil
}

func (h *SystemToolsHandler) AssistantServices() ([]services.Item, error) {
	items, err := h.services.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		// The action target is deliberately a basename. It is sufficient for
		// Scanner.RunAction and cannot be repurposed as an arbitrary path.
		items[i].Script = filepath.Base(items[i].Script)
	}
	return items, nil
}

func (h *SystemToolsHandler) AssistantPorts() ([]ports.Binding, error) {
	items, err := h.ports.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Cmdline = ""
	}
	return items, nil
}

func (h *SystemToolsHandler) AssistantPackages(kind, query string) (any, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "installed":
		return h.opkg.ListInstalled()
	case "upgradable":
		return h.opkg.ListUpgradable()
	case "search":
		return h.opkg.Search(query)
	default:
		return nil, fmt.Errorf("package query kind must be installed, upgradable or search")
	}
}

// AssistantFilesList exposes the same sandboxed Entware roots as the file
// manager. The result is capped because it is sent to a model as tool output.
func (h *SystemToolsHandler) AssistantFilesList(path string) (any, error) {
	items, resolved, err := h.files.ListDir(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	const maxEntries = 200
	truncated := len(items) > maxEntries
	if truncated {
		items = items[:maxEntries]
	}
	return map[string]any{
		"path": resolved, "entries": items, "truncated": truncated,
	}, nil
}

// AssistantFileRead reads text only from the file-manager sandbox and masks
// credential-bearing lines before any content can be sent to a cloud model.
func (h *SystemToolsHandler) AssistantFileRead(path string) (any, error) {
	content, info, err := h.files.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	truncated := len(content) > assistantFileReadLimit
	if truncated {
		content = content[:assistantFileReadLimit]
	}
	return map[string]any{
		"file": info, "content": redactAssistantFile(content), "truncated": truncated,
	}, nil
}

func redactAssistantFile(content string) string {
	lines := strings.Split(content, "\n")
	inPrivateBlock := false
	for i, line := range lines {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "-----BEGIN ") && strings.Contains(upper, "PRIVATE KEY-----") {
			inPrivateBlock = true
			lines[i] = "[REDACTED PRIVATE KEY BLOCK]"
			continue
		}
		if inPrivateBlock {
			lines[i] = ""
			if strings.Contains(upper, "-----END ") && strings.Contains(upper, "PRIVATE KEY-----") {
				inPrivateBlock = false
			}
			continue
		}
		if assistantSensitiveLine.MatchString(line) {
			lines[i] = "[REDACTED SENSITIVE VALUE]"
			continue
		}
		line = assistantURLUserInfo.ReplaceAllString(line, `${1}[REDACTED]@`)
		lines[i] = assistantBearer.ReplaceAllString(line, "Bearer [REDACTED]")
	}
	return strings.Join(lines, "\n")
}

func (h *SystemToolsHandler) AssistantServiceAction(script, action string) (string, error) {
	name := services.ServiceName(script)
	if action == "stop" && (name == "awg-manager" || name == "dropbear" || name == "ttyd") {
		return "", fmt.Errorf("AI assistant cannot stop access-critical service %s", name)
	}
	return h.services.RunAction(script, action)
}

func (h *SystemToolsHandler) AssistantOpkgAction(action, packageName string) (string, error) {
	if action == "remove" {
		switch packageName {
		case "awg-manager", "dropbear", "busybox", "entware-release", "entware-opt":
			return "", fmt.Errorf("AI assistant cannot remove access-critical package %s", packageName)
		}
	}
	switch action {
	case "update":
		return h.opkg.Update()
	case "install":
		return h.opkg.Install([]string{packageName})
	case "upgrade":
		return h.opkg.UpgradePackages([]string{packageName})
	case "remove":
		return h.opkg.Remove([]string{packageName})
	default:
		return "", fmt.Errorf("unsupported opkg action %q", action)
	}
}

func (h *SystemToolsHandler) AssistantPackageInstalled(name string) (bool, error) {
	items, err := h.opkg.ListInstalled()
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Name == name {
			return true, nil
		}
	}
	return false, nil
}
