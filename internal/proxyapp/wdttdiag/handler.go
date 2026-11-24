package wdttdiag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/response"
)

const (
	defaultDiagTimeout = 45 * time.Second
	maxRequestBody     = 16 << 10
	maxTailChars       = 400
)

// RecordSource — источник записей инстансов proxyrt.
type RecordSource interface {
	Get(key string) (instancestore.Record, bool)
}

// Runner запускает разовую проверку `wt-client -diag`.
type Runner func(ctx context.Context, binary string, args []string) (stdout string, stderr string, exitCode int, err error)

// Deps — зависимости обработчика диагностики wdtt-клиента.
type Deps struct {
	Records RecordSource
	Binary  func(instancestore.Kind) (string, bool)
	Runner  Runner
	Timeout time.Duration
}

// Handler обслуживает POST /api/proxyrt/instances/{key}/diag.
type Handler struct {
	deps    Deps
	mu      sync.Mutex
	running bool
}

// New создаёт обработчик диагностики wdtt-клиента.
func New(deps Deps) *Handler {
	if deps.Timeout <= 0 {
		deps.Timeout = defaultDiagTimeout
	}
	return &Handler{
		deps: deps,
	}
}

// Serve запускает разовую проверку `wt-client -diag` для wdtt-клиента.
//
// Вердикт с severity=="error" приводит к коду возврата 2 у процесса, но сама
// диагностика состоялась — HTTP-ответ остаётся 200 с заполненным Verdict.
//
//	@Summary		Диагностика подключения wdtt-клиента
//	@Description	Одноразовый прогон `wt-client -diag` через TURN VK без запуска туннеля.
//	@Description	409 WDTT_DIAG_UNSUPPORTED — установлен старый `wt-client` без флага `-diag`.
//	@Description	409 WDTT_DIAG_BUSY — диагностика уже запущена.
//	@Description	504 WDTT_DIAG_TIMEOUT — превышен лимит времени ожидания ответа.
//	@Tags			proxyrt
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			key		path		string	true	"Ключ инстанса (роль:id)"
//	@Param			body	body		Request	false	"Опции диагностики (транспорт, одноразовый ключ)"
//	@Success		200		{object}	DiagResponse
//	@Failure		400		{object}	api.APIErrorEnvelope
//	@Failure		404		{object}	api.APIErrorEnvelope
//	@Failure		409		{object}	api.APIErrorEnvelope
//	@Failure		502		{object}	api.APIErrorEnvelope
//	@Failure		504		{object}	api.APIErrorEnvelope
//	@Router			/proxyrt/instances/{key}/diag [post]
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	req, err := decodeRequest(r)
	if err != nil {
		response.Error(w, err.Error(), "BAD_REQUEST")
		return
	}

	if h.deps.Records == nil {
		response.InternalError(w, "источник записей не подключён")
		return
	}
	rec, ok := h.deps.Records.Get(key)
	if !ok {
		response.ErrorWithStatus(w, http.StatusNotFound, fmt.Sprintf("инстанс %s не найден", key), "NOT_FOUND")
		return
	}
	cfg, err := rec.WdttClientConfig()
	if err != nil {
		response.Error(w, err.Error(), "BAD_REQUEST")
		return
	}
	if strings.TrimSpace(cfg.Peer) == "" || strings.TrimSpace(cfg.Password) == "" || strings.TrimSpace(cfg.VKHashes) == "" {
		response.Error(w, "конфиг клиента неполный: для диагностики нужны сервер (peer), пароль и VK-хеши", "WDTT_DIAG_CONFIG_INCOMPLETE")
		return
	}

	binary := ""
	if h.deps.Binary != nil {
		if b, found := h.deps.Binary(instancestore.KindWdttClient); found {
			binary = strings.TrimSpace(b)
		}
	}
	if binary == "" {
		response.ErrorWithStatus(w, http.StatusConflict, "бинарник wt-client не найден — установите пакет WDTT", "WDTT_DIAG_NO_BINARY")
		return
	}
	runner := h.deps.Runner
	if runner == nil {
		if info, statErr := os.Stat(binary); statErr != nil || info.IsDir() {
			response.ErrorWithStatus(w, http.StatusConflict, "бинарник wt-client отсутствует на диске — установите пакет WDTT", "WDTT_DIAG_NO_BINARY")
			return
		}
		runner = execRunner
	}

	if !h.acquire() {
		response.ErrorWithStatus(w, http.StatusConflict, "диагностика уже запущена", "WDTT_DIAG_BUSY")
		return
	}
	defer h.release()

	args, effectiveKey := buildDiagArgs(cfg, req)

	ctx, cancel := context.WithTimeout(r.Context(), h.deps.Timeout)
	defer cancel()

	start := time.Now()
	stdout, stderr, exitCode, runErr := runner(ctx, binary, args)
	durationMs := time.Since(start).Milliseconds()

	mode, steps, verdict := parseDiagOutput(stdout)
	for i := range steps {
		steps[i].Title = redactSecrets(steps[i].Title, cfg.Password, effectiveKey, req.DiagKey)
		steps[i].Text = redactSecrets(steps[i].Text, cfg.Password, effectiveKey, req.DiagKey)
	}
	if verdict != nil {
		verdict.Title = redactSecrets(verdict.Title, cfg.Password, effectiveKey, req.DiagKey)
		verdict.Text = redactSecrets(verdict.Text, cfg.Password, effectiveKey, req.DiagKey)
		for i := range verdict.Actions {
			verdict.Actions[i] = redactSecrets(verdict.Actions[i], cfg.Password, effectiveKey, req.DiagKey)
		}
		for k, v := range verdict.Facts {
			if s, ok := v.(string); ok {
				verdict.Facts[k] = redactSecrets(s, cfg.Password, effectiveKey, req.DiagKey)
			}
		}
		response.Success(w, Result{
			Mode:       mode,
			Transport:  req.Transport,
			DurationMs: durationMs,
			ExitCode:   exitCode,
			Steps:      steps,
			Verdict:    verdict,
		})
		return
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		response.ErrorWithStatus(w, http.StatusGatewayTimeout, "превышено время ожидания диагностики", "WDTT_DIAG_TIMEOUT")
		return
	}
	if r.Context().Err() != nil {
		response.ErrorWithStatus(w, http.StatusRequestTimeout, "диагностика отменена", "WDTT_DIAG_CANCELED")
		return
	}

	combined := stderr
	if stdout != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stdout
	}
	if isUnsupportedFlagErr(combined) {
		response.ErrorWithStatus(w, http.StatusConflict,
			"установленная версия wt-client не поддерживает диагностику (-diag) — обновите бинарник WDTT",
			"WDTT_DIAG_UNSUPPORTED")
		return
	}

	msg := "диагностика завершилась без вердикта"
	redacted := strings.TrimSpace(redactSecrets(combined, cfg.Password, effectiveKey, req.DiagKey))
	if redacted != "" {
		msg = fmt.Sprintf("%s: %s", msg, tailString(redacted, maxTailChars))
	} else if runErr != nil {
		msg = fmt.Sprintf("%s: %s", msg, redactSecrets(runErr.Error(), cfg.Password, effectiveKey, req.DiagKey))
	}
	response.ErrorWithStatus(w, http.StatusBadGateway, msg, "WDTT_DIAG_FAILED")
}

