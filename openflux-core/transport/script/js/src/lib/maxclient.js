// Shared MAX (VK) signaling-server client: WS connect to ws-api.oneme.ru,
// device/login RPC, keepalive, and the seq-correlated invoke() used for
// every request/response exchange. Literal port of transport/oneme/
// max_wclient.go's MaxClient. Shared by oneme-iceinject.js and
// oneme-webrtc.js - the two transports differ only in how a call's DATA
// actually moves once it's established; everything here (who you are,
// whether you're logged in, whether the signaling socket is alive) is
// identical for both.

var WS_HOST = "wss://ws-api.oneme.ru/websocket";
var RPC_VERSION = 11;
var USER_AGENT = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36";
var KEEPALIVE_MS = 30000;
var INVOKE_TIMEOUT_MS = 30000;

function genUUID() {
  var b = new Array(16);
  for (var i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256);
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  function hex(n, len) {
    var s = n.toString(16);
    while (s.length < len) s = "0" + s;
    return s;
  }
  var out = "";
  for (var j = 0; j < 16; j++) out += hex(b[j], 2);
  return out.slice(0, 8) + "-" + out.slice(8, 12) + "-" + out.slice(12, 16) + "-" + out.slice(16, 20) + "-" + out.slice(20, 32);
}

// decodeCallDetails: the incoming-call payload's "vcp" field is
// "<3-digit-size> <base64 LZ4 block>" - literal port of
// max_call_helper.go's decodeCallDetails.
function decodeCallDetails(vcp) {
  if (vcp.length < 4) throw new Error("vcp too short");
  var size = parseInt(vcp.slice(0, 3), 10);
  var decoded = base64.decode(vcp.slice(4));
  var decompressed = lz4.decompressBlock(decoded, size);
  return text.decode(decompressed);
}

// craftEndpoint: literal port of max_call_helper.go's craftEndpoint. No
// URL-encoding - matches the native fmt.Sprintf exactly.
function craftEndpoint(convID, jsonConfig) {
  var config = JSON.parse(jsonConfig);
  var baseURL = config.wse || "";
  if (baseURL.length > 4) baseURL = baseURL.slice(0, baseURL.length - 4);
  var turnUsername = config.trnu || "";
  var userID = turnUsername;
  var idx = turnUsername.lastIndexOf(":");
  if (idx >= 0) userID = turnUsername.slice(idx + 1);
  return baseURL + "/ws2?userId=" + userID + "&entityType=USER&deviceIdx=0&conversationId=" + convID +
    "&token=" + (config.tkn || "") + "&platform=WEB&appVersion=1.1&version=5&device=browser" +
    "&capabilities=2A03F&clientType=ONE_ME&tgt=accept";
}

// connect() dials ws-api.oneme.ru and returns a client object:
//   client.invoke(opcode, payload) -> Promise<parsed response packet>
//   client.setOnEvent(fn)           -> fn(packet) for anything NOT a
//                                      response to a pending invoke() -
//                                      max_wclient.go's onEvent.
//   client.deviceId
function connect() {
  return ws.open(WS_HOST, { Origin: "https://web.max.ru", "User-Agent": USER_AGENT }).then(function (sock) {
    var deviceId = genUUID();
    var seq = 0;
    var pending = {};
    var onEventCb = null;

    sock.onmessage = function (msg) {
      var packet;
      try {
        packet = JSON.parse(typeof msg === "string" ? msg : text.decode(msg));
      } catch (e) {
        return;
      }
      var waiter = pending[packet.seq];
      if (waiter) {
        delete pending[packet.seq];
        waiter.resolve(packet);
      } else if (onEventCb) {
        onEventCb(packet);
      }
    };
    sock.onclose = function () {
      for (var s in pending) {
        pending[s].reject(new Error("signaling closed"));
      }
      pending = {};
      if (onEventCb) onEventCb({ opcode: -1, __closed: true });
    };

    function invoke(opcode, payload) {
      return new Promise(function (resolve, reject) {
        seq++;
        var mySeq = seq;
        pending[mySeq] = { resolve: resolve, reject: reject };
        sock.send(JSON.stringify({ ver: RPC_VERSION, cmd: 0, seq: mySeq, opcode: opcode, payload: payload }));
        setTimeout(function () {
          if (pending[mySeq]) {
            delete pending[mySeq];
            reject(new Error("invoke timeout"));
          }
        }, INVOKE_TIMEOUT_MS);
      });
    }

    var client = {
      deviceId: deviceId,
      sock: sock,
      invoke: invoke,
      setOnEvent: function (fn) { onEventCb = fn; },
    };
    return client;
  });
}

// loginByToken: device-info RPC (opcode 6) then token login (opcode 19) -
// literal port of MaxClient.LoginByToken minus the console-printed contact
// list (this is a headless transport, not a chat client).
function loginByToken(client, token) {
  return client.invoke(6, {
    userAgent: {
      deviceType: "WEB", locale: "ru_RU", osVersion: "macOS",
      deviceName: "vkmax Go", appVersion: "25.9.15",
      screen: "956x1470 2.0x", timezone: "Asia/Vladivostok",
    },
    deviceId: client.deviceId,
  }).then(function () {
    return client.invoke(19, {
      interactive: true, token: token, chatsSync: 0,
      contactsSync: 0, presenceSync: 0, draftsSync: 0, chatsCount: 40,
    });
  }).then(function (resp) {
    var payload = resp.payload || {};
    if (payload.error) throw new Error("login failed: " + JSON.stringify(payload.error));
    return resp;
  });
}

function startKeepalive(client) {
  return setInterval(function () {
    client.invoke(1, { interactive: false }).catch(function () {});
  }, KEEPALIVE_MS);
}

module.exports = {
  genUUID: genUUID,
  decodeCallDetails: decodeCallDetails,
  craftEndpoint: craftEndpoint,
  connect: connect,
  loginByToken: loginByToken,
  startKeepalive: startKeepalive,
};
