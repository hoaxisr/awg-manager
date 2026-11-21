// cups.online: packets ride a real Centrifugo pub/sub channel as {row, column}
// integer pairs in a "shared_editor_change_cursors" RPC (cups.online rejects a
// non-numeric column but relays a cursor array of integers verbatim, in order).
// Port of transport/cupsonline (cupsonline.go, stripe.go) as of core 0.14.5.
//
// Rooms: an exit started without a list (or whose rooms are all closed) creates
// NUM_ROOMS of them and reports the packed list with raise("roomList"); a client
// never creates rooms. Each room has its own cookie jar (http.newSession()):
// cookie names collide across rooms on one domain.
//
// Two wires, chosen by the room list, as in the native transport:
//   * striped (list v2, what every exit makes since 0.7.1): every room is a lane
//     of the core's stripe (lanes.open). A room sends one message at a time
//     (SEND_INTERVAL_MS apart, at most SEND_WINDOW unanswered), each holding
//     whole stripe pieces only, [len u16][piece]..., pulled right before it
//     leaves. The stripe numbers, acks, resends and reorders the pieces.
//   * old (list v1 / a single ?room=): frames round-robin over the rooms; a
//     batch is one byte stream cut into messages and put together per sender.

var UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36";
var BASE_ROOM_URL = "https://interview.cups.online/live-coding/";

var NUM_ROOMS = 8; // an exit creates this many rooms = stripe lanes; cfg.params.rooms overrides
var WS_HANDSHAKE_MS = 15000;
var WS_READ_TIMEOUT_MS = 90000;
var KEEPALIVE_MS = 20000;
var RECONNECT_MIN_MS = 100;
var RECONNECT_MAX_MS = 10000;
var RECONNECT_MULTIPLIER = 1.3;
var ROOM_GONE_RETRY_MIN_MS = 60000;
var ROOM_GONE_RETRY_MAX_MS = 30 * 60000;
var ROOM_DEAD_AFTER_FAILS = 5;

var BATCH_MAX_PACKETS = 256;
var BATCH_MAX_BYTES = 22000;
// 12288 bytes of data -> ~2050 cursor integers -> ~38 KB on the wire: the live
// relay delivers it whole (~200 KB/s per room); 16000 trips the server's 1009.
var MAX_MESSAGE_DATA = 12288;
var BATCH_TIMEOUT_MS = 2;
var SEND_INTERVAL_MS = 12;
// Cursor RPCs a striped room keeps unanswered before it waits: a room relays
// only so fast and drops what comes faster, so the room paces itself by the
// server's replies (3 was the knee in the live sweep).
var SEND_WINDOW = 3;
var LANE_POLL_IDLE_MS = 50;
var STREAM_RESYNC_AFTER_MS = 2000;
var MAX_PAYLOAD_BYTES = 65535;
var BYTES_PER_NUMBER = 6;

// The stripe, as stripe.go: a room loses nothing while its socket is up (no
// spare copies) and answers a busy message in up to a second or so.
var STRIPE = {
  pieceBytes: 8192 / 4 - 2 - 10,
  deadAfterMs: 3000,
  noDups: true,
  initRtoMs: 2000,
  minRtoMs: 1500,
  maxRtoMs: 8000,
  noPacing: true,
  codelTargetMs: 1500,
  codelIntervalMs: 6000,
  ackIntervalMs: 100,
  holeDeadlineMs: 10000,
  laneWindow: true,
};

var ROOM_RE = /data-room="\{&quot;uuid&quot;:\s*&quot;([0-9a-f-]{36})&quot;/;
var USER_RE = /data-user="\{&quot;uuid&quot;:\s*&quot;([0-9a-f-]{36})&quot;/;
var CONN_TOKEN_RE = /<meta[^>]+name="centrifuge-connection-token"[^>]+content="([^"]+)"/;
var CONN_URL_RE = /<meta[^>]+name="centrifuge-connection-url"[^>]+content="([^"]+)"/;
var SUB_URL_RE = /<meta[^>]+name="centrifuge-subscription-token-url"[^>]+content="([^"]+)"/;

