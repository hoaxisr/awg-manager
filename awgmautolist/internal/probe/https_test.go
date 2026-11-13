package probe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// bigPayload is just over bodyLimit so a serving handler proves the
// "more than 30 KiB" path.
var bigPayload = strings.Repeat("a", bodyLimit+1)

// tcpTarget builds a TCP target pinned to the local test listener.
func tcpTarget(t *testing.T, port int) model.Target {
	t.Helper()
	target, err := model.NewTarget("", "127.0.0.1", model.NetworkTCP, uint16(port))
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// localTLSServer is an HTTPS test server that trusts nobody; the probe runs
// curl with --insecure, so the self-signed certificate is fine.
func localTLSServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestHTTPSReachableCompleteResponse(t *testing.T) {
	server := localTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	})
	port := server.Listener.Addr().(*net.TCPAddr).Port
	stubPort(t, port)

	got := Check(context.Background(), tcpTarget(t, port), Path{})
	if got.Verdict != model.VerdictReachable || got.HTTPStatus != 200 {
		t.Fatalf("result=%+v, want reachable/200", got)
	}
	if got.Bytes != 5 {
		t.Fatalf("bytes=%d, want 5 (body only, curl write-out trailer excluded)", got.Bytes)
	}
	if got.TimeConnect <= 0 {
		t.Fatalf("missing connect timing: %+v", got)
	}
}

func TestHTTPSReachableLargeBody(t *testing.T) {
	server := localTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(bigPayload))
	})
	port := server.Listener.Addr().(*net.TCPAddr).Port
	stubPort(t, port)

	got := Check(context.Background(), tcpTarget(t, port), Path{})
	if got.Verdict != model.VerdictReachable {
		t.Fatalf("result=%+v, want reachable", got)
	}
	if got.Bytes <= bodyLimit {
		t.Fatalf("bytes=%d, want more than %d", got.Bytes, bodyLimit)
	}
}

func TestHTTPSBlockedConnectRefused(t *testing.T) {
	// Nothing listens on a freshly allocated port after the listener closes.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	got := Check(context.Background(), tcpTarget(t, port), Path{})
	if got.Verdict != model.VerdictBlocked {
		t.Fatalf("result=%+v, want blocked", got)
	}
}

func TestHTTPSBlockedTimeout(t *testing.T) {
	old := probeTimeout
	probeTimeout = 700 * time.Millisecond
	t.Cleanup(func() { probeTimeout = old })

	// A TCP listener that accepts but never answers: connect succeeds and
	// the TLS handshake hangs until the probe deadline.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	stubPort(t, port)

	got := Check(context.Background(), tcpTarget(t, port), Path{})
	if got.Verdict != model.VerdictBlocked {
		t.Fatalf("result=%+v, want blocked", got)
	}
}

func TestHTTPSDNSErrorIsError(t *testing.T) {
	target, err := model.NewTarget("probe-test.invalid", "", model.NetworkTCP, uint16(httpsPort))
	if err != nil {
		t.Fatal(err)
	}
	got := Check(context.Background(), target, Path{})
	if got.Verdict != model.VerdictError {
		t.Fatalf("result=%+v, want error for DNS failure", got)
	}
}

// TestHTTPSRedirectFollowedPinnedHost covers the one redirect case that is
// safe by construction: the Location keeps the original host, so the pinned
// observed IP still governs the connection.
func TestHTTPSRedirectFollowedPinnedHost(t *testing.T) {
	server := localTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("destination"))
	})
	port := server.Listener.Addr().(*net.TCPAddr).Port
	stubPort(t, port)

	// Fake curl: first call answers 302 with a Location that keeps the
	// host of the probe URL; the second call is a normal 200 response.
	// Response headers go to fd 3, the write-out trailer to stdout.
	// The fake curl keeps its call counter in a fresh directory; $TMPDIR is
	// shared on macOS and would leak state between test runs.
	state := t.TempDir()
	fakeCurl(t, fmt.Sprintf(`
n=$(cat %[1]s/count 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > %[1]s/count
if [ "$n" -eq 1 ]; then
  printf 'HTTP/1.1 302 Found\r\nLocation: https://127.0.0.1:%[2]d/landing\r\n\r\n' >&3
  printf '\n__AWGL_PROBE__|302|0.000000|0.010000|0.020000|https://127.0.0.1/'
  exit 0
fi
printf 'destination'
printf '\n__AWGL_PROBE__|200|0.000000|0.010000|0.020000|https://127.0.0.1/landing'
exit 0
`, state, port))

	// The URL host stays 127.0.0.1 across the redirect, so the pin remains.
	// model.NewTarget rejects an IP literal in the domain field, so build
	// the pinned pair directly.
	target := model.Target{Domain: "127.0.0.1", IP: "127.0.0.1", Network: model.NetworkTCP, Port: uint16(httpsPort)}
	got := Check(context.Background(), target, Path{})
	if got.Verdict != model.VerdictReachable {
		t.Fatalf("result=%+v, want reachable after one redirect", got)
	}
	if got.HTTPStatus != 200 {
		t.Fatalf("status=%d, want 200 from the redirect destination", got.HTTPStatus)
	}
}

