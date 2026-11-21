// Mail.ru Docs: packets ride the chat ("message") channel of a public document's
// co-editing WebSocket (R7/OnlyOffice). Port of transport/mailru/mailru.go as of
// core 0.14.5.
//
// Why the chat channel and not the cursor field: the current server validates
// cursor and saveChanges writes against the live document model and closes the
// socket ("access deny", 4004) on a synthetic one; a chat message is relayed
// verbatim to every other participant. Our messages carry CHAT_DATA_TAG + base64.
//
// What the server enforces and how this copes (all measured on the native one):
//   * a public document opens only for a signed-in visitor: r7/edit with the
//     account's cookies and Cloud's CSRF token (auth below);
//   * ~12 chat messages per connection regardless of size, then a 4004 cut, and
//     a token bucket of ~80-90 messages refilled at ~2/s: so big messages
//     (batchBytes: the Session coalesces up to 500 KB), at most 5 a second, and
//     a fresh connection is brought up and swapped in (make-before-break) after
//     60 messages or 25 s, before the server cuts the old one;
//   * the server echoes our own chat back: a frame is ours if its per-connection
//     editor id is one of ours (learned from the auth reply), so an exit and a
//     client signed in as one account still hear each other;
//   * a second editor locks the document on the first (connectState): unlock.

var USER_AGENT = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:155.0) Gecko/20100101 Firefox/155.0";
var CHAT_DATA_TAG = "OFX1";
var MSG_INTERVAL_MS = 200;
var ROTATE_MS = 25000;
var ROTATE_MSGS = 60;
var ROTATE_AUTH_WAIT_MS = 8000;
var HANDSHAKE_MS = 15000;
var QUEUE_MAX = 1024;
var UNLOCK_MSG = '42["message",{"type":"unLockDocument","unlock":true,"isSave":false,"releaseLocks":false,"deleteIndex":-1}]';

var weblink = "";
var running = false;
var baseUserID = "";
var userID = "";
var cur = null; // the active connection: { sock, sid, sent, startedAt, authed, onAuthed }
var rotating = false;
var reconnectTimer = null;
var reconnecting = false;
var queue = []; // packets waiting for the writer
var writerTimer = null;
var lastWrite = 0;
var ourIDs = {}; // account-level editor ids we authenticated as
var ourConns = {}; // per-connection editor ids ("uid...108"), from the auth reply
var connGen = 0;

function pad(n, width) {
  var s = String(n);
  while (s.length < width) s = "0" + s;
  return s;
}

function normalizeWeblink(link) {
  link = String(link).trim();
  var prefixes = ["https://cloud.mail.ru/public/", "http://cloud.mail.ru/public/", "https://cloud.mail.ru/", "http://cloud.mail.ru/"];
  for (var i = 0; i < prefixes.length; i++) {
    if (link.indexOf(prefixes[i]) === 0) return link.slice(prefixes[i].length).replace(/^\/+|\/+$/g, "");
  }
  return link;
}

function reconnectBackoff(n) {
  if (n < 1) n = 1;
  var d = Math.min(500 * Math.pow(2, Math.min(n - 1, 5)), 15000);
  return d + Math.floor(Math.random() * (d / 2 + 1));
}

function sleep(ms) {
  return new Promise(function (resolve) { setTimeout(resolve, ms); });
}

// ---- the document's editor API ----

// csrfToken: Cloud's token for the signed-in session, "" when not signed in.
async function csrfToken() {
  try {
    var res = await http.fetch({
      url: "https://cloud.mail.ru/api/v2/tokens/csrf",
      method: "POST",
      headers: { Accept: "application/json, text/plain, */*", "Accept-Language": "en-US,en;q=0.9",
        "User-Agent": USER_AGENT, Referer: "https://cloud.mail.ru/home/" },
      body: "",
    });
    if (res.status !== 200) return "";
    return ((JSON.parse(res.body) || {}).body || {}).token || "";
  } catch (e) {
    return "";
  }
}

