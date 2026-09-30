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
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
)

// respPoster returns a fixed body, mimicking a real NDMS POST response.
type respPoster struct{ body string }

func (r *respPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	return json.RawMessage(r.body), nil
}

type errPoster struct{ err error }

func (e *errPoster) Post(context.Context, any) (json.RawMessage, error) { return nil, e.err }

// Real success response captured from a router (5.01.A.x): the import
// command returns the created interface name in import.created, plus an
// informational status[] entry and an empty intersects field.
const realImportSuccess = `{
  "interface": {
    "wireguard": {
      "import": {
        "intersects": "",
        "created": "Wireguard3",
        "status": [
          {"status": "message", "code": "75507472", "ident": "Wireguard::Interface", "message": "\"Wireguard3\": imported settings."}
        ]
      }
    }
  }
}`

// listQueries — список интерфейсов, где уже есть names: импорт через
// respPoster до FakeNDMS не доходит, созданное подтверждается этим списком.
func listQueries(names ...string) *query.Queries {
	ifaces := make([]ndms.Interface, 0, len(names))
	for _, n := range names {
		ifaces = append(ifaces, ndms.Interface{ID: n})
	}
	return query.NewQueries(query.Deps{Getter: query.NewFakeNDMS(ifaces...), Logger: query.NopLogger()})
}

func TestImportWireguardConfig_Success_ReturnsCreated(t *testing.T) {
	c := NewWireguardCommands(&respPoster{body: realImportSuccess}, nil, listQueries("Wireguard3"))

	res, err := c.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Created.Name() != "Wireguard3" {
		t.Errorf("created name: want Wireguard3, got %q", res.Created.Name())
	}
}

func TestImportWireguardConfig_Success_PreservesIntersectAndMessages(t *testing.T) {
	// Importing a config whose keys collide with an existing interface still
	// creates a new one, but the router reports the collision in intersects.
	// The caller must not lose that context.
	body := `{"interface":{"wireguard":{"import":{"intersects":"Wireguard3","created":"Wireguard4","status":[{"status":"message","code":"75507472","ident":"Wireguard::Interface","message":"\"Wireguard4\": imported settings."}]}}}}`
	c := NewWireguardCommands(&respPoster{body: body}, nil, listQueries("Wireguard3", "Wireguard4"))

	res, err := c.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Created.Name() != "Wireguard4" {
		t.Errorf("created: want Wireguard4, got %q", res.Created.Name())
	}
	if res.Intersects != "Wireguard3" {
		t.Errorf("intersects: want Wireguard3, got %q", res.Intersects)
	}
	if len(res.Messages) != 1 || !strings.Contains(res.Messages[0], "imported settings") {
		t.Errorf("messages: want one 'imported settings', got %v", res.Messages)
	}
}

func TestImportWireguardConfig_EmptyCreated_SurfacesStatusMessage(t *testing.T) {
	// Same object schema as the real response, but created is empty and the
	// status[] carries the router's explanation. The current code throws
	// this message away and reports an opaque "empty created field".
	body := `{"interface":{"wireguard":{"import":{"intersects":"","created":"","status":[{"status":"error","code":"75507473","ident":"Wireguard::Interface","message":"interface limit reached"}]}}}}`
	c := NewWireguardCommands(&respPoster{body: body}, nil, nil)

	_, err := c.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err == nil {
		t.Fatal("expected error when created is empty")
	}
	if !strings.Contains(err.Error(), "interface limit reached") {
		t.Errorf("error should surface router status message, got: %v", err)
	}
}

func TestImportWireguardConfig_EmptyCreated_IncludesIntersects(t *testing.T) {
	body := `{"interface":{"wireguard":{"import":{"intersects":"Wireguard5","created":"","status":[]}}}}`
	c := NewWireguardCommands(&respPoster{body: body}, nil, nil)

	_, err := c.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err == nil {
		t.Fatal("expected error when created is empty")
	}
	if !strings.Contains(err.Error(), "Wireguard5") {
		t.Errorf("error should name the intersecting interface, got: %v", err)
	}
}

func TestImportWireguardConfig_PosterError(t *testing.T) {
	c := NewWireguardCommands(&errPoster{err: errors.New("boom")}, nil, nil)
	_, err := c.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err == nil || !strings.Contains(err.Error(), "import wireguard") {
		t.Fatalf("err = %v, want contains import wireguard", err)
	}
}

