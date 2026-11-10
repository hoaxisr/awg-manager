package autolist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Prober — активная проба прямого пути: «открывается ли хост без туннеля».
// Классификация повторяет autolist (recheck.c autolist_classify):
//   - TCP connect не удался                  → Blocked/NoConnect
//   - TLS: RST                               → Blocked/Reset
//   - TLS: таймаут рукопожатия               → Blocked/NoAnswer
//   - ответ встал после 8..32 КБ на 5 с      → Blocked/Freeze
//   - тело больше 32 КБ или полный ответ     → Reachable
//   - прочее (сеть лежит и т.п.)             → Unknown
type Prober struct {
	// TimeoutSec — потолок всей пробы (autolist: 30).
	TimeoutSec int
	// OnStart — тестовый сигнал о запуске; не является альтернативным транспортом.
	OnStart func(host string)
	// CurlPath — тестовый/необязательный путь; пусто — curl из PATH.
	CurlPath string
	// Interface — необязательный kernel interface для прямого пути.
	Interface string
	// WANURL — независимая проверка, нужная для BLOCKED/NO_CONNECT.
	WANURL string
	// WANExpects204 — true: WANURL обязан вернуть 204; false: любой HTTP status.
	WANExpects204 bool
}

// ProbeResult — вердикт одной пробы.
type ProbeResult struct {
	Verdict   Verdict
	Why       Why
	Bytes     int
	ShortBody bool // complete && bytes <= 32768, как autolist
	Err       string
	// Tunnel* заполняются только после blocked при включённой дополнительной пробе.
	TunnelProbed  bool
	TunnelVerdict Verdict
	TunnelErr     string
}

// errStalled — ответ частично пришёл и повис (freeze).
var errStalled = errors.New("stalled")

// Probe выполняет GET https://<name>/ по прямому пути через обычный DNS.
func (p *Prober) Probe(ctx context.Context, name string) ProbeResult {
	return p.ProbeAt(ctx, name, "")
}

// ProbeAt проверяет hostname на конкретном наблюдаемом IP. TCP идёт на ip:443,
// а HTTP Host и TLS SNI остаются исходными. Это исключает ложный reachable,
// когда свежий DNS уже выдаёт другой, работающий адрес того же сайта.
func (p *Prober) ProbeAt(ctx context.Context, name, observedIP string) ProbeResult {
	if ip := net.ParseIP(name); ip != nil {
		return ProbeResult{Verdict: VerdictUnknown, Err: "not probed: a bare IP has no SNI"}
	}
	if name == "" {
		return ProbeResult{Verdict: VerdictUnknown, Err: "not probed: no name"}
	}
	if observedIP != "" {
		ip := net.ParseIP(observedIP)
		if ip == nil || privateIP(observedIP) {
			return ProbeResult{Verdict: VerdictUnknown, Err: "not probed: observed address is invalid or local"}
		}
		observedIP = ip.String()
	}
	timeout := time.Duration(p.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = ProbeTimeoutSec * time.Second
	}
	if p.OnStart != nil {
		p.OnStart(name)
	}
	curlPath := p.CurlPath
	if curlPath == "" {
		var err error
		curlPath, err = exec.LookPath("curl")
		if err != nil {
			return ProbeResult{Verdict: VerdictUnknown, Err: "curl is required: executable not found"}
		}
	}
	return p.probeCurl(ctx, name, observedIP, curlPath, timeout)
}

const curlMetaMarker = "\n__AWGM_PROBE__|"

type curlMeta struct {
	known        bool
	httpCode     int
	connectSec   float64
	appConnect   float64
	effectiveURL string
}

// splitCurlMeta отделяет служебный write-out от тела. Маркер не считается
// содержимым сайта и не влияет на Bytes.
func splitCurlMeta(body []byte) ([]byte, curlMeta) {
	idx := bytes.LastIndex(body, []byte(curlMetaMarker))
	if idx < 0 {
		return body, curlMeta{}
	}
	raw := body[idx+len(curlMetaMarker):]
	body = body[:idx]
	parts := strings.Split(string(raw), "|")
	if len(parts) < 4 {
		return body, curlMeta{}
	}
	code, err1 := strconv.Atoi(parts[0])
	connect, err2 := strconv.ParseFloat(parts[1], 64)
	app, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return body, curlMeta{}
	}
	return body, curlMeta{
		known: true, httpCode: code, connectSec: connect, appConnect: app,
		effectiveURL: strings.Join(parts[3:], "|"),
	}
}

type curlResponse struct {
	status      int
	location    string
	hasLocation bool
}

