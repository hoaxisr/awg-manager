package monitoring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

type stubRunner struct {
	stdout   string
	exitCode int
	err      error
}

func (s stubRunner) Run(_ context.Context, _ string, _ ...string) (*exec.Result, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &exec.Result{Stdout: s.stdout, ExitCode: s.exitCode}, nil
}

// HTTPProber output format: %{http_code}|%{time_namelookup}|%{time_connect}|%{time_total}.
// Latency = (time_connect - time_namelookup) * 1000 ms.
func TestHTTPProber_ParseLatency(t *testing.T) {
	cases := []struct {
		name     string
		stdout   string
		exitCode int
		err      error
		wantOK   bool
		wantMs   int
	}{
		{
			name:     "ok 200, TCP RTT 12ms",
			stdout:   "200|0.001|0.013|0.020",
			exitCode: 0,
			wantOK:   true,
			wantMs:   12,
		},
		{
			name:     "ok with 404 still reachable, TCP RTT 25ms",
			stdout:   "404|0.002|0.027|0.030",
			exitCode: 0,
			wantOK:   true,
			wantMs:   25,
		},
		{
			name:     "no response — code 0",
			stdout:   "000|0.000|0.000|5.000",
			exitCode: 0,
			wantOK:   false,
		},
		{
			name:     "fallback to time_total when timings invalid",
			stdout:   "200|0.020|0.010|0.030",
			exitCode: 0,
			wantOK:   true,
			wantMs:   30,
		},
		{
			name:   "exec error",
			err:    errors.New("boom"),
			wantOK: false,
		},
		{
			name:     "garbage output",
			stdout:   "no separator here",
			exitCode: 0,
			wantOK:   false,
		},
		{
			name:     "non-numeric code",
			stdout:   "abc|0.001|0.013|0.020",
			exitCode: 0,
			wantOK:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &HTTPProber{Runner: stubRunner{stdout: c.stdout, exitCode: c.exitCode, err: c.err}}
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

// ICMPProber parses `time=NN.N ms` from busybox ping output.
func TestICMPProber_ParseLatency(t *testing.T) {
	cases := []struct {
		name     string
		stdout   string
		exitCode int
		err      error
		wantOK   bool
		wantMs   int
	}{
		{
			name:     "stdout with time=14.2 ms",
			stdout:   "PING 1.1.1.1\n64 bytes from 1.1.1.1: time=14.2 ms",
			exitCode: 0,
			wantOK:   true,
			wantMs:   14,
		},
		{
			name:     "exit code != 0 means failure",
			stdout:   "request timeout",
			exitCode: 1,
			wantOK:   false,
		},
		{
			name:     "exit 0 without timing — floor latency 1ms",
			stdout:   "PING 8.8.8.8\n64 bytes from 8.8.8.8",
			exitCode: 0,
			wantOK:   true,
			wantMs:   1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &ICMPProber{Runner: stubRunner{stdout: c.stdout, exitCode: c.exitCode, err: c.err}}
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
