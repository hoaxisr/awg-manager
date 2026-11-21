// Script-transport port of transport/yandex/vyandex.go ("Volga"). Same
// wire protocol (fake collaborative-editing bundle smuggling a
// length-prefixed, base64-encoded packet batch over HTTP POST /relay;
// receive via a push.yandex.ru WebSocket subscription), same batch
// framing/timing/bundle shape as the native transport - but the
// send-side concurrency is re-architected, not literally translated.
//
// Native Volga runs 2000 Go workers, each independently draining a shared
// channel into its own batch. That's a real-concurrency design goja can't
// reproduce by counting to 2000 in a single-threaded interpreter. The
// concurrency.pool/http.fetch host primitives (see transport/script/
// host.go) solve this differently: JS accumulates ONE batch at a time
// (same BatchSize/BatchTimeout/BatchMaxBytes as native) and hands each
// flushed batch to concurrency.pool.run(), which gates SEND_CONCURRENCY
// concurrent in-flight POSTs - but each POST still runs on its own Go
// goroutine with true OS-level concurrency, same as any http.fetch call.
// A smaller pool number is fine here because the earlier bottleneck this
// was defending against (worker starvation) doesn't exist in this model;
// what actually matters for throughput - real concurrent HTTP dispatch
// and a connection pool sized to match - is asked for via info()'s
// httpMaxConnsPerHost/httpMaxIdleConns, not by counting workers in JS.

var captcha = require("./lib/captcha.js");
var yandexauth = require("./lib/yandexauth.js");

var UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0";
var CLIENT_CONFIG_RE = /<script[^>]*id="client-config"[^>]*>(.*?)<\/script>/;

var SEND_CONCURRENCY = 64; // vs. native's 2000 Go workers - see file header
var BATCH_SIZE = 20;
var BATCH_TIMEOUT_MS = 2;
var BATCH_MAX_BYTES = 4 * 1024 * 1024;
var MAX_PAYLOAD_BYTES = 5000000;

var RECONNECT_MIN_MS = 500;
var RECONNECT_MAX_MS = 30000;
var RECONNECT_MULTIPLIER = 1.5;
var WS_READ_TIMEOUT_MS = 60000;
var KEEPALIVE_MS = 10000;
var MAX_SESSION_AGE_MS = 30 * 60 * 1000;
var STALL_CHECK_MS = 5000;
var STALL_INTERVALS = 12; // 12 * 5s = 60s of sent-but-unanswered before forcing a reconnect

var docURL = "";
var running = false;
var sendPool = null;

// auth: mirrors volgaAuth. Refreshed on every WS reconnect after the first.
var auth = null;

var pendingBatch = [];
var pendingBytes = 0;
var batchTimer = null;
var bundleSeq = 0;
var opSeq = 0;
var localIdSeq = 0;
var frontier = "";

var sock = null;
var wsReconnectDelay = RECONNECT_MIN_MS;
var sessionRotateTimer = null;
var started = false; // the links (WebSocket, loops) are up: the first authorization succeeded
var initialRetryTimer = null;
var wsReconnectTimer = null; // at most one WebSocket reconnect pending
var connectWSGen = 0; // bumped by every connectWS: a stale attempt closes what it opened

// stats feed the stall detector, same shape as native's stalledTraffic.
var dataSentWindow = 0;
var dataRecvWindow = 0;
var stallUnanswered = 0;
var stallPending = false;

// ---- authorize (literal port of authorizeWithJar) ----

function jsonNumStr(v) {
  if (v === null || v === undefined) return "0";
  return String(v);
}

