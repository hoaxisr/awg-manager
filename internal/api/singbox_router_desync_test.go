package api

import (
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

func TestDesyncProbeOptions(t *testing.T) {
	got, err := desyncProbeOptions(router.Outbound{
		Type: "desync", Tag: "dpi", Strategy: "mode=split",
		DomainResolver: &router.DomainResolver{Server: "dns-direct"},
		DesyncFields:   router.DesyncFields{Preset: "auto", TTL: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"preset":"auto","strategy":"mode=split","ttl":4}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if _, err := desyncProbeOptions(router.Outbound{Type: "selector", Tag: "g"}); err == nil {
		t.Error("non-desync outbound must be rejected")
	}
}

func TestProbeErrorText(t *testing.T) {
	stderr := "Error: ttl and udp_ttl must be 1..255\nUsage:\n  awgm-box desync probe [flags]\nFATAL ttl\n"
	if got := probeErrorText(stderr, errors.New("exit status 1")); got != "ttl and udp_ttl must be 1..255" {
		t.Errorf("got %q", got)
	}
	if got := probeErrorText("", errors.New("exec: not found")); got != "exec: not found" {
		t.Errorf("got %q", got)
	}
}