// parseCurlResponse читает последний HTTP-блок из отдельного header-потока.
// Тело ограничивается и обрывается независимо, поэтому Location уже доступен,
// даже если финальный write-out после body cap не выполнился.
func parseCurlResponse(raw []byte) curlResponse {
	var out curlResponse
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for _, block := range strings.Split(normalized, "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) == 0 || !strings.HasPrefix(lines[0], "HTTP/") {
			continue
		}
		fields := strings.Fields(lines[0])
		if len(fields) < 2 {
			continue
		}
		status, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		out = curlResponse{status: status}
		for _, line := range lines[1:] {
			name, value, ok := strings.Cut(line, ":")
			if ok && strings.EqualFold(strings.TrimSpace(name), "Location") {
				out.location = strings.TrimSpace(value)
				out.hasLocation = true
			}
		}
	}
	return out
}

func redirectStatus(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308:
		return true
	default:
		return false
	}
}

// probeCurl использует клиентский стек curl/libcurl, как активная проба autolist.
// Redirects выполняются вручную: каждый следующий URL проверяется до запроса,
// а все переходы и WAN-check делят один общий timeout.
func (p *Prober) probeCurl(ctx context.Context, name, observedIP, curlPath string, timeout time.Duration) ProbeResult {
	chainCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	current, err := url.Parse(probeURL(name))
	if err != nil {
		return ProbeResult{Verdict: VerdictUnknown, Err: err.Error()}
	}
	for redirects := 0; ; {
		remaining := time.Until(mustDeadline(chainCtx))
		if remaining <= 0 {
			return ProbeResult{Verdict: VerdictUnknown, Err: "redirect chain exceeded the total probe timeout"}
		}
		if remaining < time.Millisecond {
			remaining = time.Millisecond
		}
		resolveIP := ""
		if current.Hostname() == name && (current.Port() == "" || current.Port() == "443") {
			resolveIP = observedIP
		}
		result, response := p.curlOnce(chainCtx, current.String(), name, resolveIP, curlPath, remaining)
		if !redirectStatus(response.status) {
			return result
		}

		fail := func(message string) ProbeResult {
			return ProbeResult{Verdict: VerdictUnknown, Bytes: result.Bytes, Err: message}
		}
		if !response.hasLocation || response.location == "" {
			return fail("redirect response has no valid Location")
		}
		next, err := current.Parse(response.location)
		if err != nil || next.Hostname() == "" {
			return fail("redirect has an invalid Location")
		}
		if next.Scheme != "http" && next.Scheme != "https" {
			return fail("redirect uses an unsupported scheme")
		}
		if registeredDomain(next.Hostname()) != registeredDomain(name) {
			return fail("redirect left the registered domain")
		}
		if redirects >= ProbeRedirectsMax {
			return fail("redirect limit exceeded")
		}
		redirects++
		current = next
	}
}

func mustDeadline(ctx context.Context) time.Time {
	deadline, _ := ctx.Deadline()
	return deadline
}

const curlHeaderCap = 1 << 20

type curlHeaderResult struct {
	data []byte
	err  error
}

func readCurlHeaders(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, curlHeaderCap+1))
	if err != nil {
		return nil, err
	}
	if len(data) > curlHeaderCap {
		_, _ = io.Copy(io.Discard, r)
		return nil, errors.New("curl response headers exceeded the size limit")
	}
	return data, nil
}

func curlHeaderFDPath() string {
	// Entware не создаёт /dev/fd, но procfs предоставляет тот же descriptor.
	if runtime.GOOS == "linux" {
		return "/proc/self/fd/3"
	}
	return "/dev/fd/3"
}

