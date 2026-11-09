// Package autolistdetect — обнаружение сателлита awgmautolist на роутере.
//
// awgmautolist — отдельный процесс (см. awgmautolist/ в корне репозитория):
// он не установлен этим пакетом и не управляется им, как и zapret. Здесь
// только детект живого сателлита (pid-файл + его статус-эндпоинт) и
// проксирование запросов UI к нему. Сам сателлит обязан слушать только
// loopback: наружу его статус не отдаётся, доступ к UI awg-manager
// остаётся единственной дверью.
package autolistdetect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Пути по конвенции init-скрипта сателлита (awgmautolist/entware).
const (
	pidFile = "/opt/var/run/awgmautolist.pid"
	// listenFile — сателлит пишет адрес статуса при старте, когда отличен
	// от дефолта; пусто — дефолт.
	listenFile = "/opt/var/run/awgmautolist.listen"
	// defaultStatus — адрес статус-сервера по умолчанию (main.go флаг -listen).
	defaultStatus = "127.0.0.1:2226"
	// initScript и enabledState управляются только через expert-only API.
	initScript   = "/opt/etc/init.d/S99awgmautolist"
	enabledState = "/opt/etc/awgmautolist.enabled"
	// httpTimeout — статус сателлита локальный, дольше ждать нечего.
	httpTimeout = 5 * time.Second
	// commandHTTPTimeout — /add проверяет покрытие, материализует SRS и
	// применяет конфиг. На роутере это закономерно дольше пяти секунд.
	commandHTTPTimeout = 75 * time.Second
)

// Candidate — строка таблицы сателлита (awgmautolist/internal/autolist).
type Candidate struct {
	Domain         string   `json:"domain"`
	Host           string   `json:"host"`
	IP             string   `json:"ip,omitempty"`
	Why            string   `json:"why"`
	State          string   `json:"state"`
	Fails          int      `json:"fails"`
	Sources        []string `json:"sources"`
	LastAt         string   `json:"lastAt"`
	Verdict        string   `json:"verdict,omitempty"`
	ProbedAt       string   `json:"probedAt,omitempty"`
	Stage          string   `json:"stage,omitempty"`
	ProbeError     string   `json:"probeError,omitempty"`
	TunnelVerdict  string   `json:"tunnelVerdict,omitempty"`
	TunnelProbedAt string   `json:"tunnelProbedAt,omitempty"`
	TunnelError    string   `json:"tunnelError,omitempty"`
}

// HistoryEntry — сохранённое действие сателлита над доменом.
type HistoryEntry struct {
	Domain string `json:"domain"`
	Host   string `json:"host,omitempty"`
	Action string `json:"action"`
	Why    string `json:"why,omitempty"`
	Source string `json:"source"`
	At     string `json:"at"`
}

// Status — снимок состояния сателлита для UI.
type Status struct {
	// Running — pid-файл существует и процесс жив (kill -0).
	Running bool   `json:"running"`
	Version string `json:"version,omitempty"`
	// StatusURL — адрес статус-сервера сателлита, если найден.
	StatusURL string `json:"statusUrl,omitempty"`
	// Auto — режим автодобавления сателлита (последний /status).
	Auto bool `json:"auto,omitempty"`
	// TargetInterface — общий интерфейс tunnel-пробы и новых правил.
	TargetInterface string `json:"targetInterface,omitempty"`
	// TargetOutbound — legacy-поле старого конфига сателлита.
	TargetOutbound string `json:"targetOutbound,omitempty"`
	// Почасовые бюджеты для индикации исчерпания в UI. Нулевой Limit означает,
	// что соответствующий лимит отключён при сборке сателлита.
	ProbesUsedHour int `json:"probesUsedHour,omitempty"`
	ProbesLimit    int `json:"probesLimit,omitempty"`
	AddsUsedHour   int `json:"addsUsedHour,omitempty"`
	AddsLimit      int `json:"addsLimit,omitempty"`
	// Candidates — таблица кандидатов; пуста, когда сателлит не запущен.
	Candidates []Candidate `json:"candidates,omitempty"`
	// History — сохранённые добавления и скрытия, новые записи первыми.
	History []HistoryEntry `json:"history"`
	// Installed/Enabled различают отсутствие пакета, намеренное отключение и сбой.
	Installed bool `json:"installed"`
	Enabled   bool `json:"enabled"`
	// LastError — причина, по которой живой процесс не ответил.
	LastError string `json:"lastError,omitempty"`
}

