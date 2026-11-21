// Script-transport port of transport/yandex/boards.go (+ the shared PoW
// captcha solver from transport/yandex/captcha.go, now factored into
// ./lib/captcha.js and shared with yandex.js/vyandex.js). Boards is a
// different protocol from mailru/yandex: a real Socket.IO namespace with a
// "dashboard" event channel, guest-token auth over two POST /api calls, and
// the send path is modify-objects (a text object whose value is the base64
// payload; the receiver deletes it again with drop-objects). The receive
// side still also understands the old notify-position form, so a peer on an
// older build keeps working.
//
// Kept in step with the native transport (parity is tested byte for byte in
// transport/script/parity_test.go).
// This is the SOURCE for the bundled, signed transport/script/js/boards.js -
// build it with scriptbundle, then re-sign the output.

var captcha = require("./lib/captcha.js");
var yandexauth = require("./lib/yandexauth.js");

var BOARDS_BASE = "boards.yandex.ru";
var UA = "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 " +
  "(KHTML, like Gecko) Chrome/153.0.0.0 Mobile Safari/537.36";
var SOCKET_HOST_DEFAULT = "socket33.boards.yandex.ru";
var HEARTBEAT_MS = 20000; // dashboard heartbeat; NO client engine.io "2" (see startHeartbeats)
var READ_DEADLINE_MS = 90000;
var HANDSHAKE_WAIT_MS = 15000;

var running = false;
var hash = "";
var name = "";
var userHash = "";
var jwt = "";
var wsHost = "";
var dashboard = "";
var currentSlide = "";
var sessionId = "";
var participantHash = "";
var creatorHash = "";

var sock = null;
var kaTimer = null;
var ack = 0;
var reconnectTimer = null; // at most one reconnect pending
var connectGen = 0; // bumped by every connectOnce: a stale attempt closes what it opened

function randHex(n) {
  var s = "";
  for (var i = 0; i < n; i++) s += "0123456789abcdef"[Math.floor(Math.random() * 16)];
  return s;
}

function shortStr(s, n) { return (s || "").length <= n ? s : s.slice(0, n); }

// ---- captcha (literal duplicate of yandex.js's, UA swapped to boardsUA) ----

// ---- auth ----

function extractHash(rawURL) {
  var q = url.parse(rawURL).search || "";
  var m = /(?:^|&)hash=([^&]*)/.exec(q);
  return m ? decodeURIComponent(m[1]) : "";
}

async function getAllowCaptcha(docURL, h) {
  var res = await http.fetch({
    url: docURL,
    headers: {
      "User-Agent": UA,
      Accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
      "Accept-Language": "en-US,en;q=0.9",
      Referer: "https://" + BOARDS_BASE + "/guest/?hash=" + h,
    },
    redirect: "manual",
  });
  if (res.status >= 300 && res.status < 400) {
    var loc = res.headers["Location"] || "";
    if (loc.indexOf("showcaptchafast") !== -1) return true;
  }
  return false;
}

async function postAPI(h, action, content) {
  var body = JSON.stringify({ action: action, content: base64.encode(text.encode(JSON.stringify(content))) });
  return http.fetch({
    url: "https://" + BOARDS_BASE + "/api",
    method: "POST",
    headers: {
      "User-Agent": UA,
      "Content-Type": "application/json",
      "X-Requested-With": "XMLHttpRequest",
      Accept: "application/json, text/javascript, */*; q=0.01",
      Referer: "https://" + BOARDS_BASE + "/guest/?hash=" + h,
      Origin: "https://" + BOARDS_BASE,
    },
    body: body,
    redirect: "manual",
  });
}

function jwtPayload(token) {
  var parts = token.split(".");
  if (parts.length < 2) return null;
  var b64 = parts[1].replace(/-/g, "+").replace(/_/g, "/");
  var padLen = (4 - (b64.length % 4)) % 4;
  for (var i = 0; i < padLen; i++) b64 += "=";
  try {
    return JSON.parse(text.decode(base64.decode(b64)));
  } catch (e) {
    return null;
  }
}

