package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/transport/control"
	"github.com/p1neappleXpress/OpenFlux/transport/manager"
)

type jarTransport struct {
	mu  sync.Mutex
	jar map[string]string
}

func (j *jarTransport) Start() error                    { return nil }
func (j *jarTransport) Stop() error                     { return nil }
func (j *jarTransport) Send([]byte) error               { return nil }
func (j *jarTransport) Receive(func([]byte))            {}
func (j *jarTransport) IsConnected() bool               { return false }
func (j *jarTransport) Stats() transport.TransportStats { return transport.TransportStats{} }
func (j *jarTransport) FetchCookies() (map[string]string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[string]string{}
	for k, v := range j.jar {
		out[k] = v
	}
	return out, nil
}
func (j *jarTransport) ApplyCookies(jar map[string]string) error {
	j.mu.Lock()
	j.jar = jar
	j.mu.Unlock()
	return nil
}

// node-install.sh asks the core what it can do before it leaves a sign-in
// in the inbox instead of overwriting cookies.json.
func TestCoreFeatures(t *testing.T) {
	for _, f := range []string{"cookie-inbox", "signin-state"} {
		if !strings.Contains("\n"+coreFeatures, "\n"+f+"\n") {
			t.Fatalf("--features lacks %s", f)
		}
	}
}

// A sign-in left in the inbox before the node starts (set-transports
// restarts it) is taken at start, and the state file says the document is
// not up yet.
func TestExitSignInKeeperTakesTheInboxAtStart(t *testing.T) {
	dir := t.TempDir()
	store, err := transport.NewCookieStore(filepath.Join(dir, "cookies.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc := "https://disk.yandex.ru/i/x"
	mr := "https://cloud.mail.ru/public/a/b"
	_ = store.Save(doc, map[string]string{"Session_id": "old"})
	_ = store.Save(mr, map[string]string{"Mpop": "keep"})
	sess, err := transport.NewSession(transport.PeerParameters{Capabilities: control.CapabilityIPv4 | control.CapabilityTCP, MaxPacketSize: 1500}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Stop()
	m := manager.New(sess, nil, "test-secret-long-enough", "ctx")
	jt := &jarTransport{}
	if err := m.Add("vyandex", "vyandex", jt, 100, jt); err != nil {
		t.Fatal(err)
	}
	if err := m.UseCookieStore(store, "vyandex", doc); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(transport.CookieInboxPath(store.Path()), []byte(`{"`+doc+`":{"Session_id":"new"}}`), 0o600)
	startExitSignInKeeper(m, store)
	if store.Load(doc)["Session_id"] != "new" || store.Load(mr)["Mpop"] != "keep" {
		t.Fatalf("store %v %v", store.Load(doc), store.Load(mr))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		body, _ := os.ReadFile(filepath.Join(dir, signinStateFile))
		if strings.HasPrefix(string(body), "down ") && strings.HasSuffix(string(body), " "+doc+"\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state file %q", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
