// Script-transport port of transport/oneme (MAX/VK), the "iceinject"
// strategy: the ONE data path the native transport actually moves bytes
// over today. transport/oneme/max_call.go creates a real pion PeerConnection
// per call (SDP offer/answer, DataChannel, the works) but max_call.go's
// sendSDP() never actually writes to the socket (the WriteMessage call is
// commented out) and both SetLocalDescription calls are commented out too -
// so ICE gathering never starts, no STUN/TURN traffic is ever sent, and the
// real DataChannel never opens. The transport still works because
// useICEInjection=true smuggles the payload as a base64 blob inside a fake
// "ICE candidate" JSON message sent over the CALL's plain signaling
// WebSocket (injectICE/msgHandler's candidate branch) - a pipe the
// PeerConnection ritual never actually touches.
//
// This port keeps exactly that: MaxClient login/keepalive/invoke, the call
// signaling handshake (opcode 78 to start a call / opcode 137 for an
// incoming one), accept-call, and the candidate-smuggling send/receive. It
// does NOT recreate the inert PeerConnection dance, since it has zero
// observable effect on the wire in the native version either - see
// oneme-webrtc.js for the OTHER strategy, which makes that dance real.
//
// Params (cfg.params): token (MAX web token), uid (callee id, caller role
// only), exit ("true"/true = receiver/listener role, matching native's
// isExit).

var maxclient = require("./lib/maxclient.js");

var running = false;
var client = null;
var role = "caller"; // "caller" | "receiver"
var calleeID = "";
var maxToken = "";

// ---- per-call state (one CallHandler equivalent) ----
var callConn = null;
var callSeq = 1;
var localID = 0;
var remoteID = 0;
var callAccepted = false;
var acceptSent = false;
var reconnectAttempt = 0;
var reconnectTimer = null;

function resetCallState() {
  callAccepted = false;
  acceptSent = false;
  callSeq = 1;
  localID = 0;
  remoteID = 0;
  callConn = null;
}

// injectICE: literal port of CallHandler.injectICE - the actual Send() path.
function injectICE(bytes) {
  if (!callConn) return;
  var b64 = base64.encode(bytes);
  var msg = JSON.stringify({
    command: "transmit-data",
    sequence: callSeq,
    participantId: localID,
    data: { candidate: { candidate: b64 } },
    participantType: "USER",
  });
  callSeq++;
  try {
    callConn.send(msg);
  } catch (e) {
    setState("degraded", String(e));
  }
}

function sendAcceptCall() {
  if (acceptSent || !callConn) return;
  acceptSent = true;
  var msg = JSON.stringify({
    command: "accept-call",
    sequence: callSeq,
    mediaSettings: {
      isAudioEnabled: true, isVideoEnabled: false, isScreenSharingEnabled: false,
      isFastScreenSharingEnabled: false, isAudioSharingEnabled: false, isAnimojiEnabled: false,
    },
  });
  callSeq++;
  callConn.send(msg);
}

// findLocalID: literal port of the participants-scan in startOutgoingCall/
// startIncomingListener's msgHandler - caller is the non-creator, receiver
// is the creator.
function findLocalID(data, wantCreator) {
  if (localID !== 0) return;
  var conv = data.conversation;
  if (!conv || !conv.participants) return;
  for (var i = 0; i < conv.participants.length; i++) {
    var p = conv.participants[i];
    var isCreator = (p.roles || []).indexOf("CREATOR") !== -1;
    if (isCreator === wantCreator) {
      localID = p.id;
    }
  }
}

function handleCallMessage(text_, wantCreator) {
  var data;
  try { data = JSON.parse(text_); } catch (e) { return; }

  findLocalID(data, wantCreator);
  if (typeof data.participantId === "number") remoteID = data.participantId;

  if (data.conversationParams) {
    // The real PeerConnection/SDP ritual would start here in native - it's
    // inert (see file header), so there's nothing to do: the candidate
    // pipe below is already open and usable.
    return;
  }
  var d = data.data;
  if (!d) return;
  if (d.candidate && d.candidate.candidate) {
    var decoded = base64.decode(d.candidate.candidate);
    emit(decoded);
  }
}

function onCallSocketClose() {
  callConn = null;
  if (!running) return;
  // Only the caller re-places its call. A receiver has nobody to call: it waits
  // for the next incoming call event (opcode 137), as the native one does -
  // letting it fall into connectCallerLoop would start placing calls to an
  // empty callee id every second.
  if (role !== "caller") return;
  setState("reconnecting");
  scheduleReconnect();
}

