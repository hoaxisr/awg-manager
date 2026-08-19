package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// RawConf — ответ сервера RAWCONF:ip|dns|mtu (qWDTT 1.4).
type RawConf struct {
	ClientIP string
	DNS      string
	MTU      int
}

func SendRawAuth(conn net.Conn, deviceID, password string) error {
	payload := fmt.Sprintf("AUTH:%s|%s", deviceID, password)
	if _, err := conn.Write([]byte(payload)); err != nil {
		return fmt.Errorf("AUTH: %w", err)
	}
	return nil
}

func RequestRawConfig(conn net.Conn, deviceID, password string) (RawConf, error) {
	payload := fmt.Sprintf("GETCONF_RAW:%s|%s", deviceID, password)
	if _, err := conn.Write([]byte(payload)); err != nil {
		return RawConf{}, fmt.Errorf("GETCONF_RAW: %w", err)
	}
	b := make([]byte, 512)
	if err := conn.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
		return RawConf{}, err
	}
	n, err := conn.Read(b)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return RawConf{}, fmt.Errorf("RAWCONF read: %w", err)
	}
	return parseRawConf(string(b[:n]))
}

func parseRawConf(resp string) (RawConf, error) {
	resp = strings.TrimSpace(resp)
	if resp == "NOCONF" {
		return RawConf{}, fmt.Errorf("NOCONF")
	}
	if strings.HasPrefix(resp, "DENIED:") {
		reason := strings.TrimPrefix(resp, "DENIED:")
		return RawConf{}, fmt.Errorf("FATAL_AUTH: %s", reason)
	}
	if !strings.HasPrefix(resp, "RAWCONF:") {
		return RawConf{}, fmt.Errorf("unexpected RAWCONF: %q", resp)
	}
	parts := strings.Split(strings.TrimPrefix(resp, "RAWCONF:"), "|")
	if len(parts) < 3 {
		return RawConf{}, fmt.Errorf("bad RAWCONF format: %q", resp)
	}
	mtu, err := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err != nil || mtu < 576 {
		mtu = 1300
	}
	return RawConf{
		ClientIP: strings.TrimSpace(parts[0]),
		DNS:      strings.TrimSpace(parts[1]),
		MTU:      mtu,
	}, nil
}
