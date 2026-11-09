package autolist

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func redirectCountScript(t *testing.T, first, later string) (curlPath string, count func() int) {
	t.Helper()
	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	script := fmt.Sprintf(`n=0
[ -f %q ] && n=$(cat %q)
n=$((n+1))
printf '%%s' "$n" > %q
if [ "$n" -eq 1 ]; then
%s
else
%s
fi
`, countPath, countPath, countPath, first, later)
	return fakeCurl(t, script), func() int {
		data, err := os.ReadFile(countPath)
		if err != nil {
			return 0
		}
		var n int
		_, _ = fmt.Sscanf(string(data), "%d", &n)
		return n
	}
}

func TestRealCurlReadsHeadersThroughDescriptor(t *testing.T) {
	curlPath, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is not installed")
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Location", "/next")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	p := &Prober{}
	result, response := p.curlOnce(context.Background(), srv.URL+"/", "127.0.0.1", "", curlPath, 5*time.Second)
	if result.Verdict != VerdictReachable || response.status != http.StatusFound || response.location != "/next" || requests != 1 {
		t.Fatalf("real curl header FD: result=%+v response=%+v requests=%d", result, response, requests)
	}
}

func TestRedirectGuardSurvivesBodyCap(t *testing.T) {
	script := fmt.Sprintf(`printf 'HTTP/1.1 302 Found\r\nLocation: https://outside.test/\r\n\r\n' >&3
printf '%%s' '%s'
`, strings.Repeat("x", ProbeBodyCap))
	p := &Prober{CurlPath: fakeCurl(t, script)}
	got := p.Probe(context.Background(), "example.com")
	if got.Verdict != VerdictUnknown || !strings.Contains(got.Err, "registered domain") {
		t.Fatalf("external redirect at body cap = %+v, want unknown domain error", got)
	}
}

func TestRedirectSupportsRelativeLocation(t *testing.T) {
	curl, count := redirectCountScript(t,
		`printf 'HTTP/1.1 302 Found\r\nLocation: /next\r\n\r\n' >&3`,
		`printf 'HTTP/1.1 200 OK\r\n\r\n' >&3
printf ok`,
	)
	p := &Prober{CurlPath: curl}
	got := p.Probe(context.Background(), "example.com")
	if got.Verdict != VerdictReachable || count() != 2 {
		t.Fatalf("relative redirect = %+v, requests=%d", got, count())
	}
}

func TestRedirectRejectsMissingAndInvalidLocation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
	}{
		{name: "missing", header: `HTTP/1.1 302 Found\r\n\r\n`},
		{name: "empty", header: `HTTP/1.1 302 Found\r\nLocation:\r\n\r\n`},
		{name: "invalid", header: `HTTP/1.1 302 Found\r\nLocation: http://[::1\r\n\r\n`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := fmt.Sprintf("printf '%s' >&3\n", tc.header)
			p := &Prober{CurlPath: fakeCurl(t, script)}
			got := p.Probe(context.Background(), "example.com")
			if got.Verdict != VerdictUnknown || !strings.Contains(got.Err, "Location") {
				t.Fatalf("bad redirect = %+v, want explicit Location error", got)
			}
		})
	}
}

func TestRedirectLimit(t *testing.T) {
	p := &Prober{CurlPath: fakeCurl(t, `printf 'HTTP/1.1 302 Found\r\nLocation: /again\r\n\r\n' >&3
`)}
	got := p.Probe(context.Background(), "example.com")
	if got.Verdict != VerdictUnknown || !strings.Contains(got.Err, "limit") {
		t.Fatalf("redirect loop = %+v, want limit error", got)
	}
}

func TestRedirectChainUsesOneTotalTimeout(t *testing.T) {
	curl, count := redirectCountScript(t,
		`printf 'HTTP/1.1 302 Found\r\nLocation: /second\r\n\r\n' >&3`,
		`printf 'HTTP/1.1 302 Found\r\nLocation: /third\r\n\r\n' >&3
exec sleep 5`,
	)
	p := &Prober{CurlPath: curl, TimeoutSec: 1}
	started := time.Now()
	got := p.Probe(context.Background(), "example.com")
	if got.Verdict != VerdictUnknown || !strings.Contains(got.Err, "total probe timeout") {
		t.Fatalf("redirect timeout = %+v", got)
	}
	if count() != 2 || time.Since(started) > 3*time.Second {
		t.Fatalf("timeout restarted per request: requests=%d elapsed=%v", count(), time.Since(started))
	}
}

func TestTunnelRedirectGuardPreventsAutoAdd(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	eng.prober.CurlPath = fakeCurl(t, "printf 'i/o timeout\\n' >&2\nexit 28")
	tunnelScript := fmt.Sprintf(`printf 'HTTP/1.1 302 Found\r\nLocation: https://outside.test/\r\n\r\n' >&3
printf '%%s' '%s'
`, strings.Repeat("x", ProbeBodyCap))
	eng.tunnelProber = &Prober{CurlPath: fakeCurl(t, tunnelScript)}
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	eng.table.Note(Host{Name: "www.redirect.example"}, WhyReset, "packet", time.Now())
	if !eng.enqueue(probeTask{host: "www.redirect.example", domain: "redirect.example", source: "packet"}) {
		t.Fatal("probe was not queued")
	}
	task, ok := eng.nextProbe()
	if !ok {
		t.Fatal("probe was not issued")
	}
	eng.probeOneContext(context.Background(), task)
	row, exists := eng.table.get("redirect.example")
	if !exists || row.TunnelVerdict != VerdictUnknown.String() || len(awgm.addedRules()) != 0 {
		t.Fatalf("external tunnel redirect permitted addition: row=%+v added=%v", row, awgm.addedRules())
	}
}
