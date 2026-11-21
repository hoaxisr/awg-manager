// OpenFlux script-transport template.
//
// A script transport is one .js file (or a .flux package - see below) that
// implements everything a native Go transport would: the auth/handshake
// flow, wire framing, reconnect and keepalive policy. It is loaded into its
// own goja runtime (one per transport instance) and gets an unrestricted
// host API - there is no capability sandboxing. The only gate is the
// signature check the Go side runs before this file is ever evaluated: it
// must ship as `<name>.js` + `<name>.js.sig`, or as a signed `<name>.flux`
// package, signed with a key the exit/node trusts (see
// transport/script/sign.go, transport/script/flux.go, cmd/scriptsign).
//
// Everything below is the full contract. Delete what you don't need,
// but keep the shape: a global `Transport` object with these members.

var Transport = {

  // info() -> manifest object. Called once, synchronously, right after
  // this file is evaluated - before open(). All fields optional except
  // `name`.
  info: function () {
    return {
      name: "template",
      version: "1.0.0",

      // Base URL cookieJar.get()/set() read and write cookies against.
      // Leave empty if this transport doesn't need cookies.
      cookieDomain: "https://example.com/",

      // If true, ApplyCookies (the generic downward cookie-exchange path -
      // see onEvent below) scopes applied cookies to the PARENT domain
      // instead of cookieDomain's exact host, so a cookie obtained on one
      // subdomain (e.g. a captcha solved on disk.example.com) still reaches
      // a sibling subdomain (docs.example.com) this transport actually
      // talks to. Leave false if you only ever touch cookieDomain's own host.
      scopeCookiesToParentDomain: false,

      // Advisory only. The core never fragments, reorders or rate-limits
      // on your behalf unless you ask - omit these (or leave at defaults)
      // to get plain passthrough: whatever byte string write() is called
      // with is exactly what core handed to Send(), uncapped.
      mtu: 0,             // 0 = unbounded
      reliable: false,    // does the underlying channel guarantee delivery?
      ordered: false,      // does it guarantee order?
      halfDuplex: false,
      minIntervalMs: 0,   // floor between sends, if the channel needs one

      // HTTP connection-pool tuning for the shared *http.Transport behind
      // http.fetch/http.newSession. Leave at 0 for Go's small default pool
      // (fine for a low-rate doc-cursor transport); size these up if you
      // fan out many concurrent requests via concurrency.pool (see below).
      httpMaxConnsPerHost: 0,
      httpMaxIdleConns: 0,
      httpIdleConnTimeoutMs: 0,

      // What this transport asks the user for, and what it lets them tune.
      // Purely declaration: the apps build their forms from it (the profile
      // editor, and the "Настройки" wizard page generated for you - no HTML to
      // write), and open()'s cfg gets the values back, ALWAYS AS STRINGS.
      //
      // The FIRST param is the profile's one input and arrives as cfg.url.
      // Every other param is a setting: asked for in the wizard, delivered in
      // cfg.params (declared defaults filled in for what the user never set).
      // Say it outright with scope: "profile" | "settings".
      //
      //   key          required, unique
      //   label        what the user sees
      //   type         "text" (default) | "url" | "secret" | "number" |
      //                "boolean" | "select" | "textarea"
      //   required     the wizard will not save it empty
      //   default      string | number | boolean
      //   description  help text under the field
      //   placeholder  grey hint in the field
      //   options      select choices: ["a", "b"] or [{value, label}]
      //   min, max     bounds of a number
      //   pattern      regexp for text/url (JS RegExp in the page, RE2 in the core)
      //   group        section heading
      //   advanced     fold it under "Дополнительно"
      //
      // A boolean arrives as "true"/"false", a number as "7". scripttest -settings
      // opens the wizard for you; docs/07-settings-and-setup-pages.md (SDK) has
      // the whole story.
      params: [
        { key: "url", label: "Board URL", type: "url", required: true },
        // { key: "token", label: "API token", type: "secret", required: true, group: "Account" },
        // { key: "retries", label: "Retries", type: "number", default: 3, min: 1, max: 10 },
        // { key: "compress", label: "Compress", type: "boolean", default: false },
        // { key: "region", label: "Region", type: "select", options: ["eu", "us"], default: "eu", advanced: true },
      ],
    };
  },

  // open(cfg) -> Promise|undefined. cfg = { url, params: {...} } - same
  // shape as a native transport's control.TransportConfig. Do all your
  // dialing/auth here. This is NOT awaited by the Go side: return (or let
  // an async function return) as soon as you've kicked off the connection,
  // the same way a native transport's Start() returns immediately and
  // finishes connecting on a background goroutine.
  //
  // Call setState("connected") the moment the link is actually usable -
  // IsConnected() on the Go side is driven ONLY by your setState() calls.
  // Call setState("connecting" | "reconnecting" | "degraded" | "dead", err)
  // at every other transition; the core has no other way to know your
  // link's health.
  open: async function (cfg) {
    setState("connecting");
    try {
      // Example: fetch a token, then open a WebSocket.
      // var res = await http.fetch({ url: cfg.url, method: "GET" });
      // if (res.status !== 200) throw new Error("http " + res.status);
      //
      // this._sock = await ws.open("wss://example.com/socket", {
      //   "User-Agent": "Mozilla/5.0",
      // });
      // this._sock.onmessage = onMessage;
      // this._sock.onclose = onClose;

      setState("connected");
    } catch (e) {
      setState("dead", String(e));
      // Your own reconnect policy goes here - e.g. setTimeout(() =>
      // Transport.open(cfg), backoffMs()). The core will not retry for you.
    }
  },

  // write(bytes) -> throws on failure, returns normally on success. Called
  // by Go once per queued packet, off Send()'s hot path (Go already
  // queues; this only runs when a packet is actually being sent). `bytes`
  // is an ArrayBuffer - the packet's raw bytes, no copy, no codec. Pass it
  // straight to a binary WS frame (sock.send(bytes)), or - if your wire
  // format needs text - base64.encode(bytes) first and embed the string.
  // Throwing causes Go to retry this same packet after a short backoff, so
  // it's safe (and expected) to throw when the socket momentarily isn't
  // open.
  write: function (bytes) {
    // if (!this._sock) throw new Error("not connected");
    // this._sock.send(bytes); // binary frame, zero-copy
    // this._sock.send(JSON.stringify({ type: "data", p: base64.encode(bytes) })); // text protocol
  },

  // close() -> called on Stop(). Clean up your own state; the core also
  // clears every setTimeout/setInterval you registered via Terminate(), so
  // you don't strictly need to clearTimeout yourself, but closing sockets
  // here avoids a lingering half-open connection until GC catches it.
  close: function () {
    // if (this._sock) this._sock.close();
  },

  // settings(values) -> optional. A settings page of your own, instead of the
  // wizard generated from info().params: return an HTML string (or {html}).
  // It runs like info() does (no network, no sockets, three seconds,
  // synchronous) because the apps call it before anything about the script is
  // connected; the page itself may call out from the browser. values = the
  // current settings, strings, defaults filled in. The page hands the new
  // values back with window.openfluxSubmit({...}); only the keys you declare
  // in info().params are kept. See js/template_html.html for the page contract.
  // settings: function (values) { return "<!doctype html>..."; },

  // onEvent(kind, payload) -> optional. Downward OOB delivery: the host
  // application calls this (via ScriptTransport.Deliver on the Go side)
  // to hand you a captcha answer, fresh cookies, or an arbitrary inject.
  // Omit this entirely if your transport never needs it. "cookiesApplied"
  // specifically fires after the Go side has ALREADY written externally
  // supplied cookies into the shared jar (see ApplyCookies/
  // scopeCookiesToParentDomain above) - you just need to act on them
  // (usually: tear down and reopen your socket).
  onEvent: function (kind, payload) {
    // if (kind === "cookiesApplied") { /* reconnect with the new cookies */ }
    // if (kind === "captchaAnswer") { ... }
  },
};

