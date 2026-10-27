package pingcheck

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

type checkerCaptureDoer struct {
	cfg httpclient.CallConfig
	res *httpclient.Result
	err error
}

func (d *checkerCaptureDoer) Do(_ context.Context, cfg httpclient.CallConfig) (*httpclient.Result, error) {
	d.cfg = cfg
	return d.res, d.err
}

func TestPerformCheckHTTPUsesCustomURLAndAcceptsHTTP200(t *testing.T) {
	orig := httpprobe.Client
	defer func() { httpprobe.Client = orig }()

	doer := &checkerCaptureDoer{
		res: &httpclient.Result{
			Metrics: httpclient.Metrics{HTTPCode: 200, TimeNameLookup: 0.0101, TimeConnect: 0.0304, TimeTotal: 0.050},
		},
	}
	httpprobe.Client = doer

	result := performCheck(context.Background(), "wg0", "http", "", "https://probe.example.net/ping", nil)
	if !result.Success {
		t.Fatalf("Success = false, error=%s", result.Error)
	}
	if doer.cfg.URL != "https://probe.example.net/ping" {
		t.Fatalf("URL = %q, want custom URL", doer.cfg.URL)
	}
	if doer.cfg.Interface != "wg0" {
		t.Fatalf("Interface = %q, want wg0", doer.cfg.Interface)
	}
	if result.Latency != 20 {
		t.Fatalf("Latency = %d, want 20ms", result.Latency)
	}
}

// Цель из настроек туннеля важнее общего адреса: прежде HTTP-проверка
// молча игнорировала её, а карточка показывала именно её.
func TestPerformCheckHTTPUsesTunnelTarget(t *testing.T) {
	orig := httpprobe.Client
	defer func() { httpprobe.Client = orig }()

	const global = "https://cp.cloudflare.com/generate_204"
	cases := []struct{ target, want string }{
		{"", global},
		{"  ", global},
		{"8.8.8.8", global}, // умолчание ICMP, разложенное по записям с http
		{"2001:db8::1", global},
		{"web.telegram.org", "https://web.telegram.org"},
		{"https://web.telegram.org/", "https://web.telegram.org/"},
		{"http://probe.example.net/ping", "http://probe.example.net/ping"},
	}
	for _, c := range cases {
		doer := &checkerCaptureDoer{res: &httpclient.Result{Metrics: httpclient.Metrics{HTTPCode: 204}}}
		httpprobe.Client = doer
		performCheck(context.Background(), "wg0", "http", c.target, global, nil)
		if doer.cfg.URL != c.want {
			t.Errorf("target %q: URL = %q, want %q", c.target, doer.cfg.URL, c.want)
		}
	}
}
