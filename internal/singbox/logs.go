package singbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// LogForwarder streams sing-box's runtime log over the Clash API /logs
// endpoint and republishes each line into the app's AppLogger under
// (GroupSystem, SubSingbox). This replaces file-based logging — sing-box
// itself writes stdout/stderr to /dev/null, and all runtime output
// surfaces inside the awg-manager UI's log view like every other subsystem.
type LogForwarder struct {
	clashAddr string
	logger    *logging.ScopedLogger

	// http is reused across reconnects; no Timeout because /logs is open-ended.
	http *http.Client

	// backoff between reconnect attempts when /logs is unavailable
	// (e.g. sing-box not running yet, or just restarted).
	reconnect time.Duration
}

// NewLogForwarder returns a forwarder that pushes log lines from the clash
// API at clashAddr into appLogger. A nil appLogger is accepted — the
// forwarder is still safe to Run but becomes a no-op.
func NewLogForwarder(clashAddr string, appLogger logging.AppLogger) *LogForwarder {
	return &LogForwarder{
		clashAddr: clashAddr,
		logger:    logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubSingbox),
		http:      &http.Client{},
		reconnect: 3 * time.Second,
	}
}

// Run blocks until ctx is canceled. Reconnects to the /logs stream with
// a small backoff whenever the connection drops (sing-box not running,
// restart, network blip).
func (f *LogForwarder) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		f.runOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(f.reconnect):
		}
	}
}

func (f *LogForwarder) runOnce(ctx context.Context) {
	url := fmt.Sprintf("http://%s/logs?level=info", f.clashAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	// /logs is a streaming endpoint — one JSON object per line, no EOF
	// until the server goes away. Bump the scanner buffer so a long
	// single-line payload (e.g. a fatal stacktrace) isn't dropped.
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		f.forward(sc.Bytes())
	}
}

// clashLogEntry is the shape emitted by clash_api /logs.
type clashLogEntry struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

func (f *LogForwarder) forward(line []byte) {
	if len(line) == 0 {
		return
	}
	var e clashLogEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	payload := strings.TrimSpace(e.Payload)
	if payload == "" {
		return
	}
	switch strings.ToLower(strings.TrimSpace(e.Type)) {
	case "error", "fatal", "panic":
		f.logger.Error("run", "sing-box", payload)
	case "warn", "warning":
		f.logger.Warn("run", "sing-box", payload)
	case "info":
		f.logger.Info("run", "sing-box", payload)
	case "debug":
		f.logger.Debug("run", "sing-box", payload)
	default:
		f.logger.Full("run", "sing-box", payload)
	}
}
