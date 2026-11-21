package manager

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/transport/control"
)

// countingProvider is a fake transport jar that counts applies (each apply
// reconnects a real transport).
type countingProvider struct {
	mu      sync.Mutex
	jar     map[string]string
	applies int
}

func (p *countingProvider) FetchCookies() (map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string, len(p.jar))
	for k, v := range p.jar {
		out[k] = v
	}
	return out, nil
}

func (p *countingProvider) ApplyCookies(jar map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applies++
	p.jar = make(map[string]string, len(jar))
	for k, v := range jar {
		p.jar[k] = v
	}
	return nil
}

// setLive is the service answering with Set-Cookie: the live jar changes.
func (p *countingProvider) setLive(name, value string) {
	p.mu.Lock()
	p.jar[name] = value
	p.mu.Unlock()
}

func (p *countingProvider) count() int { p.mu.Lock(); defer p.mu.Unlock(); return p.applies }

const (
	yaDoc = "https://disk.yandex.ru/i/doc"
	mrDoc = "https://cloud.mail.ru/public/aa/bb"
)

// twoDocs is an exit carrying a Yandex and a Mail.ru document, each signed in.
func twoDocs(t *testing.T, exit bool) (*Manager, *transport.CookieStore, *countingProvider, *countingProvider, *fakeTransport, *fakeTransport) {
	t.Helper()
	sess, err := transport.NewSession(transport.PeerParameters{
		Capabilities: control.CapabilityIPv4 | control.CapabilityTCP, MaxPacketSize: 1500,
	}, exit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Stop() })
	m := New(sess, nil, "test-secret-long-enough", "test-ctx")
	store, err := transport.NewCookieStore(filepath.Join(t.TempDir(), "cookies.json"))
	if err != nil {
		t.Fatal(err)
	}
	ya, mr := &countingProvider{}, &countingProvider{}
	yt, mt := &fakeTransport{live: true}, &fakeTransport{live: true}
	if err := m.Add("vyandex", "vyandex", yt, 100, ya); err != nil {
		t.Fatal(err)
	}
	if err := m.Add("mailru", "mailru", mt, 90, mr); err != nil {
		t.Fatal(err)
	}
	_ = store.Save(yaDoc, map[string]string{"Session_id": "y-old", "yandexuid": "u", "spravka": "pass"})
	_ = store.Save(mrDoc, map[string]string{"Mpop": "m-old", "sdcs": "s"})
	if err := m.UseCookieStore(store, "vyandex", yaDoc); err != nil {
		t.Fatal(err)
	}
	if err := m.UseCookieStore(store, "mailru", mrDoc); err != nil {
		t.Fatal(err)
	}
	return m, store, ya, mr, yt, mt
}

// A Yandex push changes the Yandex document's jar only: the Mail.ru
// document's sign-in stays (node-install.sh used to overwrite cookies.json).
func TestMergeSignInsTouchesOnlyItsDocument(t *testing.T) {
	m, store, ya, mr, _, _ := twoDocs(t, true)
	before := mr.count()
	applied, refused := m.MergeSignIns(map[string]map[string]string{yaDoc: {"Session_id": "y-new", "sessionid2": "y2"}})
	if len(applied) != 1 || applied[0] != "vyandex" || len(refused) != 0 {
		t.Fatalf("applied %v refused %v", applied, refused)
	}
	got := store.Load(yaDoc)
	if got["Session_id"] != "y-new" || got["sessionid2"] != "y2" || got["spravka"] != "pass" || got["yandexuid"] != "u" {
		t.Fatalf("yandex jar %v", got)
	}
	if live, _ := ya.FetchCookies(); live["Session_id"] != "y-new" {
		t.Fatal("the running transport did not get the push")
	}
	if mr.count() != before || store.Load(mrDoc)["Mpop"] != "m-old" {
		t.Fatal("the Mail.ru document was touched")
	}
	reopened, _ := transport.NewCookieStore(store.Path())
	if reopened.Load(mrDoc)["Mpop"] != "m-old" || reopened.Load(yaDoc)["Session_id"] != "y-new" {
		t.Fatal("not persisted per document")
	}
}

// A Yandex login never goes into a Mail.ru document or the reverse, a jar
// without a login is not a sign-in, and an unknown document is refused.
func TestMergeSignInsRefusesTheWrongService(t *testing.T) {
	m, store, ya, mr, _, _ := twoDocs(t, true)
	y0, m0 := ya.count(), mr.count()
	_, refused := m.MergeSignIns(map[string]map[string]string{
		mrDoc:                       {"Session_id": "y"},
		yaDoc:                       {"Mpop": "m", "Session_id": "y"},
		"https://example.com/other": {"Session_id": "y"},
	})
	if len(refused) != 3 {
		t.Fatalf("refused %v", refused)
	}
	_, refused = m.MergeSignIns(map[string]map[string]string{yaDoc: {"yandexuid": "x"}, mrDoc: {"Mpop": ""}})
	if len(refused) != 2 {
		t.Fatalf("a signed-out jar was taken: refused %v", refused)
	}
	if ya.count() != y0 || mr.count() != m0 || store.Load(yaDoc)["Session_id"] != "y-old" || store.Load(mrDoc)["Mpop"] != "m-old" {
		t.Fatal("a refused jar changed something")
	}
}

