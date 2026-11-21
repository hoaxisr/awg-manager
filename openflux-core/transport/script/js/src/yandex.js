// Script-transport port of transport/yandex/yandex.go +
// transport/yandex/captcha.go. Same protocol, same constants/timings, same
// two-tier captcha handling (PoW "showcaptchafast" solved in-band, second-
// tier SmartCaptcha "showcaptcha" surfaced as an OOB event for the app to
// solve out of band, exactly like the native transport's ErrCaptchaRequired
// + SetErrorNotifier). The captcha solver itself lives in ./lib/captcha.js,
// shared with vyandex.js/boards.js - see that file for the actual logic.
// This is the SOURCE for the bundled, signed transport/script/js/yandex.js -
// build it with scriptbundle, then re-sign the output.
//
// Kept in step with the native transport (parity is tested byte for byte in
// transport/script/parity_test.go): the saveChanges "editor activity" stream
// and a failed keep-alive closing the socket so the transport reconnects.

var captcha = require("./lib/captcha.js");
var yandexauth = require("./lib/yandexauth.js");

var UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0";
var CURSOR_RE = /"cursor":"[^;]+;([^"]+)"/;
var CLIENT_CONFIG_RE = /<script[^>]*id="client-config"[^>]*>(.*?)<\/script>/;
var KEEPALIVE_MS = 10000;
var MAX_RECONNECT_ATTEMPTS = 999999;
var CAPTCHA_WAIT_MS = 30000; // scheduleReconnectNoCaptcha's fixed delay
var ACTIVITY_MIN_MS = 500; // editorActivityLoop: a random delay in [0.5 s, 5 s]
var ACTIVITY_MAX_MS = 5000;

// saveChanges pieces, byte-identical to yandex.go. The op format is the
// editor's closed binary; only the participant's id, the cursor positions
// and the sequence counter vary. modelMetaOp is tied to the editor's version.
var MODEL_META_OP =
  "76;AgAAADEA//8BAJ+fl7jkAwIALQEAAAMAAAAAAAAAAAAAAAAAAAAAAAAA9v///xoAAAAyADAAMgA2AC4AMgAuADEALgAyADIANgA4AA==";
var ENTITY_CURSOR = "35"; // cursor stream id (as in the original)
var CURSOR_STREAM = "14"; // cursor stream id in CursorInfo

var docURL = "";
var running = false;
var sock = null;
var baseUserID = "";
var userID = null;
var userCounter = 0;
var connectedAt = null;
var keepAliveTimer = null;
var activityTimer = null;
var docInfo = null; // the live session's fetchDocInfo result (editor user id, isExcel)
var connectGen = 0; // bumped by every connectToDoc: a stale attempt closes what it opened
var reconnectTimer = null; // at most one reconnect pending
// captchaWaitPending/captchaWaitAttempt/captchaWaitToken together guard
// scheduleReconnectNoCaptcha's wait: a "cookiesApplied" event checks
// captchaWaitPending, and if a wait IS outstanding right now, bumps
// captchaWaitToken so the pending setTimeout becomes a no-op and
// reconnects immediately with the same attempt+1 the timer would have
// used - the same effect as the native transport's unbuffered
// cookiesApplied channel racing its 30s timer.
var captchaWaitPending = false;
var captchaWaitAttempt = 0;
var captchaWaitToken = 0;

function pad(n, width) {
  var s = String(n);
  while (s.length < width) s = "0" + s;
  return s;
}

function randUserID() {
  return pad(Math.floor(Math.random() * 1000000000), 10);
}

// ---- saveChanges (editor activity) ----

function utf16le(str) {
  var out = [];
  for (var i = 0; i < str.length; i++) out.push(str.charCodeAt(i) & 0xff, 0x00);
  return out;
}

function bytesToB64(arr) {
  return base64.encode(new Uint8Array(arr));
}