function scheduleReconnect() {
  reconnectAttempt++;
  if (reconnectAttempt > 10) reconnectAttempt = 10; // native's cap, never reset
  var delay = 1000;
  setTimeout(function () {
    if (running) connectCallerLoop();
  }, delay);
}

// ---- caller role: transport/oneme's startOutgoingCall ----

function connectCallerLoop() {
  if (!running) return;
  resetCallState();

  client.invoke(78, {
    conversationId: maxclient.genUUID(),
    calleeIds: [calleeID],
    internalParams: JSON.stringify({
      deviceId: client.deviceId, sdkVersion: "2.8.9", clientAppKey: "CNHIJPLGDIHBABABA",
      platform: "WEB", protocolVersion: 5, domainId: "", capabilities: "2A03F",
    }),
    isVideo: false,
  }).then(function (resp) {
    var payload = resp.payload || {};
    var params = JSON.parse(payload.internalCallerParams || "{}");
    var endpoint = params.endpoint + "&platform=WEB&appVersion=1.1&version=5&device=browser&capabilities=2A03F&clientType=ONE_ME&tgt=start";
    return ws.open(endpoint, {}, { readTimeoutMs: 0 });
  }).then(function (sock) {
    callConn = sock;
    sock.onmessage = function (msg) { onCallSocketMessage(msg, false); };
    sock.onclose = onCallSocketClose;
    raise("callConnected", { role: "caller" });
  }).catch(function (e) {
    setState("reconnecting", String(e));
    scheduleReconnect();
  });
}

function onCallSocketMessage(msg, wantCreator) {
  var t = typeof msg === "string" ? msg : text.decode(msg);
  if (t.indexOf("accepted-call") !== -1) {
    callAccepted = true;
    return;
  }
  if (t === "ping") {
    try { callConn.send("pong"); } catch (e) {}
    return;
  }
  if (t.length < 10) return;
  handleCallMessage(t, wantCreator);
}

// ---- receiver role: transport/oneme's startIncomingListener ----

function onIncomingCallEvent(packet) {
  if (packet.opcode !== 137) return;
  var payload = packet.payload || {};
  var callDetails;
  try {
    callDetails = maxclient.decodeCallDetails(payload.vcp || "");
  } catch (e) {
    return;
  }
  var endpoint = maxclient.craftEndpoint(payload.conversationId, callDetails);
  resetCallState();
  ws.open(endpoint, {}, { readTimeoutMs: 0 }).then(function (sock) {
    callConn = sock;
    sock.onmessage = function (msg) { onCallSocketMessage(msg, true); };
    sock.onclose = onCallSocketClose;
    raise("callConnected", { role: "receiver" });
    setTimeout(function () {
      if (!acceptSent) sendAcceptCall();
    }, 1000);
  }).catch(function (e) {
    utilsDebug("incoming call connect failed: " + String(e));
  });
}

function utilsDebug(msg) {
  // No console-spam policy beyond what other scripts already emit; raise()
  // makes this observable to the host if it wants it, same as everywhere.
  raise("debug", { msg: msg });
}

var Transport = {
  info: function () {
    return {
      name: "oneme-iceinject",
      version: "1.1.0",
      mtu: 0,
      reliable: false,
      ordered: false,
      params: [
        { key: "token", label: "MAX web token", type: "secret", required: true },
        { key: "uid", label: "Callee id (caller role only)", type: "text", required: false },
        { key: "exit", label: "Receiver role (true/false)", type: "text", required: false },
      ],
    };
  },

  open: function (cfg) {
    var p = (cfg && cfg.params) || {};
    maxToken = p.token || "";
    calleeID = p.uid || "";
    role = (p.exit === true || p.exit === "true") ? "receiver" : "caller";
    running = true;

    maxclient.connect().then(function (c) {
      client = c;
      return maxclient.loginByToken(client, maxToken);
    }).then(function () {
      maxclient.startKeepalive(client);
      // setState("connected") mirrors native OneMeTransport.IsConnected(),
      // which always returns true unconditionally - not gated on any real
      // call being up. Faithful to the current (arguably too optimistic)
      // native behavior, not a "fixed" version of it.
      setState("connected");
      if (role === "receiver") {
        client.setOnEvent(onIncomingCallEvent);
      } else {
        connectCallerLoop();
      }
    }).catch(function (e) {
      setState("dead", String(e));
    });
  },

  write: function (bytes) {
    injectICE(bytes);
  },

  close: function () {
    running = false;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    if (callConn) { try { callConn.close(); } catch (e) {} }
    if (client && client.sock) { try { client.sock.close(); } catch (e) {} }
  },
};
