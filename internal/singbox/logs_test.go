package singbox

import (
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

func TestLogForwarder_ForwardByLevel(t *testing.T) {
	cap := &captureLogger{}
	f := NewLogForwarder("unused", cap)

	cases := []struct {
		name  string
		line  string
		want  logging.Level
		msg   string
	}{
		{"info", `{"type":"info","payload":"started inbound"}`, logging.LevelInfo, "started inbound"},
		{"warn", `{"type":"warning","payload":"slow dial"}`, logging.LevelWarn, "slow dial"},
		{"error", `{"type":"error","payload":"boom"}`, logging.LevelError, "boom"},
		{"fatal", `{"type":"fatal","payload":"cfg bad"}`, logging.LevelError, "cfg bad"},
		{"debug", `{"type":"debug","payload":"tick"}`, logging.LevelDebug, "tick"},
		{"unknown-level-falls-through-to-full", `{"type":"trace","payload":"trace msg"}`, logging.LevelFull, "trace msg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(cap.snapshot())
			f.forward([]byte(tc.line))
			got := cap.snapshot()
			if len(got) != before+1 {
				t.Fatalf("expected one new entry, got %d total", len(got))
			}
			e := got[before]
			if e.Level != tc.want {
				t.Errorf("level = %q, want %q", e.Level, tc.want)
			}
			if e.Group != logging.GroupSystem || e.Sub != logging.SubSingbox {
				t.Errorf("scope = (%q,%q), want (%q,%q)", e.Group, e.Sub, logging.GroupSystem, logging.SubSingbox)
			}
			if e.Message != tc.msg {
				t.Errorf("message = %q, want %q", e.Message, tc.msg)
			}
		})
	}
}

func TestLogForwarder_DropsEmptyAndMalformed(t *testing.T) {
	cap := &captureLogger{}
	f := NewLogForwarder("unused", cap)

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
	f := NewLogForwarder("unused", nil)
	// Should not panic even with content.
	f.forward([]byte(`{"type":"info","payload":"hello"}`))
}
