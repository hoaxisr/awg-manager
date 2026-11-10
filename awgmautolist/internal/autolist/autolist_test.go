package autolist

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- registeredDomain --------------------------------------------------------

func TestRegisteredDomain(t *testing.T) {
	cases := map[string]string{
		"www.google.com":               "google.com",
		"google.com":                   "google.com",
		"a.b.google.co.uk":             "google.co.uk",
		"example.com.":                 "example.com",
		"1.2.3.4":                      "",         // IP — не домен
		"user.github.io":               "",         // shared-хостинг
		"bucket.amazonaws.com":         "",         // shared-хостинг
		"myapp.pages.dev":              "",         // shared-хостинг
		"sub.myapp.vercel.app":         "",         // shared-хостинг
		"site.tilda.ws":                "",         // shared-хостинг
		"something.pages.dev.evil.com": "evil.com", // shared внутри, но суффикс другой
	}
	for host, want := range cases {
		if got := registeredDomain(host); got != want {
			t.Errorf("registeredDomain(%q) = %q, want %q", host, got, want)
		}
	}
}

// --- privateIP ---------------------------------------------------------------

func TestPrivateIP(t *testing.T) {
	priv := []string{"192.168.1.1", "10.0.0.5", "172.16.0.9", "127.0.0.1",
		"100.64.0.1", "198.18.0.5", "198.19.255.1", "169.254.1.1", "fe80::1", "fd00::5"}
	for _, ip := range priv {
		if !privateIP(ip) {
			t.Errorf("%s должен считаться приватным", ip)
		}
	}
	pub := []string{"142.250.74.36", "1.1.1.1", "188.114.96.3", "2606:4700::1111"}
	for _, ip := range pub {
		if privateIP(ip) {
			t.Errorf("%s не должен считаться приватным", ip)
		}
	}
}

// --- Prober ------------------------------------------------------------------

func TestProbeReachableFullResponse(t *testing.T) {
	p := &Prober{CurlPath: fakeCurl(t, fmt.Sprintf("printf '%%s' '%s'", strings.Repeat("x", 40000)))}
	got := p.Probe(context.Background(), "example.com")
	if got.Verdict != VerdictReachable {
		t.Errorf("verdict = %v, want reachable (err=%v)", got.Verdict, got.Err)
	}
}

func TestProbeReachableSmallComplete(t *testing.T) {
	p := &Prober{CurlPath: fakeCurl(t, "printf ok")}
	got := p.Probe(context.Background(), "example.com")
	if got.Verdict != VerdictReachable || !got.ShortBody {
		t.Errorf("маленький полный ответ = %+v, want reachable/shortBody", got)
	}
}

func TestProbeBlockedReset(t *testing.T) {
	p := &Prober{CurlPath: fakeCurl(t, "printf 'read: connection reset by peer\\n' >&2\nexit 35")}
	got := p.Probe(context.Background(), "example.com")
	if got.Verdict != VerdictBlocked || got.Why != WhyReset {
		t.Errorf("verdict=%v why=%v, want blocked/reset", got.Verdict, got.Why)
	}
}

func TestProbeBlockedTimeout(t *testing.T) {
	p := &Prober{CurlPath: fakeCurl(t, "printf 'i/o timeout\\n' >&2\nexit 28")}
	got := p.Probe(context.Background(), "example.com")
	if got.Verdict != VerdictBlocked || got.Why != WhyNoAnswer {
		t.Errorf("verdict=%v why=%v, want blocked/no-answer", got.Verdict, got.Why)
	}
}

type errTimeout struct{}

func (errTimeout) Error() string   { return "i/o timeout" }
func (errTimeout) Timeout() bool   { return true }
func (errTimeout) Temporary() bool { return true }

func TestProbeBareIPNotProbed(t *testing.T) {
	p := &Prober{}
	got := p.Probe(context.Background(), "188.114.96.3")
	if got.Verdict != VerdictUnknown {
		t.Errorf("голый IP должен давать unknown, got %v", got.Verdict)
	}
}

// fakeCurl — исполняемый shell-сценарий для тестов curl-пробы.
func fakeCurl(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "curl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCurlProbePinsObservedIP(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	curl := fakeCurl(t, fmt.Sprintf("printf '%%s\\n' \"$@\" > %q\nprintf 'ok'\n", argsFile))
	p := &Prober{CurlPath: curl}
	got := p.ProbeAt(context.Background(), "pocket-id.org", "203.0.113.10")
	if got.Verdict != VerdictReachable {
		t.Fatalf("complete curl response = %+v", got)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "pocket-id.org:443:203.0.113.10") {
		t.Fatalf("наблюдаемый IP не передан curl --resolve: %s", args)
	}
}

func TestCurlProbeFreezeStall(t *testing.T) {
	curl := fakeCurl(t, fmt.Sprintf("printf '%%s' '%s'\nexec sleep 30\n", strings.Repeat("x", 16*1024)))
	p := &Prober{CurlPath: curl}
	got := p.ProbeAt(context.Background(), "example.com", "203.0.113.10")
	if got.Verdict != VerdictBlocked || got.Why != WhyFreeze || got.Bytes != 16*1024 {
		t.Fatalf("curl stall = %+v, want blocked/freeze/16384", got)
	}
}

func TestCurlProbeSlowBelowStallBytes(t *testing.T) {
	curl := fakeCurl(t, "printf x\nexec sleep 30\n")
	p := &Prober{CurlPath: curl}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got := p.ProbeAt(ctx, "example.com", "203.0.113.10")
	if got.Verdict != VerdictBlocked || got.Why != WhyFreeze {
		t.Fatalf("curl slow timeout = %+v, want blocked/freeze", got)
	}
}

func TestProbeWaitsForFirstByteBeyondStall(t *testing.T) {
	curl := fakeCurl(t, "sleep 6\nprintf ok\n")
	p := &Prober{CurlPath: curl}
	got := p.ProbeAt(context.Background(), "example.com", "203.0.113.10")
	if got.Verdict != VerdictReachable {
		t.Fatalf("первый байт после 5 с не stall; ждём общий timeout: %+v", got)
	}
}

// --- Table -------------------------------------------------------------------

func TestTableNoteConfirmLifecycle(t *testing.T) {
	now := time.Now()
	tbl := NewTable()
	h := Host{Name: "www.example.com", IP: "1.2.3.4"}

	if !tbl.Note(h, WhyReset, "dial", now) {
		t.Fatal("первый Note должен вернуть true (новый)")
	}
	if !tbl.Note(h, WhyReset, "dial", now) {
		t.Fatal("повторная номинация существующей строки тоже принимается")
	}
	rows := tbl.List()
	if len(rows) != 1 || rows[0].Domain != "example.com" || rows[0].Fails != 2 {
		t.Fatalf("строки = %+v", rows)
	}

	// reachable в состоянии candidate — строка исчезает
	tbl.Confirm("example.com", VerdictReachable, now)
	if len(tbl.List()) != 0 {
		t.Fatal("reachable должен был удалить кандидата")
	}

	// dismissed прячет на сутки
	tbl.Note(h, WhyReset, "dial", now)
	tbl.Dismiss("example.com", now)
	if tbl.Note(h, WhyReset, "dial", now) || len(tbl.List()) != 0 {
		t.Fatal("dismissed не должен пускать кандидата сутки")
	}
}