func TestImportWireguardConfig_InvalidResponseJSON(t *testing.T) {
	c := NewWireguardCommands(&respPoster{body: `{"interface":`}, nil, nil)
	_, err := c.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err == nil || !strings.Contains(err.Error(), "import wireguard: decode") {
		t.Fatalf("err = %v, want decode error", err)
	}
}

func TestImportWireguardConfig_EmptyCreatedNoStatusMessage(t *testing.T) {
	body := `{"interface":{"wireguard":{"import":{"intersects":"","created":"","status":[]}}}}`
	c := NewWireguardCommands(&respPoster{body: body}, nil, nil)
	_, err := c.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err == nil || !strings.Contains(err.Error(), "no status message") {
		t.Fatalf("err = %v, want no status message", err)
	}
}

func TestWireguardCommands_SetASCParams(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	cmds := NewWireguardCommands(poster, sc, q)

	params := json.RawMessage(`{"jc":"5","jmin":"50","jmax":"1000","s1":"10","s2":"20","h1":"aabbcc","h2":"ddeeff","h3":"112233","h4":"445566"}`)
	if err := cmds.SetASCParams(context.Background(), confirmed(t, "Wireguard0"), params); err != nil {
		t.Fatalf("SetASCParams: %v", err)
	}

	p := poster.Payloads()[0].(map[string]any)
	asc := p["interface"].(map[string]any)["Wireguard0"].(map[string]any)["wireguard"].(map[string]any)["asc"].(map[string]any)
	if asc["jc"] != "5" || asc["h1"] != "aabbcc" {
		t.Errorf("asc: %#v", asc)
	}
}

// Disabling a peer must carry its comment, else NDMS wipes the stored name.
func TestWireguardCommands_SetPeerConnect_PreservesComment(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{"Wireguard0":{"id":"Wireguard0","type":"Wireguard"}}`) // кэш интерфейсов: rc читается только по известным (F546)
	fg.SetRC("Wireguard0", `{"wireguard":{"peer":[{"key":"KEY="}]}}`)
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	cmds := NewWireguardCommands(poster, sc, q)

	if err := cmds.SetPeerConnect(context.Background(), confirmed(t, "Wireguard0"), "KEY=", false, "dacha"); err != nil {
		t.Fatalf("SetPeerConnect: %v", err)
	}

	p := poster.Payloads()[0].(map[string]any)
	peer := p["interface"].(map[string]any)["Wireguard0"].(map[string]any)["wireguard"].(map[string]any)["peer"].([]map[string]any)[0]
	if peer["connect"] != false {
		t.Errorf("connect: %#v", peer["connect"])
	}
	if peer["comment"] != "dacha" {
		t.Errorf("comment not carried: %#v", peer["comment"])
	}
}

// An empty name must NOT emit a comment key (nothing to preserve).
func TestWireguardCommands_SetPeerConnect_OmitsEmptyComment(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{"Wireguard0":{"id":"Wireguard0","type":"Wireguard"}}`) // кэш интерфейсов: rc читается только по известным (F546)
	fg.SetRC("Wireguard0", `{"wireguard":{"peer":[{"key":"KEY="}]}}`)
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	cmds := NewWireguardCommands(poster, sc, q)

	if err := cmds.SetPeerConnect(context.Background(), confirmed(t, "Wireguard0"), "KEY=", true, ""); err != nil {
		t.Fatalf("SetPeerConnect: %v", err)
	}

	p := poster.Payloads()[0].(map[string]any)
	peer := p["interface"].(map[string]any)["Wireguard0"].(map[string]any)["wireguard"].(map[string]any)["peer"].([]map[string]any)[0]
	if _, ok := peer["comment"]; ok {
		t.Errorf("comment key must be absent for empty name: %#v", peer)
	}
}

func TestWireguardCommands_SetASCParams_InvalidJSON(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	cmds := NewWireguardCommands(poster, sc, q)

	err := cmds.SetASCParams(context.Background(), confirmed(t, "Wireguard0"), json.RawMessage(`not json`))
	if err == nil {
		t.Fatalf("SetASCParams on invalid JSON: want error, got nil")
	}
	if poster.Calls() != 0 {
		t.Errorf("POST must not be called on parse error, got %d", poster.Calls())
	}
}

