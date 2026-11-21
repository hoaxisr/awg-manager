package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/p1neappleXpress/OpenFlux/utils"
)

// An exit node opens its Yandex and Mail.ru documents as its owner's account.
// This file keeps that sign-in alive on the node side:
//
//   - MergeSignIns takes a sign-in the owner pushed over SSH (the cookie
//     inbox, see transport.TakeCookieInbox) into the running node, per
//     document and without a restart;
//   - KeepRefreshedSignIns saves what the service itself refreshed in the
//     live jar (Set-Cookie), which before was lost at the next restart;
//   - ObserveSignIns tells, per document, whether the node is signed out
//     ("login") or has been down for a while ("down"), for the owner's app
//     to read over SSH (WriteSignInStates, node-install.sh show).
//
// None of it logs a cookie value.

// loginCookie is the cookie that shows a signed-in account of the service a
// transport type opens, "" for a type that opens no account.
func loginCookie(typ string) string {
	switch typ {
	case "vyandex", "yandex":
		return "Session_id"
	case "mailru":
		return "Mpop"
	}
	return ""
}

// signInTrack is what ObserveSignIns and NotifyCaptcha learned per transport.
type signInTrack struct {
	mu          sync.Mutex
	loginSince  map[string]time.Time // the transport reported a login wall
	downSince   map[string]time.Time // first seen not connected
	refusePeers bool                 // see SetRefusePeerLogin
}

func (s *signInTrack) init() {
	if s.loginSince == nil {
		s.loginSince = make(map[string]time.Time)
		s.downSince = make(map[string]time.Time)
	}
}

// noteLogin records a transport's "login required" report (NotifyCaptcha).
func (m *Manager) noteLogin(name, reason string) {
	if reason != "login" {
		return
	}
	s := &m.signIn
	s.mu.Lock()
	s.init()
	if _, ok := s.loginSince[name]; !ok {
		s.loginSince[name] = time.Now()
	}
	s.mu.Unlock()
}

func (m *Manager) clearSignInState(name string) {
	s := &m.signIn
	s.mu.Lock()
	s.init()
	delete(s.loginSince, name)
	delete(s.downSince, name)
	s.mu.Unlock()
}

// SetRefusePeerLogin makes the exit drop account login cookies (Yandex's,
// Mail.ru's) from what a client sends over the Session (CookiesOffer,
// CookiesResponse): anyone holding the channel link is such a client, so with
// this on only the owner (over SSH, MergeSignIns) can change whose account
// the node opens its documents as. A passed check's cookies still go in.
func (m *Manager) SetRefusePeerLogin(on bool) {
	m.signIn.mu.Lock()
	m.signIn.refusePeers = on
	m.signIn.mu.Unlock()
}

func (m *Manager) refusesPeerLogin() bool {
	m.signIn.mu.Lock()
	defer m.signIn.mu.Unlock()
	return m.signIn.refusePeers
}

// signInEntry is one attached transport that opens an account's document.
type signInEntry struct {
	name, typ, key, login string
	connected             func() bool
}

func (m *Manager) signInEntries() []signInEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []signInEntry
	for _, n := range m.order {
		e := m.entries[n]
		login := loginCookie(e.Type)
		key := m.cookieKeys[n]
		if login == "" || key == "" || e.Provider == nil {
			continue
		}
		out = append(out, signInEntry{name: n, typ: e.Type, key: key, login: login, connected: e.Raw.IsConnected})
	}
	return out
}

// MergeSignIns merges pushed sign-ins ({document key: jar}) into the jars of
// the transports whose cookie store key is that document. Per document the
// pushed cookies win over kept ones of the same name and the rest stay; a
// document of the channel that the push does not name is not touched. A jar
// is refused, and reported in refused, when it is not a sign-in of the
// document's own service: a Yandex document takes a jar with Session_id and
// without Mpop, a Mail.ru document the reverse; a document this node does not
// carry is refused too. A jar that changes nothing is not applied, so the
// transport is not reconnected for it.
func (m *Manager) MergeSignIns(byDoc map[string]map[string]string) (applied, refused []string) {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	entries := m.signInEntries()
	docs := make([]string, 0, len(byDoc))
	for doc := range byDoc {
		docs = append(docs, doc)
	}
	sort.Strings(docs)
	for _, doc := range docs {
		jar := byDoc[doc]
		matched := false
		for _, e := range entries {
			if e.key != doc {
				continue
			}
			matched = true
			if !signInFor(e.typ, jar) {
				refused = append(refused, e.name)
				utils.Infof("[COOKIE] a pushed sign-in for %s is not a %s sign-in: refused", e.name, e.typ)
				continue
			}
			var kept map[string]string
			if store != nil {
				kept = store.Load(e.key)
			}
			if kept == nil {
				kept, _ = m.FetchCookiesFor(e.name)
			}
			merged := make(map[string]string, len(kept)+len(jar))
			for k, v := range kept {
				merged[k] = v
			}
			changed := false
			for k, v := range jar {
				if merged[k] != v {
					changed = true
				}
				merged[k] = v
			}
			if !changed {
				utils.Infof("[COOKIE] the pushed sign-in for %s is the one it has", e.name)
				continue
			}
			if err := m.ApplyCookiesFor(e.name, merged); err != nil {
				utils.Infof("[COOKIE] apply the pushed sign-in to %s: %v", e.name, err)
				refused = append(refused, e.name)
				continue
			}
			if store != nil {
				if err := store.Save(e.key, merged); err != nil {
					utils.Infof("[COOKIE] save the pushed sign-in of %s: %v", e.name, err)
				}
			}
			m.clearSignInState(e.name)
			applied = append(applied, e.name)
			utils.Infof("[COOKIE] %s took the owner's pushed sign-in (%d cookies)", e.name, len(jar))
		}
		if !matched {
			refused = append(refused, doc)
			utils.Infof("[COOKIE] a pushed sign-in names a document this node does not carry: refused")
		}
	}
	return applied, refused
}