async function fetchDocInfo(link) {
  var headers = {
    "Content-Type": "application/json",
    Accept: "application/json, text/plain, */*",
    "Accept-Language": "en-US,en;q=0.9",
    "User-Agent": USER_AGENT,
    "X-Api-Version": "4",
    Referer: "https://cloud.mail.ru/public/" + link + "?weblink=" + link,
  };
  // Mail.ru refuses an anonymous r7/edit ("public is available with auth
  // only"); with the sign-in's cookies it wants the CSRF token too.
  var token = await csrfToken();
  if (token) headers["X-CSRF-Token"] = token;
  var res = await http.fetch({
    url: "https://cloud.mail.ru/api/v4/r7/edit",
    method: "POST",
    headers: headers,
    body: JSON.stringify({ "x-email": "anonym", public: "/" + link, platform: "desktop_web" }),
  });
  if (res.status === 401 || res.status === 403) {
    raise("authRequired", { account: "mailru", reason: "Mail.ru opens the document only for a signed-in visitor (status " + res.status + ")" });
  }
  if (res.status !== 200) throw new Error("mailru: api returned status " + res.status);
  var data = JSON.parse(res.body);
  var doc = data.document;
  if (!doc) throw new Error("mailru: document object missing");
  var editorConfig = data.editorConfig;
  if (!editorConfig) throw new Error("mailru: editorConfig object missing");
  return {
    token: data.token || "",
    docKey: doc.key,
    wsURL: String(data.api || "").replace("https://", "wss://") + "/doc/" + doc.key + "/c/?EIO=4&transport=websocket",
    fileType: doc.fileType,
    docUrl: doc.url,
    docTitle: doc.title,
    permissions: doc.permissions || {},
    callbackUrl: editorConfig.callbackUrl || "",
    editorUserId: (editorConfig.user && editorConfig.user.id) || "",
  };
}

// ---- one connection ----

// dialAndAuth opens a connection, completes the Engine.IO/Socket.IO handshake
// and sends the editor auth. It reads from the start (to catch the auth reply)
// but delivers only once installed as the active connection.
async function dialAndAuth() {
  var info = await fetchDocInfo(weblink);
  var sock = await ws.open(info.wsURL, {
    "User-Agent": USER_AGENT,
    "Accept-Language": "en-US,en;q=0.9", // some edges answer 409 without it (#164)
    Origin: "https://docs.datacloudmail.ru",
  }, { compress: true }); // nginx answers 409 to an upgrade without permessage-deflate (#163)
  var c = { sock: sock, sid: "", sent: 0, startedAt: 0, authed: false, onAuthed: null, retired: false, active: false, gen: ++connGen };
  if (info.editorUserId) ourIDs[info.editorUserId] = true;

  var pending = [], wake = null, closed = false;
  sock.onmessage = function (text) {
    if (text === "2") { try { sock.send("3"); } catch (e) {} return; }
    if (!c.handshaken) { pending.push(text); if (wake) wake(); return; }
    handleMessage(c, text);
  };
  sock.onclose = function (reason) {
    closed = true;
    if (wake) wake();
    onClose(c, reason);
  };
  var waitFor = async function (match) {
    var deadline = Date.now() + HANDSHAKE_MS;
    while (Date.now() < deadline && !closed) {
      while (pending.length) {
        var t = pending.shift();
        if (match(t)) return t;
      }
      await new Promise(function (resolve) { wake = resolve; setTimeout(resolve, 200); });
      wake = null;
    }
    throw new Error(closed ? "closed during the handshake" : "handshake timeout");
  };
  try {
    await waitFor(function (t) { return t.indexOf("0{") === 0; });
    sock.send('40{"token":"' + info.token + '"}');
    var ack = await waitFor(function (t) { return t.indexOf("40") === 0; });
    try { c.sid = JSON.parse(ack.slice(2)).sid || ""; } catch (e) {}
  } catch (e) {
    c.retired = true;
    try { sock.close(); } catch (e2) {}
    throw new Error("socket.io handshake: " + e.message);
  }
  c.handshaken = true;
  sock.send("42" + JSON.stringify(["message", {
    type: "auth",
    docid: info.docKey,
    documentCallbackUrl: info.callbackUrl,
    token: "fghhfgsjdgfjs",
    user: { id: info.editorUserId, username: userID, indexUser: -1 },
    editorType: 0,
    lastOtherSaveTime: -1,
    block: [],
    documentFormatSave: 65,
    view: false,
    isCloseCoAuthoring: false,
    openCmd: { c: "open", id: info.docKey, userid: info.editorUserId, format: info.fileType, url: info.docUrl,
      title: info.docTitle, lcid: 25, nobase64: true, convertToOrigin: ".pdf.xps.oxps.djvu" },
    lang: "ru",
    mode: "edit",
    permissions: info.permissions,
    IsAnonymousUser: false,
    timezoneOffset: -180,
    coEditingMode: "fast",
    jwtOpen: info.token,
    time: 1000,
    supportAuthChangesAck: true,
  }]));
  for (var i = 0; i < pending.length; i++) handleMessage(c, pending[i]);
  return c;
}

