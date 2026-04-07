package rci

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestBatch_AddAndLen(t *testing.T) {
	b := NewBatch()
	if b.Len() != 0 {
		t.Fatalf("new batch Len() = %d, want 0", b.Len())
	}
	b.Add(CmdInterfaceCreate("Wireguard0"))
	b.Add(CmdInterfaceUp("Wireguard0", true))
	if b.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", b.Len())
	}
}

func TestBatch_Reset(t *testing.T) {
	b := NewBatch()
	b.Add(CmdSave())
	b.Reset()
	if b.Len() != 0 {
		t.Fatalf("after Reset Len() = %d, want 0", b.Len())
	}
}

func TestBatch_FluentChaining(t *testing.T) {
	b := NewBatch().
		InterfaceCreate("Wireguard0").
		InterfaceDescription("Wireguard0", "test").
		InterfaceSecurityLevel("Wireguard0", "public").
		InterfaceUp("Wireguard0", true).
		InterfaceIPAddress("Wireguard0", "10.0.0.1", "255.255.255.0").
		InterfaceMTU("Wireguard0", 1420).
		InterfaceAdjustMSS("Wireguard0", true).
		InterfaceIPGlobal("Wireguard0", true).
		InterfaceDNS("Wireguard0", []string{"8.8.8.8"}).
		InterfaceDNSClear("Wireguard0").
		InterfaceIPv6Address("Wireguard0", "fd00::1").
		InterfaceIPv6AddressClear("Wireguard0").
		WireguardPrivateKey("Wireguard0", "key=").
		WireguardPeer("Wireguard0", PeerConfig{PublicKey: "pk="}).
		WireguardPeerDelete("Wireguard0", "pk=").
		WireguardPeerEndpoint("Wireguard0", "pk=", "1.2.3.4:51820").
		WireguardPeerConnect("Wireguard0", "pk=", "ISP").
		SetDefaultRoute("Wireguard0").
		RemoveDefaultRoute("Wireguard0").
		InterfaceDelete("Wireguard0").
		Save()

	if b.Len() != 21 {
		t.Fatalf("fluent chain Len() = %d, want 21", b.Len())
	}
}

func TestBatch_ExecuteEmpty(t *testing.T) {
	b := NewBatch()
	err := b.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute empty batch: %v", err)
	}
}

func TestBatch_ExecuteWithResultsEmpty(t *testing.T) {
	b := NewBatch()
	results, err := b.ExecuteWithResults(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecuteWithResults empty: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results, got %v", results)
	}
}

func TestBatch_ExecuteSendsArray(t *testing.T) {
	var receivedBody []byte
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{},{}]`))
	}))
	defer srv.Close()

	b := NewBatch().
		InterfaceCreate("Wireguard0").
		InterfaceUp("Wireguard0", true)

	err := b.Execute(context.Background(), c)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(receivedBody, &arr); err != nil {
		t.Fatalf("body is not JSON array: %v\nbody: %s", err, receivedBody)
	}
	if len(arr) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(arr))
	}
}

func TestBatch_ExecuteWithResultsReturnsResponses(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"ok":true},{"ok":true},{"ok":true}]`))
	}))
	defer srv.Close()

	b := NewBatch().
		InterfaceCreate("Wireguard0").
		InterfaceUp("Wireguard0", true).
		Save()

	results, err := b.ExecuteWithResults(context.Background(), c)
	if err != nil {
		t.Fatalf("ExecuteWithResults: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}