// ---- Host API available to every script (all globals, no import) ----
//
// Nothing here is capability-scoped: dial anywhere, fetch anything. The
// only trust boundary is the signature on this file (or .flux package).
//
// -- HTTP --
//
// http.fetch(opts) -> Promise<{status, url, body, headers}>
//   opts: { url, method, headers, body, redirect }. `url` in the result is
//   the FINAL url after redirects - check it against what you asked for to
//   detect a bounce to a login/captcha page. redirect: "manual" (default
//   "follow") stops Go from auto-following, so you see the 3xx and its
//   Location header yourself instead of only the final response.
//
// http.newSession() -> Session { fetch(opts), cookies }
//   An ISOLATED cookie jar + http.Client pair (same dialer/pool tuning as
//   the default session). Use this when you manage several independent
//   logical sessions against the same domain - a single shared jar lets
//   the last session's cookies silently clobber every earlier one's,
//   since cookie names collide across sessions on one domain.
//   session.cookies.get(url) -> {name: value, ...}
//   session.cookies.set(url, {name: value, ...}, domain?)
//
// -- WebSocket --
//
// ws.open(url, headers, opts?) -> Promise<Socket>
//   opts: { readTimeoutMs } - reset before every read; a connection that
//   falls silent longer than this fires onclose instead of hanging the
//   reader forever. Omit/0 = no deadline.
//   Socket.send(text)             - text frame (JSON/Socket.IO protocols)
//   Socket.send(bytes)            - binary frame, bytes = ArrayBuffer/TypedArray
//   Socket.close()
//   Socket.onmessage = function(textOrBytes) {} - string for a text frame,
//                                                  ArrayBuffer for a binary one
//   Socket.onclose   = function(reason) {}
//   (assign these any time after open() resolves; they're read lazily
//   on every dispatch, so re-assigning mid-connection is fine)
//
// -- UDP --
//
// udp.open(remoteAddr, opts?) -> Promise<Socket>
//   Dial-only (connected) datagram socket, same Socket shape as ws.open
//   (send(bytes)/onmessage/onclose/close). opts: { readTimeoutMs }.
//
// -- WebRTC --
//
// webrtc.newPeerConnection({iceServers, iceTransportPolicy}) -> PeerConnection
//   PeerConnection.createDataChannel(label, {ordered, maxRetransmits}) -> DataChannel
//   PeerConnection.createOffer() -> Promise<sdpString>
//   PeerConnection.createAnswer() -> Promise<sdpString>
//   PeerConnection.setLocalDescription(type, sdp) -> Promise
//   PeerConnection.setRemoteDescription(type, sdp) -> Promise   (type: "offer"|"answer")
//   PeerConnection.addIceCandidate({candidate, sdpMid, sdpMLineIndex}) -> Promise
//   PeerConnection.close()
//   PeerConnection.onicecandidate = function(candidateInitOrNull) {}
//   PeerConnection.onconnectionstatechange = function(stateString) {}
//   PeerConnection.oniceconnectionstatechange = function(stateString) {}
//   PeerConnection.ondatachannel = function(dataChannel) {}   // remote-initiated
//   DataChannel.send(bytesOrText) / .onmessage / .onopen / .onclose / .close()
//   ICE/DTLS/SCTP are native (pion/webrtc) - genuinely can't be JS. Everything
//   ABOVE this (signaling, when to offer/answer, retry policy) is yours.
//
// -- HTTP server (a setup/login mini-app) --
//
// httpserver.listen(handler, port?) -> Server { port, addr, close() }
//   A real HTTP server, 127.0.0.1 only - there is no host argument, by
//   design: everything else here dials out, this is the one primitive
//   that can be reached by another local process, not just sites this
//   script talks to. port 0/omitted picks a free one.
//   handler(req) -> response | Promise<response>, req = {method, path,
//   query: {...}, headers: {...}, body (ArrayBuffer)}, response =
//   {status, headers: {...}, body (string or ArrayBuffer)}. Returning a
//   Promise (an async handler) works exactly like a sync return - use it
//   for a route that itself calls http.fetch before answering.
//   Point the operator at it with raise("needsSetup", {url: "http://" +
//   srv.addr + "/"}) - see js/template_html.html for the simpler static
//   alternative (no server) when a single page is all a script needs.
//
// -- Cookies --
//
// cookieJar.get() -> {name: value, ...}      (against info().cookieDomain)
// cookieJar.set({name: value, ...}, domain?)
//
// -- Codecs --
//
// base64.encode(bytes) -> string     base64.decode(string) -> ArrayBuffer
// text.encode(string) -> ArrayBuffer  text.decode(bytes) -> string   (UTF-8)
// gzip.compress(bytes) -> ArrayBuffer  gzip.decompress(bytes) -> ArrayBuffer
// lz4.decompressBlock(bytes, expectedSize) -> ArrayBuffer
//   (LZ4 BLOCK format, not the streaming/frame format - needs the
//   decompressed size upfront, there's no end marker)
//
// -- Crypto --
//
// crypto.sha256(bytes) -> ArrayBuffer
// crypto.solvePow(prefixHexOrRaw, complexity) -> {nonceHex, attempts}
//   The one native-speed exception: a hash-based PoW brute force (e.g. a
//   captcha's proof-of-work) needs millions of attempts, and a per-attempt
//   JS<->Go call would dwarf the hash cost. Everything ELSE stays in JS.
//
// -- Concurrency --
//
// concurrency.pool(n) -> Pool { run(fn) -> Promise, size() }
//   Gates at most n concurrent in-flight fn() calls through a Go semaphore.
//   fn's own work still runs with true OS-level concurrency (e.g. an
//   http.fetch call) - this only adds bounded fan-out bookkeeping, for a
//   transport that needs real concurrent throughput (goja itself is
//   single-threaded; this is how you get real parallelism anyway).
//
// -- Misc --
//
// url.parse(str) -> {href, protocol, hostname, host, pathname, search, hash}
//   goja has no WHATWG URL global; backed by Go's net/url.
// setTimeout/setInterval/clearTimeout/clearInterval, console.log/warn/error
//   - standard, run on this transport's own loop.
// require("./local/module.js") - only at AUTHOR time: see cmd/scriptbundle.
//   The runtime itself never loads a module from disk; scriptbundle inlines
//   local requires into one flat, self-contained file BEFORE you sign it.
//
// -- The Go <-> JS packet boundary --
//
// emit(bytes)                 - deliver one received application packet up,
//                                bytes = ArrayBuffer/TypedArray, zero-copy
// setState(state, errMsg?)    - "connecting"|"connected"|"reconnecting"|
//                                "degraded"|"dead"
// raise(kind, payload)        - upward OOB event, e.g.
//                                raise("captchaRequired", {url: "..."})
//                                "captchaRequired" and "needsSetup" also
//                                reach the app's browser surface with
//                                payload.url or payload.html (see
//                                js/template_html.html) and payload.reason
//                                - raised reactively mid-session or
//                                proactively from open() ("nothing is
//                                configured yet"), the two read differently
//                                but do exactly the same thing. Every other
//                                kind only reaches onEvent's caller (the
//                                app's own event log, scripttest, ...).