// cursorOp: one cursor op "<entity>;<base64>" - the same shape as the
// original dump (string length prefix, userShortID in UTF-16LE, fixed
// fields, a changing sequence byte).
function cursorOp(userShortID, seq) {
  var payload = [0x06, 0x00, 0x00, 0x00].concat(utf16le(userShortID), [
    0x01, 0x00, 0x1c, 0x00,
    0x01, 0x00, 0x00, 0x00,
    seq & 0xff, 0x00, 0x00, 0x00,
    0x01, 0x00, 0x00, 0x00,
    0x00, 0x00, 0x00, 0x00,
    0x00, 0x00, 0x00, 0x00,
  ]);
  return ENTITY_CURSOR + ";" + bytesToB64(payload);
}

function cursorInfo(userShortID, seq) {
  var payload = [0x06, 0x00, 0x00, 0x00].concat(utf16le(userShortID), [0x03, 0x00, 0x00, 0x00, seq & 0xff, 0x00]);
  return CURSOR_STREAM + ";" + bytesToB64(payload);
}

// buildSaveChanges: the saveChanges message for one participant and
// iteration. Same as BuildSaveChanges in yandex.go; object keys are written
// in sorted order because Go marshals maps that way and the parity test
// compares the bytes.
function buildSaveChanges(userId, isExcel, seq) {
  var short = userId.length > 10 ? userId.slice(0, 10) : userId;
  var changes = [MODEL_META_OP, cursorOp(short, seq), cursorOp(short, seq + 1), cursorOp(short, seq + 2)];
  var excelInfo = {
    CursorInfo: cursorInfo(short, seq),
    UserId: userId,
    UserShortId: short,
  };
  var msg = {
    changes: JSON.stringify(changes),
    deleteIndex: null,
    endSaveChanges: true,
    excelAdditionalInfo: JSON.stringify(excelInfo),
    isCoAuthoring: true,
    isExcel: !!isExcel,
    releaseLocks: true,
    startSaveChanges: true,
    type: "saveChanges",
    unlock: false,
  };
  return "42" + JSON.stringify(["message", msg]);
}

// isExcelFile: a spreadsheet, by editor_config.document.fileType.
function isExcelFile(fileType) {
  var t = String(fileType || "").replace(/^\./, "").toLowerCase();
  return t === "xlsx" || t === "xls" || t === "xlsm" || t === "csv";
}

function reconnectBackoff(n) {
  if (n < 1) n = 1;
  var shift = n - 1;
  if (shift > 4) shift = 4;
  var d = 1500 * Math.pow(2, shift);
  if (d > 30000) d = 30000;
  d += Math.floor(Math.random() * (d / 2 + 1));
  return d;
}

function scheduleReconnect(attempt) {
  var next = attempt + 1;
  if (!running || next >= MAX_RECONNECT_ATTEMPTS) return;
  if (reconnectTimer) return; // one is already pending: a second would open a duplicate session
  var d = reconnectBackoff(next);
  reconnectTimer = setTimeout(function () {
    reconnectTimer = null;
    if (!running) return;
    connectToDoc(next);
  }, d);
}

// scheduleReconnectNoCaptcha: fetchDocInfo hit a SmartCaptcha or login wall
// that can't be solved in-band. Wait for either the fixed delay or an
// early "cookiesApplied" wake, then retry - identical shape to
// yandex.go's scheduleReconnectNoCaptcha.
function scheduleReconnectNoCaptcha(attempt) {
  if (!running) return;
  setState("reconnecting");
  captchaWaitPending = true;
  captchaWaitAttempt = attempt;
  var myToken = ++captchaWaitToken;
  setTimeout(function () {
    if (captchaWaitToken !== myToken || !running) return;
    captchaWaitPending = false;
    connectToDoc(attempt + 1);
  }, CAPTCHA_WAIT_MS);
}

