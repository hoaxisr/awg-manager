package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type bindFailPoster struct{ calls int }

func (p *bindFailPoster) Post(_ context.Context, _ any) (json.RawMessage, error) {
	p.calls++
	if p.calls == 5 {
		return nil, errors.New("bind-fail")
	}
	return json.RawMessage(`{}`), nil
}

func newTestPingCheckCommands(_ *testing.T) (*PingCheckCommands, *fakePoster) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	return NewPingCheckCommands(poster, sc, q), poster
}

func TestPingCheckCommands_ConfigureProfile_PostSequence(t *testing.T) {
	cmds, poster := newTestPingCheckCommands(t)
	err := cmds.ConfigureProfile(context.Background(), "myprofile", confirmed(t, "Wireguard0"), ndms.PingCheckConfig{
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
	_ = cmds.ConfigureProfile(context.Background(), "p", confirmed(t, "W0"), ndms.PingCheckConfig{
		Host: "8.8.8.8", Mode: "ip", UpdateInterval: 60, Timeout: 1, Port: 443,
	})
	profile := poster.Payloads()[3].(map[string]any)["ping-check"].(map[string]any)["profile"].(map[string]any)["p"].(map[string]any)
	if _, ok := profile["port"]; ok {
		t.Errorf("port must be omitted for ip mode, got %v", profile["port"])
	}
}

func TestPingCheckCommands_ConfigureProfile_PortIncludedForConnectMode(t *testing.T) {
	cmds, poster := newTestPingCheckCommands(t)
	_ = cmds.ConfigureProfile(context.Background(), "p", confirmed(t, "W0"), ndms.PingCheckConfig{
		Host: "example.com", Mode: "connect", UpdateInterval: 60, Timeout: 1, Port: 443,
	})
	profile := poster.Payloads()[3].(map[string]any)["ping-check"].(map[string]any)["profile"].(map[string]any)["p"].(map[string]any)
	if profile["port"] != 443 {
		t.Errorf("port: %v", profile["port"])
	}
}

func TestPingCheckCommands_ConfigureProfile_PortIncludedForTLSMode(t *testing.T) {
	cmds, poster := newTestPingCheckCommands(t)
	_ = cmds.ConfigureProfile(context.Background(), "p", confirmed(t, "W0"), ndms.PingCheckConfig{
		Host: "dns.example", Mode: "tls", UpdateInterval: 60, Timeout: 1, Port: 853,
	})
	profile := poster.Payloads()[3].(map[string]any)["ping-check"].(map[string]any)["profile"].(map[string]any)["p"].(map[string]any)
	if profile["port"] != 853 {
		t.Errorf("port: %v", profile["port"])
	}
}

func TestPingCheckCommands_ConfigureProfile_CreateError(t *testing.T) {
	cmds, poster := newTestPingCheckCommands(t)
	poster.SetError(errors.New("boom"))
	err := cmds.ConfigureProfile(context.Background(), "p", confirmed(t, "W0"), ndms.PingCheckConfig{
		Host: "1.1.1.1", Mode: "icmp", UpdateInterval: 45, Timeout: 5, Restart: true,
	})
	if err == nil || !strings.Contains(err.Error(), "create ping-check profile") {
		t.Fatalf("err = %v, want contains create ping-check profile", err)
	}
}

func TestPingCheckCommands_ConfigureProfile_BindError(t *testing.T) {
	poster := &bindFailPoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	cmds := NewPingCheckCommands(poster, sc, q)
	err := cmds.ConfigureProfile(context.Background(), "p", confirmed(t, "W0"), ndms.PingCheckConfig{
		Host: "1.1.1.1", Mode: "icmp", UpdateInterval: 45, Timeout: 5, Restart: true,
	})
	if err == nil || !strings.Contains(err.Error(), "bind ping-check profile") {
		t.Fatalf("err = %v, want contains bind ping-check profile", err)
	}
}

func TestPingCheckCommands_RemoveProfile_3PostsIgnoreErrors(t *testing.T) {
	cmds, poster := newTestPingCheckCommands(t)
	poster.SetError(nil)
	if err := cmds.RemoveProfile(context.Background(), "myprofile", confirmed(t, "Wireguard0")); err != nil {
		t.Fatalf("RemoveProfile: %v", err)
	}
	if len(poster.Payloads()) != 3 {
		t.Errorf("POST count: want 3, got %d", len(poster.Payloads()))
	}
}

// pingCheckStatus — /show/ping-check/ с профилем p, привязанным к ifaces.
func pingCheckStatus(ifaces ...string) map[string]string {
	parts := make([]string, 0, len(ifaces))
	for _, i := range ifaces {
		parts = append(parts, `"`+i+`":{"status":"pass"}`)
	}
	return map[string]string{"/show/ping-check/": `{"pingcheck":[{"profile":"p","interface":{` + strings.Join(parts, ",") + `}}]}`}
}

// unbindPosts — команды `interface X ping-check restart no` / `profile no`.
func unbindPosts(posts []string) int {
	n := 0
	for _, p := range posts {
		if strings.Contains(p, `"interface":`) && strings.Contains(p, `"no":true`) {
			n++
		}
	}
	return n
}

// Привязки у интерфейса нет — снимать её нечем: NDMS на такую команду пишет E
// «interface "X" has no assigned profile».
func TestConfigureProfile_NoUnbindWhenNotBound(t *testing.T) {
	cmds, f, q := newOracleCommands(t, pingCheckStatus("Wireguard5"), ndms.Interface{ID: "Wireguard0"})
	c, _, _, _ := q.Interfaces.Confirm(context.Background(), "Wireguard0")
	if err := cmds.PingCheck.ConfigureProfile(context.Background(), "p", c, ndms.PingCheckConfig{Host: "8.8.8.8", Mode: "icmp"}); err != nil {
		t.Fatal(err)
	}
	if n := unbindPosts(f.Posts); n != 0 {
		t.Fatalf("снятие несуществующей привязки: %d команд, posts=%v", n, f.Posts)
	}
	var created, bound bool
	for _, p := range f.Posts {
		created = created || strings.HasPrefix(p, `{"ping-check":{"profile":{"p":{"host"`)
		bound = bound || strings.HasPrefix(p, `{"interface":{"Wireguard0":{"ping-check":{"profile":"p"`)
	}
	if !created || !bound {
		t.Fatalf("created=%v bound=%v posts=%v", created, bound, f.Posts)
	}
	if f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("phantoms=%d E=%d", f.Phantoms, f.E)
	}
}

// Статус не прочитался — привязка неизвестна: шлются все три команды, как
// раньше (E в журнале дешевле оставленной привязки).
func TestRemoveProfile_StatusFetchError_SendsAll(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil, ndms.Interface{ID: "Wireguard0"}) // /show/ping-check/ вне модели → ошибка
	c, _, _, _ := q.Interfaces.Confirm(context.Background(), "Wireguard0")
	if err := cmds.PingCheck.RemoveProfile(context.Background(), "p", c); err != nil {
		t.Fatal(err)
	}
	var cmdsSent int // без чтений {"show":…} (точечное чтение после Invalidate)
	for _, p := range f.Posts {
		if !strings.HasPrefix(p, `{"show":`) {
			cmdsSent++
		}
	}
	if cmdsSent != 3 || unbindPosts(f.Posts) != 2 {
		t.Fatalf("posts=%v", f.Posts)
	}
}

// Привязка есть — снимается, как раньше.
func TestConfigureProfile_UnbindWhenBound(t *testing.T) {
	cmds, f, q := newOracleCommands(t, pingCheckStatus("Wireguard0"), ndms.Interface{ID: "Wireguard0"})
	c, _, _, _ := q.Interfaces.Confirm(context.Background(), "Wireguard0")
	if err := cmds.PingCheck.ConfigureProfile(context.Background(), "p", c, ndms.PingCheckConfig{Host: "8.8.8.8", Mode: "icmp"}); err != nil {
		t.Fatal(err)
	}
	if n := unbindPosts(f.Posts); n != 2 {
		t.Fatalf("снятие привязки: %d команд, want 2; posts=%v", n, f.Posts)
	}
}

func TestRemoveOrphanProfile_NoInterfaceCommands(t *testing.T) {
	cmds, f, _ := newOracleCommands(t, nil)
	if err := cmds.PingCheck.RemoveOrphanProfile(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	if len(f.Posts) != 1 || f.Posts[0] != `{"ping-check":{"profile":{"p":{"no":true}}}}` {
		t.Fatalf("posts=%v", f.Posts)
	}
	if f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("phantoms=%d E=%d", f.Phantoms, f.E)
	}
}
