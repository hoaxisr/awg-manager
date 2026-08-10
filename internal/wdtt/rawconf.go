package wdtt

import (
	"strconv"
	"strings"
	"time"
)

const rawConfLinePrefix = "RAWCONF|"

// RawConfPayload — ответ VPS RAWCONF:ip|dns|mtu (qWDTT 1.4).
type RawConfPayload struct {
	ClientIP string
	DNS      string
	MTU      int
}

func parseRawConfLine(line string) (RawConfPayload, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, rawConfLinePrefix) {
		return RawConfPayload{}, false
	}
	parts := strings.Split(strings.TrimPrefix(line, rawConfLinePrefix), "|")
	if len(parts) < 3 {
		return RawConfPayload{}, false
	}
	mtu, err := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err != nil || mtu < 576 || mtu > 9000 {
		mtu = 1300
	}
	ip := strings.TrimSpace(parts[0])
	if ip == "" {
		return RawConfPayload{}, false
	}
	return RawConfPayload{
		ClientIP: ip,
		DNS:      strings.TrimSpace(parts[1]),
		MTU:      mtu,
	}, true
}

func ExtractRawConfFromLog(log string) (RawConfPayload, bool) {
	var last RawConfPayload
	found := false
	for _, line := range strings.Split(log, "\n") {
		if conf, ok := parseRawConfLine(line); ok {
			last = conf
			found = true
		}
	}
	return last, found
}

func (s *Service) waitForClientRawConf(id string, timeout time.Duration) (RawConfPayload, bool) {
	proc := s.clientProcs.get(id)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if conf, ok := proc.lastRawConf(); ok {
			return conf, true
		}
		st := proc.Status()
		if conf, ok := ExtractRawConfFromLog(st.Log); ok {
			return conf, true
		}
		if !st.Running {
			return RawConfPayload{}, false
		}
		time.Sleep(200 * time.Millisecond)
	}
	st := proc.Status()
	conf, ok := ExtractRawConfFromLog(st.Log)
	return conf, ok
}
