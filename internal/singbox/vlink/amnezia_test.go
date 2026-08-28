package vlink

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

func stdBase64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func TestParseAmnezia_VLESSPayload(t *testing.T) {
	xrayConfig := `{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"example.com","port":443,"users":[{"id":"3a3b1c2e-9999-4321-aaaa-1234567890ab","flow":"xtls-rprx-vision"}]}]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"serverName":"foo.com","publicKey":"PBK","shortId":"ab12","fingerprint":"chrome"}}}]}`
	link := "vpn://" + stdBase64(xrayConfig) + "#name"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	if ob["type"] != "vless" {
		t.Errorf("expected vless, got %v", ob["type"])
	}
}

func TestParseAmnezia_NotVLESS_StructuredError(t *testing.T) {
	xrayConfig := `{"outbounds":[{"protocol":"trojan","settings":{}}]}`
	link := "vpn://" + stdBase64(xrayConfig) + "#name"
	_, err := ParseLink(link)
	if err == nil {
		t.Fatal("expected error")
	}
	var aerr *ErrAmneziaUnsupportedProtocol
	if !errors.As(err, &aerr) {
		t.Errorf("expected ErrAmneziaUnsupportedProtocol, got %T: %v", err, err)
	} else if aerr.Protocol != "trojan" {
		t.Errorf("Protocol=%q want trojan", aerr.Protocol)
	}
}

// Битый streamSettings внутри vpn:// обязан ронять ссылку: молча отдать
// reality-сервер как plain-TCP vless — худший исход, туннель «есть», но не
// защищает.
func TestParseAmnezia_BrokenStreamSettings(t *testing.T) {
	cfg := `{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":443,` +
		`"users":[{"id":"11111111-2222-3333-4444-555555555555"}]}]},"streamSettings":{"network":123}}]}`
	link := "vpn://" + base64.RawURLEncoding.EncodeToString([]byte(cfg))
	got, err := ParseLink(link)
	if err == nil {
		t.Fatalf("битый streamSettings принят: %s", got.Outbound)
	}
}
