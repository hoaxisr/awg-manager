package warpscout

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// EngineCandidates — где искать C-модуль на роутере.
var EngineCandidates = []string{"/opt/sbin/awgm-warpscout", "/opt/bin/awgm-warpscout"}

// DefaultPrefixes — пулы адресов эндпоинтов WARP.
var DefaultPrefixes = []string{
	"162.159.192.0/24", "162.159.193.0/24", "162.159.195.0/24",
	"188.114.96.0/24", "188.114.97.0/24", "188.114.98.0/24", "188.114.99.0/24",
	"8.6.112.0/24", "8.34.70.0/24", "8.34.146.0/24", "8.35.211.0/24",
	"8.39.125.0/24", "8.39.204.0/24", "8.39.214.0/24", "8.47.69.0/24",
}

// DefaultPorts — порты, на которых WARP принимает WireGuard.
var DefaultPorts = []int{
	2408, 500, 1701, 4500, 854, 859, 864, 878, 880, 890, 891, 894, 903, 908, 928,
	934, 939, 942, 943, 945, 946, 955, 968, 987, 988, 1002, 1010, 1014, 1018, 1070,
	1074, 1180, 1387, 1843, 2371, 2506, 3138, 3476, 3581, 3854, 4177, 4198, 4233,
	5279, 5956, 7103, 7152, 7156, 7281, 7559, 8319, 8742, 8854, 8886,
}

// EngineAWG — блок awg во входе сканера.
type EngineAWG struct {
	Jc   int       `json:"jc"`
	Jmin int       `json:"jmin"`
	Jmax int       `json:"jmax"`
	I    [5]string `json:"i"`
}

// ScanParams — вход C-модуля (stdin). Схема зафиксирована в c-core/warpscout.
type ScanParams struct {
	PrivateKey       string    `json:"private_key"`
	PeerPublicKey    string    `json:"peer_public_key"`
	PresharedKey     string    `json:"preshared_key"`
	TunnelIP         string    `json:"tunnel_ip"`
	Reserved         [3]int    `json:"reserved"`
	Endpoints        []string  `json:"endpoints"`
	Prefixes         []string  `json:"prefixes"`
	Ports            []int     `json:"ports"`
	ExcludeEndpoints []string  `json:"exclude_endpoints"`
	MaxCandidates    int       `json:"max_candidates"`
	Concurrency      int       `json:"concurrency"`
	PPS              int       `json:"pps"`
	TimeoutMs        int       `json:"timeout_ms"`
	DeadlineMs       int       `json:"deadline_ms"`
	MaxHits          int       `json:"max_hits"`
	Seed             int64     `json:"seed"`
	Ifname           string    `json:"ifname"`
	Fwmark           int       `json:"fwmark"`
	AWG              EngineAWG `json:"awg"`
	PingIP           string    `json:"ping_ip"`
	ProbeTrace       bool      `json:"probe_trace"`
	TraceIP          string    `json:"trace_ip"`
	TraceHost        string    `json:"trace_host"`
	ProbeTG          bool      `json:"probe_tg"`
	ExcludeColos     []string  `json:"exclude_colos"`
	AllowedCountries []string  `json:"allowed_countries"`
	MaxRTTMs             int       `json:"max_rtt_ms"`
	RequireTrace         bool      `json:"require_trace"`
	RequireTG            bool      `json:"require_tg"`
	EmitRejects          bool      `json:"emit_rejects"`
	DurabilityPings      int       `json:"durability_pings,omitempty"`
	DurabilityIntervalMs int       `json:"durability_interval_ms,omitempty"`
}

// EngineEvent — строка JSONL из stdout C-модуля.
type EngineEvent struct {
	Type        string `json:"type"`
	Version     string `json:"version,omitempty"`
	Total       int    `json:"total,omitempty"`
	Done        int    `json:"done,omitempty"`
	Hits        int    `json:"hits,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	HandshakeMs int    `json:"handshake_ms,omitempty"`
	PingMs      int    `json:"ping_ms,omitempty"`
	Colo        string `json:"colo,omitempty"`
	Loc         string `json:"loc,omitempty"`
	Warp        string `json:"warp,omitempty"`
	TgOK        bool   `json:"tg_ok,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Scanned     int    `json:"scanned,omitempty"`
	ElapsedMs   int    `json:"elapsed_ms,omitempty"`
	Aborted     bool   `json:"aborted,omitempty"`
	Message     string `json:"message,omitempty"`
}

// Hit превращает событие hit в Hit.
func (e EngineEvent) Hit() Hit {
	return Hit{Endpoint: e.Endpoint, HandshakeMs: e.HandshakeMs, PingMs: e.PingMs,
		Colo: e.Colo, Loc: e.Loc, Warp: e.Warp, TgOK: e.TgOK}
}

// Runner запускает скан. Реализация по умолчанию — BinaryRunner.
type Runner interface {
	Available() (string, bool)
	Run(ctx context.Context, p ScanParams, onEvent func(EngineEvent)) error
}

// BinaryRunner запускает C-модуль дочерним процессом.
type BinaryRunner struct {
	// Path — явный путь (тесты); пусто — EngineCandidates.
	Path string
}

// Available ищет бинарник.
func (r *BinaryRunner) Available() (string, bool) {
	if r.Path != "" {
		if fileExecutable(r.Path) {
			return r.Path, true
		}
		return r.Path, false
	}
	for _, p := range EngineCandidates {
		if fileExecutable(p) {
			return p, true
		}
	}
	return EngineCandidates[0], false
}

func fileExecutable(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return st.Mode()&0o111 != 0
}

// maxEngineLine — потолок одной строки JSONL из stdout C-модуля.
const maxEngineLine = 1 << 20

// ErrEngineMissing — C-модуль не установлен.
var ErrEngineMissing = errors.New("модуль сканера awgm-warpscout не найден")

// Run запускает скан и вызывает onEvent на каждое событие. Возвращает
// ошибку запуска, событие error или ненулевой код выхода.
func (r *BinaryRunner) Run(ctx context.Context, p ScanParams, onEvent func(EngineEvent)) error {
	path, ok := r.Available()
	if !ok {
		return ErrEngineMissing
	}
	input, err := json.Marshal(p)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path)
	// Мягкая остановка: модуль на SIGTERM печатает done и выходит сам.
	cmd.Cancel = func() error { return terminate(cmd.Process) }
	cmd.WaitDelay = 3 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr limitedBuf
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("запуск %s: %w", path, err)
	}
	go func() {
		_, _ = stdin.Write(input)
		_ = stdin.Close()
	}()
	var fatal string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 4096), maxEngineLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var ev EngineEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "error" {
			fatal = ev.Message
		}
		onEvent(ev)
	}
	// Строка длиннее буфера обрывает разбор: без этой ошибки скан выглядел бы
	// как успешный и пустой.
	scanErr := sc.Err()
	_, _ = io.Copy(io.Discard, stdout)
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return nil // остановлено вызывающим
	}
	if fatal != "" {
		return errors.New(fatal)
	}
	if werr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%v: %s", werr, msg)
		}
		return werr
	}
	if scanErr != nil {
		return fmt.Errorf("разбор вывода сканера: %w", scanErr)
	}
	return nil
}

// limitedBuf — stderr с потолком, чтобы болтливый процесс не съел память.
type limitedBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (b *limitedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := 2048 - len(b.buf); room > 0 {
		if len(p) > room {
			b.buf = append(b.buf, p[:room]...)
		} else {
			b.buf = append(b.buf, p...)
		}
	}
	return len(p), nil
}

func (b *limitedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
