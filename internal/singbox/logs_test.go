package singbox

import (
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

type captured struct {
	Level   logging.Level
	Group   string
	Sub     string
	Action  string
	Target  string
	Message string
}

type captureLogger struct {
	mu   sync.Mutex
	logs []captured
}

func (c *captureLogger) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, captured{level, group, subgroup, action, target, message})
}

func (c *captureLogger) snapshot() []captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]captured, len(c.logs))
	copy(out, c.logs)
	return out
}

func TestLogForwarder_LevelMapping(t *testing.T) {
	cap := &captureLogger{}
	f := NewLogForwarder(func() string { return "unused" }, cap)

	cases := []struct {
		name string
		line string
		want logging.Level
		msg  string
	}{
		{"info", `{"type":"info","payload":"started"}`, logging.LevelInfo, "started"},
		{"warn", `{"type":"warning","payload":"slow"}`, logging.LevelWarn, "slow"},
		{"error", `{"type":"error","payload":"boom"}`, logging.LevelError, "boom"},
		{"fatal", `{"type":"fatal","payload":"cfg bad"}`, logging.LevelError, "cfg bad"},
		{"debug", `{"type":"debug","payload":"tick"}`, logging.LevelDebug, "tick"},
		{"unknown-falls-to-full", `{"type":"trace","payload":"trace msg"}`, logging.LevelFull, "trace msg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(cap.snapshot())
			f.forward([]byte(tc.line))
			got := cap.snapshot()
			if len(got) != before+1 {
				t.Fatalf("expected one new entry, got %d", len(got))
			}
			e := got[before]
			if e.Level != tc.want {
				t.Errorf("level=%q want %q", e.Level, tc.want)
			}
			if e.Group != logging.GroupSingbox {
				t.Errorf("group=%q want %q", e.Group, logging.GroupSingbox)
			}
			if e.Message != tc.msg {
				t.Errorf("message=%q want %q", e.Message, tc.msg)
			}
		})
	}
}

func TestLogForwarder_SubgroupClassification(t *testing.T) {
	cases := []struct {
		name     string
		payload  string
		subgroup string
		target   string
		message  string
	}{
		{
			"inbound",
			"inbound/tproxy[tproxy-in]: inbound connection from 192.168.1.5:50000",
			logging.SubSBInbound,
			"tproxy-in",
			"inbound connection from 192.168.1.5:50000",
		},
		{
			"outbound",
			"outbound/direct[direct]: outbound connection to 1.1.1.1:443",
			logging.SubSBOutbound,
			"direct",
			"outbound connection to 1.1.1.1:443",
		},
		{
			"dns",
			"dns/transport[dns-bootstrap]: exchange query for example.com",
			logging.SubSBDNS,
			"dns-bootstrap",
			"exchange query for example.com",
		},
		{
			"router-colon",
			"router: match rule inbound=tproxy-in -> outbound=awg10",
			logging.SubSBRouter,
			"router",
			"match rule inbound=tproxy-in -> outbound=awg10",
		},
		{
			"runtime-fallback",
			"sing-box started",
			logging.SubSBRuntime,
			"sing-box",
			"sing-box started",
		},
		{
			"timestamp-stripped",
			"+0000 2026-04-20 12:34:56 INFO [1 0ms] inbound/tproxy[tproxy-in]: hello",
			logging.SubSBInbound,
			"tproxy-in",
			"hello",
		},
		{
			"clash-api-outbound-with-conn-id",
			"[2659891384 130ms] outbound/hysteria2[IPv4]: outbound connection to 34.160.111.145:443",
			logging.SubSBOutbound,
			"IPv4",
			"outbound connection to 34.160.111.145:443",
		},
		{
			"clash-api-inbound-with-conn-id",
			"[2659891384 0ms] inbound/mixed[IPv4-in]: inbound connection from 127.0.0.1:53814",
			logging.SubSBInbound,
			"IPv4-in",
			"inbound connection from 127.0.0.1:53814",
		},
		{
			"clash-api-connection-fallback",
			"[2659891384 5.13s] connection: open connection to 34.160.111.145:443 using outbound/hysteria2[IPv4]: timeout",
			logging.SubSBRuntime,
			"sing-box",
			"connection: open connection to 34.160.111.145:443 using outbound/hysteria2[IPv4]: timeout",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub, tgt, msg := classifyPayload(tc.payload)
			if sub != tc.subgroup {
				t.Errorf("subgroup=%q want %q", sub, tc.subgroup)
			}
			if tgt != tc.target {
				t.Errorf("target=%q want %q", tgt, tc.target)
			}
			if msg != tc.message {
				t.Errorf("message=%q want %q", msg, tc.message)
			}
		})
	}
}

