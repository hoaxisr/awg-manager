package probe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

const (
	// redirectsMax bounds manually followed redirects.
	redirectsMax = 3
	// bodyLimit is the reachable threshold: more data than this proves the
	// path carries traffic even though reading stopped at the cap.
	bodyLimit = 30 * 1024
	// headerCap bounds the separate header stream.
	headerCap = 1 << 20

	userAgent      = "awgmautolist-probe/0.10.0"
	curlMetaMarker = "\n__AWGL_PROBE__|"
)

// httpsPort is the port the HTTPS probe talks to. A variable so local tests
// can point the probe at an ephemeral listener; production always sees 443.
var httpsPort = 443

// curl exit codes the classification understands.
const (
	curlExitDNS     = 6  // could not resolve host
	curlExitTimeout = 28 // operation timeout, includes a stalled partial body
	curlExitWrite   = 23 // write error, e.g. our own body cap
	curlExitConnect = 7  // TCP connect refused/unreachable
	curlExitTLS     = 35 // TLS handshake failure
	curlExitReset   = 51 // TLS peer reset during handshake
	curlExitEmpty   = 52 // empty reply
	curlExitSend    = 55 // send failure
	curlExitRecv    = 56 // receive failure, e.g. RST mid-transfer
	curlExitLocal   = 45 // bad --interface option: local setup, not a block
)

var errBodyLimit = errors.New("body limit reached")

// bodySink counts received bytes, keeps the bounded content and deliberately
// kills the stream past bodyLimit: the copy fails, curl hits a write error
// and exits, so the probe never downloads unbounded bodies.
type bodySink struct {
	buf bytes.Buffer
	n   int
}

func (w *bodySink) Write(p []byte) (int, error) {
	w.n += len(p)
	if w.n > bodyLimit {
		return 0, errBodyLimit
	}
	w.buf.Write(p)
	return len(p), nil
}

// httpsURL renders the probe URL for a domain or an IP literal host.
func httpsURL(host string) string {
	return "https://" + net.JoinHostPort(host, strconv.Itoa(httpsPort)) + "/"
}

// resolveArg pins the observed IP for the original host. Applied on every
// path, so the direct check and the tunnel check reach the same endpoint the
// capture observed instead of whatever fresh DNS answers.
func resolveArg(target model.Target) (string, bool) {
	if target.IP == "" || target.Domain == "" {
		return "", false
	}
	parsed := net.ParseIP(target.IP)
	if parsed == nil {
		return "", false
	}
	ip := parsed.String()
	if parsed.To4() == nil {
		ip = "[" + ip + "]"
	}
	host := target.Host
	if host == "" {
		host = target.Domain
	}
	return net.JoinHostPort(host, strconv.Itoa(httpsPort)) + ":" + ip, true
}

type curlMeta struct {
	known        bool
	httpCode     int
	dnsSec       float64
	connectSec   float64
	appConnect   float64
	effectiveURL string
}

// splitCurlMeta separates the write-out trailer from the body. The marker is
// not site content and never counts towards bytes.
func splitCurlMeta(raw []byte) ([]byte, curlMeta) {
	idx := bytes.LastIndex(raw, []byte(curlMetaMarker))
	if idx < 0 {
		return raw, curlMeta{}
	}
	parts := strings.SplitN(string(raw[idx+len(curlMetaMarker):]), "|", 5)
	if len(parts) < 5 {
		return raw, curlMeta{}
	}
	code, err1 := strconv.Atoi(parts[0])
	dns, err2 := strconv.ParseFloat(parts[1], 64)
	connect, err3 := strconv.ParseFloat(parts[2], 64)
	app, err4 := strconv.ParseFloat(parts[3], 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return raw, curlMeta{}
	}
	return raw[:idx], curlMeta{
		known: true, httpCode: code, dnsSec: dns, connectSec: connect, appConnect: app,
		effectiveURL: parts[4],
	}
}

type curlResponse struct {
	status      int
	location    string
	hasLocation bool
}

// parseCurlResponse reads the last HTTP block of the separate header stream,
// so Location is available even when the body cap stopped curl early.
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