async function authorize() {
  var currentURL = docURL;
  var finalBody = null;

  for (var i = 0; i < 15; i++) {
    var headers = captcha.browserHeaders(UA);
    headers["Accept-Language"] = "ru-RU,ru;q=0.9";
    if (i > 0) headers["Referer"] = docURL;
    var res = await http.fetch({ url: currentURL, headers: headers, redirect: "manual" });

    if (res.status === 200) { finalBody = res.body; break; }

    if (res.status >= 300 && res.status < 400) {
      var loc = res.headers["Location"];
      if (!loc) throw new Error("redirect without Location from " + currentURL);

      if (loc.indexOf("showcaptcha") !== -1 && loc.indexOf("showcaptchafast") === -1) {
        raise("captchaRequired", { url: docURL, location: loc, reason: "smartcaptcha" });
        throw new SentinelError("captchaRequired");
      }
      if (loc.indexOf("passport.yandex") !== -1) {
        // A login wall reaches the app through the same notifier as a captcha, told apart
        // by reason (the native transport does the same): the app opens a sign-in page.
        raise("captchaRequired", { url: docURL, reason: "login" });
        throw new SentinelError("loginRequired");
      }
      if (loc.indexOf("showcaptchafast") !== -1) {
        await captcha.solveCaptcha(UA, docURL);
        currentURL = docURL;
        continue;
      }
      if (loc.indexOf("/") === 0) {
        var base = url.parse(currentURL);
        loc = base.protocol + "//" + base.host + loc;
      }
      currentURL = loc;
      continue;
    }
    throw new Error("unexpected status " + res.status + " at " + currentURL);
  }
  if (finalBody === null) throw new Error("too many redirects from " + docURL);

  var m = CLIENT_CONFIG_RE.exec(finalBody);
  if (!m) throw new Error("client-config not found");
  var cfg = JSON.parse(m[1]);

  var office = cfg.officeActionData;
  var editor = cfg.editorParams;
  if (!office) throw new Error("officeActionData missing");

  var actionURL = office.action_url;
  var accessToken = office.access_token;
  if (!actionURL) throw new Error("action_url missing");
  if (!accessToken) throw new Error("access_token missing");

  var a = {
    accessToken: accessToken,
    resourceURL: office.resource_url || "",
    docId: (editor && editor.idDoc) || "",
    action: (editor && editor.action) || "",
  };

  var form = "access_token=" + encodeURIComponent(accessToken) +
    "&access_token_ttl=" + encodeURIComponent(jsonNumStr(office.access_token_ttl));

  var res2 = await http.fetch({
    url: actionURL,
    method: "POST",
    headers: {
      "User-Agent": UA,
      "Content-Type": "application/x-www-form-urlencoded",
      Origin: "https://disk.yandex.ru",
      Referer: currentURL,
      Accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
      "Accept-Language": "ru-RU,ru;q=0.9",
      "Upgrade-Insecure-Requests": "1",
      "Sec-Fetch-Dest": "iframe",
      "Sec-Fetch-Mode": "navigate",
      "Sec-Fetch-Site": "cross-site",
    },
    body: form,
    redirect: "manual",
  });
  if (res2.status !== 302) throw new Error("auth/initial status " + res2.status + " (expected 302)");

  var location = res2.headers["Location"];
  if (!location) throw new Error("auth/initial no Location");
  if (location.indexOf("/document/error/") !== -1) {
    throw new Error("auth/initial returned /document/error/ - check access_token_ttl and Referer");
  }

  var loc2 = url.parse(location);
  var qs = parseQuery(loc2.search);
  a.token = qs.token || "";
  a.requestPath = qs["request-path"] || "";
  var jsonStr = qs.json || "";
  if (!jsonStr) throw new Error("no json in Location");

  var jsonData = JSON.parse(jsonStr);
  a.sessionId = jsonData.sessionId || "";
  a.userId = jsonData.userId || 0;
  if (jsonData.xiva) {
    a.sign = jsonData.xiva.sign || "";
    a.ts = jsonData.xiva.ts || "";
    a.userIdStr = jsonData.xiva.user || "";
  }

  // Completes server-side session setup; response is discarded, like the
  // native transport.
  await http.fetch({
    url: location,
    headers: { "User-Agent": UA, Accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", Referer: actionURL },
    redirect: "manual",
  });

  var cookies = cookieJar.get();
  a.cookieHeader = Object.keys(cookies).map(function (k) { return k + "=" + cookies[k]; }).join("; ");

  if (!a.token || !a.requestPath || !a.userIdStr || !a.sign) {
    throw new Error("incomplete auth: token=" + !!a.token + " rp=" + !!a.requestPath + " user=" + !!a.userIdStr + " sign=" + !!a.sign);
  }
  return a;
}

function parseQuery(qs) {
  var out = {};
  if (!qs) return out;
  var parts = qs.split("&");
  for (var i = 0; i < parts.length; i++) {
    if (!parts[i]) continue;
    var kv = parts[i].split("=");
    out[decodeURIComponent(kv[0])] = decodeURIComponent((kv[1] || "").replace(/\+/g, "%20"));
  }
  return out;
}

function SentinelError(kind) {
  this.message = kind;
  this.kind = kind;
}
SentinelError.prototype = Object.create(Error.prototype);

// ---- batching / send (packBatch mirrors sendBatch's blob framing) ----

function packBatch(packets) {
  var total = 0;
  for (var i = 0; i < packets.length; i++) total += 2 + packets[i].byteLength;
  var out = new Uint8Array(total);
  var off = 0;
  for (i = 0; i < packets.length; i++) {
    var p = new Uint8Array(packets[i]);
    out[off] = (p.length >> 8) & 0xff;
    out[off + 1] = p.length & 0xff;
    off += 2;
    out.set(p, off);
    off += p.length;
  }
  return out.buffer;
}

function nextOpID() {
  opSeq++;
  return "1-" + auth.userId + "." + opSeq;
}

async function sendBatch(packets) {
  if (!auth) throw new Error("volga: authorization unavailable");
  var blob = packBatch(packets);
  var encoded = base64.encode(blob);

  var opID = nextOpID();
  var relayOpID = nextOpID();
  var frontierArr = frontier ? [frontier] : [];

  var bundle = [
    {
      id: opID, frontier: frontierArr, undoable: true, actionName: "textInsert",
      ops: [["it", "vyd:t/00000000000008", 0, "A"]],
      sideEffect: false, localId: ++localIdSeq,
    },
    {
      id: relayOpID, frontier: [opID], undoable: false, actionName: "setCaret",
      ops: [["us", auth.userId, [[["vyd:t/00000000000008", 0, -1], ["vyd:t/00000000000008", 0, -1]]]]],
      sideEffect: true, localId: ++localIdSeq,
    },
    encoded,
  ];

  bundleSeq++;
  var payload = { message: { bundleId: bundleSeq, bundle: bundle }, targetUserId: null };

  var res = await http.fetch({
    url: "https://volga.yandex.ru/session/main/" + auth.requestPath + "/relay",
    method: "POST",
    headers: {
      "User-Agent": UA,
      Authorization: "Bearer " + auth.token,
      "Content-Type": "application/json",
      Origin: "https://volga.yandex.ru",
      Referer: "https://volga.yandex.ru/document/?request-path=" + auth.requestPath,
      Accept: "*/*",
      "Sec-Fetch-Dest": "empty",
      "Sec-Fetch-Mode": "cors",
      "Sec-Fetch-Site": "same-origin",
      Cookie: auth.cookieHeader || "",
    },
    body: JSON.stringify(payload),
    redirect: "manual",
  });
  if (res.status !== 204 && res.status !== 200) {
    throw new Error("status " + res.status);
  }

  var isData = !(packets.length === 1 && packets[0].byteLength === 1 && new Uint8Array(packets[0])[0] === 0);
  if (isData) dataSentWindow++;
}

var relayFailures = 0;

function flushBatch() {
  if (pendingBatch.length === 0) return;
  var batch = pendingBatch;
  pendingBatch = [];
  pendingBytes = 0;
  if (batchTimer) { clearTimeout(batchTimer); batchTimer = null; }

  sendPool.run(function () {
    // A batch the relay turned down (a 429 under load) is dropped, as the
    // native relay does: the Session above resends what matters. It must not
    // change the state - anything but "connected" stops every Send.
    return sendBatch(batch).catch(function (e) {
      relayFailures++;
      if (relayFailures === 1 || relayFailures % 100 === 0) console.log("[VOLGA] relay batch failed (" + relayFailures + " so far): " + e);
    });
  });
}

function queuePacket(bytes) {
  pendingBatch.push(bytes);
  pendingBytes += bytes.byteLength;
  if (pendingBatch.length >= BATCH_SIZE || pendingBytes >= BATCH_MAX_BYTES) {
    flushBatch();
  } else if (pendingBatch.length === 1) {
    batchTimer = setTimeout(flushBatch, BATCH_TIMEOUT_MS);
  }
}

// ---- receive (WS subscribe to push.yandex.ru) ----

function decodeBatch(bytes) {
  var packets = [];
  var view = new Uint8Array(bytes);
  var off = 0;
  while (view.length - off >= 2) {
    var ln = (view[off] << 8) | view[off + 1];
    off += 2;
    if (ln === 0 || view.length - off < ln) break;
    packets.push(view.slice(off, off + ln).buffer);
    off += ln;
  }
  if (packets.length === 0 && view.length - off > 0) {
    packets.push(view.slice(off).buffer);
  }
  return packets;
}

function handleBundleItem(item) {
  if (item && typeof item === "object" && !Array.isArray(item)) {
    if (item.actionName) {
      if (item.id) frontier = item.id;
      return;
    }
  }
  if (typeof item === "string" && item !== "") {
    var decoded;
    try { decoded = base64.decode(item); } catch (e) { return; }
    var packets = decodeBatch(decoded);
    for (var i = 0; i < packets.length; i++) {
      var pkt = packets[i];
      var isData = !(pkt.byteLength === 1 && new Uint8Array(pkt)[0] === 0);
      if (isData) dataRecvWindow++;
      emit(pkt);
    }
  }
}

function handleBundle(raw) {
  if (Array.isArray(raw)) {
    for (var i = 0; i < raw.length; i++) handleBundleItem(raw[i]);
    return;
  }
  if (raw && Array.isArray(raw.value)) {
    for (var j = 0; j < raw.value.length; j++) handleBundleItem(raw.value[j]);
  }
}

function handleMessage(msg) {
  var envelope;
  try { envelope = JSON.parse(msg); } catch (e) { return; }
  if (!envelope || envelope.operation === "ping") return;
  if (envelope.operation !== "SESSION" && envelope.operation !== "WORKER") return;
  if (!envelope.message) return;

  var inner;
  try { inner = JSON.parse(envelope.message); } catch (e) { return; }
  if (!inner) return;
  if (auth && inner.userId === auth.userId) return; // our own echo

  if (inner.t === "relay") {
    if (inner.message && inner.message.bundle) handleBundle(inner.message.bundle);
  } else if (inner.t === "exchange") {
    handleBundle(inner.bundle);
  }
}

function wsURLFor(a) {
  return "wss://push.yandex.ru/v2/subscribe/websocket?" +
    "service=volga" +
    "&user=" + encodeURIComponent(a.userIdStr) +
    "&sign=" + a.sign +
    "&ts=" + a.ts +
    "&client=web" +
    "&session=" + a.sessionId +
    "&fetch_history=" + encodeURIComponent(a.userIdStr + ":volga:0:1") +
    "&x_request_attempt=0";
}

function scheduleSessionRotation() {
  if (sessionRotateTimer) clearTimeout(sessionRotateTimer);
  if (MAX_SESSION_AGE_MS <= 0) return;
  sessionRotateTimer = setTimeout(function () {
    if (sock) { try { sock.close(); } catch (e) {} }
  }, MAX_SESSION_AGE_MS);
}

function scheduleWSReconnect(d) {
  if (!running) return;
  if (wsReconnectTimer) return; // one is already pending: a second would open a duplicate session
  wsReconnectTimer = setTimeout(function () {
    wsReconnectTimer = null;
    connectWS(false);
  }, d);
}

async function connectWS(isFirst) {
  if (!running) return;
  var myGen = ++connectWSGen;
  if (!isFirst) {
    try {
      var fresh = await authorize();
      auth = fresh;
      frontier = "";
    } catch (e) {
      // Keep the old auth, same as refreshAuth's failure path.
    }
  }
  if (!running || myGen !== connectWSGen) return;

  try {
    var newSock = await ws.open(wsURLFor(auth), {
      "User-Agent": UA,
      Origin: "https://volga.yandex.ru",
      Cookie: auth.cookieHeader || "",
    }, { readTimeoutMs: WS_READ_TIMEOUT_MS });
    if (!running || myGen !== connectWSGen) {
      try { newSock.close(); } catch (e) {}
      return;
    }

    sock = newSock;
    var connectedAt = Date.now();
    sock.onmessage = handleMessage;
    sock.onclose = function () {
      if (sock !== newSock) return; // a socket we already replaced or dropped on purpose
      sock = null;
      if (!running) return;
      var connectedFor = Date.now() - connectedAt;
      if (connectedFor >= 2 * WS_READ_TIMEOUT_MS) {
        wsReconnectDelay = RECONNECT_MIN_MS;
      }
      var d = wsReconnectDelay;
      wsReconnectDelay = Math.min(wsReconnectDelay * RECONNECT_MULTIPLIER, RECONNECT_MAX_MS);
      setState("reconnecting");
      scheduleWSReconnect(d);
    };

    scheduleSessionRotation();
    setState("connected");
  } catch (e) {
    setState("reconnecting", String(e));
    var d2 = wsReconnectDelay;
    wsReconnectDelay = Math.min(wsReconnectDelay * RECONNECT_MULTIPLIER, RECONNECT_MAX_MS);
    scheduleWSReconnect(d2);
  }
}

// ---- keepalive / stall detection ----

var keepAliveTimer = null;
var stallTimer = null;

function startBackgroundLoops() {
  keepAliveTimer = setInterval(function () {
    queuePacket(new Uint8Array([0]).buffer);
  }, KEEPALIVE_MS);

  stallTimer = setInterval(function () {
    var sent = dataSentWindow, recv = dataRecvWindow;
    dataSentWindow = 0; dataRecvWindow = 0;
    if (recv > 0) { stallUnanswered = 0; stallPending = false; return; }
    if (sent > 0) stallPending = true;
    if (!stallPending) return;
    stallUnanswered++;
    if (stallUnanswered < STALL_INTERVALS) return;
    stallUnanswered = 0;
    stallPending = false;
    if (sock) { try { sock.close(); } catch (e) {} }
  }, STALL_CHECK_MS);
}

function stopBackgroundLoops() {
  if (keepAliveTimer) { clearInterval(keepAliveTimer); keepAliveTimer = null; }
  if (stallTimer) { clearInterval(stallTimer); stallTimer = null; }
  if (sessionRotateTimer) { clearTimeout(sessionRotateTimer); sessionRotateTimer = null; }
  if (batchTimer) { clearTimeout(batchTimer); batchTimer = null; }
}

// startLinks brings the WebSocket and the background loops up with the first
// good authorization - from open(), a retry, or the "cookiesApplied" that
// follows a captcha/login solved while the transport had never started.
function startLinks(a) {
  if (started || !running) return;
  started = true;
  auth = a;
  startBackgroundLoops();
  connectWS(true);
}

function initialAuthorize() {
  if (!running || started) return;
  authorize().then(
    function (a) {
      startLinks(a);
    },
    function (e) {
      if (!running || started) return;
      if (e instanceof SentinelError) {
        // a captcha/login wall: the app handles the raise(); the cookies it
        // applies start the transport (onEvent below)
        setState("dead", e.kind);
        return;
      }
      // a transient failure (network, a page that did not parse): try again
      setState("reconnecting", String(e));
      var d = wsReconnectDelay;
      wsReconnectDelay = Math.min(wsReconnectDelay * RECONNECT_MULTIPLIER, RECONNECT_MAX_MS);
      initialRetryTimer = setTimeout(function () {
        initialRetryTimer = null;
        initialAuthorize();
      }, d);
    }
  );
}

var Transport = {
  info: function () {
    return {
      name: "vyandex",
      version: "1.2.1",
      cookieDomain: "https://yandex.ru/",
      auth: yandexauth.auth("always"),
      scopeCookiesToParentDomain: true,
      mtu: 0,
      reliable: false,
      ordered: false,
      // Real Go-side concurrency for the send fan-out: a connection pool
      // sized to comfortably exceed SEND_CONCURRENCY, so the pool's gate
      // is the actual bottleneck, not Go's default (2 idle conns/host).
      httpMaxConnsPerHost: 128,
      httpMaxIdleConns: 256,
      httpIdleConnTimeoutMs: 90000,
      params: [
        { key: "url", label: "Yandex Volga document URL", type: "url", required: true },
      ],
    };
  },

  open: function (cfg) {
    docURL = (cfg.params && cfg.params.url) || cfg.url || "";
    running = true;
    sendPool = concurrency.pool(SEND_CONCURRENCY);
    setState("connecting");
    initialAuthorize();
  },

  write: function (bytes) {
    if (bytes.byteLength > MAX_PAYLOAD_BYTES) {
      throw new Error("packet too large: " + bytes.byteLength + " > " + MAX_PAYLOAD_BYTES);
    }
    queuePacket(bytes);
  },

  close: function () {
    running = false;
    connectWSGen++;
    if (initialRetryTimer) clearTimeout(initialRetryTimer);
    initialRetryTimer = null;
    if (wsReconnectTimer) clearTimeout(wsReconnectTimer);
    wsReconnectTimer = null;
    stopBackgroundLoops();
    if (sock) { try { sock.close(); } catch (e) {} }
    sock = null;
  },

  onEvent: function (kind) {
    if (kind === "cookiesApplied") {
      // Generic ApplyCookies already updated the shared jar (parent-domain
      // scoped, see scopeCookiesToParentDomain); re-authorize with it and
      // swap in the fresh session, same as native ApplyCookies.
      authorize().then(
        function (a) {
          if (!started) {
            // the first authorization never succeeded (a captcha or login
            // wall): the solved one starts the transport
            startLinks(a);
            return;
          }
          auth = a;
          frontier = "";
          if (sock) { try { sock.close(); } catch (e) {} }
        },
        function (e) { setState("degraded", String(e)); }
      );
    }
  },
};