async function getWhiteboardInfo(h) {
  var res = await postAPI(h, "get-whiteboard-info", { hash: h });
  if (res.status !== 200) throw new Error("get-whiteboard-info status " + res.status);
  var info = JSON.parse(res.body);
  var out = { current_slide: "", dashboard: "", ws_host: "" };
  var props = info.presentation && info.presentation.properties;
  if (props && props.current_slide) {
    out.current_slide = props.current_slide;
    out.dashboard = props.current_slide;
  }
  var items = info.presentation && info.presentation.items;
  if (items) {
    try {
      var arr = JSON.parse(text.decode(base64.decode(items)));
      if (arr && arr.length > 0 && arr[0].hash && !out.dashboard) {
        out.dashboard = arr[0].hash;
        out.current_slide = arr[0].hash;
      }
    } catch (e) { /* best-effort, same as native */ }
  }
  if (info.socket_servers && info.socket_servers.length > 0) {
    out.ws_host = info.socket_servers[0].ip;
  }
  return out;
}

async function authorize() {
  var docURL = "https://" + BOARDS_BASE + "/whiteboard/?hash=" + hash;

  if (await getAllowCaptcha(docURL, hash)) {
    await captcha.solveCaptcha(UA, docURL);
    // Re-fetch for fresh post-captcha cookies; a captcha again here is
    // silently tolerated, same as the native transport.
    await getAllowCaptcha(docURL, hash);
  }

  var tokenRes = await postAPI(hash, "request-guest-token", { name: name, hash: hash });
  if (tokenRes.status !== 200) throw new Error("request-guest-token status " + tokenRes.status);

  var cookies = cookieJar.get();
  jwt = cookies["token_" + hash] || "";
  if (!jwt) throw new Error("token_" + hash + " not found");
  var payload = jwtPayload(jwt) || {};
  userHash = payload.u || "";

  var state = await getWhiteboardInfo(hash);
  dashboard = state.dashboard;
  currentSlide = state.current_slide;
  wsHost = state.ws_host || SOCKET_HOST_DEFAULT;

  participantHash = userHash;
  creatorHash = userHash;
}

// ---- WS lifecycle ----

function reconnectBackoff(n) {
  if (n < 1) n = 1;
  var shift = n - 1;
  if (shift > 4) shift = 4;
  var d = 500 * Math.pow(2, shift);
  if (d > 15000) d = 15000;
  d += Math.floor(Math.random() * (d / 2 + 1));
  return d;
}

function writeEventObj(ns, obj) {
  var msg = "42" + ack + JSON.stringify([ns, obj]);
  ack++;
  sock.send(msg);
}

function sendSubscribe() {
  var data = {
    session: sessionId,
    dashboard: dashboard,
    presentation: hash,
    properties: {
      guest_mode: true, guest_role: 1, guest_password: null,
      guest_password_expiration_date: null, current_slide: currentSlide,
    },
    participant_team_role: -1,
    participant: participantHash,
    options: {
      type: "landing",
      participant: {
        hash: participantHash, partner: "yandex", userHash: userHash,
        name: name, additional: { guest: true }, module: "yandex",
        presentation: hash,
        identityCandidates: { uidHash: null, legacyHash: participantHash, uid: null, partner: "yandex" },
        module_type: "yandex", participantCaptionName: name,
      },
      intermediate: "",
      device: {
        screen: "674 x 619", screen_width: 674, screen_height: 619,
        browser: "Chrome", browserVersion: "153.0.0.0", browserMajorVersion: 153,
        mobile: true, os: "Android", osVersion: "15", osMajorVersion: 15,
        cookies: true, flashVersion: "no check", agent: "Chrome",
        appVersion: UA, userAgent: UA, appName: "Netscape", platform: "MacIntel",
      },
    },
  };
  writeEventObj("dashboard", { action: "subscribe-slide-dashboard", data: data, participant: participantHash });
}

