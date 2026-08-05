package freeturn

import (
	"strings"
	"testing"
	"time"
)

func TestLogRecentHandshakeFailures(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		sb.WriteString("2026/08/05 09:00:00 [WARN] Handshake failed: handshake error: context deadline exceeded\n")
	}
	if !logRecentHandshakeFailures(sb.String(), handshakeFailWindowLines, handshakeFailMinCount) {
		t.Fatal("expected handshake failure spam to be unhealthy")
	}
	if logRecentHandshakeFailures("2026/08/05 09:00:00 [INFO] ok\n", handshakeFailWindowLines, handshakeFailMinCount) {
		t.Fatal("single line must not trigger")
	}
}

func TestClientPeerStaleZombie(t *testing.T) {
	started := time.Now().Add(-13 * time.Hour)
	old := time.Now().Add(-8 * time.Hour).Format("2006/01/02 15:04:05")
	log := old + " [INFO] [STREAM 1] Established DTLS connection\n" +
		old + " [INFO] [STREAM 2] Established DTLS connection\n"
	st := ProcessStatus{
		Running:   true,
		StartedAt: &started,
		Log:       log,
	}
	if !clientPeerStaleZombie(st, time.Now()) {
		t.Fatal("expected stale zombie with old log and counted DTLS")
	}
}

func TestServerPeerUnhealthyHandshakeSpam(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	var sb strings.Builder
	for i := 0; i < 8; i++ {
		sb.WriteString("2026/08/05 09:06:00 [WARN] Handshake failed: handshake error: context deadline exceeded\n")
	}
	st := ProcessStatus{Running: true, StartedAt: &started, Log: sb.String()}
	if !serverPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected unhealthy server on handshake spam")
	}
}

func TestClientPeerUnhealthyHandshakeSpam(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	var sb strings.Builder
	for i := 0; i < 8; i++ {
		sb.WriteString("2026/08/05 09:06:00 [WARN] Handshake failed: handshake error: context deadline exceeded\n")
	}
	st := ProcessStatus{Running: true, StartedAt: &started, Log: sb.String()}
	if !clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected unhealthy client on handshake spam")
	}
}