function install(c) {
  c.active = true;
  c.startedAt = Date.now();
  cur = c;
  setState("connected");
  pumpWriter();
}

function handleMessage(c, text) {
  if (text === "3") return;
  if (text.indexOf('"type":"auth"') !== -1 && text.indexOf('"result":1') !== -1) {
    learnConnID(c, text);
    c.authed = true;
    if (c.onAuthed) c.onAuthed();
    return;
  }
  if (text.indexOf("disconnectReason") !== -1) {
    console.log("[M-DOCS] server disconnectReason: " + text.slice(0, 200));
    return;
  }
  // A second editor locks the document on this one until it unlocks it; the
  // server drops a holder that does not within 30 s (4007).
  if (text.indexOf('"type":"connectState"') !== -1) {
    try { c.sock.send(UNLOCK_MSG); } catch (e) {}
    return;
  }
  if (text.indexOf('"messages"') !== -1 && c.active) {
    var packets = decodeChatFrames(text);
    for (var i = 0; i < packets.length; i++) emit(packets[i]);
  }
}

// learnConnID: the auth reply lists the participants with their connectionId;
// ours is the one whose connectionId is this socket's sid.
function learnConnID(c, text) {
  if (!c.sid) return;
  var i = text.indexOf("[");
  if (i < 0) return;
  try {
    var arr = JSON.parse(text.slice(i));
    var ps = (arr[1] && arr[1].participants) || [];
    for (var k = 0; k < ps.length; k++) {
      if (ps[k].connectionId === c.sid && ps[k].id) { ourConns[ps[k].id] = true; return; }
    }
  } catch (e) {}
}

// isOwnFrame: once our per-connection ids are known, judge by them alone (an
// account has one useridoriginal on all its connections, the far side's too).
function isOwnFrame(user, original) {
  if (user) {
    for (var k in ourConns) return !!ourConns[user];
  }
  return !!(original && ourIDs[original]);
}

function decodeChatFrames(text) {
  var i = text.indexOf("[");
  if (i < 0) return [];
  var arr;
  try { arr = JSON.parse(text.slice(i)); } catch (e) { return []; }
  var payload = arr && arr[1];
  if (!payload || payload.type !== "message" || !payload.messages) return [];
  var out = [];
  for (var k = 0; k < payload.messages.length; k++) {
    var m = payload.messages[k];
    if (!m || typeof m.message !== "string") continue;
    if (isOwnFrame(m.user, m.useridoriginal)) continue; // the echo of one of our connections
    if (m.message.indexOf(CHAT_DATA_TAG) !== 0) continue; // a human's chat, a presence entry
    try { out.push(base64.decode(m.message.slice(CHAT_DATA_TAG.length))); } catch (e) {}
  }
  return out;
}

function onClose(c, reason) {
  if (c.retired) return; // replaced by a rotation
  if (cur !== c) return;
  console.log("[M-DOCS] connection dropped after " + c.sent + " sends, age " +
    Math.round((Date.now() - c.startedAt) / 1000) + "s: " + reason);
  cur = null;
  if (running) setState("reconnecting");
  scheduleReconnect(Date.now() - c.startedAt > 15000 ? 0 : 1);
}

function scheduleReconnect(attempt) {
  if (!running || reconnecting) return;
  reconnecting = true;
  reconnectTimer = setTimeout(function () {
    reconnectTimer = null;
    reconnecting = false;
    connect(attempt + 1);
  }, reconnectBackoff(attempt + 1));
}

async function connect(attempt) {
  if (!running) return;
  try {
    var c = await dialAndAuth();
    if (!running) { c.retired = true; try { c.sock.close(); } catch (e) {} return; }
    install(c);
  } catch (e) {
    console.log("[M-DOCS] cannot open the document: " + ((e && e.message) || e) + "; retrying");
    setState("reconnecting", String((e && e.message) || e));
    scheduleReconnect(attempt);
  }
}

