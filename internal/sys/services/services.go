package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const initDir = "/opt/etc/init.d"

// ErrManagedService — отказ трогать автозапуск службы, на которой держится
// сам AWG Manager. Обёрнут в ошибку, чтобы API отдал 403, а не 400.
var ErrManagedService = errors.New("cannot disable autostart for a managed service")

var (
	scriptNameRe = regexp.MustCompile(`^[SK][0-9]{2}[a-zA-Z0-9._-]+$`)
	csiRe        = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
)

// IsInitScriptName reports whether name follows the Entware init.d naming
// convention supported by the service tools: Sxx (autostart) and Kxx (no
// autostart) are both valid names of the same service.
func IsInitScriptName(name string) bool {
	return scriptNameRe.MatchString(name)
}

// Item describes one init.d service.
type Item struct {
	Name        string `json:"name"`
	Script      string `json:"script"`
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	StatusText  string `json:"statusText"`
	LogPath     string `json:"logPath,omitempty"`
	Managed     bool   `json:"managed"`
	ManagedHint string `json:"managedHint,omitempty"`
}

// Scanner lists Entware init.d scripts.
type Scanner struct {
	InitDir string
}

func NewScanner() *Scanner {
	return &Scanner{InitDir: initDir}
}

func (sc *Scanner) List() ([]Item, error) {
	dir := sc.InitDir
	if dir == "" {
		dir = initDir
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read init.d: %w", err)
	}
	items := make([]Item, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !IsInitScriptName(name) {
			continue
		}
		script := filepath.Join(dir, name)
		item := Item{
			Name:    ServiceName(name),
			Script:  script,
			Enabled: strings.HasPrefix(name, "S"),
			LogPath: guessLogPath(name),
		}
		if hint, ok := managedService(name); ok {
			item.Managed = true
			item.ManagedHint = hint
		}
		running, statusText := probeStatus(script)
		item.Running = running
		item.StatusText = statusText
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func ServiceName(script string) string {
	// ServiceName strips the Sxx/Kxx prefix:
	// S99awg-manager -> awg-manager, K99awg-manager -> awg-manager
	if len(script) > 3 && (script[0] == 'S' || script[0] == 'K') {
		return script[3:]
	}
	return script
}

func guessLogPath(script string) string {
	base := ServiceName(script)
	candidates := []string{
		filepath.Join("/opt/var/log", base+".log"),
		filepath.Join("/opt/var/log", base, base+".log"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func managedService(script string) (string, bool) {
	name := ServiceName(script)
	switch name {
	case "awg-manager":
		return "Управляется AWG Manager; удаление/остановка прерывает веб-интерфейс", true
	case "ttyd":
		return "Встроенный web-терминал вкладки «Система»; удаление отключит его", true
	case "sing-box":
		return "Основной прокси-движок awg-manager; удаление отключит VPN/прокси", true
	case "dropbear":
		return "SSH-сервер роутера; удаление сделает устройство недоступным по SSH", true
	default:
		return "", false
	}
}

func probeStatus(script string) (running bool, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := exec.Run(ctx, script, "status")
	text = strings.TrimSpace(result.Stdout)
	if text == "" {
		text = strings.TrimSpace(result.Stderr)
	}
	text = cleanStatusText(text)
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, "not running") || strings.Contains(lower, "stopped") {
			return false, text
		}
		if strings.Contains(lower, "running") || strings.Contains(lower, "started") {
			return true, text
		}
		return false, text
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "not running") || strings.Contains(lower, "stopped") {
		return false, text
	}
	return true, text
}

func cleanStatusText(text string) string {
	text = csiRe.ReplaceAllString(text, "")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 120 {
		text = text[:120] + "…"
	}
	return text
}

// RunAction executes start|stop|restart|status on a script.
func (sc *Scanner) RunAction(script, action string) (output string, err error) {
	action = strings.TrimSpace(strings.ToLower(action))
	switch action {
	case "start", "stop", "restart", "status":
	default:
		return "", fmt.Errorf("unsupported action: %s", action)
	}
	base := filepath.Base(script)
	if !IsInitScriptName(base) {
		return "", fmt.Errorf("invalid script name")
	}
	// Остановить службу, которая отдаёт эту же страницу, можно, а включить
	// обратно — уже нет. Перезапуск разрешён: панель вернётся сама.
	if action == "stop" && ServiceName(base) == "awg-manager" {
		return "", fmt.Errorf("cannot stop %s: остановка прервёт веб-интерфейс, включить обратно из UI будет нечем", base)
	}
	full := filepath.Join(sc.InitDir, base)
	if sc.InitDir == "" {
		full = filepath.Join(initDir, base)
	}
	if _, statErr := os.Stat(full); statErr != nil {
		return "", fmt.Errorf("script not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := exec.Run(ctx, full, action)
	out := strings.TrimSpace(result.Stdout)
	if out == "" {
		out = strings.TrimSpace(result.Stderr)
	}
	out = cleanStatusText(out)
	return out, err
}

// ToggleEnable switches service between enabled (Sxx) and disabled (Kxx).
func (sc *Scanner) ToggleEnable(script string, enable bool) (newScript string, err error) {
	base := filepath.Base(script)
	if !IsInitScriptName(base) {
		return "", fmt.Errorf("invalid script name")
	}
	if hint, managed := managedService(base); managed && !enable {
		return "", fmt.Errorf("%w: %s: %s", ErrManagedService, ServiceName(base), hint)
	}

	dir := sc.InitDir
	if dir == "" {
		dir = initDir
	}
	oldPath := filepath.Join(dir, base)
	if _, statErr := os.Stat(oldPath); statErr != nil {
		return "", fmt.Errorf("script not found: %w", statErr)
	}

	currentEnabled := strings.HasPrefix(base, "S")
	if currentEnabled == enable {
		return oldPath, nil
	}

	var newBase string
	if enable {
		newBase = "S" + base[1:]
	} else {
		newBase = "K" + base[1:]
	}

	newPath := filepath.Join(dir, newBase)

	if _, statErr := os.Lstat(newPath); statErr == nil {
		return "", fmt.Errorf("target script already exists: %s", newBase)
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect target script: %w", statErr)
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return "", fmt.Errorf("failed to rename script: %w", err)
	}

	return newPath, nil
}

// ReadScript returns the raw content of an init.d script.
func (sc *Scanner) ReadScript(script string) (string, error) {
	base := filepath.Base(script)
	if !IsInitScriptName(base) {
		return "", fmt.Errorf("invalid script name")
	}
	dir := sc.InitDir
	if dir == "" {
		dir = initDir
	}
	full := filepath.Join(dir, base)
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SaveScript writes or creates an init.d script with executable permissions (0755).
func (sc *Scanner) SaveScript(scriptName string, content string) (string, error) {
	base := filepath.Base(scriptName)
	if !IsInitScriptName(base) {
		return "", fmt.Errorf("invalid script name (must be S<number><name> or K<number><name>, e.g. S90myservice)")
	}
	// Перезапись — тот же результат, что и удаление: защита от удаления без
	// неё обходится одним сохранением пустого файла.
	if hint, managed := managedService(base); managed {
		return "", fmt.Errorf("cannot overwrite %s: %s", base, hint)
	}
	dir := sc.InitDir
	if dir == "" {
		dir = initDir
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	full := filepath.Join(dir, base)

	// Ensure unix line endings
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	if err := os.WriteFile(full, []byte(content), 0755); err != nil {
		return "", err
	}
	// Explicit chmod in case umask cleared execution bits
	_ = os.Chmod(full, 0755)

	return full, nil
}

// DeleteScript stops and removes an init.d script.
func (sc *Scanner) DeleteScript(script string) error {
	base := filepath.Base(script)
	if !IsInitScriptName(base) {
		return fmt.Errorf("invalid script name")
	}
	if hint, managed := managedService(base); managed {
		return fmt.Errorf("cannot delete %s: %s", base, hint)
	}
	dir := sc.InitDir
	if dir == "" {
		dir = initDir
	}
	full := filepath.Join(dir, base)

	// Try to stop service before deleting
	_, _ = sc.RunAction(full, "stop")

	return os.Remove(full)
}
