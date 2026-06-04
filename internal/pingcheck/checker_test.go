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

	result := performCheck(context.Background(), "wg0", "http", "", "https://probe.example.net/ping")
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