// buildModifyObjects: the modify-objects dashboard event carrying one packet
// as a text object whose value is the base64 payload. Same as
// buildModifyObjects in boards.go; keys are written in sorted order because
// Go marshals maps that way and the parity test compares the bytes.
function buildModifyObjects(b64, objID, x, y, creator, participant) {
  return {
    action: "modify-objects",
    data: {
      objects: [
        {
          _attributes_: {
            creatorHash: creator,
            id: objID,
            index: "1",
            parent: "DASHBOARD",
            style: "text;html=1;strokeColor=none;fillColor=none;align=left;verticalAlign=middle;whiteSpace=wrap;rounded=0;fontSize=1;",
            type: "textbox",
            value: b64,
            vertex: "1",
          },
          hash: objID,
          mxGeometry: [
            { _attributes_: { as: "geometry", height: "1", width: "1", x: String(x), y: String(y) } },
          ],
        },
      ],
      valueChanges: (function () { var v = {}; v[objID] = true; return v; })(),
    },
    participant: participant,
  };
}

function sendNotifyPosition(bytes) {
  var obj = buildModifyObjects(
    base64.encode(bytes), randHex(32),
    Math.floor(Math.random() * 2000), Math.floor(Math.random() * 1200),
    creatorHash, participantHash
  );
  writeEventObj("dashboard", obj);
}

// dropObjects deletes objects we have already delivered from the board.
function dropObjects(objects) {
  if (!objects.length || !sock) return;
  try {
    writeEventObj("dashboard", { action: "drop-objects", data: { objects: objects }, participant: participantHash });
  } catch (e) {}
}

// No client-side engine.io ping: in EIO=4 the server pings ("2") and the
// client answers ("3", see handleMessage). A client "2" is an invalid
// heartbeat direction to an engine.io v4 server, which then closes the
// socket - the old ping dropped the board every 20 s. The dashboard
// heartbeat keeps it busy.
function startHeartbeats() {
  if (kaTimer) clearInterval(kaTimer);
  kaTimer = setInterval(function () {
    if (!sock) return;
    try {
      writeEventObj("dashboard", { action: "heartbeat", data: {}, participant: participantHash });
    } catch (e) {
      // a heartbeat that cannot be written means a dead connection: drop it so it reconnects
      try { sock.close(); } catch (e2) {}
    }
  }, HEARTBEAT_MS);
}

function stopHeartbeats() {
  if (kaTimer) { clearInterval(kaTimer); kaTimer = null; }
}

function handle431(msg) {
  var idx = msg.indexOf("[");
  if (idx < 0) return;
  var body = msg.slice(idx);
  if (body.indexOf('"dashboard_link"') === -1 &&
      body.indexOf('"participantHash"') === -1 &&
      body.indexOf('"creatorHash"') === -1) {
    return;
  }
  var arr;
  try { arr = JSON.parse(body); } catch (e) { return; }
  if (!arr || arr.length === 0) return;
  var p = arr[0] && arr[0].participant;
  if (!p) return;

  if (p.session && !sessionId) sessionId = p.session;
  if (p.dashboard_link) {
    if (p.dashboard_link.session && !sessionId) sessionId = p.dashboard_link.session;
    if (p.dashboard_link.dashboard && !dashboard) {
      dashboard = p.dashboard_link.dashboard;
      currentSlide = dashboard;
    }
  }
  if (p.creatorHash) creatorHash = p.creatorHash;
  if (p.participantHash) creatorHash = p.participantHash;
}

function handleParticipantConnected(data) {
  var p = data && data.participant;
  if (!p) return;
  if (p.session && !sessionId) sessionId = p.session;
  if (p.name === name && p.hash) creatorHash = p.hash;
}

function deliver(bytes) {
  if (!bytes || bytes.byteLength === 0) return;
  emit(bytes);
}

// handleNotifyPosition: object form (data.position.x) first, else array
// form (data[2]=name, data[4]=base64) - own echo filtered by participant
// (object form, via the envelope) or by name (array form).
function handleNotifyPosition(data, envelopePart) {
  if (data && data.position && typeof data.position.x === "string" && data.position.x !== "") {
    if (envelopePart && envelopePart === participantHash) return;
    try {
      deliver(base64.decode(data.position.x));
    } catch (e) {}
    return;
  }
  if (Array.isArray(data) && data.length >= 5) {
    var sender = data[2];
    if (sender === name) return;
    var b64 = data[4];
    if (typeof b64 !== "string" || b64 === "") return;
    try {
      deliver(base64.decode(b64));
    } catch (e) {}
  }
}

