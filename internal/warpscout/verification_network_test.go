package warpscout

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestVerifyWithTransport_FullAndShortDownloadValidation(t *testing.T) {
	var shortDownload atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cdn-cgi/trace":
			_, _ = w.Write([]byte("colo=HEL\nloc=FI\nwarp=on\nip=172.16.0.2\n"))
		case "/generate_204":
			w.WriteHeader(http.StatusNoContent)
		case "/__down":
			n := int(speedBytes)
			if r.URL.Query().Get("bytes") == "16384" {
				n = int(probeDownloadBytes)
			}
			if shortDownload.Load() {
				w.Header().Set("Content-Length", "131072")
				_, _ = w.Write(make([]byte, 1024))
				return
			}
			_, _ = w.Write(make([]byte, n))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldTrace, old204, oldDown, oldPing := traceHTTPSURL, generate204URL, speedDownURL, tcpPingTarget
	traceHTTPSURL = srv.URL + "/cdn-cgi/trace"
	generate204URL = srv.URL + "/generate_204"
	speedDownURL = srv.URL + "/__down"
	tcpPingTarget = srv.Listener.Addr().String()
	t.Cleanup(func() {
		traceHTTPSURL, generate204URL, speedDownURL, tcpPingTarget = oldTrace, old204, oldDown, oldPing
	})

	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	tr.DialContext = (&net.Dialer{}).DialContext

	res, err := verifyWithTransport(context.Background(), tr, true, false)
	if err != nil {
		t.Fatalf("full verify failed: %v", err)
	}
	if err := CheckMeasurement(res, Filters{}); err != nil {
		t.Fatalf("CheckMeasurement failed: %v", err)
	}
	if res.Bytes != verificationRuns*speedBytes || len(res.Samples) != verificationRuns || res.Trace.LatencyKind != "tcp" {
		t.Fatalf("unexpected verification result: %+v", res)
	}

	shortDownload.Store(true)
	if _, err := verifyWithTransport(context.Background(), tr, true, false); err == nil {
		t.Fatal("expected short download to fail")
	}
}

func TestVerifyWithTransport_RejectsDirectNonWarpTraffic(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("colo=HEL\nloc=FI\nwarp=off\n"))
	}))
	defer srv.Close()

	oldTrace := traceHTTPSURL
	traceHTTPSURL = srv.URL + "/cdn-cgi/trace"
	t.Cleanup(func() { traceHTTPSURL = oldTrace })

	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	tr.DialContext = (&net.Dialer{}).DialContext

	_, err := verifyWithTransport(context.Background(), tr, false, false)
	if err == nil || !strings.Contains(err.Error(), "WARP") {
		t.Fatalf("expected non-WARP failure, got: %v", err)
	}
}