// sameHost reports that a redirect stays on the pinned original host. The
// probe must keep talking to the observed endpoint; a silent DNS change would
// probe a different server.
func sameHost(original, candidate string) bool {
	return strings.EqualFold(strings.TrimSuffix(candidate, "."), strings.TrimSuffix(original, "."))
}

func sameObservedHTTPSPort(target *url.URL) bool {
	port := target.Port()
	if port == "" {
		return httpsPort == 443
	}
	return port == strconv.Itoa(httpsPort)
}

// headerFDPath is where curl dumps response headers. Entware has no /dev/fd.
func headerFDPath() string {
	if runtime.GOOS == "linux" {
		return "/proc/self/fd/3"
	}
	return "/dev/fd/3"
}

// httpsCheck runs GET https://<domain-or-IP>/ on one path and classifies the
// outcome. One request, no subresources, no credentials; redirects are
// followed manually with a validated http(s) URL and at most redirectsMax hops.
func httpsCheck(parent context.Context, target model.Target, path Path) Result {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()

	host := target.Host
	if host == "" {
		host = target.Domain
	}
	if host == "" {
		host = target.IP
	}
	current, err := url.Parse(httpsURL(host))
	if err != nil {
		return Result{Verdict: model.VerdictError, Duration: time.Since(started), Detail: err.Error()}
	}
	for redirects := 0; ; redirects++ {
		remaining := probeTimeout
		if deadline, ok := ctx.Deadline(); ok {
			remaining = time.Until(deadline)
		}
		if remaining <= 0 {
			return Result{Verdict: model.VerdictBlocked, Duration: time.Since(started), Detail: "probe exceeded its total timeout"}
		}
		// The observed IP is pinned only while talking to the original host.
		pin := ""
		if sameHost(host, current.Hostname()) {
			pin, _ = resolveArg(target)
		}
		one, response := curlOnce(ctx, current.String(), pin, path, remaining)
		one.Duration = time.Since(started)
		if one.Verdict != model.VerdictNotChecked {
			return one
		}
		// VerdictNotChecked means "the response asks to follow".
		if redirects >= redirectsMax {
			// A redirect loop is a working HTTP conversation, not a block.
			one.Verdict = model.VerdictReachable
			one.Detail = "redirect limit reached with a working path"
			return one
		}
		if !response.hasLocation || response.location == "" {
			one.Verdict = model.VerdictError
			one.Detail = "redirect response has no valid Location"
			return one
		}
		next, err := current.Parse(response.location)
		if err != nil || next.Hostname() == "" {
			one.Verdict = model.VerdictError
			one.Detail = "redirect has an invalid Location"
			return one
		}
		if next.Scheme != "http" && next.Scheme != "https" {
			one.Verdict = model.VerdictError
			one.Detail = "redirect uses an unsupported scheme " + next.Scheme
			return one
		}
		if next.Scheme != "https" || next.User != nil || !sameHost(host, next.Hostname()) || !sameObservedHTTPSPort(next) {
			// The complete redirect already proves that the observed endpoint
			// answered. Do not turn it into a fresh-DNS probe of another endpoint.
			one.Verdict = model.VerdictReachable
			one.Detail = "redirect left the observed HTTPS endpoint; original response is reachable"
			return one
		}
		current = next
	}
}