// wakeCaptchaWait fires when cookies arrive while a wait is outstanding:
// invalidate the pending timer and reconnect right away, continuing from
// the SAME attempt+1 the timer would have used.
function wakeCaptchaWait() {
  captchaWaitToken++;
  captchaWaitPending = false;
  var next = captchaWaitAttempt + 1;
  if (running) connectToDoc(next);
}

// ---- fetchDocInfo ----

async function fetchDocInfo(url, uid) {
  var currentURL = url;
  var res = null;
  for (var hop = 0; hop < 10; hop++) {
    res = await http.fetch({ url: currentURL, headers: { "User-Agent": UA }, redirect: "manual" });
    if (res.status === 200) break;

    if (res.status >= 300 && res.status < 400) {
      var loc = res.headers["Location"];
      if (!loc) throw new Error("redirect without Location from " + currentURL);

      if (loc.indexOf("showcaptcha") !== -1 && loc.indexOf("showcaptchafast") === -1) {
        raise("captchaRequired", { url: url, location: loc, reason: "smartcaptcha" });
        throw new SentinelError("captchaRequired");
      }
      if (loc.indexOf("showcaptchafast") !== -1) {
        await captcha.solveCaptcha(UA, currentURL);
        currentURL = url; // retry the original URL, like the native transport
        continue;
      }
      if (loc.indexOf("passport.yandex") !== -1) {
        // A login wall reaches the app through the same notifier as a captcha, told apart
        // by reason (the native transport does the same): the app opens a sign-in page.
        raise("captchaRequired", { url: url, reason: "login" });
        throw new SentinelError("loginRequired");
      }
      currentURL = loc;
      continue;
    }

    throw new Error("unexpected status " + res.status + " at " + currentURL);
  }
  if (!res || res.status !== 200) {
    throw new Error("final status " + (res && res.status));
  }

  var html = res.body;
  var cookies = cookieJar.get();
  var cookieStr = Object.keys(cookies)
    .map(function (k) { return k + "=" + cookies[k]; })
    .join("; ");

  var m = CLIENT_CONFIG_RE.exec(html);
  if (!m) {
    var hint = "no client-config script";
    if (html.indexOf("passport") !== -1 || html.toLowerCase().indexOf("login") !== -1) {
      hint = "looks like a login page (doc not public?)";
    }
    throw new Error("config not found: " + hint);
  }

  var config = JSON.parse(m[1]);
  var officeAction = config.officeActionData;
  if (!officeAction) throw new Error("officeActionData missing - will reconnect");

  var editorConfig = officeAction.editor_config;
  if (!editorConfig) throw new Error("editor_config nil - will reconnect");

  var balancerURL = officeAction.balancer_url;
  if (!balancerURL) throw new Error("officeActionData.balancer_url missing - will reconnect");
  var host = balancerURL.indexOf("https://") === 0 ? balancerURL.slice(8) : balancerURL;

  var document_ = editorConfig.document;
  if (!document_) throw new Error("editor_config.document missing - will reconnect");

  var token = editorConfig.token;
  if (!token) throw new Error("editor_config.token missing - will reconnect");

  var docKey = document_.key;
  if (!docKey) throw new Error("editor_config.document.key missing - will reconnect");

  // editor_config.user.id is the participant's real id in the document: the
  // server signs presence/cursors under it, so saveChanges must carry it.
  var editorUser = editorConfig.user;
  var editorUserId = editorUser && typeof editorUser.id === "string" ? editorUser.id : "";

  return {
    cookieStr: cookieStr,
    token: token,
    docId: docKey,
    editorUserId: editorUserId,
    isExcel: isExcelFile(document_.fileType),
    origin: balancerURL,
    host: host,
    wsURL: "wss://" + host + "/2024.1.1-375/doc/" + docKey + "/c/?EIO=4&transport=websocket",
    permissions: document_.permissions || {},
    openCmd: {
      c: "open",
      id: docKey,
      userid: uid,
      format: document_.fileType,
      url: document_.url,
      title: document_.title,
      lcid: 25,
    },
  };
}

