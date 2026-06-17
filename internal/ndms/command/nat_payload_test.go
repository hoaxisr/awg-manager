package command

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newTestNATCommands(_ *testing.T) (*NATCommands, *fakePoster) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewNATCommands(poster, sc, q), poster
}

func TestNATCommands_SetSegmentNAT(t *testing.T) {
	cmds, poster := newTestNATCommands(t)
	if err := cmds.SetSegmentNAT(context.Background(), "Home"); err != nil {
		t.Fatal(err)
	}
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["nat"].(map[string]any)
	if r["interface"] != "Home" {
		t.Errorf("set nat: %#v", r)
	}
}

func TestNATCommands_RemoveSegmentNAT(t *testing.T) {
	cmds, poster := newTestNATCommands(t)
	_ = cmds.RemoveSegmentNAT(context.Background(), "Home")
	arr := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["nat"].([]map[string]any)
	if arr[0]["no"] != true || arr[0]["interface"] != "Home" {
		t.Errorf("no nat: %#v", arr)
	}
}

func TestNATCommands_SetStaticNAT(t *testing.T) {
	cmds, poster := newTestNATCommands(t)
	_ = cmds.SetStaticNAT(context.Background(), "Home", "PPPoE0")
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["static"].(map[string]any)
	if r["interface"] != "Home" || r["to-interface"] != "PPPoE0" {
		t.Errorf("static: %#v", r)
	}
}

func TestNATCommands_RemoveStaticNAT(t *testing.T) {
	cmds, poster := newTestNATCommands(t)
	_ = cmds.RemoveStaticNAT(context.Background(), "Home", "PPPoE0")
	arr := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["static"].([]map[string]any)
	if arr[0]["no"] != true || arr[0]["interface"] != "Home" || arr[0]["to-interface"] != "PPPoE0" {
		t.Errorf("no static: %#v", arr)
	}
}