// signInFor reports whether jar is a sign-in of the service typ opens and of
// no other.
func signInFor(typ string, jar map[string]string) bool {
	login := loginCookie(typ)
	if login == "" || jar[login] == "" {
		return false
	}
	for _, other := range []string{"Session_id", "Mpop"} {
		if other != login && jar[other] != "" {
			return false
		}
	}
	return true
}

// KeepRefreshedSignIns saves to the cookie store the account cookies a
// service refreshed in a working transport's live jar, so a restart (an
// auto-update, a reboot) starts from them rather than from the jar the owner
// pushed days ago. Only a connected transport whose live jar still holds its
// login is saved from, and only names it changed are written; nothing is
// removed from the store. Returns the transports saved for.
func (m *Manager) KeepRefreshedSignIns() []string {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == nil {
		return nil
	}
	var saved []string
	for _, e := range m.signInEntries() {
		if !e.connected() {
			continue
		}
		live, err := m.FetchCookiesFor(e.name)
		if err != nil || live[e.login] == "" {
			continue
		}
		kept := store.Load(e.key)
		changed := false
		for k, v := range live {
			if accountCookies[k] && kept[k] != v {
				changed = true
				break
			}
		}
		if !changed {
			continue
		}
		merged := make(map[string]string, len(kept)+len(live))
		for k, v := range kept {
			merged[k] = v
		}
		for k, v := range live {
			merged[k] = v
		}
		if err := store.Save(e.key, merged); err != nil {
			utils.Infof("[COOKIE] save the refreshed sign-in of %s: %v", e.name, err)
			continue
		}
		saved = append(saved, e.name)
		utils.Infof("[COOKIE] %s: the service refreshed the sign-in, saved", e.name)
	}
	return saved
}

// SignInState is one document's sign-in as the node sees it.
type SignInState struct {
	Transport string
	Doc       string
	// State is "login" (the service asks the node to sign in) or "down" (not
	// connected since Since, cause unknown: Mail.ru gives no login wall of its
	// own).
	State string
	Since time.Time
}

// ObserveSignIns updates and returns the documents whose transport is not
// fine now: signed out, or not connected. A connected transport is fine and
// its record is cleared.
func (m *Manager) ObserveSignIns(now time.Time) []SignInState {
	var out []SignInState
	s := &m.signIn
	for _, e := range m.signInEntries() {
		up := e.connected()
		s.mu.Lock()
		s.init()
		if up {
			delete(s.loginSince, e.name)
			delete(s.downSince, e.name)
			s.mu.Unlock()
			continue
		}
		if _, ok := s.downSince[e.name]; !ok {
			s.downSince[e.name] = now
		}
		st := SignInState{Transport: e.name, Doc: e.key, State: "down", Since: s.downSince[e.name]}
		if t, ok := s.loginSince[e.name]; ok {
			st.State, st.Since = "login", t
		}
		s.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// WriteSignInStates writes states to path, one "STATE SINCE DOC" line each
// (SINCE in Unix seconds), atomically; node-install.sh show turns it into
// JSON. A document key with a space or a line break is left out.
func WriteSignInStates(path string, states []SignInState) error {
	var b strings.Builder
	for _, st := range states {
		if strings.ContainsAny(st.Doc, " \t\r\n") {
			continue
		}
		b.WriteString(st.State + " " + strconv.FormatInt(st.Since.Unix(), 10) + " " + st.Doc + "\n")
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("signin state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("signin state: %w", err)
	}
	return nil
}