function SentinelError(kind) {
  this.message = kind;
  this.kind = kind;
}
SentinelError.prototype = Object.create(Error.prototype);

// ---- keepalive / message handling / write (identical shape to mailru.js) ----

// Keepalives of one size at one interval are a signature that survives TLS:
// random padding after the marker (peers look for the marker anywhere, #81).
var KA_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
function keepAliveMarker() {
  var n = 4 + Math.floor(Math.random() * 48), pad = "";
  for (var i = 0; i < n; i++) pad += KA_ALPHABET[Math.floor(Math.random() * KA_ALPHABET.length)];
  return "---KA---" + pad;
}

// ... and at 0.5-1.5x the interval, not on a metronome.
function startKeepAlive() {
  if (keepAliveTimer) clearTimeout(keepAliveTimer);
  keepAliveTimer = setTimeout(function () {
    keepAliveTimer = null;
    if (!sock) return;
    try {
      sock.send('42["message",{"type":"cursor","cursor":"18;' + keepAliveMarker() + '"}]');
    } catch (e) {
      // Same as the native keep-alive: a write that fails means a dead
      // connection - close it so the reconnect (via onclose) happens.
      setState("reconnecting", String(e));
      try { sock.close(); } catch (e2) {}
      return;
    }
    startKeepAlive();
  }, KEEPALIVE_MS / 2 + Math.floor(Math.random() * KEEPALIVE_MS));
}

// editorActivityLoop: sends a saveChanges message at a random interval in
// [0.5 s, 5 s] so the stream looks like a live person editing. Skipped while
// there is no session (mid-reconnect), exactly like the native loop.
var activitySeq = 0;
function startEditorActivity() {
  if (activityTimer) clearTimeout(activityTimer);
  activitySeq = 0;
  var nextDelay = function () {
    return ACTIVITY_MIN_MS + Math.floor(Math.random() * (ACTIVITY_MAX_MS - ACTIVITY_MIN_MS + 1));
  };
  var tick = function () {
    activityTimer = null;
    if (!running) return;
    if (sock && docInfo) {
      activitySeq++;
      try {
        sock.send(buildSaveChanges(docInfo.editorUserId || userID, docInfo.isExcel, activitySeq));
      } catch (e) {
        // the next tick tries again; a dead socket is the keep-alive's to notice
      }
    }
    activityTimer = setTimeout(tick, nextDelay());
  };
  activityTimer = setTimeout(tick, nextDelay());
}

// extractBase64: yandex's collab server carries the payload either in a
// cursor field (like mailru) or, for spreadsheet saves, in a top-level
// "excelAdditionalInfo" string - literal port of extractBase64String.
function extractBase64(text) {
  if (text.indexOf("saveChanges") !== -1) {
    var marker = '"excelAdditionalInfo":"';
    var left = text.indexOf(marker);
    if (left === -1) return "";
    left += marker.length;
    var right = text.indexOf('"', left);
    if (right === -1) return "";
    return text.slice(left, right);
  }
  var m = CURSOR_RE.exec(text);
  return (m && m[1]) || "";
}

function handleMessage(msg) {
  if (msg.indexOf("---KA---") !== -1) return;
  if (msg === "2") {
    if (sock) { try { sock.send("3"); } catch (e) {} }
    return;
  }
  if (msg === "3") return;

  if (msg.indexOf("saveChanges") !== -1 || msg.indexOf("cursor") !== -1) {
    var b64 = extractBase64(msg);
    if (!b64) return;
    emit(base64.decode(b64));
  }
}

function onSocketClose(attempt) {
  sock = null;
  docInfo = null;
  var next = attempt;
  if (connectedAt !== null && Date.now() - connectedAt > 15000) {
    next = -1;
  }
  if (running) setState("reconnecting");
  scheduleReconnect(next);
}