var ROOM_CREATE_PAUSE_MS = 500;
var ROOM_CREATE_ATTEMPTS = 6;

var running = false;
var isClient = true;
var striped = false;
var rooms = [];
var rrIndex = 0;

// ---- room list (transport.PackRooms: base64url, no padding, of a JSON array
// (old wire) or {"v":2,"rooms":[...]} (striped)) ----

function b64urlDecode(s) {
  var b64 = s.replace(/-/g, "+").replace(/_/g, "/");
  var pad = (4 - (b64.length % 4)) % 4;
  for (var i = 0; i < pad; i++) b64 += "=";
  return base64.decode(b64);
}

function b64urlEncode(bytes) {
  return base64.encode(bytes).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function packRooms(ids, isStriped) {
  return b64urlEncode(text.encode(JSON.stringify(isStriped ? { v: 2, rooms: ids } : ids)));
}

function unpackRooms(s) {
  var v = JSON.parse(text.decode(b64urlDecode(s)));
  if (Array.isArray(v)) return { ids: v, striped: false };
  if (v && v.v === 2 && Array.isArray(v.rooms) && v.rooms.length) return { ids: v.rooms, striped: true };
  throw new Error("room list: unknown format");
}

function parseRooms(rawURL) {
  rawURL = (rawURL || "").trim();
  if (!rawURL) return { ids: [], striped: false };
  try {
    var p = url.parse(rawURL);
    var qs = parseQuery(p.search);
    if (qs.rooms) return unpackRooms(qs.rooms);
    if (qs.room) return { ids: [qs.room], striped: false };
  } catch (e) { /* not a URL with a query - fall through */ }
  try { return unpackRooms(rawURL); } catch (e) {}
  return { ids: [], striped: false };
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

function originOf(u) {
  var p = url.parse(u);
  return p.protocol + "//" + p.host;
}

function shortRoom(id) { return (id || "").slice(0, 8); }

// ---- one room ----

function Room(idx, id) {
  this.idx = idx;
  this.id = id;
  this.session = http.newSession();
  this.auth = null;
  this.sock = null;
  this.connected = false;
  this.dead = false;
  this.goneDelay = ROOM_GONE_RETRY_MIN_MS;
  this.reconnectDelay = RECONNECT_MIN_MS;
  this.fails = 0;
  this.rpcId = 2; // 1 and 2 are connect and subscribe
  this.pendingWaiters = [];
  // old wire
  this.batch = [];
  this.batchBytes = 0;
  this.batchTimer = null;
  this.sendChain = Promise.resolve();
  this.recvBufs = {};
  this.recvSince = {};
  // striped wire
  this.lastSend = 0;
  this.inflight = 0;
  this.dataIDs = {};
  this.next = null;
  this.pumpTimer = null;
  this.needJoin = true;
}

Room.prototype.nextID = function () { return ++this.rpcId; };

Room.prototype.joinURL = function () { return BASE_ROOM_URL + "?room=" + this.id; };

Room.prototype.authorize = function () { return this.authorizeAt(this.joinURL(), this.id); };

// authorizeAt loads a room page and collects what it takes to join the room's
// channel. expectId, when set, is the room it must turn out to be: a closed
// room can come back as a fresh one (another uuid), which counts as gone.
Room.prototype.authorizeAt = async function (roomURL, expectId) {
  var res = await this.session.fetch({
    url: roomURL,
    headers: {
      "User-Agent": UA,
      Accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
      "Accept-Language": "ru-RU,ru;q=0.9",
    },
  });
  if (res.status === 404 || res.status === 410) throw new RoomGoneError("GET room status " + res.status);
  if (res.status !== 200) throw new Error("GET room status " + res.status);

  var html = res.body;
  var roomUUID = firstMatch(ROOM_RE, html);
  var userUUID = firstMatch(USER_RE, html);
  var connToken = firstMatch(CONN_TOKEN_RE, html);
  var connURL = firstMatch(CONN_URL_RE, html);
  var subURL = firstMatch(SUB_URL_RE, html);
  if (!roomUUID || !userUUID) throw new RoomGoneError("room/user uuid missing");
  if (expectId && roomUUID !== expectId) throw new RoomGoneError("вместо неё выдана новая " + shortRoom(roomUUID));
  if (!connToken || !connURL || !subURL) throw new Error("centrifuge meta missing");

  var csrfToken = this.session.cookies.get(roomURL)["csrftoken"] || "";
  if (!csrfToken) throw new Error("csrftoken missing");
  var channel = "$shared_editor:room-" + roomUUID;

  var subRes = await this.session.fetch({
    url: subURL,
    method: "POST",
    headers: {
      "User-Agent": UA,
      "Content-Type": "application/json",
      "X-CSRFToken": csrfToken,
      Origin: originOf(roomURL),
      Referer: roomURL,
    },
    body: JSON.stringify({ channel: channel }),
  });
  if (subRes.status !== 200) throw new Error("sub token status " + subRes.status);
  var subToken = (JSON.parse(subRes.body) || {}).token;
  if (!subToken) throw new Error("empty sub token");

  var jar = this.session.cookies.get(connURL);
  var cookieHeader = Object.keys(jar).map(function (k) { return k + "=" + jar[k]; }).join("; ");
  this.auth = {
    roomUUID: roomUUID, userUUID: userUUID, connToken: connToken,
    connURL: connURL, subToken: subToken, channel: channel, cookieHeader: cookieHeader,
  };
};

function RoomGoneError(msg) {
  this.message = msg;
  this.isRoomGone = true;
}
RoomGoneError.prototype = Object.create(Error.prototype);

function firstMatch(re, s) {
  var m = re.exec(s);
  return m ? m[1] : "";
}

// ---- replies (a frame may pack several JSON lines; "{}" is a ping) ----

function isPing(line) { return line === "{}"; }

Room.prototype.waitForReply = function (id, timeoutMs) {
  var room = this;
  return new Promise(function (resolve, reject) {
    var waiter = { done: false, id: id };
    var timer = setTimeout(function () {
      if (waiter.done) return;
      waiter.done = true;
      var i = room.pendingWaiters.indexOf(waiter);
      if (i >= 0) room.pendingWaiters.splice(i, 1);
      reject(new Error("timeout waiting for reply " + id));
    }, timeoutMs);
    waiter.settle = function (reply) {
      if (waiter.done) return;
      waiter.done = true;
      clearTimeout(timer);
      if (reply.error) {
        var err = new Error("refused: " + reply.error.code + " " + reply.error.message);
        err.isRefused = true; // Centrifugo turned the token down - only a fresh join fixes that
        reject(err);
      } else {
        resolve(reply);
      }
    };
    room.pendingWaiters.push(waiter);
  });
};

Room.prototype.writeRaw = function (data) {
  if (!this.sock) throw new Error("ws not connected");
  try {
    this.sock.send(data);
  } catch (e) {
    // A failed write leaves the socket unusable: close it so the room
    // reconnects instead of sitting on a dead channel until the read timeout.
    try { this.sock.close(); } catch (e2) {}
    throw e;
  }
};

Room.prototype.writeJSON = function (v) { this.writeRaw(JSON.stringify(v)); };

// ---- connect, handshake, serve ----

Room.prototype.wsURL = function () {
  var u = this.auth.connURL.replace("https://", "wss://").replace("http://", "ws://");
  return u.replace(/\/+$/, "") + "/websocket";
};

Room.prototype.connectAndServe = async function () {
  var room = this;
  var sock = await ws.open(this.wsURL(), {
    Origin: originOf(this.auth.connURL),
    "User-Agent": UA,
    Cookie: this.auth.cookieHeader,
  }, { readTimeoutMs: WS_READ_TIMEOUT_MS });

  this.sock = sock;
  // Fresh socket, fresh stream; the replies to RPCs sent on the old socket
  // will never come, so the window starts clean.
  this.recvBufs = {};
  this.recvSince = {};
  this.inflight = 0;
  this.dataIDs = {};

  sock.onmessage = function (raw) { room.onFrame(raw); };
  var closed = new Promise(function (resolve) { sock.onclose = function (reason) { resolve(reason); }; });
  // Centrifugo hanging up mid-handshake is how it can turn the tokens down
  // without a word: it counts as a refusal (only a fresh join fixes it).
  var hungUp = closed.then(function (reason) {
    var err = new Error("refused: connection closed during the handshake: " + reason);
    err.isRefused = true;
    throw err;
  });
  hungUp.catch(function () {});

  this.writeJSON({ id: 1, connect: { token: this.auth.connToken, name: "js" } });
  await Promise.race([this.waitForReply(1, WS_HANDSHAKE_MS), hungUp]);
  this.writeJSON({ id: 2, subscribe: { channel: this.auth.channel, token: this.auth.subToken } });
  await Promise.race([this.waitForReply(2, WS_HANDSHAKE_MS), hungUp]);

  this.connected = true;
  this.markAlive();
  updateState();
  if (striped) {
    lanes.state(this.idx, true);
    this.pump();
  }

  var ka = setInterval(function () {
    try {
      room.writeJSON({
        rpc: { method: "shared_editor_ping", data: { room: room.auth.roomUUID, user: room.auth.userUUID } },
        id: room.nextID(),
      });
    } catch (e) {}
  }, KEEPALIVE_MS);

  await closed;
  clearInterval(ka);
  this.down();
};

Room.prototype.down = function () {
  var was = this.connected;
  this.connected = false;
  this.sock = null;
  if (this.pumpTimer) { clearTimeout(this.pumpTimer); this.pumpTimer = null; }
  if (striped) lanes.state(this.idx, false);
  if (was) updateState();
};

Room.prototype.onFrame = function (raw) {
  var lines = raw.split("\n");
  for (var i = 0; i < lines.length; i++) {
    var line = lines[i].trim();
    if (line) this.handleLine(line);
  }
};

Room.prototype.handleLine = function (line) {
  if (isPing(line)) {
    try { this.writeRaw("{}"); } catch (e) {}
    return;
  }
  var obj;
  try { obj = JSON.parse(line); } catch (e) { return; }
  if (!obj) return;
  if (typeof obj.id === "number") {
    // A reply to a cursor RPC (ok or error) frees a slot in the window.
    if (this.dataIDs[obj.id]) {
      delete this.dataIDs[obj.id];
      if (--this.inflight < 0) this.inflight = 0;
      this.wake();
    }
    for (var i = 0; i < this.pendingWaiters.length; i++) {
      if (this.pendingWaiters[i].id === obj.id) {
        var w = this.pendingWaiters.splice(i, 1)[0];
        w.settle(obj);
        return;
      }
    }
  }
  if (obj.error) return; // a message the server would not take: the stripe resends its pieces
  var data = obj.push && obj.push.pub && obj.push.pub.data;
  if (!data || data.type !== "cursors_update") return;
  var payload = data.payload;
  if (!payload || payload.user_uuid === this.auth.userUUID) return; // our own echo
  var cursors = payload.cursors;
  if (!cursors || cursors.length === 0) return;

  var nums = new Array(cursors.length * 2);
  for (var j = 0; j < cursors.length; j++) {
    var c = cursors[j];
    if (!c || typeof c.row !== "number" || c.row < 0) return;
    nums[2 * j] = c.row;
    nums[2 * j + 1] = typeof c.column === "number" && c.column >= 0 ? c.column : 0;
  }
  var decoded = numbersToBytes(nums);
  if (decoded.length < 2) return;
  var dataLen = (decoded[0] << 8) | decoded[1];
  if (2 + dataLen > decoded.length) return;
  var chunk = decoded.subarray(2, 2 + dataLen);
  if (striped) {
    this.readWhole(chunk);
  } else {
    this.readStream(payload.user_uuid || "", chunk);
  }
};

// readWhole: on the striped wire a message holds whole pieces only.
Room.prototype.readWhole = function (chunk) {
  var off = 0;
  while (chunk.length - off >= 2) {
    var ln = (chunk[off] << 8) | chunk[off + 1];
    if (ln === 0 || off + 2 + ln > chunk.length) break;
    lanes.deliver(this.idx, chunk.slice(off + 2, off + 2 + ln).buffer);
    off += 2 + ln;
  }
};

// readStream: the old wire - a sender's batch is one stream cut into messages.
Room.prototype.readStream = function (sender, chunk) {
  var now = Date.now();
  var prev = this.recvBufs[sender];
  var pending = !!(prev && prev.length);
  // A part whose rest never came (a message of the batch was lost) would hold
  // the stream out of step: a batch comes as a burst, so give it up.
  if (pending && now - this.recvSince[sender] > STREAM_RESYNC_AFTER_MS) {
    prev = null;
    pending = false;
  }
  var merged;
  if (prev && prev.length) {
    merged = new Uint8Array(prev.length + chunk.length);
    merged.set(prev, 0);
    merged.set(chunk, prev.length);
  } else {
    merged = chunk;
  }
  var off = 0, count = 0;
  while (merged.length - off >= 2) {
    var ln = (merged[off] << 8) | merged[off + 1];
    if (ln === 0) { off = merged.length; break; } // out of step: drop
    if (merged.length - off < 2 + ln) break;
    emit(merged.slice(off + 2, off + 2 + ln).buffer);
    off += 2 + ln;
    count++;
  }
  var rest = merged.slice(off);
  if (rest.length === 0 || rest.length > MAX_PAYLOAD_BYTES + MAX_MESSAGE_DATA) {
    delete this.recvBufs[sender];
    delete this.recvSince[sender];
  } else {
    this.recvBufs[sender] = rest;
    if (!pending || count > 0) this.recvSince[sender] = now;
  }
};

function numbersToBytes(nums) {
  var out = new Uint8Array(nums.length * BYTES_PER_NUMBER);
  for (var i = 0; i < nums.length; i++) {
    var v = nums[i];
    // 48 bits: the high 16 and the low 32 are exact in a double.
    var hi = Math.floor(v / 4294967296), lo = v - hi * 4294967296;
    var o = i * BYTES_PER_NUMBER;
    out[o] = (hi >>> 8) & 255;
    out[o + 1] = hi & 255;
    out[o + 2] = (lo >>> 24) & 255;
    out[o + 3] = (lo >>> 16) & 255;
    out[o + 4] = (lo >>> 8) & 255;
    out[o + 5] = lo & 255;
  }
  return out;
}

// cursorsFromBytes packs a blob into {row, column} pairs of 6-byte integers,
// the last one zero-padded.
function cursorsFromBytes(blob) {
  var n = Math.ceil(blob.length / BYTES_PER_NUMBER);
  var nums = new Array(n);
  for (var i = 0; i < n; i++) {
    var o = i * BYTES_PER_NUMBER, b = [0, 0, 0, 0, 0, 0];
    for (var j = 0; j < BYTES_PER_NUMBER && o + j < blob.length; j++) b[j] = blob[o + j];
    nums[i] = ((b[0] << 8) | b[1]) * 4294967296 + (((b[2] << 24) >>> 0) + (b[3] << 16) + (b[4] << 8) + b[5]);
  }
  var cursors = new Array(Math.ceil(n / 2));
  for (i = 0; i < n; i += 2) cursors[i / 2] = { row: nums[i], column: i + 1 < n ? nums[i + 1] : 0 };
  return cursors;
}

// sendChunk sends one message: a 2-byte length of the chunk, then the chunk,
// packed into cursor integers.
Room.prototype.sendChunk = function (chunk, id) {
  var payload = new Uint8Array(2 + chunk.length);
  payload[0] = (chunk.length >> 8) & 255;
  payload[1] = chunk.length & 255;
  payload.set(chunk, 2);
  this.writeJSON({
    rpc: {
      method: "shared_editor_change_cursors",
      data: { cursors: cursorsFromBytes(payload), ranges: [], room: this.auth.roomUUID, user: this.auth.userUUID },
    },
    id: id,
  });
};

// ---- striped send: pump is pullLoop of cupsonline.go ----

// schedulePump: idle timers (nothing to send, window full) give way to a wake
// (a reply, the stripe); the pacing timer between two messages does not.
Room.prototype.schedulePump = function (ms, idle) {
  if (this.pumpTimer || !running) return;
  var room = this;
  this.pumpIdle = !!idle;
  this.pumpTimer = setTimeout(function () { room.pumpTimer = null; room.pump(); }, ms);
};

Room.prototype.wake = function () {
  if (this.pumpTimer && this.pumpIdle) {
    clearTimeout(this.pumpTimer);
    this.pumpTimer = null;
  }
  this.pump();
};

Room.prototype.pump = function () {
  if (!striped || !this.connected || this.pumpTimer) return;
  if (SEND_WINDOW > 0 && this.inflight >= SEND_WINDOW) {
    this.schedulePump(LANE_POLL_IDLE_MS, true); // a reply wakes it sooner
    return;
  }
  var wait = SEND_INTERVAL_MS - (Date.now() - this.lastSend);
  if (wait > 0) {
    this.schedulePump(wait, false);
    return;
  }
  var parts = [], size = 0;
  var add = function (p) {
    if (size + 2 + p.length > MAX_MESSAGE_DATA) return false;
    parts.push(p);
    size += 2 + p.length;
    return true;
  };
  if (this.next && add(this.next)) this.next = null;
  while (!this.next) {
    var pieces = lanes.pull(this.idx, 1);
    if (pieces.length === 0) break;
    for (var i = 0; i < pieces.length; i++) {
      var p = new Uint8Array(pieces[i]);
      if (this.next || !add(p)) this.next = p;
    }
  }
  if (parts.length === 0) {
    this.next = null; // bigger than a message: the stripe never cuts one so big
    this.schedulePump(LANE_POLL_IDLE_MS, true);
    return;
  }
  var msg = new Uint8Array(size), off = 0;
  for (i = 0; i < parts.length; i++) {
    msg[off] = (parts[i].length >> 8) & 255;
    msg[off + 1] = parts[i].length & 255;
    msg.set(parts[i], off + 2);
    off += 2 + parts[i].length;
  }
  this.lastSend = Date.now();
  var id = this.nextID();
  this.dataIDs[id] = true;
  this.inflight++;
  try {
    this.sendChunk(msg, id);
  } catch (e) {
    delete this.dataIDs[id];
    this.inflight--;
    return; // the socket is closing; the stripe resends
  }
  this.schedulePump(SEND_INTERVAL_MS, false);
};

// ---- old-wire send ----

Room.prototype.sendBatch = async function (batch) {
  var total = 0, i;
  for (i = 0; i < batch.length; i++) total += 2 + batch[i].length;
  var blob = new Uint8Array(total), off = 0;
  for (i = 0; i < batch.length; i++) {
    blob[off] = (batch[i].length >> 8) & 255;
    blob[off + 1] = batch[i].length & 255;
    blob.set(batch[i], off + 2);
    off += 2 + batch[i].length;
  }
  for (off = 0; off < blob.length; off += MAX_MESSAGE_DATA) {
    var wait = SEND_INTERVAL_MS - (Date.now() - this.lastSend);
    if (wait > 0) await sleep(wait);
    this.lastSend = Date.now();
    this.sendChunk(blob.subarray(off, Math.min(off + MAX_MESSAGE_DATA, blob.length)), this.nextID());
  }
};

Room.prototype.flushBatch = function () {
  if (this.batchTimer) { clearTimeout(this.batchTimer); this.batchTimer = null; }
  if (this.batch.length === 0) return;
  var batch = this.batch, room = this;
  this.batch = [];
  this.batchBytes = 0;
  this.sendChain = this.sendChain.then(function () {
    // Gone down meanwhile: by now the batch is stale, dropped as native.
    if (!room.connected) return;
    return room.sendBatch(batch);
  }).catch(function () {});
};

Room.prototype.queuePacket = function (bytes) {
  var pkt = new Uint8Array(bytes);
  if (this.batch.length > 0 && this.batchBytes + 2 + pkt.length > BATCH_MAX_BYTES) this.flushBatch();
  this.batch.push(pkt);
  this.batchBytes += 2 + pkt.length;
  if (this.batch.length >= BATCH_MAX_PACKETS || this.batchBytes >= BATCH_MAX_BYTES) {
    this.flushBatch();
  } else if (this.batch.length === 1) {
    var room = this;
    this.batchTimer = setTimeout(function () { room.flushBatch(); }, BATCH_TIMEOUT_MS);
  }
};

// ---- liveness, run loop ----

Room.prototype.markDead = function (why) {
  if (this.dead) return;
  this.dead = true;
  console.log("[ERROR] Cups: комната " + shortRoom(this.id) + " закрылась: " + why);
};

Room.prototype.markAlive = function () {
  this.goneDelay = ROOM_GONE_RETRY_MIN_MS;
  if (this.dead) console.log("[CUPS] комната " + shortRoom(this.id) + " снова доступна");
  this.dead = false;
};

Room.prototype.retryDelay = function () {
  if (!this.dead) return this.reconnectDelay;
  var d = this.goneDelay;
  this.goneDelay = Math.min(2 * d, ROOM_GONE_RETRY_MAX_MS);
  return d;
};

Room.prototype.backoff = function () {
  this.reconnectDelay = Math.min(this.reconnectDelay * RECONNECT_MULTIPLIER, RECONNECT_MAX_MS);
};

Room.prototype.run = async function () {
  while (running) {
    if (this.needJoin || this.dead) {
      try {
        await this.authorize();
        this.needJoin = false;
      } catch (e) {
        this.session = http.newSession(); // next time from a clean session
        if (e && e.isRoomGone) this.markDead(e.message);
        await sleep(this.retryDelay());
        this.backoff();
        continue;
      }
    }
    var wasReady = false, refused = false;
    try {
      await this.connectAndServe();
      wasReady = true;
    } catch (e) {
      wasReady = this.connected;
      refused = !!(e && e.isRefused);
      this.down();
    }
    if (!running) return;
    if (wasReady) {
      this.reconnectDelay = RECONNECT_MIN_MS;
      this.fails = 0;
      this.needJoin = false;
    } else {
      this.fails++;
      if (this.fails === ROOM_DEAD_AFTER_FAILS) this.markDead("канал не поднимается " + this.fails + " раз подряд");
      // Tokens can expire: re-join when refused, and every other failure.
      this.needJoin = refused || this.fails % 2 === 0;
    }
    await sleep(this.retryDelay());
    this.backoff();
  }
};

function sleep(ms) {
  return new Promise(function (resolve) { setTimeout(resolve, ms); });
}

// ---- transport ----

function updateState() {
  for (var i = 0; i < rooms.length; i++) {
    if (rooms[i].connected) { setState("connected"); return; }
  }
  if (running) setState("reconnecting");
}

function pickRoom() {
  var n = rooms.length;
  for (var i = 0; i < n; i++) {
    var r = rooms[(rrIndex + i) % n];
    if (r.connected) { rrIndex = (rrIndex + i + 1) % n; return r; }
  }
  return null;
}

// createRooms makes n fresh rooms, each loaded in a session of its own,
// retried with a wait that grows faster on a rate limit (403/429).
async function createRooms(n) {
  var out = [], delay = ROOM_CREATE_PAUSE_MS, lastErr = null;
  for (var i = 0; i < n; i++) {
    var room = null;
    for (var attempt = 0; attempt < ROOM_CREATE_ATTEMPTS; attempt++) {
      if (!running) return out;
      var candidate = new Room(out.length, "");
      try {
        await candidate.authorizeAt(BASE_ROOM_URL, "");
        room = candidate;
        break;
      } catch (e) {
        lastErr = e;
        var msg = String((e && e.message) || e);
        await sleep(msg.indexOf("403") !== -1 || msg.indexOf("429") !== -1
          ? Math.min(delay * Math.pow(2, attempt), 30000)
          : delay * (attempt + 1));
      }
    }
    if (!room) continue;
    room.id = room.auth.roomUUID;
    room.needJoin = false;
    out.push(room);
    if (i < n - 1) await sleep(delay);
  }
  if (out.length === 0) throw new Error("could not create any room: " + ((lastErr && lastErr.message) || lastErr));
  return out;
}

function begin() {
  if (striped) lanes.open(rooms.length, STRIPE);
  console.log("[CUPS] " + rooms.length + " rooms, striped=" + striped);
  for (var j = 0; j < rooms.length; j++) rooms[j].run();
}

// startRooms is enterRooms of cupsonline.go: join the listed rooms all at once;
// one that is not reachable keeps being retried. With none joined: a client
// fails; an exit whose rooms are only unreachable fails too (new rooms for a
// network hiccup would cost the phone its link); an exit whose rooms are all
// closed - or that had none - creates new ones.
async function startRooms(ids) {
  try {
    if (ids.length > 0) {
      rooms = ids.map(function (id, i) { return new Room(i, id); });
      var results = await Promise.all(rooms.map(function (r) {
        return r.authorize().then(
          function () { r.needJoin = false; return { ok: true }; },
          function (e) {
            if (e && e.isRoomGone) r.markDead(e.message);
            return { ok: false, gone: !!(e && e.isRoomGone) };
          });
      }));
      if (!running) return;
      var joined = results.filter(function (x) { return x.ok; }).length;
      var allGone = results.every(function (x) { return x.ok || x.gone; });
      console.log("[CUPS] Cups: зашли в " + joined + " из " + ids.length + " комнат");
      if (joined > 0) {
        raise("roomList", { rooms: packRooms(ids, striped) });
        begin();
        return;
      }
      if (isClient) {
        setState("dead", allGone ? "cupsonline: all rooms are gone" : "cupsonline: no rooms joined");
        return;
      }
      if (!allGone) {
        setState("dead", "cupsonline: saved rooms unreachable");
        return;
      }
      striped = true;
    } else if (isClient) {
      setState("dead", "cupsonline: no room ids in url");
      return;
    }
    rooms = await createRooms(NUM_ROOMS);
    if (!running) return;
    raise("roomList", { rooms: packRooms(rooms.map(function (r) { return r.id; }), striped) });
    begin();
  } catch (e) {
    setState("dead", String(e));
  }
}

var Transport = {
  info: function () {
    return {
      name: "cupsonline",
      version: "1.3.0",
      mtu: 0,
      reliable: false,
      ordered: false,
      params: [
        { key: "url", label: "Packed room list (?rooms=... or ?room=...); empty on an exit creates new rooms", type: "text", required: false },
        { key: "rooms", label: "Rooms an exit creates (= parallel lanes; more = faster, 8 default)", type: "number", required: false, scope: "settings", default: "8" },
      ],
    };
  },

  open: function (cfg) {
    var p = cfg.params || {};
    var list = parseRooms(p.url || cfg.url || "");
    // The core tells every script its role (params.exit); a client never
    // creates rooms of its own.
    isClient = !(p.exit === true || p.exit === "true");
    var nr = parseInt(p.rooms, 10);
    if (nr >= 1 && nr <= 48) NUM_ROOMS = nr;
    striped = list.ids.length ? list.striped : true; // an exit's new rooms are striped
    running = true;
    setState("connecting");
    startRooms(list.ids);
  },

  write: function (bytes) {
    if (bytes.byteLength > MAX_PAYLOAD_BYTES) {
      throw new Error("cupsonline: packet " + bytes.byteLength + " bytes over the " + MAX_PAYLOAD_BYTES + " limit");
    }
    var room = pickRoom();
    if (!room) throw new Error("cupsonline: no room connected");
    room.queuePacket(bytes);
  },

  laneWake: function (i) {
    if (rooms[i]) rooms[i].wake();
  },

  // The stripe's own frames: a puller lane never gets them, but if it does
  // they go as one message of their own.
  laneSend: function (i, data) {
    var r = rooms[i];
    if (!r || !r.connected) return;
    var p = new Uint8Array(data), msg = new Uint8Array(2 + p.length);
    msg[0] = (p.length >> 8) & 255;
    msg[1] = p.length & 255;
    msg.set(p, 2);
    r.sendChunk(msg, r.nextID());
  },

  close: function () {
    running = false;
    for (var i = 0; i < rooms.length; i++) {
      var r = rooms[i];
      if (r.batchTimer) clearTimeout(r.batchTimer);
      if (r.pumpTimer) clearTimeout(r.pumpTimer);
      if (r.sock) { try { r.sock.close(); } catch (e) {} }
    }
    rooms = [];
  },
};
