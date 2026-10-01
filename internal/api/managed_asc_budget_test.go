package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/managed"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F600: PUT ASC managed-сервера от запроса до ответа со снимком серверов —
// не больше двух деревьев /show/rc/interface/: свежее перед записью (Q6) и
// сверка. Дерево сверки прочитано после записи — снимок ответа берёт его
// из кэша, а не читает третье (стенд A4/D6: было 3 по ~650 мс).
func TestManagedASC_PutRCTreeBudget(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{
		"Wireguard3":{"id":"Wireguard3","type":"Wireguard","description":"srv","state":"up","link":"up","address":"10.9.3.1","mask":"255.255.255.0","wireguard":{"peer":[]}}}`)
	fg.SetRC("Wireguard3", `{"wireguard":{"asc":{"jc":"1","jmin":"10","jmax":"20","s1":"1","s2":"2","h1":"1","h2":"2","h3":"3","h4":"4"}}}`)
	fg.SetJSON("/show/running-config", `{"message":["!"]}`)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	poster := &natPoster{}
	// Роутер применяет запись ASC сразу (стенд 01.10: rc отражает её после ответа на POST).
	poster.setFailOn(func(payload string) error {
		var p struct {
			Interface map[string]struct {
				Wireguard struct {
					ASC map[string]any `json:"asc"`
				} `json:"wireguard"`
			} `json:"interface"`
		}
		if json.Unmarshal([]byte(payload), &p) == nil && p.Interface["Wireguard3"].Wireguard.ASC != nil {
			b, _ := json.Marshal(map[string]any{"wireguard": map[string]any{"asc": p.Interface["Wireguard3"].Wireguard.ASC}})
			fg.SetRC("Wireguard3", string(b))
		}
		return nil
	})
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard3", Address: "10.9.3.1", Mask: "255.255.255.0", ListenPort: 51003}); err != nil {
		t.Fatal(err)
	}
	cmds := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  poster,
		Queries: queries,
		Save:    ndmscommand.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil),
	})
	svc := managed.New(poster, nil, queries, cmds, store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	sh := NewServersHandler(queries, store, nil, &appLogSpy{})
	sh.SetManagedService(svc)
	p := newBusProbe(t)
	sh.SetEventBus(p.bus())
	h := NewManagedServerHandler(svc, nil)
	h.SetServersHandler(sh)
	sh.SetManagedHandler(h)

	before := fg.Calls("/show/rc/interface/")
	rr := perform(h.Subtree, http.MethodPut, "/api/managed-servers/Wireguard3/asc",
		`{"jc":3,"jmin":64,"jmax":256,"s1":15,"s2":16,"h1":"100000001","h2":"1200000002","h3":"2400000003","h4":"3600000004"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("код=%d тело=%s", rr.Code, rr.Body.String())
	}
	if n := fg.Calls("/show/rc/interface/") - before; n > 2 {
		t.Fatalf("деревьев rc на правку ASC: %d, want ≤2 (до записи + сверка)", n)
	}

	// Без сброса кэша показ не устарел: GET после PUT отдаёт НОВЫЕ значения.
	rr = perform(h.Subtree, http.MethodGet, "/api/managed-servers/Wireguard3/asc", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET: код=%d тело=%s", rr.Code, rr.Body.String())
	}
	data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
	for k, want := range map[string]string{"jc": "3", "jmin": "64", "jmax": "256", "s1": "15", "s2": "16", "h1": "100000001", "h4": "3600000004"} {
		if got := fmt.Sprint(data[k]); got != want {
			t.Fatalf("GET после PUT: %s=%s, want %s (data=%v)", k, got, want, data)
		}
	}
}