func (h *Handler) acquire() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	return true
}

func (h *Handler) release() {
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
}

func decodeRequest(r *http.Request) (Request, error) {
	var req Request
	if r.Body == nil {
		return req, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		return req, fmt.Errorf("не удалось прочитать тело запроса: %w", err)
	}
	if len(body) > maxRequestBody {
		return req, errors.New("слишком большое тело запроса")
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return req, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &req); err != nil {
		return req, fmt.Errorf("некорректный JSON запроса: %w", err)
	}
	req.Transport = strings.ToLower(strings.TrimSpace(req.Transport))
	switch req.Transport {
	case "", "auto", "udp", "tcp":
	default:
		return req, fmt.Errorf("transport %q: ожидали auto, udp или tcp", req.Transport)
	}
	req.DiagKey = strings.TrimSpace(req.DiagKey)
	return req, nil
}

// buildDiagArgs собирает аргументы для одноразового `wt-client -diag`.
// Никогда не передаём `--awgm-*`, `-listen`, `-tun-name`, `-n`, `-diag-auto` или `-diag-trigger`.
func buildDiagArgs(cfg roles.WdttClientConfig, req Request) ([]string, string) {
	args := []string{
		"-diag",
		"-peer", strings.TrimSpace(cfg.Peer),
		"-password", cfg.Password,
		"-vk", strings.TrimSpace(cfg.VKHashes),
	}
	str := func(flag, val string) {
		if v := strings.TrimSpace(val); v != "" {
			args = append(args, flag, v)
		}
	}
	if cfg.Mode == "raw" {
		args = append(args, "-mode", "rawtun")
	}
	str("-obfs", cfg.Obfs)
	str("-fingerprint", cfg.Fingerprint)
	str("-device-id", cfg.DeviceID)
	str("-captcha-mode", cfg.CaptchaMode)
	vkMode := strings.TrimSpace(cfg.VKAuthMode)
	if vkMode == "" {
		vkMode = "vkcalls"
	}
	args = append(args, "-vk-auth-mode", vkMode)

	effectiveKey := req.DiagKey
	if effectiveKey == "" {
		effectiveKey = strings.TrimSpace(cfg.DiagKey)
	}
	str("-diag-key", effectiveKey)
	str("-turn-transport", req.Transport)
	return args, effectiveKey
}

func execRunner(ctx context.Context, binary string, args []string) (string, string, int, error) {
	var stdout, stderr limitedBuffer
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return stdout.String(), stderr.String(), exitCode, err
}

func tailString(s string, maxChars int) string {
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return "..." + string(runes[len(runes)-maxChars:])
}
