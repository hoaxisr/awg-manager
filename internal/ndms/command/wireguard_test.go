package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
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

func TestImportWireguardConfig_Success_ReturnsCreated(t *testing.T) {
	c := NewWireguardCommands(&respPoster{body: realImportSuccess}, nil, nil)

	res, err := c.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Created != "Wireguard3" {
		t.Errorf("created name: want Wireguard3, got %q", res.Created)
	}
}

func TestImportWireguardConfig_Success_PreservesIntersectAndMessages(t *testing.T) {
	// Importing a config whose keys collide with an existing interface still
	// creates a new one, but the router reports the collision in intersects.
	// The caller must not lose that context.
	body := `{"interface":{"wireguard":{"import":{"intersects":"Wireguard3","created":"Wireguard4","status":[{"status":"message","code":"75507472","ident":"Wireguard::Interface","message":"\"Wireguard4\": imported settings."}]}}}}`
	c := NewWireguardCommands(&respPoster{body: body}, nil, nil)

	res, err := c.ImportWireguardConfig(context.Background(), []byte("conf"), "x.conf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Created != "Wireguard4" {
		t.Errorf("created: want Wireguard4, got %q", res.Created)
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
	if err := cmds.SetASCParams(context.Background(), "Wireguard0", params); err != nil {
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
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	cmds := NewWireguardCommands(poster, sc, q)

	if err := cmds.SetPeerConnect(context.Background(), "Wireguard0", "KEY=", false, "dacha"); err != nil {
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
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	cmds := NewWireguardCommands(poster, sc, q)

	if err := cmds.SetPeerConnect(context.Background(), "Wireguard0", "KEY=", true, ""); err != nil {
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

	err := cmds.SetASCParams(context.Background(), "Wireguard0", json.RawMessage(`not json`))
	if err == nil {
		t.Fatalf("SetASCParams on invalid JSON: want error, got nil")
	}
	if poster.Calls() != 0 {
		t.Errorf("POST must not be called on parse error, got %d", poster.Calls())
	}
}
