package warpscout

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

// TraceURL — диагностическая страница Cloudflare. Внутри WARP отвечает
// открытым текстом на порту 80: colo=…, loc=…, warp=on.
const TraceURL = "http://cp.cloudflare.com/cdn-cgi/trace"

// TraceProber снимает trace через интерфейс туннеля.
type TraceProber interface {
	Trace(ctx context.Context, iface string) (TraceResult, error)
}

// HTTPTraceProber — реализация на httpclient (SO_BINDTODEVICE).
type HTTPTraceProber struct {
	Client httpclient.HTTPDoer
}

// Trace запрашивает TraceURL через iface; DNS — тоже через туннель.
func (p *HTTPTraceProber) Trace(ctx context.Context, iface string) (TraceResult, error) {
	c := p.Client
	if c == nil {
		c = httpclient.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	res, err := c.Do(ctx, httpclient.CallConfig{
		URL:            TraceURL,
		Interface:      iface,
		DNSServers:     []string{"1.1.1.1", "1.0.0.1"},
		ConnectTimeout: 6 * time.Second,
		MaxTime:        10 * time.Second,
	})
	if err != nil {
		return TraceResult{}, err
	}
	if res.Metrics.HTTPCode != 200 {
		return TraceResult{}, errors.New("trace: HTTP " + strconv.Itoa(res.Metrics.HTTPCode))
	}
	tr := ParseTrace(res.Body)
	if tr.Colo == "" {
		return tr, errors.New("trace: в ответе нет colo")
	}
	if res.Metrics.TimeConnect > 0 && res.Metrics.TimeConnect >= res.Metrics.TimeNameLookup {
		tr.LatencyMs = httpclient.SecToMs(res.Metrics.TimeConnect - res.Metrics.TimeNameLookup)
	} else {
		tr.LatencyMs = httpclient.SecToMs(res.Metrics.TimeTotal)
	}
	if tr.LatencyMs <= 0 && res.Metrics.TimeTotal > 0 {
		tr.LatencyMs = 1
	}
	return tr, nil
}

// ParseTrace разбирает тело cdn-cgi/trace.
func ParseTrace(body string) TraceResult {
	var tr TraceResult
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "colo":
			tr.Colo = strings.ToUpper(v)
		case "loc":
			tr.Loc = strings.ToUpper(v)
		case "warp":
			tr.Warp = v
		case "ip":
			tr.IP = v
		}
	}
	return tr
}
