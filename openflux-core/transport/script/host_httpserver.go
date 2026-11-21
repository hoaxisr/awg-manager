package script

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/dop251/goja"

	"github.com/p1neappleXpress/OpenFlux/utils"
)

// maxServerRequestBody caps one request's body on a script's own HTTP
// server - generous for a setup form, not for someone trying to park large
// uploads in this process's memory. Loopback-only (see registerHTTPServer),
// but another local process could still reach it.
const maxServerRequestBody = 4 << 20

// ---- httpserver.listen ----
//
// A script transport's own setup/login mini-app (see
// js/template_html.html): a script that needs richer UI than a single
// static page - its own routes, fetch() calls with a real origin, a form
// that posts back to itself - serves it over a real HTTP server instead of
// raise()-ing inline HTML. The app opens a WebView/browser at the URL this
// returns (raise("captchaRequired", {url: "http://" + srv.addr + "/"})),
// exactly like it already does for a real site's login page.
//
// Always 127.0.0.1, never a port argument's host: scripts run with the
// same unrestricted host API as every other call here (no capability
// sandboxing - see registerHostAPI), but a listening socket is the one
// primitive that can reach OTHER processes on the device, not just ones
// this script dials out to, so it stays off the network entirely rather
// than trusting the script to ask for loopback itself.
func registerHTTPServer(vm *goja.Runtime, t *ScriptTransport) {
	srvObj := vm.NewObject()
	srvObj.Set("listen", func(call goja.FunctionCall) goja.Value {
		handler := call.Argument(0)
		if _, ok := goja.AssertFunction(handler); !ok {
			panic(vm.NewTypeError("httpserver.listen(handler, port?): handler must be a function"))
		}
		port := 0
		if v := call.Argument(1); v != nil && !goja.IsUndefined(v) {
			port = int(v.ToInteger())
		}

		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			panic(vm.NewGoError(err))
		}

		respond := newLoopRespondFunc(vm)
		hs := &scriptHTTPServer{t: t, vm: vm, handler: handler, respond: respond}
		srv := &http.Server{Handler: hs}
		addr := ln.Addr().(*net.TCPAddr)
		t.addHTTPServer(srv, addr.Port)

		utils.SafeGo("script."+t.name+".httpserver", func() {
			_ = srv.Serve(ln)
			t.removeHTTPServer(srv)
		})

		obj := vm.NewObject()
		obj.Set("port", addr.Port)
		obj.Set("addr", "127.0.0.1:"+strconv.Itoa(addr.Port))
		obj.Set("close", func(goja.FunctionCall) goja.Value {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
			t.removeHTTPServer(srv)
			return goja.Undefined()
		})
		return obj
	})
	vm.Set("httpserver", srvObj)
}

// scriptHTTPServer dispatches every request on this listener into the
// script's own loop and blocks the net/http goroutine (never the loop
// itself) until the handler - sync or async, see loopRespond - produces a
// response or the loop is gone (transport stopped mid-request).
type scriptHTTPServer struct {
	t       *ScriptTransport
	vm      *goja.Runtime
	handler goja.Value
	respond loopRespondFunc
}

func (hs *scriptHTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxServerRequestBody)
	body, _ := io.ReadAll(r.Body)

	type result struct {
		status  int
		headers map[string]string
		body    []byte
		errMsg  string
	}
	done := make(chan result, 1)

	scheduled := hs.t.loop.RunOnLoop(func(vm *goja.Runtime) {
		req := vm.NewObject()
		req.Set("method", r.Method)
		req.Set("path", r.URL.Path)
		query := vm.NewObject()
		for k, v := range r.URL.Query() {
			if len(v) > 0 {
				query.Set(k, v[0])
			}
		}
		req.Set("query", query)
		headers := vm.NewObject()
		for k, v := range r.Header {
			if len(v) > 0 {
				headers.Set(k, v[0])
			}
		}
		req.Set("headers", headers)
		req.Set("body", vm.NewArrayBuffer(body))

		hs.respond(vm, hs.handler, req, func(status int, respHeaders map[string]string, respBody []byte, errMsg string) {
			done <- result{status: status, headers: respHeaders, body: respBody, errMsg: errMsg}
		})
	})
	if !scheduled {
		http.Error(w, "script transport stopped", http.StatusServiceUnavailable)
		return
	}

	res := <-done
	if res.errMsg != "" {
		utils.Debugf("[SCRIPT:%s] httpserver handler: %s", hs.t.name, res.errMsg)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for k, v := range res.headers {
		w.Header().Set(k, v)
	}
	if res.status == 0 {
		res.status = http.StatusOK
	}
	w.WriteHeader(res.status)
	_, _ = w.Write(res.body)
}