// The same sign-in pushed again does not reconnect the transport.
func TestMergeSignInsSameJarIsNoReconnect(t *testing.T) {
	m, _, ya, _, _, _ := twoDocs(t, true)
	n := ya.count()
	applied, refused := m.MergeSignIns(map[string]map[string]string{yaDoc: {"Session_id": "y-old"}})
	if len(applied) != 0 || len(refused) != 0 || ya.count() != n {
		t.Fatalf("applied %v refused %v applies %d->%d", applied, refused, n, ya.count())
	}
}

// What the service refreshed in a working transport's live jar is saved, so
// a restart does not go back to the jar pushed days ago; a transport that is
// down, or whose live jar lost its login, is not saved from.
func TestKeepRefreshedSignIns(t *testing.T) {
	m, store, ya, mr, _, mt := twoDocs(t, true)
	if saved := m.KeepRefreshedSignIns(); len(saved) != 0 {
		t.Fatalf("nothing changed, saved %v", saved)
	}
	ya.setLive("Session_id", "y-rotated")
	ya.setLive("spravka", "") // a non-account cookie may change too
	mr.setLive("Mpop", "m-rotated")
	mt.Stop() // Mail.ru not connected: its live jar proves nothing
	saved := m.KeepRefreshedSignIns()
	if len(saved) != 1 || saved[0] != "vyandex" {
		t.Fatalf("saved %v", saved)
	}
	if store.Load(yaDoc)["Session_id"] != "y-rotated" || store.Load(mrDoc)["Mpop"] != "m-old" {
		t.Fatalf("store %v / %v", store.Load(yaDoc), store.Load(mrDoc))
	}
	ya.setLive("Session_id", "")
	ya.setLive("sessionid2", "new")
	if saved := m.KeepRefreshedSignIns(); len(saved) != 0 {
		t.Fatalf("a live jar without its login was saved: %v", saved)
	}
	if store.Load(yaDoc)["Session_id"] != "y-rotated" {
		t.Fatal("the saved login was lost")
	}
}

// The node tells which documents need a sign-in; a push clears it.
func TestObserveSignIns(t *testing.T) {
	m, _, _, _, yt, mt := twoDocs(t, true)
	t0 := time.Unix(1_760_000_000, 0)
	if st := m.ObserveSignIns(t0); len(st) != 0 {
		t.Fatalf("both up: %v", st)
	}
	yt.Stop()
	mt.Stop()
	m.noteLogin("vyandex", "login")
	m.noteLogin("mailru", "smartcaptcha") // a captcha is not a sign-in problem
	st := m.ObserveSignIns(t0)
	if len(st) != 2 || st[0].State != "login" || st[0].Doc != yaDoc || st[1].State != "down" || !st[1].Since.Equal(t0) {
		t.Fatalf("states %v", st)
	}
	if again := m.ObserveSignIns(t0.Add(time.Hour)); !again[1].Since.Equal(t0) {
		t.Fatal("down-since moved")
	}
	m.MergeSignIns(map[string]map[string]string{yaDoc: {"Session_id": "y-new"}})
	yt.Start()
	if st = m.ObserveSignIns(t0.Add(2 * time.Hour)); len(st) != 1 || st[0].Transport != "mailru" {
		t.Fatalf("after the push: %v", st)
	}

	path := filepath.Join(t.TempDir(), "signin-state")
	bad := SignInState{Transport: "x", Doc: "a b", State: "down", Since: t0}
	if err := WriteSignInStates(path, append(st, bad)); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "down 1760000000 "+mrDoc+"\n" {
		t.Fatalf("state file %q", body)
	}
}

// With SetRefusePeerLogin a client holding the link cannot swap the exit's
// account; the cookies of a passed check still go in.
func TestRefusePeerLogin(t *testing.T) {
	m, store, _, _, _, _ := twoDocs(t, true)
	m.SetRefusePeerLogin(true)
	m.DispatchControl(control.SubtypeCookiesOffer, offer(t, "vyandex", map[string]string{"Session_id": "attacker", "spravka": "new"}))
	got := store.Load(yaDoc)
	if got["Session_id"] != "y-old" || got["spravka"] != "new" {
		t.Fatalf("jar %v", got)
	}
	// Off (today's default): the offer is merged as before.
	m.SetRefusePeerLogin(false)
	m.DispatchControl(control.SubtypeCookiesOffer, offer(t, "vyandex", map[string]string{"Session_id": "peer"}))
	if store.Load(yaDoc)["Session_id"] != "peer" {
		t.Fatal("compat: an offer must still merge with the switch off")
	}
}

// The inbox is taken once and removed; garbage is removed and reported
// without its content.
func TestTakeCookieInbox(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "cookies.json")
	in := transport.CookieInboxPath(store)
	if got, err := transport.TakeCookieInbox(in); got != nil || err != nil {
		t.Fatalf("missing inbox: %v %v", got, err)
	}
	_ = os.WriteFile(in, []byte(`{"`+yaDoc+`":{"Session_id":"v"}}`), 0o600)
	got, err := transport.TakeCookieInbox(in)
	if err != nil || got[yaDoc]["Session_id"] != "v" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := os.Stat(in); !os.IsNotExist(err) {
		t.Fatal("inbox not removed")
	}
	_ = os.WriteFile(in, []byte("Session_id=secret-value"), 0o600)
	if _, err := transport.TakeCookieInbox(in); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("bad inbox: %v", err)
	}
	if _, err := os.Stat(in); !os.IsNotExist(err) {
		t.Fatal("bad inbox not removed")
	}
}