// rotate brings a fresh connection up, waits for the server to accept its auth
// and swaps it in; the old one is retired and closed at once (every chat is
// broadcast to both, and one deliverer means no duplicate ACKs upstream).
async function rotate(old) {
  if (rotating || !running) return;
  rotating = true;
  try {
    var c = await dialAndAuth();
    if (!c.authed) {
      await new Promise(function (resolve) {
        c.onAuthed = resolve;
        setTimeout(resolve, ROTATE_AUTH_WAIT_MS);
      });
    }
    if (!running || cur !== old) { c.retired = true; try { c.sock.close(); } catch (e) {} return; }
    old.active = false;
    old.retired = true;
    install(c);
    try { old.sock.close(); } catch (e) {}
  } catch (e) {
    console.log("[M-DOCS] rotation failed, keeping the current connection: " + ((e && e.message) || e));
  } finally {
    rotating = false;
  }
}

// ---- writer: one chat message per packet (the Session sends ~500 KB batches),
// at most one per MSG_INTERVAL_MS ----

function pumpWriter() {
  if (writerTimer || !queue.length || !cur) return;
  var wait = MSG_INTERVAL_MS - (Date.now() - lastWrite);
  if (wait > 0) {
    writerTimer = setTimeout(function () { writerTimer = null; pumpWriter(); }, wait);
    return;
  }
  var c = cur;
  var pkt = queue[0];
  try {
    c.sock.send('42["message",{"type":"message","message":"' + CHAT_DATA_TAG + base64.encode(pkt) + '"}]');
  } catch (e) {
    writerTimer = setTimeout(function () { writerTimer = null; pumpWriter(); }, 15);
    return;
  }
  queue.shift();
  lastWrite = Date.now();
  if (++c.sent === ROTATE_MSGS) rotate(c);
  if (queue.length) writerTimer = setTimeout(function () { writerTimer = null; pumpWriter(); }, MSG_INTERVAL_MS);
}

var Transport = {
  info: function () {
    return {
      name: "mailru",
      version: "1.3.0",
      cookieDomain: "https://cloud.mail.ru/",
      batchBytes: 500000,
      mtu: 0,
      reliable: false,
      ordered: false,
      params: [{ key: "url", label: "Mail.ru public document link or weblink", type: "url", required: true }],
      auth: {
        account: "mailru",
        label: "Mail.ru",
        need: "always",
        signInUrl: "https://account.mail.ru/login?page=https%3A%2F%2Fcloud.mail.ru%2Fhome%2F",
        homeUrl: "https://cloud.mail.ru/home",
        // VK ID may leave the page on its own site after signing in, and Mail.ru
        // hands the session to Cloud only when Cloud is opened.
        finishUrl: "https://cloud.mail.ru/home/",
        finishCookie: "sdcs",
        cookieUrls: ["https://cloud.mail.ru/", "https://auth.mail.ru/", "https://account.mail.ru/", "https://mail.ru/", "https://e.mail.ru/"],
        loginCookie: "Mpop",
        // all the r7/edit + CSRF calls look at (measured 2026-10-20: the other
        // 22 cookies of a sign-in can be dropped)
        cookies: ["Mpop", "sdcs"],
      },
    };
  },

  open: function (cfg) {
    weblink = normalizeWeblink((cfg.params && cfg.params.url) || cfg.url || "");
    baseUserID = pad(Math.floor(Math.random() * 1000000000), 10);
    userID = baseUserID + pad(Math.floor(Math.random() * 1000), 3);
    running = true;
    setState("connecting");
    connect(0);
    // rotate by age too: the server drops a loaded connection at ~45 s
    setInterval(function () {
      if (cur && !rotating && Date.now() - cur.startedAt >= ROTATE_MS) rotate(cur);
    }, 2000);
  },

  write: function (bytes) {
    if (!cur) throw new Error("mailru: not connected");
    if (queue.length >= QUEUE_MAX) throw new Error("mailru: write queue full");
    queue.push(bytes);
    pumpWriter();
  },

  close: function () {
    running = false;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    if (writerTimer) clearTimeout(writerTimer);
    if (cur) { cur.retired = true; try { cur.sock.close(); } catch (e) {} }
    cur = null;
  },

  // New cookies (a sign-in): reconnect with them.
  onEvent: function (kind) {
    if (kind !== "cookiesApplied" || !running) return;
    var old = cur;
    cur = null;
    if (old) { old.retired = true; try { old.sock.close(); } catch (e) {} }
    setState("reconnecting");
    if (!reconnecting) scheduleReconnect(0);
  },
};
