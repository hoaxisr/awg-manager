package avito

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeSocketIO is Avito's Socket.IO endpoint as the web client meets it: open, namespace connect, join, initialize.
func fakeSocketIO(t *testing.T, joined chan<- string, refuse bool) *httptest.Server {
	up := websocket.Upgrader{EnableCompression: true}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/socket.io/") {
			http.NotFound(w, r)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.WriteMessage(websocket.TextMessage, []byte(`0{"sid":"a","upgrades":[],"pingInterval":25000}`))
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			s := string(raw)
			switch {
			case s == "40":
				c.WriteMessage(websocket.TextMessage, []byte(`40{"sid":"b"}`))
			case strings.HasPrefix(s, `42["join"`):
				joined <- s
				if refuse {
					c.WriteMessage(websocket.TextMessage, []byte(`42["join-error",{"code":"ERR_INVALID_PARTICIPANT_NAME"}]`))
				} else {
					c.WriteMessage(websocket.TextMessage, []byte(`42["initialize",{"members":[]}]`))
				}
			}
		}
	}))
}

func TestRegisterRoomJoinsOverSocketIO(t *testing.T) {
	joined := make(chan string, 1)
	srv := fakeSocketIO(t, joined, false)
	defer srv.Close()
	old := baseURL
	baseURL = srv.URL
	defer func() { baseURL = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := registerRoom(ctx, "room-1"); err != nil {
		t.Fatalf("registerRoom: %v", err)
	}
	select {
	case s := <-joined:
		if !strings.Contains(s, `"roomId":"room-1"`) || !strings.Contains(s, `"name":"`+joinName+`"`) {
			t.Fatalf("the join frame is %s", s)
		}
	default:
		t.Fatal("the server never saw a join")
	}
}

func TestRegisterRoomReportsARefusedJoin(t *testing.T) {
	joined := make(chan string, 1)
	srv := fakeSocketIO(t, joined, true)
	defer srv.Close()
	old := baseURL
	baseURL = srv.URL
	defer func() { baseURL = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := registerRoom(ctx, "room-1"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a join-error must be an error, got %v", err)
	}
}

func TestIsRoomNotFound(t *testing.T) {
	if !isRoomNotFound(map[string]interface{}{"code": "ERR_ROOM_NOT_FOUND", "message": "room is not found in MongoDB"}) {
		t.Fatal("Avito's unknown-room error was not recognised")
	}
	if isRoomNotFound(map[string]interface{}{"code": "ERR_ROOM_CLOSED_FOR_WRITES"}) || isRoomNotFound("ERR_ROOM_NOT_FOUND") || isRoomNotFound(nil) {
		t.Fatal("something else was taken for the unknown-room error")
	}
}