// loopRespondFunc invokes a request handler - which may return a plain
// value or a Promise - and calls back with the settled result. Must only
// ever be called from within a loop.RunOnLoop job (it runs JS).
type loopRespondFunc func(vm *goja.Runtime, handler goja.Value, req *goja.Object, cb func(status int, headers map[string]string, body []byte, errMsg string))

// newLoopRespondFunc compiles the small trampoline that lets
// scriptHTTPServer treat a sync return and a Promise identically:
// Promise.resolve(handler(req)) is the value itself when handler returned
// a plain object, and the same promise when it returned one - exactly the
// distinction a hand-written JS caller would make, done once here instead
// of a fragile goja.Promise introspection on the Go side.
func newLoopRespondFunc(vm *goja.Runtime) loopRespondFunc {
	trampoline, err := vm.RunString(`(function (handler, req, onDone) {
		try {
			Promise.resolve(handler(req)).then(
				function (r) { onDone(null, r); },
				function (e) { onDone(e && e.message ? e.message : String(e), null); }
			);
		} catch (e) {
			onDone(e && e.message ? e.message : String(e), null);
		}
	})`)
	if err != nil {
		panic("script: httpserver trampoline: " + err.Error()) // can't fail: fixed source
	}
	call, _ := goja.AssertFunction(trampoline)

	return func(vm *goja.Runtime, handler goja.Value, req *goja.Object, cb func(int, map[string]string, []byte, string)) {
		onDone := func(call goja.FunctionCall) goja.Value {
			if errArg := call.Argument(0); errArg != nil && !goja.IsUndefined(errArg) && !goja.IsNull(errArg) {
				cb(0, nil, nil, errArg.String())
				return goja.Undefined()
			}
			status, headers, body := decodeServerResponse(vm, call.Argument(1))
			cb(status, headers, body, "")
			return goja.Undefined()
		}
		if _, err := call(goja.Undefined(), handler, req, vm.ToValue(onDone)); err != nil {
			cb(0, nil, nil, err.Error())
		}
	}
}

// decodeServerResponse reads {status, headers, body} from a handler's
// result. body may be a string (the common case: HTML/JSON/text) or an
// ArrayBuffer/TypedArray (an image, a signed blob); anything else is
// coerced to its string form so a handler that forgets to wrap plain text
// still produces something, not a 500.
func decodeServerResponse(vm *goja.Runtime, v goja.Value) (status int, headers map[string]string, body []byte) {
	status = http.StatusOK
	obj, ok := v.(*goja.Object)
	if !ok || obj == nil {
		if v != nil && !goja.IsUndefined(v) {
			body = []byte(v.String())
		}
		return
	}
	if s := obj.Get("status"); s != nil && !goja.IsUndefined(s) {
		status = int(s.ToInteger())
	}
	if h, ok := obj.Get("headers").(*goja.Object); ok && h != nil {
		headers = make(map[string]string)
		for _, k := range h.Keys() {
			headers[k] = h.Get(k).String()
		}
	}
	if b := obj.Get("body"); b != nil && !goja.IsUndefined(b) {
		if raw, err := bytesArg(vm, b); err == nil {
			body = raw
		} else {
			body = []byte(b.String())
		}
	}
	return
}

// ownedServer is one listener a script started, with the port it got: the
// port is what lets raise() accept a setup page address only for a server
// this transport itself serves (see checkSetupURL).
type ownedServer struct {
	srv  *http.Server
	port int
}

func (t *ScriptTransport) addHTTPServer(s *http.Server, port int) {
	t.httpServersMu.Lock()
	t.httpServers = append(t.httpServers, ownedServer{srv: s, port: port})
	t.httpServersMu.Unlock()
}

func (t *ScriptTransport) removeHTTPServer(s *http.Server) {
	t.httpServersMu.Lock()
	defer t.httpServersMu.Unlock()
	for i, s2 := range t.httpServers {
		if s2.srv == s {
			t.httpServers = append(t.httpServers[:i], t.httpServers[i+1:]...)
			return
		}
	}
}

// ownsHTTPPort reports whether one of this transport's running
// httpserver.listen() servers is bound to port.
func (t *ScriptTransport) ownsHTTPPort(port int) bool {
	t.httpServersMu.Lock()
	defer t.httpServersMu.Unlock()
	for _, s := range t.httpServers {
		if s.port == port {
			return true
		}
	}
	return false
}

// closeHTTPServers shuts down every server this transport started. Called
// from Stop() so a script's own listener never outlives the transport.
func (t *ScriptTransport) closeHTTPServers() {
	t.httpServersMu.Lock()
	servers := t.httpServers
	t.httpServers = nil
	t.httpServersMu.Unlock()
	for _, s := range servers {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.srv.Shutdown(ctx)
		cancel()
	}
}
