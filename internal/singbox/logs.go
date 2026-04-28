package singbox

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

type LogForwarder struct {
	clashAddr string
	app       logging.AppLogger

	inbound  *logging.ScopedLogger
	outbound *logging.ScopedLogger
	dns      *logging.ScopedLogger
	router   *logging.ScopedLogger
	runtime  *logging.ScopedLogger

	http *http.Client

	reconnect time.Duration
}

func NewLogForwarder(clashAddr string, appLogger logging.AppLogger) *LogForwarder {
	return &LogForwarder{
		clashAddr: clashAddr,
		app:       appLogger,
		inbound:   logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBInbound),
		outbound:  logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBOutbound),
		dns:       logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBDNS),
		router:    logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBRouter),
		runtime:   logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBRuntime),
		http:      &http.Client{},
		reconnect: 3 * time.Second,
	}
}

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
	url := fmt.Sprintf("http://%s/logs?level=trace", f.clashAddr)
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
		return logging.SubSBRuntime, "sing-box", msg
	}

	category, tag := splitCategory(head)
	switch category {
	case "inbound":
		return logging.SubSBInbound, tagOr(tag, "inbound"), rest
	case "outbound":
		return logging.SubSBOutbound, tagOr(tag, "outbound"), rest
	case "dns":
		return logging.SubSBDNS, tagOr(tag, "dns"), rest
	case "router", "route":
		return logging.SubSBRouter, tagOr(tag, "router"), rest
	default:
		return logging.SubSBRuntime, tagOr(tag, "sing-box"), msg
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
	subgroup, target, message := classifyPayload(payload)
	scoped := f.scopedFor(subgroup)
	if scoped == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(e.Type)) {
	case "error", "fatal", "panic":
		scoped.Error("run", target, message)
	case "warn", "warning":
		scoped.Warn("run", target, message)
	case "info":
		scoped.Info("run", target, message)
	case "debug":
		scoped.Debug("run", target, message)
	default:
		scoped.Full("run", target, message)
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
