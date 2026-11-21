// Script-transport port of transport/oneme (MAX/VK), the "webrtc" strategy:
// a GENUINE PeerConnection/DataChannel data path, completing what the native
// transport left unfinished rather than porting its current (inert)
// behavior - see oneme-iceinject.js's header for exactly what's broken in
// max_call.go (sendSDP's write is commented out, SetLocalDescription is
// commented out, so ICE never starts and the real DataChannel never opens).
//
// This is NOT a byte-for-byte port: it actually calls setLocalDescription,
// actually transmits the SDP offer/answer over the call's signaling
// WebSocket, actually exchanges real ICE candidates (the same
// "transmit-data"/candidate JSON shape max_call.go's dead sendICE already
// defines), and sends/receives application data over the real DataChannel.
// The message SHAPES match what max_call.go defines for its "real" (non-
// injection) path; only the "always send fake candidates" behavior is
// replaced with an actual WebRTC handshake. Unverified against production
// MAX infrastructure - needs a live token to confirm the server accepts a
// real SDP exchange the way this assumes.
//
// Params: same as oneme-iceinject.js (token, uid, exit).

var maxclient = require("./lib/maxclient.js");

var running = false;
var client = null;
var role = "caller";
var calleeID = "";
var maxToken = "";

var callConn = null;
var callSeq = 1;
var localID = 0;
var remoteID = 0;
var reconnectAttempt = 0;

var pc = null;
var dc = null;
var pendingCandidates = [];
var haveRemoteDesc = false;

function resetCallState() {
  callSeq = 1;
  localID = 0;
  remoteID = 0;
  callConn = null;
  haveRemoteDesc = false;
  pendingCandidates = [];
  if (pc) { try { pc.close(); } catch (e) {} }
  pc = null;
  dc = null;
}

function sendCallJSON(obj) {
  if (!callConn) return;
  obj.sequence = callSeq;
  obj.participantId = localID;
  obj.participantType = "USER";
  callSeq++;
  try {
    callConn.send(JSON.stringify(obj));
  } catch (e) {
    setState("degraded", String(e));
  }
}

function sendSDP(sdp, sdpType) {
  sendCallJSON({ command: "transmit-data", data: { sdp: { type: sdpType, sdp: sdp }, animojiVersion: 1 } });
}

function sendICE(candidate) {
  sendCallJSON({ command: "transmit-data", data: { candidate: candidate } });
}

function sendAcceptCall() {
  sendCallJSON({
    command: "accept-call",
    mediaSettings: {
      isAudioEnabled: true, isVideoEnabled: false, isScreenSharingEnabled: false,
      isFastScreenSharingEnabled: false, isAudioSharingEnabled: false, isAnimojiEnabled: false,
    },
  });
}

function createPeerConnection(convParams) {
  var turn = convParams.turn || {};
  var stun = convParams.stun || {};
  var iceServers = [];
  if (stun.urls && stun.urls.length) iceServers.push({ urls: [stun.urls[0]] });
  if (turn.urls && turn.urls.length) {
    iceServers.push({ urls: turn.urls, username: turn.username, credential: turn.credential });
  }

  pc = webrtc.newPeerConnection({ iceServers: iceServers, iceTransportPolicy: "relay" });

  pc.onicecandidate = function (c) {
    if (c) sendICE(c);
  };
  pc.onconnectionstatechange = function (s) {
    if (s === "connected") {
      setState("connected");
    } else if (s === "failed" || s === "closed") {
      setState("reconnecting", "peerconnection " + s);
    }
  };
  pc.ondatachannel = function (remoteDC) {
    dc = remoteDC;
    dc.onmessage = function (bytes) { emit(bytes); };
  };

  dc = pc.createDataChannel("x", { ordered: true, maxRetransmits: 0 });
  dc.onmessage = function (bytes) { emit(bytes); };
}

function flushPendingCandidates() {
  for (var i = 0; i < pendingCandidates.length; i++) {
    pc.addIceCandidate(pendingCandidates[i]);
  }
  pendingCandidates = [];
}

function handleRemoteSDP(sdpType, sdp) {
  pc.setRemoteDescription(sdpType, sdp).then(function () {
    haveRemoteDesc = true;
    flushPendingCandidates();
    if (sdpType === "offer") {
      return pc.createAnswer().then(function (answer) {
        return pc.setLocalDescription("answer", answer).then(function () {
          sendSDP(answer, "answer");
        });
      });
    }
  }).catch(function (e) {
    setState("degraded", "sdp: " + String(e));
  });
}

function handleRemoteCandidate(c) {
  if (haveRemoteDesc) {
    pc.addIceCandidate(c);
  } else {
    pendingCandidates.push(c);
  }
}

function findLocalID(data, wantCreator) {
  if (localID !== 0) return;
  var conv = data.conversation;
  if (!conv || !conv.participants) return;
  for (var i = 0; i < conv.participants.length; i++) {
    var p = conv.participants[i];
    var isCreator = (p.roles || []).indexOf("CREATOR") !== -1;
    if (isCreator === wantCreator) localID = p.id;
  }
}

function handleCallMessage(text_, wantCreator) {
  var data;
  try { data = JSON.parse(text_); } catch (e) { return; }

  findLocalID(data, wantCreator);
  if (typeof data.participantId === "number") remoteID = data.participantId;

  if (data.conversationParams) {
    createPeerConnection(data.conversationParams);
    if (!wantCreator) {
      // Caller creates the offer; the receiver waits for it (below).
      pc.createOffer().then(function (offer) {
        return pc.setLocalDescription("offer", offer).then(function () {
          sendSDP(offer, "offer");
        });
      }).catch(function (e) { setState("degraded", "offer: " + String(e)); });
    }
    return;
  }
  var d = data.data;
  if (!d) return;
  if (d.sdp) handleRemoteSDP(d.sdp.type, d.sdp.sdp);
  if (d.candidate) handleRemoteCandidate(d.candidate);
}

function onCallSocketMessage(msg, wantCreator) {
  var t = typeof msg === "string" ? msg : text.decode(msg);
  if (t.indexOf("accepted-call") !== -1) return;
  if (t === "ping") {
    try { callConn.send("pong"); } catch (e) {}
    return;
  }
  if (t.length < 10) return;
  handleCallMessage(t, wantCreator);
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
  reconnectAttempt++;
  if (reconnectAttempt > 10) reconnectAttempt = 10;
  setTimeout(function () { if (running) connectCallerLoop(); }, 1000);
}

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
  }).catch(function (e) {
    setState("reconnecting", String(e));
    onCallSocketClose();
  });
}

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
    setTimeout(function () { sendAcceptCall(); }, 1000);
  }).catch(function () {});
}

var Transport = {
  info: function () {
    return {
      name: "oneme-webrtc",
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
      if (role === "receiver") {
        client.setOnEvent(onIncomingCallEvent);
      } else {
        connectCallerLoop();
      }
      // Unlike oneme-iceinject.js, "connected" here is the REAL
      // PeerConnectionStateConnected signal (see createPeerConnection's
      // onconnectionstatechange) - this transport has one, so it's used.
    }).catch(function (e) {
      setState("dead", String(e));
    });
  },

  write: function (bytes) {
    if (dc) dc.send(bytes);
  },

  close: function () {
    running = false;
    if (pc) { try { pc.close(); } catch (e) {} }
    if (callConn) { try { callConn.close(); } catch (e) {} }
    if (client && client.sock) { try { client.sock.close(); } catch (e) {} }
  },
};