// curlOnce performs one request without automatic redirects. It returns
// VerdictNotChecked for a redirect the caller may validate and follow.
func curlOnce(ctx context.Context, targetURL, resolve string, path Path, remaining time.Duration) (Result, curlResponse) {
	args := []string{
		"--silent", "--show-error", "--insecure", "--globoff",
		"--noproxy", "*", "--request", "GET",
		"--dump-header", headerFDPath(),
		"--connect-timeout", formatSeconds(remaining),
		"--max-time", formatSeconds(remaining),
		"--user-agent", userAgent,
		"--write-out", curlMetaMarker + "%{http_code}|%{time_namelookup}|%{time_connect}|%{time_appconnect}|%{url_effective}",
	}
	if path.Interface != "" {
		args = append(args, "--interface", path.Interface)
	}
	if resolve != "" {
		args = append(args, "--resolve", resolve)
	}
	args = append(args, targetURL)

	cmd := exec.CommandContext(ctx, "curl", args...)
	body := &bodySink{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = body
	cmd.Stderr = stderr
	headerR, headerW, err := os.Pipe()
	if err != nil {
		return Result{Verdict: model.VerdictError, Detail: err.Error()}, curlResponse{}
	}
	cmd.ExtraFiles = []*os.File{headerW}
	if err := cmd.Start(); err != nil {
		_ = headerR.Close()
		_ = headerW.Close()
		return Result{Verdict: model.VerdictError, Detail: err.Error()}, curlResponse{}
	}
	_ = headerW.Close()
	// Headers and body are separate streams; only a concurrent reader
	// prevents deadlock when curl fills one pipe while we drain the other.
	headerDone := make(chan curlHeaderResult, 1)
	go func() {
		data, err := readHeaders(headerR)
		_ = headerR.Close()
		headerDone <- curlHeaderResult{data: data, err: err}
	}()

	runErr := cmd.Wait()
	header := <-headerDone

	// --write-out lands on stdout, so the meta trailer is split from the
	// received body bytes. The header stream carries the raw HTTP blocks.
	payload, meta := splitCurlMeta(body.buf.Bytes())
	response := parseCurlResponse(header.data)
	if response.status == 0 && meta.known {
		response.status = meta.httpCode
	}

	result := Result{
		Bytes:       len(payload),
		HTTPStatus:  response.status,
		TimeDNS:     secondsToDuration(meta.dnsSec),
		TimeConnect: secondsToDuration(meta.connectSec),
		TimeTLS:     secondsToDuration(meta.appConnect),
	}
	detail := strings.TrimSpace(stderr.String())
	if detail == "" {
		detail = fallbackDetail(runErr, header.err)
	}
	result.Detail = detail

	switch {
	case body.n > bodyLimit:
		// Data kept flowing until the deliberate cap: the path works even
		// though reading stopped at the limit. The write-out trailer never
		// arrived, so body.n is the honest received-byte count.
		result.Verdict = model.VerdictReachable
		result.Bytes = body.n
		result.Detail = "received more than 30 KiB"
	case runErr == nil && header.err == nil && redirectStatus(response.status):
		result.Verdict = model.VerdictNotChecked
	case runErr == nil && header.err == nil:
		// A complete short response is reachable; size alone is not a block.
		result.Verdict = model.VerdictReachable
	default:
		result.Verdict = classifyFailure(ctx, runErr, stderr.String())
	}
	return result, response
}

type curlHeaderResult struct {
	data []byte
	err  error
}

// readHeaders drains the header stream up to headerCap; beyond the cap the
// response is abandoned rather than trusted.
func readHeaders(r *os.File) ([]byte, error) {
	data := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for len(data) <= headerCap {
		n, err := r.Read(buf)
		data = append(data, buf[:n]...)
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return nil, errors.New("curl response headers were cut off")
			}
			return data, nil // EOF: normal end of the header stream
		}
	}
	return nil, errors.New("curl response headers exceeded the size limit")
}

func fallbackDetail(runErr, headerErr error) string {
	if headerErr != nil {
		return headerErr.Error()
	}
	if runErr != nil {
		return runErr.Error()
	}
	return ""
}

func formatSeconds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}

func secondsToDuration(sec float64) time.Duration {
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec * float64(time.Second))
}

// classifyFailure maps a curl failure to a verdict. Error never proves a
// block: DNS, local setup and anything unrecognised stay error.
func classifyFailure(ctx context.Context, runErr error, stderr string) model.Verdict {
	detail := strings.ToLower(stderr)
	// Local network or routing failure is never evidence of a block.
	if containsAny(detail, "network is unreachable", "no route to host", "network is down") {
		return model.VerdictError
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		switch exitErr.ExitCode() {
		case curlExitTimeout:
			// Covers a silent drop and a stalled partial body alike.
			return model.VerdictBlocked
		case curlExitConnect, curlExitTLS, curlExitReset, curlExitEmpty, curlExitSend, curlExitRecv:
			return model.VerdictBlocked
		case curlExitDNS, curlExitWrite, curlExitLocal:
			return model.VerdictError
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return model.VerdictBlocked
	}
	return model.VerdictError
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