function connectToDoc(attempt) {
  if (!running) return;
  var myGen = ++connectGen;

  (async function () {
    try {
      if (userID === null) {
        userID = baseUserID + pad(userCounter++ % 1000, 3);
      }

      var info;
      try {
        info = await fetchDocInfo(docURL, userID);
      } catch (e) {
        if (e instanceof SentinelError) {
          scheduleReconnectNoCaptcha(attempt);
          return;
        }
        throw e;
      }

      if (myGen !== connectGen || !running) return; // superseded while fetching
      var newSock = await ws.open(info.wsURL, {
        "User-Agent": "Mozilla/5.0",
        Origin: info.origin,
        Cookie: info.cookieStr,
        Host: info.host,
      });
      if (myGen !== connectGen || !running) {
        try { newSock.close(); } catch (e) {}
        return;
      }
      sock = newSock;
      docInfo = info;
      sock.onmessage = handleMessage;
      sock.onclose = function () {
        if (sock !== newSock) return; // a socket we already replaced or dropped on purpose
        onSocketClose(attempt);
      };

      sock.send('40{"token":"' + info.token + '"}');
      var authData = {
        type: "auth", docid: info.docId, token: "fghhfgsjdgfjs",
        user: { id: userID }, editorType: 0,
        lastOtherSaveTime: -1, permissions: info.permissions,
        openCmd: info.openCmd, coEditingMode: "fast", jwtOpen: info.token,
      };
      sock.send("42" + JSON.stringify(["message", authData]));

      connectedAt = Date.now();
      setState("connected");
    } catch (e) {
      setState("reconnecting", String(e));
      scheduleReconnect(attempt);
    }
  })();
}

var Transport = {
  info: function () {
    return {
      name: "yandex",
      version: "1.2.0",
      cookieDomain: "https://yandex.ru/",
      auth: yandexauth.auth("always"),
      scopeCookiesToParentDomain: true,
      mtu: 0,
      reliable: false,
      ordered: false,
      params: [
        { key: "url", label: "Yandex.Docs document URL", type: "url", required: true },
      ],
    };
  },

  open: function (cfg) {
    docURL = (cfg.params && cfg.params.url) || cfg.url || "";
    baseUserID = randUserID();
    running = true;
    startKeepAlive();
    startEditorActivity();
    connectToDoc(0);
  },

  write: function (bytes) {
    if (!sock) throw new Error("yandex: not connected");
    // The cursor's numeric prefix must be the packet's real length: Yandex Disk
    // does not relay a cursor whose declared length does not match (the fixed
    // "18" the first port sent dropped every packet; native fix 4a17023, #165).
    sock.send('42["message",{"type":"cursor","cursor":"' + bytes.byteLength + ';' + base64.encode(bytes) + '"}]');
  },

  close: function () {
    running = false;
    connectGen++;
    captchaWaitToken++;
    captchaWaitPending = false;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    reconnectTimer = null;
    if (keepAliveTimer) clearTimeout(keepAliveTimer);
    if (activityTimer) clearTimeout(activityTimer);
    activityTimer = null;
    if (sock) { try { sock.close(); } catch (e) {} }
    sock = null;
  },

  // "cookiesApplied": generic ApplyCookies (transport.go) already wrote
  // the cookies into the shared jar with parent-domain scoping (see
  // scopeCookiesToParentDomain above) before delivering this - all that's
  // left is to wake a pending captcha wait, or force a normal reconnect if
  // one wasn't in progress.
  onEvent: function (kind) {
    if (kind === "cookiesApplied") {
      var wasWaiting = captchaWaitPending;
      // The native ApplyCookies drops the session, so the next connect is a
      // fresh participant: a new user id.
      userID = null;
      docInfo = null;
      connectGen++; // drop an attempt in flight: the one below starts from the new cookies
      var old = sock;
      sock = null; // its onclose is ignored (sock !== newSock): one reconnect, below
      if (old) { try { old.close(); } catch (e) {} }
      setState("reconnecting");
      if (wasWaiting) {
        wakeCaptchaWait();
      } else {
        scheduleReconnect(0);
      }
    }
  },
};