func TestLogForwarder_DropsEmptyAndMalformed(t *testing.T) {
	cap := &captureLogger{}
	f := NewLogForwarder(func() string { return "unused" }, cap)

	f.forward(nil)
	f.forward([]byte(""))
	f.forward([]byte("not-json"))
	f.forward([]byte(`{"type":"info","payload":""}`))
	f.forward([]byte(`{"type":"info","payload":"   "}`))

	if got := cap.snapshot(); len(got) != 0 {
		t.Fatalf("expected no entries, got %d: %+v", len(got), got)
	}
}

func TestLogForwarder_NilAppLoggerIsSafe(t *testing.T) {
	f := NewLogForwarder(func() string { return "unused" }, nil)
	f.forward([]byte(`{"type":"info","payload":"hello"}`))
}

func TestSanitizeSingboxLogText_RedactsDomainsAndIPs(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "lookup domain node.example.org",
			want: "lookup domain no************rg",
		},
		{
			in:   "lookup succeed for node.example.org: 203.0.113.77",
			want: "lookup succeed for no************rg: 20********77",
		},
		{
			in:   "outbound connection to example.com:443",
			want: "outbound connection to ex*******om:443",
		},
		{
			in:   "inbound connection from 192.168.1.50:54321",
			want: "inbound connection from 19********50:54321",
		},
		{
			in:   "outbound connection to [2606:2800:220:1:248:1893:25c8:1946]:443",
			want: "outbound connection to [26******************************46]:443",
		},
		{
			in:   "lookup succeed for node.example.org: 2606:2800:220:1:248:1893:25c8:1946",
			want: "lookup succeed for no************rg: 26******************************46",
		},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := sanitizeSingboxLogText(tc.in)
			if got != tc.want {
				t.Fatalf("sanitize = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLogForwarder_PreservesSensitiveHostsBeforeOutputMasking(t *testing.T) {
	cap := &captureLogger{}
	f := NewLogForwarder(func() string { return "unused" }, cap)

	f.forward([]byte(`{"type":"debug","payload":"dns: lookup succeed for node.example.org: 203.0.113.77"}`))

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected one log, got %d", len(got))
	}
	if got[0].Sub != logging.SubSBDNS {
		t.Fatalf("subgroup = %q, want %q", got[0].Sub, logging.SubSBDNS)
	}
	if got[0].Target != "dns" {
		t.Fatalf("target = %q, want dns", got[0].Target)
	}
	msg := got[0].Message
	if !strings.Contains(msg, "node.example.org") {
		t.Fatalf("raw domain missing before output masking: %q", msg)
	}
	if !strings.Contains(msg, "203.0.113.77") {
		t.Fatalf("raw ip missing before output masking: %q", msg)
	}
	if strings.Contains(msg, "no************rg") || strings.Contains(msg, "20********77") {
		t.Fatalf("message was redacted before output masking: %q", msg)
	}
}

func TestLogForwarder_PreservesSensitiveHosts_AllLevels(t *testing.T) {
	cases := []string{"error", "fatal", "panic", "warn", "warning", "info", "debug", "trace"}
	for _, lvl := range cases {
		t.Run(lvl, func(t *testing.T) {
			cap := &captureLogger{}
			f := NewLogForwarder(func() string { return "unused" }, cap)
			line := `{"type":"` + lvl + `","payload":"dns: lookup succeed for node.example.org: 203.0.113.77 and [2606:2800:220:1:248:1893:25c8:1946]:443"}`
			f.forward([]byte(line))

			got := cap.snapshot()
			if len(got) != 1 {
				t.Fatalf("expected one log, got %d", len(got))
			}
			msg := got[0].Message
			if !strings.Contains(msg, "node.example.org") || !strings.Contains(msg, "203.0.113.77") || !strings.Contains(msg, "2606:2800:220:1:248:1893:25c8:1946") {
				t.Fatalf("raw sensitive value missing before output masking on level=%s: %q", lvl, msg)
			}
			if strings.Contains(msg, "no************rg") || strings.Contains(msg, "20********77") || strings.Contains(msg, "[26******************************46]:443") {
				t.Fatalf("message was redacted before output masking on level=%s: %q", lvl, msg)
			}
		})
	}
}