// modifyObjectsPayloads: the packets carried by the objects of a
// (server-)modify-objects event that are not our own echo (as ArrayBuffers,
// in order), and the drop-objects entries that delete exactly those objects.
// Same as ModifyObjectsPayloads in boards.go.
function modifyObjectsPayloads(data, myPart, myUser, myName) {
  var payloads = [];
  var drop = [];
  if (!data || !data.objects || data.objects.length === 0) return { payloads: payloads, drop: drop };
  for (var i = 0; i < data.objects.length; i++) {
    var o = data.objects[i];
    var attrs = o._attributes_;
    if (!attrs) continue;
    var val = attrs.value;
    if (typeof val !== "string" || val === "") continue;
    var creator = attrs.creatorHash;
    if (creator && (creator === myPart || creator === myUser)) continue; // own echo
    if (data.name && data.name === myName) continue; // own echo
    var bytes;
    try {
      bytes = base64.decode(val);
    } catch (e) { continue; }
    if (bytes.byteLength === 0) continue;
    payloads.push(bytes);
    drop.push({ _attributes_: attrs, mxGeometry: o.mxGeometry || null, hash: o.hash || "" });
  }
  return { payloads: payloads, drop: drop };
}

function handleServerModifyObjects(data) {
  var r = modifyObjectsPayloads(data, participantHash, userHash, name);
  for (var i = 0; i < r.payloads.length; i++) deliver(r.payloads[i]);
  if (r.drop.length > 0) {
    var s = sock;
    setTimeout(function () {
      if (sock === s) dropObjects(r.drop); // a small delay before deleting, as the native transport
    }, 100);
  }
}

function handleMessage(msg) {
  if (msg === "2") { if (sock) { try { sock.send("3"); } catch (e) {} } return; }
  if (msg === "3") return;
  if (msg.indexOf("43") === 0) { handle431(msg); return; }
  if (msg.indexOf("42[") !== 0) return;

  var idx = msg.indexOf("[");
  var arr;
  try { arr = JSON.parse(msg.slice(idx)); } catch (e) { return; }
  if (!arr || arr.length < 2) return;
  var envelope = arr[1] || {};

  switch (envelope.action) {
    case "participant-connected":
      handleParticipantConnected(envelope.data);
      break;
    case "notify-position":
      handleNotifyPosition(envelope.data, envelope.participant);
      break;
    case "server-modify-objects":
    case "modify-objects":
      handleServerModifyObjects(envelope.data);
      break;
  }
}

async function handshake() {
  // Engine.io hello, then socket.io connect ack - content unused, just
  // needs to have arrived before we speak.
  await waitRaw();
  sock.send("40");
  await waitRaw();

  writeEventObj("im", { operation: "subscribe", user: null });

  var subscribed = await waitFor(function (m) { return m.indexOf('"subscribed"') !== -1; }, 10000);
  if (!subscribed) throw new Error("wait subscribed: timed out");

  sendSubscribe();

  var ok = await waitFor(function (m) {
    handleMessage(m); // native calls handleMessage on every message in this wait too
    return m.indexOf("431[") === 0 ||
      (m.indexOf('"subscribed":true') !== -1 && m.indexOf('"dashboard_link"') !== -1);
  }, HANDSHAKE_WAIT_MS);
  if (!ok) throw new Error("wait 431: timed out");
}

// waitRaw/waitFor: the handshake needs to synchronously await specific
// messages before the general onmessage handler takes over - a small
// promise-based queue bridges that.
var pendingWaiters = [];

function waitRaw() {
  return new Promise(function (resolve) {
    pendingWaiters.push({ pred: function () { return true; }, resolve: resolve });
  });
}

function waitFor(pred, timeoutMs) {
  return new Promise(function (resolve) {
    var waiter = { pred: pred, resolve: null, done: false };
    var timer = setTimeout(function () {
      if (waiter.done) return;
      waiter.done = true;
      var i = pendingWaiters.indexOf(waiter);
      if (i >= 0) pendingWaiters.splice(i, 1);
      resolve(false);
    }, timeoutMs);
    waiter.resolve = function (matched) {
      if (waiter.done) return;
      waiter.done = true;
      clearTimeout(timer);
      resolve(matched);
    };
    pendingWaiters.push(waiter);
  });
}

