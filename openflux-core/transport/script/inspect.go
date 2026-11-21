package script

import (
	"fmt"
	"time"

	"github.com/dop251/goja"
)

// inspectBudget bounds how long reading a script's manifest may take. Inspect
// runs on code nobody has verified yet (an import dialog, a link), so a
// `while(true){}` at the top level must cost three seconds, not a hung app.
const inspectBudget = 3 * time.Second

// registerInspectAPI is the host API an UNVERIFIED script sees while its
// manifest is read: the pure helpers (codecs, hashes, url parsing) are real,
// everything that reaches outside the runtime (network, sockets, servers,
// cookies, state, events) throws. A script that talks to the world at the top
// level - before any signature or user decision - cannot do it here.
func registerInspectAPI(vm *goja.Runtime) {
	registerBase64(vm)
	registerText(vm)
	registerURL(vm)
	registerGzip(vm)
	registerLZ4(vm)
	registerCrypto(vm)
	if _, err := vm.RunString(`(function (g) {
  var deny = function () { throw new Error("not available while the transport is only being inspected"); };
  ["http", "ws", "udp", "webrtc", "httpserver", "cookieJar", "concurrency", "emit", "setState", "raise", "lanes", "shared"].forEach(function (n) {
    g[n] = new Proxy(deny, { get: function () { return deny; }, apply: deny });
  });
})(this);`); err != nil {
		panic("script: inspect stubs: " + err.Error()) // a bug in the snippet above, not in a script
	}
}

// inspectTimer interrupts vm after inspectBudget; call the result to stop it.
func inspectTimer(vm *goja.Runtime) (stop func()) {
	timer := time.AfterFunc(inspectBudget, func() { vm.Interrupt("inspect: the script ran too long") })
	return func() { timer.Stop() }
}

// Inspect evaluates a script's source just far enough to read its runtime
// manifest (Transport.info()) and returns it, WITHOUT ever calling open() or
// touching the network: the script runs against registerInspectAPI (I/O
// throws) and under inspectBudget. It is the read an app does before a transport is
// trusted or connected: it surfaces the name/version and the params a user
// must fill.
//
// Inspect does NOT verify the signature - that is a separate decision the
// caller makes with VerifyScript (or RawPackage.Verify for a .flux), so the
// UI can show "what this claims to be" and "whether it is signed by a key you
// trust" as two distinct facts. Running info() on an untrusted script is safe:
// it returns a literal object, and every side-effecting host call
// (http.fetch, ws.open, emit, setState) fires only from open()/write(), which
// Inspect never invokes.
func Inspect(src []byte) (Info, error) {
	vm := goja.New()
	registerInspectAPI(vm)
	defer inspectTimer(vm)()
	if _, err := vm.RunString(string(src)); err != nil {
		return Info{}, fmt.Errorf("eval: %w", err)
	}
	obj, ok := vm.Get("Transport").(*goja.Object)
	if !ok || obj == nil {
		return Info{}, fmt.Errorf("script must define a global `Transport` object")
	}
	infoFn, ok := goja.AssertFunction(obj.Get("info"))
	if !ok {
		return Info{}, fmt.Errorf("Transport.info is not a function")
	}
	infoVal, err := infoFn(obj)
	if err != nil {
		return Info{}, fmt.Errorf("Transport.info(): %w", err)
	}
	info, err := parseInfo(infoVal)
	if err != nil {
		return Info{}, err
	}
	_, info.CustomSettings = goja.AssertFunction(obj.Get("settings"))
	return info, nil
}
