package mihomo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// LogForwarder forwards logs from Mihomo's /logs endpoint to the logging service.
type LogForwarder struct {
	clashAddr func() string
	gate      logging.LevelGate

	inbound  *logging.ScopedLogger
	outbound *logging.ScopedLogger
	dns      *logging.ScopedLogger
	router   *logging.ScopedLogger
	runtime  *logging.ScopedLogger

	http *http.Client

	reconnect  time.Duration
	levelWatch time.Duration
}

const defaultLevelWatchInterval = 5 * time.Second

// NewLogForwarder forwards a Clash/Mihomo-compatible /logs stream into the
// Mihomo log bucket.
func NewLogForwarder(clashAddr func() string, appLogger logging.AppLogger) *LogForwarder {
	gate, _ := appLogger.(logging.LevelGate)
	return &LogForwarder{
		clashAddr:  clashAddr,
		gate:       gate,
		inbound:    logging.NewScopedLogger(appLogger, logging.GroupMihomo, logging.SubSBInbound),
		outbound:   logging.NewScopedLogger(appLogger, logging.GroupMihomo, logging.SubSBOutbound),
		dns:        logging.NewScopedLogger(appLogger, logging.GroupMihomo, logging.SubSBDNS),
		router:     logging.NewScopedLogger(appLogger, logging.GroupMihomo, logging.SubSBRouter),
		runtime:    logging.NewScopedLogger(appLogger, logging.GroupMihomo, logging.SubSBRuntime),
		http:       &http.Client{},
		reconnect:  3 * time.Second,
		levelWatch: defaultLevelWatchInterval,
	}
}

// Run starts the forwarder loop until ctx is canceled.
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
	level := f.desiredClashLevel()
	if level == "" {
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		t := time.NewTicker(f.levelWatch)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if f.desiredClashLevel() != level {
					cancel()
					return
				}
			}
		}
	}()

	url := fmt.Sprintf("http://%s/logs?level=%s", f.clashAddr(), level)
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
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		f.forward(sc.Bytes())
	}
}

type clashLogEntry struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

var timestampPrefix = regexp.MustCompile(`^[+\-]\d{4}\s+\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}(?:\.\d+)?\s+(?:FATAL|ERROR|WARN|INFO|DEBUG|TRACE)\s+`)
var connIDPrefix = regexp.MustCompile(`^\[\d+\s+[\d.]+[a-zµ]+\]\s+`)
var contextBracket = regexp.MustCompile(`\[[^\]]*\]`)

func classifyPayload(payload string) (subgroup, target, message string) {
	msg := timestampPrefix.ReplaceAllString(payload, "")
	msg = connIDPrefix.ReplaceAllString(msg, "")
	msg = strings.TrimSpace(msg)

	head, rest, hasSep := cutSegment(msg)
	if !hasSep {
		if strings.HasPrefix(msg, "[") {
			if end := strings.Index(msg, "]"); end > 0 {
				bracket := strings.ToLower(msg[1:end])
				body := strings.TrimSpace(msg[end+1:])
				switch bracket {
				case "dns":
					return logging.SubSBDNS, "dns", body
				case "tcp", "udp", "inbound":
					return logging.SubSBInbound, bracket, body
				case "rule", "router", "route":
					return logging.SubSBRouter, "router", body
				case "outbound", "proxy":
					return logging.SubSBOutbound, bracket, body
				}
			}
		}
		return logging.SubSBRuntime, "mihomo", msg
	}

	category, tag := splitCategory(head)
	switch category {
	case "inbound", "tcp", "udp":
		return logging.SubSBInbound, tagOr(tag, "inbound"), rest
	case "outbound", "proxy":
		return logging.SubSBOutbound, tagOr(tag, "outbound"), rest
	case "dns":
		return logging.SubSBDNS, tagOr(tag, "dns"), rest
	case "router", "route", "rule":
		return logging.SubSBRouter, tagOr(tag, "router"), rest
	default:
		return logging.SubSBRuntime, tagOr(tag, "mihomo"), msg
	}
}

func cutSegment(msg string) (head, rest string, ok bool) {
	idx := strings.Index(msg, ": ")
	if idx < 0 {
		return msg, "", false
	}
	return msg[:idx], strings.TrimSpace(msg[idx+2:]), true
}

func splitCategory(head string) (category, tag string) {
	if br := contextBracket.FindStringIndex(head); br != nil {
		tag = strings.TrimSpace(head[br[0]+1 : br[1]-1])
		head = strings.TrimSpace(head[:br[0]])
	}
	if i := strings.IndexAny(head, "/ "); i >= 0 {
		return head[:i], tag
	}
	return head, tag
}

func tagOr(tag, fallback string) string {
	if tag == "" {
		return fallback
	}
	return tag
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
	level := levelForClashType(e.Type)
	if f.gate != nil && !f.gate.Visible(level) {
		return
	}

	subgroup, target, message := classifyPayload(payload)
	scoped := f.scopedFor(subgroup)
	if scoped == nil {
		return
	}
	scoped.At(level, "run", target, message)
}

func levelForClashType(clashType string) logging.Level {
	switch strings.ToLower(strings.TrimSpace(clashType)) {
	case "error", "fatal", "panic":
		return logging.LevelError
	case "warn", "warning":
		return logging.LevelWarn
	case "info":
		return logging.LevelInfo
	case "debug":
		return logging.LevelFull
	case "trace":
		return logging.LevelDebug
	default:
		return logging.LevelWarn
	}
}

func (f *LogForwarder) desiredClashLevel() string {
	if f.gate == nil {
		return "trace"
	}
	if !f.gate.Visible(logging.LevelError) {
		return ""
	}
	switch {
	case f.gate.Visible(logging.LevelDebug):
		return "trace"
	case f.gate.Visible(logging.LevelFull):
		return "debug"
	case f.gate.Visible(logging.LevelInfo):
		return "info"
	default:
		return "warn"
	}
}

func (f *LogForwarder) scopedFor(subgroup string) *logging.ScopedLogger {
	switch subgroup {
	case logging.SubSBInbound:
		return f.inbound
	case logging.SubSBOutbound:
		return f.outbound
	case logging.SubSBDNS:
		return f.dns
	case logging.SubSBRouter:
		return f.router
	default:
		return f.runtime
	}
}
