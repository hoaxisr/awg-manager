package avito

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/p1neappleXpress/OpenFlux/netbind"
)

// joinName is the participant name the carrier joins a room under (letters, digits, spaces and hyphens are allowed).
const joinName = "OpenFlux"

// registerRoom does what Avito's web client does when it opens a room: it joins it over Socket.IO
// (`42["join",{"name","roomId"}]`, answered by `initialize`). A room made by the REST call alone is unknown to the
// realtime side ("room is not found in MongoDB" when its document is created); once it has been joined, once, the
// document can be created and used over ShareDB, also by those who join later and with the Socket.IO connection closed.
// The handshake counts against Avito's rate limit like any other, so it goes through the gate.
func registerRoom(ctx context.Context, roomID string) error {
	if !processGate.wait(ctx) {
		return errStopped
	}
	wsURL := strings.Replace(baseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1) + "/socket.io/?EIO=4&transport=websocket"
	header := http.Header{}
	header.Set("User-Agent", avitoUA)
	header.Set("Origin", baseURL)
	dialer := websocket.Dialer{
		NetDialContext:    netbind.DialContext,
		HandshakeTimeout:  15 * time.Second,
		EnableCompression: true,
	}
	conn, resp, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
			processGate.limited(parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()))
			return fmt.Errorf("join: HTTP %d (too many connections from this address): %w", resp.StatusCode, err)
		}
		return fmt.Errorf("join: %w", err)
	}
	processGate.ok()
	defer conn.Close()

	// read waits for a frame starting with prefix, answering Engine.IO pings on the way.
	read := func(prefix string) (string, error) {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			_ = conn.SetReadDeadline(deadline)
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return "", err
			}
			s := string(raw)
			if s == "2" {
				_ = conn.WriteMessage(websocket.TextMessage, []byte("3"))
				continue
			}
			if strings.HasPrefix(s, prefix) {
				return s, nil
			}
			if strings.HasPrefix(s, `42["join-error"`) {
				return "", fmt.Errorf("join refused: %s", truncate(raw, 120))
			}
		}
		return "", fmt.Errorf("no %q from Avito in 10 s", prefix)
	}
	if _, err := read("0{"); err != nil {
		return fmt.Errorf("join: engine.io open: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte("40")); err != nil {
		return fmt.Errorf("join: %w", err)
	}
	if _, err := read("40"); err != nil {
		return fmt.Errorf("join: socket.io connect: %w", err)
	}
	payload, _ := json.Marshal([]interface{}{"join", map[string]string{"name": joinName, "roomId": roomID}})
	if err := conn.WriteMessage(websocket.TextMessage, append([]byte("42"), payload...)); err != nil {
		return fmt.Errorf("join: %w", err)
	}
	if _, err := read(`42["initialize"`); err != nil {
		return fmt.Errorf("join: %w", err)
	}
	return nil
}

// isRoomNotFound reports whether a ShareDB error is Avito's "this room was never joined".
func isRoomNotFound(errMsg interface{}) bool {
	m, ok := errMsg.(map[string]interface{})
	if !ok {
		return false
	}
	code, _ := m["code"].(string)
	return code == "ERR_ROOM_NOT_FOUND"
}