function dispatchToWaiters(msg) {
  for (var i = 0; i < pendingWaiters.length; i++) {
    var w = pendingWaiters[i];
    if (w.pred(msg)) {
      pendingWaiters.splice(i, 1);
      w.resolve(true);
      return true;
    }
  }
  return false;
}

function onSocketMessage(msg) {
  if (dispatchToWaiters(msg)) return;
  handleMessage(msg);
}

// reconnectAttempt counts consecutive failures, capped at 10; a session that
// held for more than a minute is not part of a failure streak (as in
// connectLoop in boards.go).
var reconnectAttempt = 0;
var connectedSince = 0;

function scheduleNextConnect() {
  if (!running) return;
  if (reconnectTimer) return; // one is already pending: a second would open a duplicate session
  var d = reconnectBackoff(reconnectAttempt);
  reconnectAttempt++;
  if (reconnectAttempt > 10) reconnectAttempt = 10;
  reconnectTimer = setTimeout(function () {
    reconnectTimer = null;
    if (running) connectOnce();
  }, d);
}

function connectOnce() {
  var myGen = ++connectGen;
  connectAndServe(myGen).then(
    function () { /* connected; onSocketClose schedules the next retry when it eventually closes */ },
    function (e) {
      if (myGen !== connectGen) return; // superseded
      setState("reconnecting", String(e));
      scheduleNextConnect();
    }
  );
}

function onSocketClose() {
  stopHeartbeats();
  sock = null;
  if (connectedSince && Date.now() - connectedSince > 60000) reconnectAttempt = 0;
  connectedSince = 0;
  if (!running) return;
  setState("reconnecting");
  scheduleNextConnect();
}

async function connectAndServe(myGen) {
  // Every connection numbers its Socket.IO events from 0, the handshake's
  // included: the wait for the subscribe answer ("431[...") depends on it.
  ack = 0;
  var wsURL = "wss://" + wsHost + "/socket.io/?EIO=4&transport=websocket";
  var cookies = cookieJar.get();
  if (!cookies["token_" + hash]) cookies["token_" + hash] = jwt;
  var cookieStr = Object.keys(cookies).map(function (k) { return k + "=" + cookies[k]; }).join("; ");

  var newSock = await ws.open(wsURL, {
    "User-Agent": UA,
    Origin: "https://" + BOARDS_BASE,
    "Accept-Language": "en-US,en;q=0.9",
    Cookie: cookieStr,
  }, { readTimeoutMs: READ_DEADLINE_MS });
  if (myGen !== connectGen || !running) {
    try { newSock.close(); } catch (e) {}
    return;
  }

  sock = newSock;
  sock.onmessage = onSocketMessage;
  sock.onclose = function () {
    if (sock !== newSock) return; // a socket we already replaced or dropped on purpose
    onSocketClose();
  };

  await handshake();

  startHeartbeats();
  connectedSince = Date.now();
  setState("connected");
}

var Transport = {
  info: function () {
    return {
      name: "boards",
      version: "1.2.0",
      cookieDomain: "https://boards.yandex.ru/",
      auth: yandexauth.auth("create"),
      mtu: 0,
      reliable: false,
      ordered: false,
      params: [
        { key: "url", label: "Yandex Boards whiteboard URL (?hash=...)", type: "url", required: true },
      ],
    };
  },

  open: function (cfg) {
    var raw = (cfg.params && cfg.params.url) || cfg.url || "";
    hash = extractHash(raw);
    if (!hash) {
      setState("dead", "boards: no hash in URL");
      return;
    }
    name = "guest_" + randHex(6);
    running = true;
    setState("connecting");

    authorize().then(
      function () { connectOnce(); },
      function (e) { setState("dead", String(e)); }
    );
  },

  write: function (bytes) {
    if (!sock) throw new Error("boards: not connected");
    sendNotifyPosition(bytes);
  },

  close: function () {
    running = false;
    connectGen++;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    reconnectTimer = null;
    stopHeartbeats();
    pendingWaiters = [];
    if (sock) { try { sock.close(); } catch (e) {} }
    sock = null;
  },

  onEvent: function (kind) {
    if (kind === "cookiesApplied") {
      // The generic ApplyCookies wrote the jar; drop the socket so the next
      // connection carries them. onclose then reconnects (once).
      if (sock) { try { sock.close(); } catch (e) {} }
    }
  },
};
