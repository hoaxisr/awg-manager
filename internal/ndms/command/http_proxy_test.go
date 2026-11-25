package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newTestHTTPProxyCommands(_ *testing.T) (*HTTPProxyCommands, *fakePoster) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, time.Hour, time.Hour, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewHTTPProxyCommands(poster, sc, q), poster
}

func TestValidateKeenDNSLabel(t *testing.T) {
	valid := []string{"connect", "vless", "tg", "a", "my-vless-1", "x123"}
	for _, lbl := range valid {
		if err := ValidateKeenDNSLabel(lbl); err != nil {
			t.Errorf("ValidateKeenDNSLabel(%q) unexpected error: %v", lbl, err)
		}
	}

	invalid := []string{
		"",
		"-start",
		"end-",
		"UpperCase",
		"with.dot",
		"*.wildcard",
		"sub/path",
		"host:9009",
		"with space",
		"no",
		"admin",
		"keenetic",
		strings.Repeat("a", 64),
	}
	for _, lbl := range invalid {
		if err := ValidateKeenDNSLabel(lbl); err == nil {
			t.Errorf("ValidateKeenDNSLabel(%q) expected error, got nil", lbl)
		}
	}
}

func TestValidatePublicationSpec_SecurityGuardrails(t *testing.T) {
	// Valid AWGM ingress port 9009
	err := ValidatePublicationSpec(HTTPProxyPublicationSpec{
		Label:        "connect",
		UpstreamHost: "127.0.0.1",
		UpstreamPort: 9009,
	})
	if err != nil {
		t.Fatalf("expected valid spec for 127.0.0.1:9009, got: %v", err)
	}

	// Forbidden ports: AWGM admin (2222), SSH (22), Keenetic Web UI (80, 443, 79), Mihomo (1099), custom admin port (3333)
	forbidden := []int{2222, 22, 80, 443, 79, 1099, 2398, 8086, 3333, 8080}
	for _, port := range forbidden {
		err := ValidatePublicationSpec(HTTPProxyPublicationSpec{
			Label:          "connect",
			UpstreamHost:   "127.0.0.1",
			UpstreamPort:   port,
			AllowedPorts:   []int{9009},
			ForbiddenPorts: []int{3333},
		})
		if err == nil || !errors.Is(err, ErrForbiddenPublicationPort) {
			t.Errorf("expected ErrForbiddenPublicationPort for port %d, got: %v", port, err)
		}
	}

	// Arbitrary LAN host rejected
	err = ValidatePublicationSpec(HTTPProxyPublicationSpec{
		Label:        "connect",
		UpstreamHost: "192.168.1.50",
		UpstreamPort: 9009,
	})
	if err == nil || !errors.Is(err, ErrInvalidPublicationTarget) {
		t.Errorf("expected ErrInvalidPublicationTarget for non-loopback host, got: %v", err)
	}
}

func TestHTTPProxyCommands_UpsertPreservesUnknownFieldsAndDelete(t *testing.T) {
	cmds, poster := newTestHTTPProxyCommands(t)
	ctx := context.Background()

	err := cmds.UpsertPublication(ctx, HTTPProxyPublicationSpec{
		Label:        "connect",
		UpstreamHost: "127.0.0.1",
		UpstreamPort: 9009,
		BaseRawJSON:  `{"custom-vendor-flag": true, "preserve-host": false, "ssl": {"redirect": false, "hsts": true}, "upstream": {"timeout": 30}, "domain": {"custom-sub": "val"}, "security-level": {"rate-limit": 100}}`,
	})
	if err != nil {
		t.Fatalf("UpsertPublication: %v", err)
	}

	payloads := poster.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("expected 1 payload, got %d", len(payloads))
	}
	raw, _ := json.Marshal(payloads[0])
	var parsed struct {
		IP struct {
			HTTP struct {
				Proxy map[string]map[string]any `json:"proxy"`
			} `json:"http"`
		} `json:"ip"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	entry := parsed.IP.HTTP.Proxy["connect"]
	if entry == nil {
		t.Fatalf("expected proxy.connect in payload: %s", string(raw))
	}
	if entry["custom-vendor-flag"] != true {
		t.Errorf("expected unknown field custom-vendor-flag preserved, got %#v", entry)
	}
	if entry["preserve-host"] != false {
		t.Errorf("expected existing preserve-host=false preserved when BaseRawJSON provided, got %#v", entry)
	}
	sslMap, _ := entry["ssl"].(map[string]any)
	if sslMap["redirect"] != false || sslMap["hsts"] != true {
		t.Errorf("expected existing ssl.redirect=false and ssl.hsts=true preserved, got %#v", sslMap)
	}
	up := entry["upstream"].(map[string]any)
	if up["proto"] != "http" || up["upstream"] != "127.0.0.1" || up["port"] != "9009" || up["timeout"] != float64(30) {
		t.Errorf("unexpected upstream: %#v", up)
	}
	dom := entry["domain"].(map[string]any)
	if dom["ndns"] != true || dom["custom-sub"] != "val" {
		t.Errorf("unexpected domain: %#v", dom)
	}
	sec := entry["security-level"].(map[string]any)
	if sec["public"] != true || sec["rate-limit"] != float64(100) {
		t.Errorf("unexpected security-level: %#v", sec)
	}

	// Delete publication
	if err := cmds.DeletePublication(ctx, "connect"); err != nil {
		t.Fatalf("DeletePublication: %v", err)
	}
	delRaw, _ := json.Marshal(poster.Payloads()[1])
	if !strings.Contains(string(delRaw), `"connect":{"no":true}`) {
		t.Errorf("unexpected delete payload: %s", string(delRaw))
	}
}
