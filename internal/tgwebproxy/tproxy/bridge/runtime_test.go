package bridge

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestWebSocketLaneCancellation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the bridge lifecycle test")
	}
	page, err := Render("proxy.example.com", "", "bootstrap", "websocket-lanes", 2*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "testdata/lane_cancellation.js")
	command.Stdin = bytes.NewReader(page.Body)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bridge lifecycle test: %v\n%s", err, output)
	}
}
