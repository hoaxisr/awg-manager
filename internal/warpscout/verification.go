package warpscout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

const (
	verificationVersion       = 3
	verificationRuns          = 3
	speedBytes          int64 = 128 * 1024
	probeDownloadBytes  int64 = 16 * 1024
)

var (
	traceHTTPSURL  = "https://www.cloudflare.com/cdn-cgi/trace"
	generate204URL = "https://www.gstatic.com/generate_204"
	speedDownURL   = "https://speed.cloudflare.com/__down"
	tcpPingTarget  = "1.1.1.1:443"
)

// ErrRollback signals that restoring the previous working tunnel config failed.
var ErrRollback = errors.New("не удалось полностью восстановить прежний туннель")

// VerificationResult stores the verified tunnel traffic, TCP latency, speed, and trace.
type VerificationResult struct {
	VerificationVersion int         `json:"verificationVersion"`
	Trace               TraceResult `json:"trace"`
	Endpoint            string      `json:"endpoint,omitempty"`
	Protocol            string      `json:"protocol,omitempty"`
	PingMs              int         `json:"pingMs"`
	DownloadMbps        float64     `json:"downloadMbps"`
	Samples             []float64   `json:"samples"`
	Bytes               int64       `json:"bytes"`
	TestedAt            time.Time   `json:"testedAt"`
}

func CheckMeasurement(r *VerificationResult, filters Filters) error {
	if err := validateMeasurement(r); err != nil {
		return err
	}
	if filters.RequireTelegram && !r.Trace.TelegramOK {
		return errors.New("MTProto resPQ missing")
	}
	if reason, _ := evaluate(r.Trace, nil, filters); reason != "" {
		return errors.New(reason)
	}
	return nil
}

func validateMeasurement(r *VerificationResult) error {
	if r == nil ||
		r.VerificationVersion != verificationVersion ||
		len(r.Samples) != verificationRuns ||
		r.Bytes != verificationRuns*speedBytes ||
		r.DownloadMbps <= 0 ||
		math.IsNaN(r.DownloadMbps) ||
		math.IsInf(r.DownloadMbps, 0) {
		return errors.New("нет трёх полных HTTPS-загрузок; узел не подтверждён")
	}
	for _, n := range r.Samples {
		if n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return errors.New("некорректный замер скорости")
		}
	}
	if r.Trace.Warp != "on" && r.Trace.Warp != "plus" {
		return errors.New("выход через WARP не подтверждён")
	}
	if r.PingMs <= 0 || r.Trace.LatencyMs <= 0 || r.Trace.LatencyKind != "tcp" {
		return errors.New("задержка TCP внутри туннеля не измерена")
	}
	return nil
}

func measureSample(ctx context.Context, client *http.Client, size int64) (*VerificationResult, error) {
	get := func(url string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Cache-Control", "no-cache, no-store")
		req.Header.Set("Accept-Encoding", "identity")
		req.Close = true
		return client.Do(req)
	}

	start := time.Now()
	resp, err := get(traceHTTPSURL)
	if err != nil {
		return nil, fmt.Errorf("DNS/TLS: HTTPS через туннель не прошёл: %w", err)
	}
	b, readErr := io.ReadAll(io.LimitReader(resp.Body, 8193))
	resp.Body.Close()
	if readErr != nil || resp.StatusCode != http.StatusOK || len(b) > 8192 {
		return nil, errors.New("trace: неполный или неверный ответ HTTPS")
	}
	trace := ParseTrace(string(b))
	if trace.Colo == "" || (trace.Warp != "on" && trace.Warp != "plus") {
		return nil, errors.New("trace: трафик не подтверждён как WARP")
	}
	trace.HTTPSMs = max(1, int(time.Since(start)/time.Millisecond))

	resp, err = get(generate204URL)
	if err != nil {
		return nil, fmt.Errorf("внешний HTTPS: %w", err)
	}
	n, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
	resp.Body.Close()
	if readErr != nil || resp.StatusCode != http.StatusNoContent || n != 0 {
		return nil, errors.New("внешний HTTPS: ожидаемый ответ 204 не получен")
	}

	return measureDownload(ctx, client, size, trace)
}

