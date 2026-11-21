package main

import (
	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/transport/ipc"
	"github.com/p1neappleXpress/OpenFlux/transport/manager"
	"github.com/p1neappleXpress/OpenFlux/transport/script"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

// localCheckRequest is what the app is asked when one of this process's own
// transports needs a page shown: a real site's check, or a script's own
// setup page (inline html, or the script's own loopback server). Own is the
// core's call, so no app has to guess it from the address.
func localCheckRequest(name, url, html, reason string) *ipc.CookiesRequestPayload {
	return &ipc.CookiesRequestPayload{
		Transport: name, URL: url, HTML: html, Reason: reason, Own: script.IsOwnPage(url, html),
	}
}

// remoteCheckRequest is the request for a check the exit node reported: the
// page is passed from the exit's address (proxy) and the answer goes back to
// the exit. Nil when the exit's page may not be shown (see
// script.AcceptRemoteCheck): its loopback address means nothing on this
// machine and must not be opened here.
func remoteCheckRequest(name, url, html, reason, proxy string) *ipc.CookiesRequestPayload {
	if !script.AcceptRemoteCheck(url, html) {
		return nil
	}
	return &ipc.CookiesRequestPayload{
		Transport: name, URL: url, HTML: html, Reason: reason, Own: html != "", Remote: true, Proxy: proxy,
	}
}

// wireCheckNotifier hands a carrier's own check or login reports (yandex and
// vyandex raise them) to the app, which opens its browser for the user. On the
// Session path the manager does this for every transport it holds; the classic
// path has none, and without this the report stayed a log line repeated every
// 30 seconds while the app never opened its browser. Reports the carrier makes
// while no app is connected are dropped, as on the Session path: it reports
// again on its next attempt. False when the carrier raises no such reports.
func wireCheckNotifier(carrier transport.Transport, srv *ipc.Server) bool {
	en, ok := carrier.(transport.ErrorNotifier)
	if !ok {
		return false
	}
	en.SetErrorNotifier(func(_ error, name, url, html, reason string) {
		_ = srv.SendCookiesRequest(localCheckRequest(name, url, html, reason))
	})
	return true
}

// coreIPCHandler is the app-facing side of the IPC bridge.
//
// It receives commands and cookies from the mobile app and forwards them
// to the manager. When the core needs fresh cookies (SmartCaptcha), the
// manager calls SetCaptchaNotifier, whose callback fires into IPC.
type coreIPCHandler struct {
	manager *manager.Manager
	// exchanger takes the cookies on the classic path without a Session
	// (manager nil). Nil when that carrier keeps no cookies.
	exchanger transport.CookieExchanger
}

func (h *coreIPCHandler) OnConnect()    { utils.Debugf("[IPC] app connected") }
func (h *coreIPCHandler) OnDisconnect() { utils.Debugf("[IPC] app disconnected") }

func (h *coreIPCHandler) OnCommand(p *ipc.CommandPayload) {
	utils.Debugf("[IPC] command action=%q params=%v", p.Action, p.Params)
	// No dynamic commands yet. Reserved for start/stop/restart from the UI.
}

func (h *coreIPCHandler) OnCookies(p *ipc.CookiesOfferPayload) {
	if p == nil || p.Transport == "" || len(p.Jar) == 0 {
		utils.Debugf("[IPC] empty cookies offer, ignoring")
		return
	}
	if h.manager == nil {
		if h.exchanger == nil || p.Remote {
			utils.Debugf("[IPC] cookies for %q: nothing on this path takes them", p.Transport)
			return
		}
		if err := h.exchanger.ApplyCookies(p.Jar); err != nil {
			utils.Debugf("[IPC] apply cookies for %q: %v", p.Transport, err)
			return
		}
		utils.Debugf("[IPC] applied %d cookies for %q", len(p.Jar), p.Transport)
		return
	}
	if p.Remote {
		if err := h.manager.OfferCookies(p.Transport, p.Jar); err != nil {
			utils.Debugf("[IPC] offer cookies to exit for %q: %v", p.Transport, err)
			return
		}
		utils.Debugf("[IPC] sent %d cookies to exit for %q", len(p.Jar), p.Transport)
		return
	}
	if err := h.manager.AcceptCookies(p.Transport, p.Jar); err != nil {
		utils.Debugf("[IPC] apply cookies for %q: %v", p.Transport, err)
		return
	}
	utils.Debugf("[IPC] applied %d cookies for %q", len(p.Jar), p.Transport)
}
