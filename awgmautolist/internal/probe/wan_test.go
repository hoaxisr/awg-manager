package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// netport extracts the numeric port from a "host:port" address string.
func netport(addr string) (int, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(port)
}

func TestWANAnyNonzeroStatusIsReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "still online", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	got := CheckWAN(context.Background(), server.URL, Path{})
	if got.Verdict != model.VerdictReachable {
		t.Fatalf("result=%+v, want reachable on HTTP 503", got)
	}
	if got.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", got.HTTPStatus)
	}
}

func TestWANNoResponseIsError(t *testing.T) {
	addr, err := netListenClosed()
	if err != nil {
		t.Fatal(err)
	}
	got := CheckWAN(context.Background(), "http://"+addr, Path{})
	if got.Verdict != model.VerdictError {
		t.Fatalf("result=%+v, want error when nothing answers", got)
	}
}

func TestWANEmptyURLIsError(t *testing.T) {
	got := CheckWAN(context.Background(), "   ", Path{})
	if got.Verdict != model.VerdictError {
		t.Fatalf("result=%+v, want error for an empty URL", got)
	}
}

func TestWANTimeoutIsBlocked(t *testing.T) {
	old := probeTimeout
	probeTimeout = 400 * time.Millisecond
	t.Cleanup(func() { probeTimeout = old })

	// A listener that accepts but never answers.
	listener := blockingListener(t)
	got := CheckWAN(context.Background(), "http://"+listener.Addr().String(), Path{})
	if got.Verdict != model.VerdictBlocked {
		t.Fatalf("result=%+v, want blocked on timeout", got)
	}
}

// TestDirectAndWANRunsConcurrently proves both checks overlap: the target
// probe hangs on a silent TLS server while the WAN check completes; the
// total stays near one probe timeout, not two.
func TestDirectAndWANRunsConcurrently(t *testing.T) {
	old := probeTimeout
	probeTimeout = 1500 * time.Millisecond
	t.Cleanup(func() { probeTimeout = old })

	// Slow target: a TCP listener that never answers TLS, so the direct
	// probe only finishes at the deadline. stubPort points the probe at
	// this listener instead of 443.
	targetListener := blockingListener(t)
	stubPort(t, targetPort(targetListener))
	target, err := model.NewTarget("", "127.0.0.1", model.NetworkTCP, uint16(targetPort(targetListener)))
	if err != nil {
		t.Fatal(err)
	}

	wan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer wan.Close()

	started := time.Now()
	out := DirectAndWAN(context.Background(), target, wan.URL, Path{})
	elapsed := time.Since(started)

	if out.Direct.Verdict != model.VerdictBlocked {
		t.Fatalf("direct=%+v, want blocked", out.Direct)
	}
	if out.WAN.Verdict != model.VerdictReachable {
		t.Fatalf("wan=%+v, want reachable", out.WAN)
	}
	// The direct probe pays the full 1.5s timeout; if the WAN check ran
	// sequentially after it, the total would clearly exceed the deadline.
	if elapsed < 1200*time.Millisecond {
		t.Fatalf("elapsed=%v, want the direct probe to pay its timeout", elapsed)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("elapsed=%v, want the WAN check to overlap the target probe", elapsed)
	}
}

// TestDirectAndWANBlockedTargetKeepsWANVerdict shows the pairing purpose:
// a blocked target does not corrupt an independent WAN verdict.
func TestDirectAndWANBlockedTargetKeepsWANVerdict(t *testing.T) {
	probeTimeout = 500 * time.Millisecond
	t.Cleanup(func() { probeTimeout = 5 * time.Second })

	wan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "degraded", http.StatusServiceUnavailable)
	}))
	defer wan.Close()

	dead, err := netListenClosed()
	if err != nil {
		t.Fatal(err)
	}
	port, parseErr := netport(dead)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	target, err := model.NewTarget("", "127.0.0.1", model.NetworkTCP, uint16(port))
	if err != nil {
		t.Fatal(err)
	}

	out := DirectAndWAN(context.Background(), target, wan.URL, Path{})
	if out.Direct.Verdict != model.VerdictBlocked {
		t.Fatalf("direct=%+v, want blocked", out.Direct)
	}
	if out.WAN.Verdict != model.VerdictReachable || out.WAN.HTTPStatus != 503 {
		t.Fatalf("wan=%+v, want reachable/503", out.WAN)
	}
}