// curlOnce выполняет один HTTP-запрос без автоматических redirects. Заголовки
// передаются через отдельный FD и не смешиваются с телом или диагностикой curl.
func (p *Prober) curlOnce(ctx context.Context, targetURL, originalName, observedIP, curlPath string, remaining time.Duration) (ProbeResult, curlResponse) {
	connectTimeout := remaining
	// Connect не должен съедать весь общий deadline: при no-connect после него
	// обязан остаться пятисекундный бюджет независимой WAN-проверки. Остальные
	// фазы по-прежнему ограничены общим remaining через --max-time и context.
	if remaining > 5*time.Second {
		connectTimeout = remaining - 5*time.Second
	}
	args := []string{
		"--silent", "--show-error", "--dump-header", curlHeaderFDPath(),
		"--max-time", fmt.Sprintf("%.3f", remaining.Seconds()),
		"--connect-timeout", fmt.Sprintf("%.3f", connectTimeout.Seconds()),
		"--noproxy", "*", "--user-agent", "awgm-autolist/" + Version + " probe",
		"--write-out", curlMetaMarker + "%{http_code}|%{time_connect}|%{time_appconnect}|%{url_effective}",
	}
	if p.Interface != "" {
		args = append(args, "--interface", p.Interface)
	}
	if observedIP != "" {
		args = append(args, "--resolve", fmt.Sprintf("%s:443:%s", originalName, observedIP))
	}
	args = append(args, targetURL)

	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, curlPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ProbeResult{Verdict: VerdictUnknown, Err: err.Error()}, curlResponse{}
	}
	headerR, headerW, err := os.Pipe()
	if err != nil {
		return ProbeResult{Verdict: VerdictUnknown, Err: err.Error()}, curlResponse{}
	}
	cmd.ExtraFiles = []*os.File{headerW}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		_ = headerR.Close()
		_ = headerW.Close()
		return ProbeResult{Verdict: VerdictUnknown, Err: err.Error()}, curlResponse{}
	}
	_ = headerW.Close()
	headerDone := make(chan curlHeaderResult, 1)
	go func() {
		data, err := readCurlHeaders(headerR)
		_ = headerR.Close()
		headerDone <- curlHeaderResult{data: data, err: err}
	}()
	body, readErr := readWithStallWatch(probeCtx, stdout, ProbeBodyCap, ProbeStallSec*time.Second)
	// После cap/stall больше читать не нужно; завершаем curl, не дожидаясь
	// оставшегося тела. При EOF процессу даём штатно выйти: закрытие stdout
	// может на несколько инструкций предшествовать завершению curl.
	if !errors.Is(readErr, io.EOF) {
		cancel()
	}
	waitErr := cmd.Wait()
	headerResult := <-headerDone
	response := parseCurlResponse(headerResult.data)
	body, meta := splitCurlMeta(body)
	got := len(body)
	errText := strings.TrimSpace(stderr.String())
	if waitErr != nil && errText == "" {
		errText = waitErr.Error()
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) && errText == "" {
		errText = readErr.Error()
	}
	if headerResult.err != nil {
		return ProbeResult{Verdict: VerdictUnknown, Bytes: got, Err: cut(headerResult.err.Error(), 200)}, response
	}
	if response.status == 0 && meta.known {
		response.status = meta.httpCode
	}
	if meta.known && meta.effectiveURL != "" {
		u, err := url.Parse(meta.effectiveURL)
		if err != nil || registeredDomain(u.Hostname()) != registeredDomain(originalName) {
			return ProbeResult{Verdict: VerdictUnknown, Bytes: got, Err: "redirect left the registered domain"}, response
		}
	}

	result := p.classifyCurlResult(ctx, curlPath, start, body, meta, readErr, waitErr, errText)
	return result, response
}

func (p *Prober) classifyCurlResult(ctx context.Context, curlPath string, start time.Time, body []byte, meta curlMeta, readErr, waitErr error, errText string) ProbeResult {
	got := len(body)
	switch {
	case got > StallMaxBytes:
		return ProbeResult{Verdict: VerdictReachable, Bytes: got}
	case errors.Is(readErr, errStalled) && got >= StallMinUplink:
		return ProbeResult{Verdict: VerdictBlocked, Why: WhyFreeze, Bytes: got, Err: "no byte for " + fmt.Sprint(ProbeStallSec) + "s after " + fmt.Sprint(got) + " bytes"}
	case (errors.Is(readErr, io.EOF) || readErr == nil) && waitErr == nil:
		return ProbeResult{Verdict: VerdictReachable, Bytes: got, ShortBody: true}
	}

	lowerErr := strings.ToLower(errText)
	if got == 0 && meta.known && meta.connectSec > 0 && meta.appConnect == 0 && strings.Contains(lowerErr, "reset") {
		return ProbeResult{Verdict: VerdictBlocked, Why: WhyReset, Err: cut(errText, 200)}
	}
	timedOut := errors.Is(readErr, context.DeadlineExceeded) || errors.Is(waitErr, context.DeadlineExceeded) ||
		strings.Contains(lowerErr, "timed out") || strings.Contains(lowerErr, "timeout")
	if timedOut {
		if got == 0 && meta.known {
			switch {
			case meta.httpCode == 0 && meta.connectSec == 0:
				if p.wanCheck(ctx, curlPath) {
					return ProbeResult{Verdict: VerdictBlocked, Why: WhyNoConnect, Err: cut(errText, 200)}
				}
				return ProbeResult{Verdict: VerdictUnknown, Err: cut(errText, 200)}
			case meta.appConnect == 0:
				return ProbeResult{Verdict: VerdictBlocked, Why: WhyNoAnswer, Err: cut(errText, 200)}
			default:
				// TLS завершился, но тело не началось: autolist считает это unknown.
				return ProbeResult{Verdict: VerdictUnknown, Err: cut(errText, 200)}
			}
		}
		if got == 0 {
			// Без метрик (старый/fake curl) сохраняем прежний fallback.
			return ProbeResult{Verdict: VerdictBlocked, Why: WhyNoAnswer, Err: cut(errText, 200)}
		}
		elapsed := time.Since(start)
		slow := elapsed > 0 && float64(got)/elapsed.Seconds() < OpenProbeMaxBPS
		if got >= StallMinUplink || slow {
			return ProbeResult{Verdict: VerdictBlocked, Why: WhyFreeze, Bytes: got, Err: cut(errText, 200)}
		}
		return ProbeResult{Verdict: VerdictUnknown, Bytes: got, Err: cut(errText, 200)}
	}
	if waitErr != nil {
		if meta.known && meta.appConnect > 0 {
			// Reset после завершённого TLS сам по себе не доказывает блокировку.
			return ProbeResult{Verdict: VerdictUnknown, Bytes: got, Err: cut(errText, 200)}
		}
		res := classifyTransportErr(errors.New(errText))
		res.Bytes = got
		return res
	}
	return ProbeResult{Verdict: VerdictUnknown, Bytes: got, Err: cut(errText, 200)}
}

