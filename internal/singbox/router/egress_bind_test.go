package router

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPatchOutboundBindInterface(t *testing.T) {
	raw := json.RawMessage(`{"type":"socks","tag":"t1","server":"1.2.3.4","server_port":1080}`)
	withBind, err := patchOutboundBindInterface(raw, "eth3")
	if err != nil {
		t.Fatal(err)
	}
	var ob map[string]any
	if err := json.Unmarshal(withBind, &ob); err != nil {
		t.Fatal(err)
	}
	if ob["bind_interface"] != "eth3" {
		t.Fatalf("bind_interface = %v", ob["bind_interface"])
	}
	cleared, err := patchOutboundBindInterface(withBind, "")
	if err != nil {
		t.Fatal(err)
	}
	var clearedOb map[string]any
	if err := json.Unmarshal(cleared, &clearedOb); err != nil {
		t.Fatal(err)
	}
	if _, ok := clearedOb["bind_interface"]; ok {
		t.Fatalf("expected bind_interface removed")
	}
}

type fakeTunnelEditor struct {
	tags    map[string]json.RawMessage
	updated map[string]json.RawMessage
}

func (f *fakeTunnelEditor) GetTunnelOutbound(_ context.Context, tag string) (json.RawMessage, error) {
	raw, ok := f.tags[tag]
	if !ok {
		return nil, ErrOutboundNotFound
	}
	return raw, nil
}

func (f *fakeTunnelEditor) UpdateTunnelOutbound(_ context.Context, tag string, outbound json.RawMessage) error {
	f.updated[tag] = outbound
	f.tags[tag] = outbound
	return nil
}

func (f *fakeTunnelEditor) IsSingboxTunnelTag(_ context.Context, tag string) bool {
	_, ok := f.tags[tag]
	return ok
}