// Service детектит и опрашивает сателлит. Как zapret.Service, безопасен
// для конкурентных вызовов и не ждёт внешние процессы дольше httpTimeout.
type Service struct {
	client *http.Client
	// now — seam для тестов.
	now func() time.Time

	controlMu sync.Mutex
	initPath  string
	statePath string
	command   func(context.Context, string, ...string) ([]byte, error)
	confirm   func(context.Context, bool) error
}

var ErrNotInstalled = errors.New("сателлит awgmautolist не установлен")

func NewService() *Service {
	return &Service{client: &http.Client{Timeout: httpTimeout}, now: time.Now}
}

func (s *Service) initScriptPath() string {
	if s.initPath != "" {
		return s.initPath
	}
	return initScript
}

func (s *Service) enabledStatePath() string {
	if s.statePath != "" {
		return s.statePath
	}
	return enabledState
}

func (s *Service) installed() bool {
	_, err := os.Stat(s.initScriptPath())
	return err == nil
}

func (s *Service) desiredEnabled() bool {
	b, err := os.ReadFile(s.enabledStatePath())
	return err != nil || strings.TrimSpace(string(b)) != "ENABLED=no"
}

func (s *Service) runInit(ctx context.Context, action string) error {
	run := s.command
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	out, err := run(ctx, s.initScriptPath(), action)
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return fmt.Errorf("init %s: %w: %s", action, err, detail)
		}
		return fmt.Errorf("init %s: %w", action, err)
	}
	return nil
}

// confirmState ждёт фактический HTTP-результат: pid-файл исчезает до
// graceful shutdown, а при старте HTTP-сервер может подняться после init return.
func (s *Service) confirmState(ctx context.Context, enabled bool) error {
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		if s.GetStatus(ctx).Running == enabled {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("состояние сателлита после init не подтверждено")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// SetEnabled сохраняет намерение пользователя и вызывает init script. Отдельный
// state-файл не перетирает awgmautolist.conf с API-ключом и аргументами запуска.
func (s *Service) SetEnabled(ctx context.Context, enabled bool) error {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if !s.installed() {
		return ErrNotInstalled
	}
	state := "ENABLED=no\n"
	action := "stop"
	if enabled {
		state = "ENABLED=yes\n"
		action = "start"
	}
	path := s.enabledStatePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(state), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := s.runInit(ctx, action); err != nil {
		return err
	}
	confirm := s.confirm
	if confirm == nil {
		confirm = s.confirmState
	}
	return confirm(ctx, enabled)
}

// statusURL читает адрес статуса: listen-файл или дефолт.
// statusURLForTest — seam для тестов прокси: не nil — используется вместо
// чтения файла (подменяет адрес сателлита тестовым сервером).
var statusURLForTest func() string

func (s *Service) statusURL() string {
	if statusURLForTest != nil {
		return statusURLForTest()
	}
	b, err := os.ReadFile(listenFile)
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return defaultStatus
	}
	addr := strings.TrimSpace(string(b))
	// только loopback: наружу статус сателлита не выставляем
	host, _, err := net.SplitHostPort(addr)
	if err == nil && host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return defaultStatus
	}
	return addr
}

// runningPID — живой pid из pid-файла, 0 когда процесса нет.
func runningPID() int {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
		// /proc может быть не смонтирован — тогда проверку процесса
		// выполняет сам HTTP-запрос ниже (Running ставится по ответу).
		return pid
	}
	return pid
}

