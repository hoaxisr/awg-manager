package main

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/transport/manager"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

// coreFeatures is what --features prints. "cookie-inbox": set-cookies may
// leave a sign-in in the inbox and send SIGHUP instead of overwriting
// cookies.json and restarting; "signin-state": the node writes
// signinStateFile for show.
const coreFeatures = "cookie-inbox\nsignin-state\n"

// signinStateFile is written next to the cookie store.
const signinStateFile = "signin-state"

// signInKeepInterval is how often an exit saves refreshed sign-ins and
// rewrites its state file; a client asks for cookies every 10 minutes too.
const signInKeepInterval = 5 * time.Minute

// startExitSignInKeeper runs the exit's side of keeping its owner's
// sign-ins: it takes a pushed sign-in at start and on SIGHUP, and every
// signInKeepInterval saves what the services refreshed and writes the state
// file. Call after the cookie store has been replayed.
func startExitSignInKeeper(m *manager.Manager, store *transport.CookieStore) {
	inbox := transport.CookieInboxPath(store.Path())
	statePath := filepath.Join(filepath.Dir(store.Path()), signinStateFile)
	take := func() {
		byDoc, err := transport.TakeCookieInbox(inbox)
		if err != nil {
			utils.Infof("[COOKIE] %v", err)
			return
		}
		if byDoc == nil {
			return
		}
		applied, refused := m.MergeSignIns(byDoc)
		utils.Infof("[COOKIE] owner's sign-in push: %d taken, %d refused", len(applied), len(refused))
	}
	take()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	utils.SafeGo("signin-inbox", func() {
		for range hup {
			take()
		}
	})
	utils.SafeGo("signin-keeper", func() {
		ticker := time.NewTicker(signInKeepInterval)
		defer ticker.Stop()
		for {
			m.KeepRefreshedSignIns()
			if err := manager.WriteSignInStates(statePath, m.ObserveSignIns(time.Now())); err != nil {
				utils.Debugf("[COOKIE] %v", err)
			}
			<-ticker.C
		}
	})
}
