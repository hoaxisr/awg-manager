package command

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newTestPingCheckCommands(_ *testing.T) (*PingCheckCommands, *fakePoster) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	return NewPingCheckCommands(poster, sc, q), poster
}

func TestPingCheckCommands_ConfigureProfile_PostSequence(t *testing.T) {
	cmds, poster := newTestPingCheckCommands(t)
	err := cmds.ConfigureProfile(context.Background(), "myprofile", "Wireguard0", ndms.PingCheckConfig{
		Host:           "8.8.8.8",
		Mode:           "ip",
		UpdateInterval: 60,
		Timeout:        1,
		MaxFails:       3,
		MinSuccess:     1,
		Restart:        true,
	})
	if err != nil {
		t.Fatalf("ConfigureProfile: %v", err)
	}
	if len(poster.Payloads()) != 5 {
		t.Fatalf("POST count: want 5, got %d", len(poster.Payloads()))
	}

	p4 := poster.Payloads()[3].(map[string]any)
	profile := p4["ping-check"].(map[string]any)["profile"].(map[string]any)["myprofile"].(map[string]any)
	if profile["host"] != "8.8.8.8" || profile["mode"] != "ip" {
		t.Errorf("profile: %#v", profile)
	}
	ui := profile["update-interval"].(map[string]any)
	if ui["seconds"] != 60 {
		t.Errorf("update-interval: %#v", ui)
	}

	p5 := poster.Payloads()[4].(map[string]any)
	pc := p5["interface"].(map[string]any)["Wireguard0"].(map[string]any)["ping-check"].(map[string]any)
	if pc["profile"] != "myprofile" || pc["restart"] != true {
		t.Errorf("bind: %#v", pc)
	}
}

func TestPingCheckCommands_ConfigureProfile_PortOmittedForIPMode(t *testing.T) {
	cmds, poster := newTestPingCheckCommands(t)
	_ = cmds.ConfigureProfile(context.Background(), "p", "W0", ndms.PingCheckConfig{
		Host: "8.8.8.8", Mode: "ip", UpdateInterval: 60, Timeout: 1, Port: 443,
	})
	profile := poster.Payloads()[3].(map[string]any)["ping-check"].(map[string]any)["profile"].(map[string]any)["p"].(map[string]any)
	if _, ok := profile["port"]; ok {
		t.Errorf("port must be omitted for ip mode, got %v", profile["port"])
	}
}

func TestPingCheckCommands_ConfigureProfile_PortIncludedForConnectMode(t *testing.T) {
	cmds, poster := newTestPingCheckCommands(t)
	_ = cmds.ConfigureProfile(context.Background(), "p", "W0", ndms.PingCheckConfig{
		Host: "example.com", Mode: "connect", UpdateInterval: 60, Timeout: 1, Port: 443,
	})
	profile := poster.Payloads()[3].(map[string]any)["ping-check"].(map[string]any)["profile"].(map[string]any)["p"].(map[string]any)
	if profile["port"] != 443 {
		t.Errorf("port: %v", profile["port"])
	}
}

func TestPingCheckCommands_RemoveProfile_3PostsIgnoreErrors(t *testing.T) {
	cmds, poster := newTestPingCheckCommands(t)
	poster.SetError(nil)
	if err := cmds.RemoveProfile(context.Background(), "myprofile", "Wireguard0"); err != nil {
		t.Fatalf("RemoveProfile: %v", err)
	}
	if len(poster.Payloads()) != 3 {
		t.Errorf("POST count: want 3, got %d", len(poster.Payloads()))
	}
}