// GetStatus опрашивает сателлит и собирает снимок для UI.
func (s *Service) GetStatus(ctx context.Context) Status {
	st := Status{History: []HistoryEntry{}}
	st.Installed = s.installed()
	st.Enabled = st.Installed && s.desiredEnabled()
	pid := runningPID()
	url := "http://" + s.statusURL() + "/status"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		st.LastError = err.Error()
		return st
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// Процесс не отвечает: pid-файл мог остаться от упавшего запуска.
		st.LastError = "сателлит не отвечает на " + url
		return st
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		st.LastError = fmt.Sprintf("статус %d", resp.StatusCode)
		return st
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		st.LastError = err.Error()
		return st
	}
	var snap struct {
		Candidates      []Candidate    `json:"candidates"`
		History         []HistoryEntry `json:"history"`
		AutoAdd         bool           `json:"autoAdd"`
		TargetInterface string         `json:"targetInterface"`
		TargetOutbound  string         `json:"targetOutbound"`
		ProbesUsedHour  int            `json:"probesUsedHour"`
		ProbesLimit     int            `json:"probesLimit"`
		AddsUsedHour    int            `json:"addsUsedHour"`
		AddsLimit       int            `json:"addsLimit"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		st.LastError = err.Error()
		return st
	}
	st.Running = pid > 0 || true // ответил — значит жив
	st.StatusURL = url
	st.Auto = snap.AutoAdd
	st.TargetInterface = snap.TargetInterface
	st.TargetOutbound = snap.TargetOutbound
	st.ProbesUsedHour = snap.ProbesUsedHour
	st.ProbesLimit = snap.ProbesLimit
	st.AddsUsedHour = snap.AddsUsedHour
	st.AddsLimit = snap.AddsLimit
	st.Candidates = snap.Candidates
	if st.Candidates == nil {
		st.Candidates = []Candidate{}
	}
	st.History = snap.History
	if st.History == nil {
		st.History = []HistoryEntry{}
	}
	return st
}

// Proxy пересылает запросы UI к статус-серверу сателлита
// (белый список: /status, /dismiss, /restore, /forget, /add, /probe, /exclude, /config).
type Proxy struct {
	svc *Service
}

func NewProxy(svc *Service) *Proxy { return &Proxy{svc: svc} }

// allowedPaths — пути сателлита, доступные из UI. POST /dismiss и /forget
// меняют состояние, POST /add меняет правила роутера, POST /config —
// режим сателлита; их гейтит expertGuarded в server_routes.go.
var allowedPaths = map[string]bool{
	"/status":  true,
	"/dismiss": true,
	"/restore": true,
	"/forget":  true,
	"/add":     true,
	"/probe":   true,
	"/exclude": true,
	"/config":  true,
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/autolist")
	if path == "" {
		path = "/status"
	}
	if !allowedPaths[path] {
		http.Error(w, `{"error":"unknown path"}`, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// /status проксируем как есть; для POST-путей читаем тело запроса UI
	// и пересылаем его. Тело {"path":"/config"} без "body" — это ЧТЕНИЕ:
	// сателлиту уходит GET (иначе пустой JSON затёр бы конфиг нулями).
	var bodyReader io.Reader
	upstreamMethod := r.Method
	if r.Method == http.MethodPost {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if len(strings.TrimSpace(string(b))) == 0 {
			b = []byte("{}")
		}
		// Разбор тела-конверта {"path":..., "body":...}: body нет — чтение.
		var envelope struct {
			Body map[string]any `json:"body"`
		}
		hasBodyField := false
		if err := json.Unmarshal(b, &envelope); err == nil {
			hasBodyField = envelope.Body != nil
		}
		if hasBodyField {
			bb, err := json.Marshal(envelope.Body)
			if err == nil {
				b = bb
			}
		} else if path != "/config" {
			// команды без конверта (dismiss/forget из прямых вызовов): тело уже
			// с доменом, пересылаем как есть; пустое {} для /add — ошибка домена,
			// безопасно.
		} else {
			// /config без body — GET-семантика чтения
			upstreamMethod = http.MethodGet
			b = nil
		}
		bodyReader = bytes.NewReader(b)
	}

	target := "http://" + p.svc.statusURL() + path
	req, err := http.NewRequestWithContext(r.Context(), upstreamMethod, target, bodyReader)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := p.svc.client
	if path == "/add" {
		commandClient := *client
		commandClient.Timeout = commandHTTPTimeout
		client = &commandClient
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, `{"error":"сателлит не отвечает: `+err.Error()+`"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	// Нормализация: 204/пустое тело → JSON-конверт успеха (фронт ожидает JSON).
	if resp.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(respBody)) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
		return
	}
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	// Сателлит отвечает голым JSON, а CoreClient AWGM возвращает вызывающему
	// коду только поле data стандартного API-конверта. Без этой обёртки
	// /config превращался в undefined и UI молча подставлял autoAdd:false.
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices && json.Valid(bytes.TrimSpace(respBody)) {
		wrapped, err := json.Marshal(struct {
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
		}{Success: true, Data: json.RawMessage(bytes.TrimSpace(respBody))})
		if err == nil {
			respBody = wrapped
			w.Header().Set("Content-Type", "application/json")
			w.Header().Del("Content-Length")
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}
