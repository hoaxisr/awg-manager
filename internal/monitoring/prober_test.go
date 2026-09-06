package monitoring

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/icmpprobe"
)

// HTTPProber: ok=true + positive latency on 2xx/3xx, ok=false on 5xx and on
// a dead endpoint. No interface binding in tests (empty ifaceName).
func TestHTTPProber_Probe(t *testing.T) {
	code := atomic.Int32{}
	code.Store(http.StatusNoContent)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(code.Load()))
	}))
	defer srv.Close()

	p := NewHTTPProber()
	ms, ok := p.Probe(context.Background(), srv.URL+"/generate_204", "", 5*time.Second)
	if !ok {
		t.Fatal("Probe() ok = false, want true for 204")
	}
	if ms < 1 {
		t.Errorf("latency = %d, want >= 1", ms)
	}

	code.Store(http.StatusInternalServerError)
	if _, ok := p.Probe(context.Background(), srv.URL+"/generate_204", "", 5*time.Second); ok {
		t.Error("Probe() ok = true, want false for 500")
	}

	dead := srv.URL
	srv.Close()
	if _, ok := p.Probe(context.Background(), dead+"/generate_204", "", 2*time.Second); ok {
		t.Error("Probe() ok = true, want false for closed server")
	}
}

func TestICMPProber_Probe(t *testing.T) {
	cases := []struct {
		name   string
		res    icmpprobe.Result
		err    error
		wantOK bool
		wantMs int
	}{
		{name: "success", res: icmpprobe.Result{LatencyMs: 14}, wantOK: true, wantMs: 14},
		{name: "probe error means failure", err: errors.New("no reply"), wantOK: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &ICMPProber{Pinger: func(context.Context, string, string, []string) (icmpprobe.Result, error) {
				return c.res, c.err
			}}
			ms, ok := p.Probe(context.Background(), "1.1.1.1", "wg0", 5*time.Second)
			if ok != c.wantOK {
				t.Errorf("ok = %v, want %v", ok, c.wantOK)
			}
			if c.wantOK && ms != c.wantMs {
				t.Errorf("latency = %d, want %d", ms, c.wantMs)
			}
		})
	}
}