func measureDownload(ctx context.Context, client *http.Client, size int64, trace TraceResult) (*VerificationResult, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s?bytes=%d&nonce=%d", speedDownURL, size, time.Now().UnixNano()), nil)
	if err != nil {
		return nil, err
	}
	req.Close = true
	req.Header.Set("Cache-Control", "no-cache, no-store")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("загрузка HTTPS: %w", err)
	}
	n, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, size+1))
	resp.Body.Close()
	elapsed := time.Since(start).Seconds()
	if readErr != nil ||
		resp.StatusCode != http.StatusOK ||
		n != size ||
		elapsed <= 0 ||
		(resp.ContentLength >= 0 && resp.ContentLength != size) ||
		(resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return nil, fmt.Errorf("загрузка HTTPS: получено %d из %d байт, HTTP %d", n, size, resp.StatusCode)
	}
	mbps := float64(n) * 8 / elapsed / 1e6

	dial, ok := dialContextFromTransport(client.Transport)
	if !ok {
		return nil, errors.New("нет привязанного к туннелю измерителя TCP")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start = time.Now()
	c, err := dial(probeCtx, "tcp4", tcpPingTarget)
	if err != nil {
		return nil, fmt.Errorf("отклик TCP через WARP: %w", err)
	}
	trace.LatencyMs = max(1, int(time.Since(start)/time.Millisecond))
	_ = c.Close()
	trace.LatencyKind = "tcp"
	return &VerificationResult{Trace: trace, PingMs: trace.LatencyMs, Samples: []float64{mbps}, Bytes: n, DownloadMbps: mbps}, nil
}

func dialContextFromTransport(rt http.RoundTripper) (func(context.Context, string, string) (net.Conn, error), bool) {
	tr, ok := rt.(*http.Transport)
	if !ok || tr.DialContext == nil {
		return nil, false
	}
	return tr.DialContext, true
}

func measureThree(ctx context.Context, newSample func(context.Context) (*VerificationResult, error)) (*VerificationResult, error) {
	r := &VerificationResult{VerificationVersion: verificationVersion}
	var seconds float64
	for i := 0; i < verificationRuns; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		one, err := newSample(ctx)
		if err != nil {
			return nil, fmt.Errorf("проверка %d/%d: %w", i+1, verificationRuns, err)
		}
		if one == nil || one.Bytes != speedBytes || len(one.Samples) != 1 || one.DownloadMbps <= 0 {
			return nil, errors.New("неполный замер")
		}
		r.Trace = one.Trace
		r.PingMs += one.PingMs
		r.Bytes += one.Bytes
		r.Samples = append(r.Samples, one.DownloadMbps)
		seconds += float64(one.Bytes) * 8 / one.DownloadMbps / 1e6
	}
	r.PingMs = max(1, r.PingMs/verificationRuns)
	r.Trace.LatencyMs = r.PingMs
	r.DownloadMbps = float64(r.Bytes) * 8 / seconds / 1e6
	r.TestedAt = time.Now()
	return r, validateMeasurement(r)
}

func measureTunnel(ctx context.Context, client *http.Client) (*VerificationResult, error) {
	var trace *TraceResult
	return measureThree(ctx, func(ctx context.Context) (*VerificationResult, error) {
		if trace == nil {
			r, err := measureSample(ctx, client, speedBytes)
			if err == nil {
				trace = &r.Trace
			}
			return r, err
		}
		return measureDownload(ctx, client, speedBytes, *trace)
	})
}

// VerifyInterfaceChecks validates strict IPv4 DNS/TLS/HTTPS traffic and optional MTProto
// through the live tunnel interface without falling back to WAN, system DNS, or proxies.
func VerifyInterfaceChecks(ctx context.Context, iface string, full, telegram bool) (*VerificationResult, error) {
	if iface == "" {
		return nil, errors.New("не назначен интерфейс проверки")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tr, err := httpclient.NewStrictIPv4Transport(iface, []string{"1.1.1.1", "1.0.0.1"})
	if err != nil {
		return nil, err
	}
	defer tr.CloseIdleConnections()
	tr.DisableCompression = true
	tr.DisableKeepAlives = true
	return verifyWithTransport(ctx, tr, full, telegram)
}

// VerifyProbeTransport measures traffic inside a temporary candidate tunnel.
func VerifyProbeTransport(ctx context.Context, tr *http.Transport, telegram bool) (*VerificationResult, error) {
	return verifyWithTransport(ctx, tr, true, telegram)
}

func verifyWithTransport(ctx context.Context, tr *http.Transport, full, telegram bool) (*VerificationResult, error) {
	client := &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	var (
		result *VerificationResult
		err    error
	)
	if full {
		result, err = measureTunnel(ctx, client)
	} else {
		result, err = measureSample(ctx, client, probeDownloadBytes)
	}
	if err == nil && telegram && result != nil {
		if dial, ok := dialContextFromTransport(tr); ok {
			err = checkTelegram(ctx, dial)
			result.Trace.TelegramOK = err == nil
		}
	}
	return result, err
}
