package testing

import (
	"context"
	gotest "testing"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

type connectivityCaptureDoer struct {
	cfg httpclient.CallConfig
	res *httpclient.Result
	err error
}

func (d *connectivityCaptureDoer) Do(_ context.Context, cfg httpclient.CallConfig) (*httpclient.Result, error) {
	d.cfg = cfg
	return d.res, d.err
}

func TestCheckConnectivityByInterfaceURL_UsesCustomURLAndAcceptsHTTP200(t *gotest.T) {
	orig := httpprobe.Client
	defer func() { httpprobe.Client = orig }()

	doer := &connectivityCaptureDoer{
		res: &httpclient.Result{
			Metrics: httpclient.Metrics{HTTPCode: 200, TimeNameLookup: 0.004, TimeConnect: 0.010, TimeTotal: 0.020},
		},
	}
	httpprobe.Client = doer

	result := CheckConnectivityByInterfaceURL(context.Background(), "wg0", "https://probe.example.net/ping")
	if !result.Connected {
		t.Fatalf("Connected = false, reason=%s", result.Reason)
	}
	if doer.cfg.URL != "https://probe.example.net/ping" {
		t.Fatalf("URL = %q, want custom URL", doer.cfg.URL)
	}
	if doer.cfg.Interface != "wg0" {
		t.Fatalf("Interface = %q, want wg0", doer.cfg.Interface)
	}
	if result.Latency == nil || *result.Latency != 6 {
		t.Fatalf("Latency = %v, want 6ms", result.Latency)
	}
}
