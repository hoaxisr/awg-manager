package bridge

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderUsesNonceAndConfiguredBatch(t *testing.T) {
	page, err := Render("proxy.example.com", "", "bootstrap-token", "https", 2*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	body := string(page.Body)
	if page.Nonce == "" || !strings.Contains(body, `script nonce="`+page.Nonce+`"`) || !strings.Contains(page.CSP, `script-src 'nonce-`+page.Nonce+`'`) {
		t.Fatal("rendered bridge does not bind its script to the response nonce")
	}
	if !strings.Contains(body, "carrierMode=\"https\"") || !strings.Contains(body, "batchLimit=2097152") {
		t.Fatal("rendered bridge omitted the configured carrier batch")
	}
	if !strings.Contains(body, "queueItemLimit=16384") || !strings.Contains(body, "setTimeout(abort,90000)") {
		t.Fatal("rendered bridge omitted its item or request bound")
	}
	if !strings.Contains(body, "fetch(relayBase+path,requestOptions)") ||
		!strings.Contains(body, "mode:'same-origin',credentials:'omit'") ||
		!strings.Contains(body, "cache:'no-store',redirect:'error',referrerPolicy:'no-referrer'") {
		t.Fatal("rendered bridge omitted its same-origin request restrictions")
	}
	if !strings.Contains(body, "t:'traffic',up:batch.total,down:0") || !strings.Contains(body, "t:'traffic',up:0,down:data.byteLength") {
		t.Fatal("rendered bridge omitted acknowledged traffic counters")
	}
	if !strings.Contains(body, "t:'tproxy-android-init',v:1,nonce:androidNonce") ||
		!strings.Contains(body, "globalThis.TelegramWebProxy") ||
		!strings.Contains(body, "androidBridge.postMessage(frame.data)") {
		t.Fatal("rendered bridge omitted the origin-scoped Android transport")
	}
	for _, unwanted := range [][]byte{
		[]byte("pause(0)"),
		[]byte(".slice(0)"),
		[]byte("__BATCH_LIMIT__"),
		[]byte("localStorage"),
		[]byte("sessionStorage"),
		[]byte("indexedDB"),
		[]byte("serviceWorker"),
		[]byte("new Worker"),
		[]byte("document.cookie"),
		[]byte("<iframe"),
		[]byte("<img"),
		[]byte("<link"),
		[]byte("<style"),
		[]byte("<audio"),
		[]byte("<video"),
		[]byte("<object"),
	} {
		if bytes.Contains(page.Body, unwanted) {
			t.Fatalf("rendered bridge retained %q", unwanted)
		}
	}
}

func TestRenderUsesHardenedExecutionPolicy(t *testing.T) {
	page, err := Render("proxy.example.com", "", "bootstrap-token", "https", 2*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"default-src":     "'none'",
		"base-uri":        "'none'",
		"child-src":       "'none'",
		"connect-src":     "'self' wss://proxy.example.com",
		"font-src":        "'none'",
		"form-action":     "'none'",
		"frame-ancestors": "http://127.0.0.1:*",
		"frame-src":       "'none'",
		"img-src":         "'none'",
		"manifest-src":    "'none'",
		"media-src":       "'none'",
		"object-src":      "'none'",
		"script-src":      "'nonce-" + page.Nonce + "'",
		"style-src":       "'none'",
		"worker-src":      "'none'",
		"sandbox":         "allow-same-origin allow-scripts",
	}
	directives := make(map[string]string)
	for _, directive := range strings.Split(page.CSP, "; ") {
		name, value, ok := strings.Cut(directive, " ")
		if !ok {
			t.Fatalf("invalid CSP directive %q", directive)
		}
		directives[name] = value
	}
	if len(directives) != len(expected) {
		t.Fatalf("CSP has %d directives, want %d: %q", len(directives), len(expected), page.CSP)
	}
	for name, value := range expected {
		if directives[name] != value {
			t.Fatalf("CSP directive %q is %q, want %q", name, directives[name], value)
		}
	}

	for _, feature := range []string{
		"autoplay=()",
		"camera=()",
		"clipboard-read=()",
		"clipboard-write=()",
		"display-capture=()",
		"geolocation=()",
		"microphone=()",
		"payment=()",
		"screen-wake-lock=()",
		"usb=()",
	} {
		if !strings.Contains(PermissionsPolicy, feature) {
			t.Fatalf("permissions policy does not deny %q", feature)
		}
	}
}

func TestRenderRejectsInvalidBatch(t *testing.T) {
	if _, err := Render("proxy.example.com", "", "bootstrap-token", "https", 0); err == nil {
		t.Fatal("accepted a nonpositive carrier batch")
	}
	if _, err := Render("proxy.example.com", "", "bootstrap-token", "invalid", 2*1024*1024); err == nil {
		t.Fatal("accepted an invalid carrier mode")
	}
}

func TestRenderIncludesSelectableCarrierImplementations(t *testing.T) {
	for _, mode := range []string{
		"https",
		"https-lanes",
		"websocket",
		"websocket-lanes",
	} {
		page, err := Render("proxy.example.com", "", "bootstrap-token", mode, 2*1024*1024)
		if err != nil {
			t.Fatal(err)
		}
		body := string(page.Body)
		if !strings.Contains(body, `carrierMode="`+mode+`"`) {
			t.Fatalf("rendered bridge omitted carrier mode %q", mode)
		}
		for _, implementation := range []string{
			"async function runLaneUp(lane)",
			"async function pollLane(lane)",
			"function openWebSocket()",
			"function runWebSocketUp()",
			"function openWebSocketLane(lane)",
			"function runWebSocketLaneUp(lane)",
			"function finishWebSocketLane(lane,notify)",
			"finishWebSocketLane(lane,!lane.localClosed&&!lane.remoteClosed)",
			"new WebSocket(target,'tproxy-lane-v1.'+sessionToken+'.'+lane.id)",
		} {
			if !strings.Contains(body, implementation) {
				t.Fatalf("rendered %s bridge omitted %q", mode, implementation)
			}
		}
	}
}

func TestRenderedBridgeSurvivesTheRestrictedProfile(t *testing.T) {
	page, err := Render("proxy.example.com", "", "bootstrap-token", "https", 2*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	// Everything the desktop client's restricted WebView profile defines away
	// (see lib_webview RestrictedScript) must stay unused by the bridge, which
	// may only rely on inline script plus fetch/WebSocket to its own origin.
	for _, forbidden := range []string{
		"WebAssembly", "RTCPeerConnection", "RTCDataChannel", "WebTransport",
		"Notification", "PaymentRequest", "PresentationRequest",
		"MediaRecorder", "SpeechRecognition", "showOpenFilePicker",
		"navigator.credentials", "navigator.mediaDevices",
		"navigator.getUserMedia", "navigator.wakeLock", "navigator.share",
		"navigator.presentation", "navigator.xr", "navigator.getGamepads",
		"navigator.storage", "navigator.locks", "sendBeacon",
		"navigator.permissions", "navigator.serviceWorker",
		"BroadcastChannel", "SharedWorker", "AudioContext",
		"caches.", "localStorage", "sessionStorage", "indexedDB",
	} {
		if strings.Contains(string(page.Body), forbidden) {
			t.Fatalf("bridge uses %q, which the restricted profile removes", forbidden)
		}
	}
}

func TestRenderedBridgeAvailabilityFixes(t *testing.T) {
	page, err := Render("proxy.example.com", "", "bootstrap-token", "https-lanes", 2*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	body := string(page.Body)
	for _, expected := range []string{
		// One request body never carries more than the relay's 4096-frame
		// batch cap, and an oversized first item is split at a frame boundary.
		"function frameBound(value,maxFrames,maxBytes)",
		"frameBound(values[count],4096,batchLimit)",
		"frames+bound.frames>4096",
		"values[0]=values[0].slice(bound.bytes)",
		// 503 honours Retry-After and uses a time budget instead of a fixed
		// attempt count, so a superseded poll or a racing uplink is retried
		// rather than treated as fatal.
		"function retryAfterMs(response)",
		"if(response.status!==503)return response",
		"const deadline=Date.now()+90000",
		"if(serviceUnavailable&&Date.now()>=deadline)throw",
		// A failing bridge tells the relay, and a session created after close
		// is deleted, so slots are not pinned for the whole reconnect grace.
		"status('failed');\n if(port)port.postMessage({t:'close'});\n close(true);",
		"if(closed){fetch(relayBase+'api/v1/session',options('DELETE',sessionToken,null,null,undefined,true)).catch(()=>{});return}",
		// Late DATA/WINDOW/CLOSE for a lane the bridge no longer knows is
		// dropped instead of killing the carrier.
		"if(!lane&&(value.type===2||value.type===3||value.type===4))return;",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("rendered bridge lacks %q", expected)
		}
	}
	if strings.Contains(body, "for(let attempt=0;attempt!==9;attempt++)") {
		t.Fatal("rendered bridge still uses the attempt-only retry loop")
	}
	if strings.Contains(body, "if(response.status<500)return response") {
		t.Fatal("rendered bridge still treats every 5xx as retryable and 503 as attempt-bounded")
	}
}

func TestRenderRejectsInvalidHostnameAndOversizedBatch(t *testing.T) {
	if _, err := Render("Proxy.Example.com", "", "bootstrap-token", "https", 2*1024*1024); err == nil {
		t.Fatal("accepted a non-canonical hostname")
	}
	if _, err := Render("proxy.example.com/x", "", "bootstrap-token", "https", 2*1024*1024); err == nil {
		t.Fatal("accepted a hostname with a path")
	}
	if _, err := Render("proxy.example.com", "", "bootstrap-token", "https", 2*1024*1024+1); err == nil {
		t.Fatal("accepted a carrier batch above the desktop loopback cap")
	}
}
