package command

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func TestWireguardCommands_SetASCParams(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	cmds := NewWireguardCommands(poster, sc, q)

	params := json.RawMessage(`{"jc":"5","jmin":"50","jmax":"1000","s1":"10","s2":"20","h1":"aabbcc","h2":"ddeeff","h3":"112233","h4":"445566"}`)
	if err := cmds.SetASCParams(context.Background(), "Wireguard0", params); err != nil {
		t.Fatalf("SetASCParams: %v", err)
	}

	p := poster.Payloads()[0].(map[string]any)
	asc := p["interface"].(map[string]any)["Wireguard0"].(map[string]any)["wireguard"].(map[string]any)["asc"].(map[string]any)
	if asc["jc"] != "5" || asc["h1"] != "aabbcc" {
		t.Errorf("asc: %#v", asc)
	}
}

func TestWireguardCommands_SetASCParams_InvalidJSON(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	cmds := NewWireguardCommands(poster, sc, q)

	err := cmds.SetASCParams(context.Background(), "Wireguard0", json.RawMessage(`not json`))
	if err == nil {
		t.Fatalf("SetASCParams on invalid JSON: want error, got nil")
	}
	if poster.Calls() != 0 {
		t.Errorf("POST must not be called on parse error, got %d", poster.Calls())
	}
}
