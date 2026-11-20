package nwg

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestNativeUplinkUsesCheckedConnectViaAndPreservesPeer(t *testing.T) {
	for _, scenario := range []string{"success", "rejected", "foreign-peer"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			var posts []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					b, _ := io.ReadAll(r.Body)
					mu.Lock()
					posts = append(posts, string(b))
					mu.Unlock()
					if scenario == "rejected" {
						io.WriteString(w, `{"status":[{"status":"error","message":"via refused"}]}`)
					} else {
						io.WriteString(w, `{}`)
					}
					return
				}
				if r.URL.Path == "/show/rc/interface/" {
					io.WriteString(w, `{"Wireguard0":{"description":"test","wireguard":{"peer":[{"key":"PUB","comment":"keep me","connect":true}]}}}`)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/show/rc/interface/") {
					io.WriteString(w, `{"description":"test","wireguard":{"peer":[{"key":"PUB","comment":"keep me","connect":true}]}}`)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/show/interface/") {
					key := "PUB"
					if scenario == "foreign-peer" {
						key = "OTHER"
					}
					json.NewEncoder(w).Encode(map[string]any{
						"Wireguard0": map[string]any{
							"id":        "Wireguard0",
							"type":      "Wireguard",
							"wireguard": map[string]any{"peer": []any{map[string]any{"public-key": key, "via": "ISP2", "remote-endpoint-address": "203.0.113.9", "remote-port": 51820, "fwmark": 16721}}},
						},
					})
					return
				}
				key := "PUB"
				if scenario == "foreign-peer" {
					key = "OTHER"
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "Wireguard0", "wireguard": map[string]any{"peer": []any{map[string]any{"public-key": key, "via": "ISP2", "remote-endpoint-address": "203.0.113.9", "remote-port": 51820, "fwmark": 16721}}}})
			}))
			defer srv.Close()
			tr := transport.NewWithURL(srv.URL, transport.NewSemaphore(2))
			defer tr.Close()
			q := query.NewQueries(query.Deps{Getter: tr, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
			save := command.NewSaveCoordinator(tr, nil, time.Hour, time.Hour, 0, nil)
			defer save.Flush(context.Background())
			op := NewOperator(q, command.NewCommands(command.Deps{Poster: tr, Queries: q, Save: save}), tr, nil)
			defer op.Close()
			op.supportsASC = func() bool { return true }
			op.supportsASC3 = func() bool { return true }
			stored := &storage.AWGTunnel{ID: "awg50", Backend: "nativewg", Enabled: true, NWGIndex: 0, Interface: storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{H1: "1", H2: "2", H3: "3", H4: "4"}}, Peer: storage.AWGPeer{PublicKey: "PUB", Endpoint: "203.0.113.9:51820"}}
			err := op.ApplyNativeUplink(context.Background(), stored, "ISP2")
			if scenario == "success" && err != nil {
				t.Fatal(err)
			}
			if scenario == "success" {
				if mark, e := op.NativeUplinkPeerMark(context.Background(), stored); e != nil || mark != 16721 {
					t.Fatal("native peer mark", mark, e)
				}
				if marks, e := op.NativeUplinkPeerMarks(context.Background()); e != nil || len(marks["nwg0"]) != 1 || marks["nwg0"][0] != 16721 {
					t.Fatal("native mark catalogue", marks, e)
				}
			}
			if scenario != "success" && err == nil {
				t.Fatal("router refusal or changed peer accepted")
			}
			mu.Lock()
			captured := append([]string(nil), posts...)
			mu.Unlock()
			if len(captured) == 0 || !strings.Contains(captured[0], `"via":"ISP2"`) || !strings.Contains(captured[0], `"comment":"keep me"`) || strings.Contains(captured[0], "endpoint") || strings.Contains(captured[0], "private-key") {
				t.Fatal(captured)
			}
			mutations := 0
			for _, post := range captured {
				if !strings.Contains(post, `"show"`) {
					mutations++
				}
			}
			if scenario == "rejected" && mutations != 1 {
				t.Fatal("continued after NDMS refusal", captured)
			}
			if scenario == "foreign-peer" && !errors.Is(err, uplink.ErrConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeUplinkPrestartFailureHasNoSideEffects(t *testing.T) {
	op := &OperatorNativeWG{}
	blocked := errors.New("uplink route unavailable")
	op.SetUplinkBeforeStart(func(context.Context, string) error { return blocked })
	if err := op.Start(context.Background(), &storage.AWGTunnel{ID: "awg50"}); !errors.Is(err, blocked) {
		t.Fatal(err)
	}
}