// wanCheck — независимый пятисекундный запрос без redirects. Только он
// позволяет превратить connect timeout в BLOCKED/NO_CONNECT.
func (p *Prober) wanCheck(ctx context.Context, curlPath string) bool {
	if p.WANURL == "" {
		return false
	}
	args := []string{
		"--silent", "--show-error", "--max-time", "5", "--connect-timeout", "5",
		"--noproxy", "*", "--output", "/dev/null", "--write-out", "%{http_code}",
	}
	if p.Interface != "" {
		args = append(args, "--interface", p.Interface)
	}
	args = append(args, p.WANURL)
	cmd := exec.CommandContext(ctx, curlPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return false
	}
	if p.WANExpects204 {
		return code == 204
	}
	return code > 0
}

// classifyTransportErr разбирает ошибку транспорта (connect/TLS) по autolist:
// таймаут → NoAnswer, отказ/сброс → Reset, прочее → Unknown.
func classifyTransportErr(err error) ProbeResult {
	msg := err.Error()
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		// Отличить connect-таймаут от TLS-таймаута просто так нельзя,
		// autolist различает их по info.connected/info.tls_done; для наших
		// целей вердикт один: Blocked, причина NoAnswer.
		return ProbeResult{Verdict: VerdictBlocked, Why: WhyNoAnswer, Err: cut(msg, 200)}
	}
	for _, s := range []string{"connection refused", "no route to host", "network is unreachable"} {
		if strings.Contains(msg, s) {
			// Autolist не называет refusal/локальную ошибку маршрута блокировкой.
			return ProbeResult{Verdict: VerdictUnknown, Err: cut(msg, 200)}
		}
	}
	for _, s := range []string{"connection reset", "broken pipe", "handshake failure"} {
		if strings.Contains(msg, s) {
			return ProbeResult{Verdict: VerdictBlocked, Why: WhyReset, Err: cut(msg, 200)}
		}
	}
	// Сеть в целом недоступна — Unknown, чтобы не записать в кандидаты
	// полсети за один обрыв провайдера.
	return ProbeResult{Verdict: VerdictUnknown, Err: cut(msg, 200)}
}

// readWithStallWatch читает r до cap байт; тишина дольше stall после
// частичного чтения — errStalled. Текущий goroutine — единственный reader-owner.
// Watchdog лишь закрывает реальный поток, чтобы заблокированный Read завершился.
func readWithStallWatch(ctx context.Context, r io.ReadCloser, cap int, stall time.Duration) ([]byte, error) {
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	progress := make(chan struct{})
	watchResult := make(chan error, 1)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		var timer *time.Timer
		var stallC <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		for {
			select {
			case <-watchCtx.Done():
				watchResult <- watchCtx.Err()
				_ = r.Close()
				return
			case <-progress:
				if stall <= 0 {
					continue
				}
				if timer == nil {
					timer = time.NewTimer(stall)
				} else {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(stall)
				}
				stallC = timer.C
			case <-stallC:
				watchResult <- errStalled
				_ = r.Close()
				return
			}
		}
	}()
	defer func() {
		cancel()
		_ = r.Close()
		<-watchDone
	}()

	buf := make([]byte, 32*1024)
	out := make([]byte, 0, cap)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
			if len(out) >= cap {
				return out[:cap], nil
			}
			// Синхронная передача гарантирует, что watchdog обработал прогресс
			// до следующего блокирующего Read. До первого байта таймер не armed.
			select {
			case progress <- struct{}{}:
			case <-watchCtx.Done():
			}
		}
		if readErr != nil {
			select {
			case err := <-watchResult:
				return out, err
			default:
			}
			return out, readErr
		}
	}
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