// TestHTTPSRedirectForeignHostNotFollowed covers the endpoint invariant: a
// redirect that changes the host stops at the original reachable response.
func TestHTTPSRedirectForeignHostNotFollowed(t *testing.T) {
	// The fake curl keeps its call counter in a fresh directory; $TMPDIR is
	// shared on macOS and would leak state between test runs.
	state := t.TempDir()
	fakeCurl(t, fmt.Sprintf(`
n=$(cat %s/count 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > %s/count
if [ "$n" -eq 1 ]; then
  printf 'HTTP/1.1 302 Found\r\nLocation: https://other.example.test/away\r\n\r\n' >&3
  printf '\n__AWGL_PROBE__|302|0.000000|0.010000|0.020000|'
  exit 0
fi
# The follow-up request must NOT carry a pin for other.example.test.
for arg in "$@"; do
  case "$arg" in
    --resolve) printf 'unexpected --resolve on foreign host\n' >&2; exit 90 ;;
    other.example.test:*) printf 'pinned foreign host\n' >&2; exit 91 ;;
  esac
done
printf 'away'
printf '\n__AWGL_PROBE__|200|0.000000|0.010000|0.020000|https://other.example.test/away'
exit 0
`, state, state))

	target, err := model.NewTarget("", "127.0.0.1", model.NetworkTCP, uint16(httpsPort))
	if err != nil {
		t.Fatal(err)
	}
	// The pinned pair: model.NewTarget rejects an IP literal in the domain
	// field, so set the domain directly on the validated struct.
	observed := target
	observed.Domain = "127.0.0.1"
	got := Check(context.Background(), observed, Path{})
	if got.Verdict != model.VerdictReachable {
		t.Fatalf("result=%+v, want reachable from the original response", got)
	}
	count, err := os.ReadFile(state + "/count")
	if err != nil || strings.TrimSpace(string(count)) != "1" {
		t.Fatalf("foreign redirect caused another request: count=%q err=%v", count, err)
	}
}

// TestHTTPSRedirectUnsupportedScheme covers the "keep code simple"
// validation: a redirect to a non-http(s) scheme is an error, never a block.
func TestHTTPSRedirectUnsupportedScheme(t *testing.T) {
	fakeCurl(t, `
printf 'HTTP/1.1 302 Found\r\nLocation: ftp://other.example.test/file\r\n\r\n' >&3
printf '\n__AWGL_PROBE__|302|0.000000|0.010000|0.020000|ftp://other.example.test/file'
exit 0
`)
	got := Check(context.Background(), tcpTarget(t, httpsPort), Path{})
	if got.Verdict != model.VerdictError {
		t.Fatalf("result=%+v, want error for a non-http(s) redirect", got)
	}
}

// TestHTTPSRedirectLoopStaysReachable proves that a redirect cycle is a
// working HTTP conversation, never a block.
func TestHTTPSRedirectLoopStaysReachable(t *testing.T) {
	fakeCurl(t, `
printf 'HTTP/1.1 302 Found\r\nLocation: https://127.0.0.1/loop\r\n\r\n' >&3
printf '\n__AWGL_PROBE__|302|0.000000|0.010000|0.020000|'
exit 0
`)
	got := Check(context.Background(), tcpTarget(t, httpsPort), Path{})
	if got.Verdict != model.VerdictReachable {
		t.Fatalf("result=%+v, want reachable for a redirect loop", got)
	}
}

func TestHTTPSMetaTrailerDoesNotCountAsBody(t *testing.T) {
	raw := []byte("hello" + curlMetaMarker + "200|0.1|0.2|0.3|https://x/")
	body, meta := splitCurlMeta(raw)
	if string(body) != "hello" || !meta.known || meta.httpCode != 200 || meta.connectSec != 0.2 {
		t.Fatalf("body=%q meta=%+v", body, meta)
	}
}

// TestHTTPSMetaTrailerKeepsMarkerInBody proves that splitCurlMeta only
// removes its own trailer: a site that legitimately serves the marker text
// keeps it, and the real trailer appended by curl is still removed.
func TestHTTPSMetaTrailerKeepsMarkerInBody(t *testing.T) {
	// The site body itself ends with a fake marker; curl appends the real
	// one. The LAST trailer is curl's, everything before it is body.
	raw := []byte("x" + curlMetaMarker + "404|0|0|0|" + curlMetaMarker + "200|0.1|0.2|0.3|https://x/")
	body, meta := splitCurlMeta(raw)
	if !meta.known || meta.httpCode != 200 || !strings.HasPrefix(string(body), "x") {
		t.Fatalf("body=%q meta=%+v, want the last trailer removed only", body, meta)
	}
}
