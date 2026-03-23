package rci

import (
	"context"
	"net/http"
	"testing"
)

func TestShowVersion(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/show/version" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"release":"4.2.1","title":"Keenetic","arch":"mips","model":"KN-1234"}`))
	}))
	defer srv.Close()

	info, err := c.ShowVersion(context.Background())
	if err != nil {
		t.Fatalf("ShowVersion: %v", err)
	}
	if info.Release != "4.2.1" {
		t.Errorf("Release = %q, want 4.2.1", info.Release)
	}
	if info.Model != "KN-1234" {
		t.Errorf("Model = %q, want KN-1234", info.Model)
	}
}

func TestShowIPRoute(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/show/ip/route" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"destination":"0.0.0.0/0","gateway":"192.168.1.1","interface":"ISP"},{"destination":"10.0.0.0/8","gateway":"10.0.0.1","interface":"Wireguard0"}]`))
	}))
	defer srv.Close()

	routes, err := c.ShowIPRoute(context.Background())
	if err != nil {
		t.Fatalf("ShowIPRoute: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("len(routes) = %d, want 2", len(routes))
	}
	if routes[0].Interface != "ISP" {
		t.Errorf("routes[0].Interface = %q", routes[0].Interface)
	}
	if routes[1].Destination != "10.0.0.0/8" {
		t.Errorf("routes[1].Destination = %q", routes[1].Destination)
	}
}

func TestGetSystemName(t *testing.T) {
	t.Run("returns name", func(t *testing.T) {
		c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/show/interface/system-name" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			if r.URL.Query().Get("name") != "Wireguard0" {
				t.Errorf("unexpected query name: %s", r.URL.Query().Get("name"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"name":"nwg0"}`))
		}))
		defer srv.Close()

		name, err := c.GetSystemName(context.Background(), "Wireguard0")
		if err != nil {
			t.Fatalf("GetSystemName: %v", err)
		}
		if name != "nwg0" {
			t.Errorf("name = %q, want nwg0", name)
		}
	})

	t.Run("empty name returns fallback", func(t *testing.T) {
		c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"name":""}`))
		}))
		defer srv.Close()

		name, err := c.GetSystemName(context.Background(), "Wireguard0")
		if err != nil {
			t.Fatalf("GetSystemName: %v", err)
		}
		if name != "Wireguard0" {
			t.Errorf("name = %q, want Wireguard0 (fallback)", name)
		}
	})
}

func TestShowInterface(t *testing.T) {
	c, srv := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/show/interface/Wireguard0" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state":"up","link":"up"}`))
	}))
	defer srv.Close()

	raw, err := c.ShowInterface(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("ShowInterface: %v", err)
	}
	if len(raw) == 0 {
		t.Error("expected non-empty response")
	}
}