// Снятие пира отвергнуто (`no input` — общая фраза, стенд 5.02.A.11): исход
// решает свежее чтение rc. Пира нет — успех; есть — исходный отказ; чтение
// упало — отказ с ErrPeerPresenceUnknown.
func TestWireguardCommands_RemovePeer_RefusalDecidedByFreshRead(t *testing.T) {
	for _, tc := range []struct {
		name        string
		rc          string // "" — чтение rc падает
		wantErr     bool
		wantUnknown bool
	}{
		{"absent", `{"wireguard":{"peer":[{"key":"OTHER="}]}}`, false, false},
		{"present", `{"wireguard":{"peer":[{"key":"KEY="}]}}`, true, false},
		{"read fails", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fg := query.NewFakeGetter()
			fg.SetJSON("/show/interface/", `{"Wireguard0":{"id":"Wireguard0","type":"Wireguard"}}`)
			if tc.rc != "" {
				fg.SetRC("Wireguard0", tc.rc)
			} else {
				fg.SetError("/show/rc/interface/", errors.New("rci down"))
			}
			q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
			poster := &errPoster{err: errors.New("no input [http/rci 127.0.0.1].")}
			sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
			err := NewWireguardCommands(poster, sc, q).RemovePeer(context.Background(), confirmed(t, "Wireguard0"), "KEY=")
			if (err != nil) != tc.wantErr || errors.Is(err, ErrPeerPresenceUnknown) != tc.wantUnknown {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// Правка по ключу на отсутствующем пире: ErrPeerNotFound и ни одного поста —
// NDMS создал бы пира.
func TestWireguardCommands_KeyedEdits_PeerAbsent_NoPost(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{"Wireguard0":{"id":"Wireguard0","type":"Wireguard"}}`) // кэш интерфейсов: rc читается только по известным (F546)
	fg.SetRC("Wireguard0", `{"wireguard":{"peer":[{"key":"OTHER="}]}}`)
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	poster := &fakePoster{}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	cmds := NewWireguardCommands(poster, sc, q)
	ctx := context.Background()
	for name, err := range map[string]error{
		"connect": cmds.SetPeerConnect(ctx, confirmed(t, "Wireguard0"), "KEY=", false, "dacha"),
		"comment": cmds.SetPeerComment(ctx, confirmed(t, "Wireguard0"), "KEY=", "dacha"),
	} {
		if !errors.Is(err, peersubnet.ErrPeerNotFound) {
			t.Errorf("%s: err = %v, want ErrPeerNotFound", name, err)
		}
	}
	if n := len(poster.Payloads()); n != 0 {
		t.Fatalf("посты при отсутствующем пире: %d", n)
	}
}

func TestImportWireguardConfig_CreatedIsConfirmed(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil)
	res, err := cmds.Wireguard.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err != nil || res.Created.Name() != "Wireguard0" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if rec, _ := q.Interfaces.Get(context.Background(), "Wireguard0"); rec == nil {
		t.Fatal("Confirm после импорта обязан положить запись в кэш")
	}
	if f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("phantoms=%d E=%d", f.Phantoms, f.E)
	}
}

// Стенд 5.01.C.6: iflayerchanged ctrl ×2 по новому WireguardN доезжают, пока
// список подтверждения в полёте, ifcreated — после. Импорт обязан пройти:
// раньше здесь было «импорт принят, но Wireguard1 нет в списке», а созданный
// интерфейс оставался сиротой.
func TestImportWireguardConfig_LayerHooksDuringConfirm(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil, ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	ctx := context.Background()
	if _, err := q.Interfaces.List(ctx); err != nil { // карта тёплая, как в проде
		t.Fatal(err)
	}
	f.InList(func() {
		f.InList(nil)
		for _, h := range f.DrainHooks() { // до ответа NDMS успел только ctrl ×2
			if h.Type == "iflayerchanged" {
				q.Interfaces.OnLayerChanged(h.ID, h.Layer, h.Level)
			}
		}
	})
	res, err := cmds.Wireguard.ImportWireguardConfig(ctx, []byte("conf"), "x.conf")
	if err != nil || res.Created.Name() != "Wireguard1" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("phantoms=%d E=%d", f.Phantoms, f.E)
	}
}

// Роутер назвал созданный интерфейс, а в свежем списке его нет — ошибка с
// именем, не пустое доказательство.
func TestImportWireguardConfig_CreatedAbsent_Error(t *testing.T) {
	q := listQueries()
	q.Interfaces.SetCreatedBackoff()
	c := NewWireguardCommands(&respPoster{body: realImportSuccess}, nil, q)
	res, err := c.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err == nil || !strings.Contains(err.Error(), "Wireguard3") || res.Created != (query.Confirmed{}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
