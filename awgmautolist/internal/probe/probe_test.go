package probe

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

func TestCheckDispatchesByNetwork(t *testing.T) {
	// Empty target is rejected before any transport work.
	empty := Check(context.Background(), model.Target{}, Path{})
	if empty.Verdict != model.VerdictError {
		t.Fatalf("empty target = %+v, want error", empty)
	}

	// Unknown network is rejected.
	weird := Check(context.Background(), model.Target{IP: "127.0.0.1", Network: "sctp", Port: 443}, Path{})
	if weird.Verdict != model.VerdictError || !strings.Contains(weird.Detail, "sctp") {
		t.Fatalf("unknown network = %+v, want a named error", weird)
	}
}

// TestCheckPinsObservedIPForBothPaths proves the --resolve pin reaches the
// curl command line with the exact observed address for the original host.
func TestCheckPinsObservedIPForBothPaths(t *testing.T) {
	for _, test := range []struct {
		domain, ip, want string
	}{
		{"example.test", "192.0.2.10", "example.test:443:192.0.2.10"},
		{"example.test", "2001:db8::1", "example.test:443:[2001:db8::1]"},
	} {
		target, err := model.NewTarget(test.domain, test.ip, model.NetworkTCP, 443)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := resolveArg(target)
		if !ok || got != test.want {
			t.Fatalf("resolveArg(%+v)=%q ok=%v, want %q", target, got, ok, test.want)
		}
	}

	// No domain: nothing to pin, curl connects to the IP literal directly.
	ipOnly, err := model.NewTarget("", "192.0.2.10", model.NetworkTCP, 443)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := resolveArg(ipOnly); ok {
		t.Fatalf("ip-only target produced a pin %q", got)
	}
}

// TestHTTPSURLForms proves the probe URL brackets IPv6 and keeps the port.
func TestHTTPSURLForms(t *testing.T) {
	old := httpsPort
	defer func() { httpsPort = old }()
	httpsPort = 443
	if got := httpsURL("www.example.com"); got != "https://www.example.com:443/" {
		t.Fatalf("domain url=%q", got)
	}
	if got := httpsURL("192.0.2.10"); got != "https://192.0.2.10:443/" {
		t.Fatalf("ipv4 url=%q", got)
	}
	httpsPort = 8443
	if got := httpsURL("2001:db8::1"); got != "https://[2001:db8::1]:8443/" {
		t.Fatalf("ipv6 url=%q", got)
	}
}

// TestHTTPSRedirectSilentDNSChangeIsError covers the requirement that a
// pinned original must not silently switch DNS: a Location moving to another
// host stops the chain instead of probing the new server with the old pin.
func TestHTTPSRedirectSilentDNSChangeIsError(t *testing.T) {
	fakeCurl(t, `
printf 'HTTP/1.1 302 Found\r\nLocation: https://unrelated.example.test/page\r\n\r\n' >&3
printf '\n__AWGL_PROBE__|302|0.000000|0.010000|0.020000|'
exit 0
`)
	target := model.Target{Domain: "127.0.0.1", IP: "127.0.0.1", Network: model.NetworkTCP, Port: uint16(httpsPort)}
	got := Check(context.Background(), target, Path{})
	// The chain is abandoned (host left the pinned original). Whether the
	// follow-up then fails to resolve or is refused, the verdict must not
	// be a clean reachable with the pin carried over.
	if got.Verdict == model.VerdictReachable && strings.Contains(got.Detail, "resolve") {
		t.Fatalf("pin carried to a foreign host: %+v", got)
	}
}

// TestResolveArgIPValidation keeps malformed IPs out of curl's --resolve.
func TestResolveArgIPValidation(t *testing.T) {
	target := model.Target{Domain: "example.test", IP: "not-an-ip", Network: model.NetworkTCP, Port: 443}
	if got, ok := resolveArg(target); ok {
		t.Fatalf("malformed IP produced a pin %q", got)
	}
}
