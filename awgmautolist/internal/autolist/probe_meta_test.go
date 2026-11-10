package autolist

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestSplitCurlMeta(t *testing.T) {
	raw := []byte("ok" + curlMetaMarker + "200|0.100000|0.250000|https://www.example.com/")
	body, meta := splitCurlMeta(raw)
	if string(body) != "ok" || !meta.known || meta.httpCode != 200 || meta.connectSec != 0.1 || meta.appConnect != 0.25 || meta.effectiveURL != "https://www.example.com/" {
		t.Fatalf("body=%q meta=%+v", body, meta)
	}
}

func curlTimeoutScript(t *testing.T, connect, app string, wanCode string) string {
	t.Helper()
	count := fmt.Sprintf("%s/count", t.TempDir())
	return fakeCurl(t, fmt.Sprintf(`n=0
[ -f %q ] && n=$(cat %q)
n=$((n+1))
printf '%%s' "$n" > %q
if [ "$n" -eq 1 ]; then
  printf '\n__AWGM_PROBE__|000|%s|%s|https://example.com/'
  printf 'curl: (28) Connection timed out\n' >&2
  exit 28
fi
printf '%s'
`, count, count, count, connect, app, wanCode))
}

func TestCurlConnectTimeoutNeedsWANCheck(t *testing.T) {
	p := &Prober{
		CurlPath: curlTimeoutScript(t, "0.000000", "0.000000", "204"),
		WANURL:   "https://wan.example/generate_204", WANExpects204: true,
	}
	got := p.Probe(context.Background(), "example.com")
	if got.Verdict != VerdictBlocked || got.Why != WhyNoConnect {
		t.Fatalf("connect timeout + WAN 204 = %+v, want blocked/no-connect", got)
	}
}

func TestCurlConnectTimeoutReservesWANBudget(t *testing.T) {
	dir := t.TempDir()
	count := dir + "/count"
	curl := fakeCurl(t, fmt.Sprintf(`n=0
[ -f %q ] && n=$(cat %q)
n=$((n+1))
printf '%%s' "$n" > %q
if [ "$n" -eq 2 ]; then
  printf '204'
  exit 0
fi
connect=
max=6
while [ "$#" -gt 0 ]; do
  case "$1" in
    --connect-timeout) connect=$2; shift 2 ;;
    --max-time) max=$2; shift 2 ;;
    *) shift ;;
  esac
done
sleep "${connect:-$max}"
printf '\n__AWGM_PROBE__|000|0.000000|0.000000|https://example.com/'
printf 'curl: (28) Connection timed out\n' >&2
exit 28
`, count, count, count))
	p := &Prober{
		CurlPath: curl, TimeoutSec: 6,
		WANURL: "https://wan.example/generate_204", WANExpects204: true,
	}
	got := p.Probe(context.Background(), "example.com")
	data, _ := os.ReadFile(count)
	if got.Verdict != VerdictBlocked || got.Why != WhyNoConnect || strings.TrimSpace(string(data)) != "2" {
		t.Fatalf("full connect budget + WAN 204 = %+v, curl calls=%q; want blocked/no-connect and 2 calls", got, data)
	}
}

func TestCurlConnectTimeoutWANFailureUnknown(t *testing.T) {
	p := &Prober{
		CurlPath: curlTimeoutScript(t, "0.000000", "0.000000", "200"),
		WANURL:   "https://wan.example/generate_204", WANExpects204: true,
	}
	got := p.Probe(context.Background(), "example.com")
	if got.Verdict != VerdictUnknown {
		t.Fatalf("connect timeout + wrong WAN status = %+v, want unknown", got)
	}
}

func TestCurlTLSPhases(t *testing.T) {
	t.Run("TLS timeout", func(t *testing.T) {
		p := &Prober{CurlPath: curlTimeoutScript(t, "0.100000", "0.000000", "204")}
		got := p.Probe(context.Background(), "example.com")
		if got.Verdict != VerdictBlocked || got.Why != WhyNoAnswer {
			t.Fatalf("%+v, want blocked/no-answer", got)
		}
	})
	t.Run("after TLS without body", func(t *testing.T) {
		p := &Prober{CurlPath: curlTimeoutScript(t, "0.100000", "0.200000", "204")}
		got := p.Probe(context.Background(), "example.com")
		if got.Verdict != VerdictUnknown {
			t.Fatalf("%+v, want unknown", got)
		}
	})
}

func TestCurlRedirectRegisteredDomain(t *testing.T) {
	for _, tc := range []struct {
		effective string
		want      Verdict
	}{
		{"https://www.example.com/", VerdictReachable},
		{"https://outside.test/", VerdictUnknown},
	} {
		t.Run(tc.effective, func(t *testing.T) {
			curl := fakeCurl(t, fmt.Sprintf("printf 'ok\\n__AWGM_PROBE__|200|0.100000|0.200000|%s'\n", tc.effective))
			p := &Prober{CurlPath: curl}
			if got := p.Probe(context.Background(), "example.com"); got.Verdict != tc.want {
				t.Fatalf("redirect %s = %+v, want %v", tc.effective, got, tc.want)
			}
		})
	}
}

func TestProbeRequiresCurl(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got := (&Prober{}).Probe(context.Background(), "example.com")
	if got.Verdict != VerdictUnknown || !strings.Contains(got.Err, "curl is required") {
		t.Fatalf("missing curl = %+v, want explicit error", got)
	}
}

func TestConnectionRefusedIsUnknown(t *testing.T) {
	if got := classifyTransportErr(fmt.Errorf("connection refused")); got.Verdict != VerdictUnknown {
		t.Fatalf("refusal = %+v, want unknown", got)
	}
}
