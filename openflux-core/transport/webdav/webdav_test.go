package webdav

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

// mockWebDAV simulates a minimal WebDAV store in-memory.
type mockWebDAV struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newMockWebDAV() *mockWebDAV {
	return &mockWebDAV{
		files: make(map[string][]byte),
	}
}

func (m *mockWebDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	path := r.URL.Path
	switch r.Method {
	case "MKCOL":
		w.WriteHeader(http.StatusCreated)
	case "PUT":
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		m.files[path] = data
		w.WriteHeader(http.StatusCreated)
	case "GET":
		data, ok := m.files[path]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case "DELETE":
		delete(m.files, path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func TestEncodeDecodeChunk(t *testing.T) {
	packets := [][]byte{
		[]byte("hello world"),
		[]byte("fast webdav stream"),
		bytes.Repeat([]byte{0x42}, 1500),
	}
	seq := uint64(42)

	encoded := EncodeChunk(seq, packets)
	if len(encoded) < 16 {
		t.Fatalf("encoded chunk too short: %d", len(encoded))
	}

	decodedSeq, decodedPackets, err := DecodeChunk(encoded)
	if err != nil {
		t.Fatalf("DecodeChunk failed: %v", err)
	}
	if decodedSeq != seq {
		t.Errorf("expected seq %d, got %d", seq, decodedSeq)
	}
	if len(decodedPackets) != len(packets) {
		t.Fatalf("expected %d packets, got %d", len(packets), len(decodedPackets))
	}
	for i := range packets {
		if !bytes.Equal(decodedPackets[i], packets[i]) {
			t.Errorf("packet %d mismatch: expected %d bytes, got %d bytes", i, len(packets[i]), len(decodedPackets[i]))
		}
	}
}

func TestWebDAVEndToEnd(t *testing.T) {
	mock := newMockWebDAV()
	server := httptest.NewServer(mock)
	defer server.Close()

	webdavURL := server.URL + "/openflux/"
	baseCfg := transport.DefaultConfig()

	client := NewWebDAVTransport(webdavURL, "user@test.ru", "secret", baseCfg, false)
	client.SetHTTPClient(server.Client())

	exit := NewWebDAVTransport(webdavURL, "user@test.ru", "secret", baseCfg, true)
	exit.SetHTTPClient(server.Client())

	var receivedByExit [][]byte
	var exitMu sync.Mutex
	exit.Receive(func(data []byte) {
		exitMu.Lock()
		defer exitMu.Unlock()
		receivedByExit = append(receivedByExit, append([]byte(nil), data...))
	})

	var receivedByClient [][]byte
	var clientMu sync.Mutex
	client.Receive(func(data []byte) {
		clientMu.Lock()
		defer clientMu.Unlock()
		receivedByClient = append(receivedByClient, append([]byte(nil), data...))
	})

	if err := client.Start(); err != nil {
		t.Fatalf("client start failed: %v", err)
	}
	defer client.Stop()

	if err := exit.Start(); err != nil {
		t.Fatalf("exit start failed: %v", err)
	}
	defer exit.Stop()

	// Send packets from Client to Exit
	testPkt1 := []byte("Client to Exit: packet 1")
	testPkt2 := []byte("Client to Exit: packet 2")
	if err := client.Send(testPkt1); err != nil {
		t.Fatalf("client.Send failed: %v", err)
	}
	if err := client.Send(testPkt2); err != nil {
		t.Fatalf("client.Send failed: %v", err)
	}

	// Send packet from Exit to Client
	testPktS := []byte("Exit to Client: response 1")
	if err := exit.Send(testPktS); err != nil {
		t.Fatalf("exit.Send failed: %v", err)
	}

	// Wait up to 3 seconds for packets to propagate through WebDAV
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		exitMu.Lock()
		c1 := len(receivedByExit)
		exitMu.Unlock()

		clientMu.Lock()
		c2 := len(receivedByClient)
		clientMu.Unlock()

		if c1 >= 2 && c2 >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	exitMu.Lock()
	if len(receivedByExit) < 2 {
		t.Fatalf("Exit received only %d packets, expected at least 2", len(receivedByExit))
	}
	if !strings.Contains(string(receivedByExit[0]), "packet 1") {
		t.Errorf("Unexpected first packet at exit: %q", string(receivedByExit[0]))
	}
	exitMu.Unlock()

	clientMu.Lock()
	if len(receivedByClient) < 1 {
		t.Fatalf("Client received only %d packets, expected at least 1", len(receivedByClient))
	}
	if !strings.Contains(string(receivedByClient[0]), "response 1") {
		t.Errorf("Unexpected packet at client: %q", string(receivedByClient[0]))
	}
	clientMu.Unlock()
}

func TestWebDAVIdlePauseNoDeadlock(t *testing.T) {
	mock := newMockWebDAV()
	server := httptest.NewServer(mock)
	defer server.Close()

	webdavURL := server.URL + "/openflux/"
	baseCfg := transport.DefaultConfig()

	client := NewWebDAVTransport(webdavURL, "user@test.ru", "secret", baseCfg, false)
	client.SetHTTPClient(server.Client())

	exit := NewWebDAVTransport(webdavURL, "user@test.ru", "secret", baseCfg, true)
	exit.SetHTTPClient(server.Client())

	var receivedByExit [][]byte
	var exitMu sync.Mutex
	exit.Receive(func(data []byte) {
		exitMu.Lock()
		defer exitMu.Unlock()
		receivedByExit = append(receivedByExit, append([]byte(nil), data...))
	})

	if err := client.Start(); err != nil {
		t.Fatalf("client start failed: %v", err)
	}
	defer client.Stop()

	if err := exit.Start(); err != nil {
		t.Fatalf("exit start failed: %v", err)
	}
	defer exit.Stop()

	// 1. Send first packet
	testPkt1 := []byte("Initial packet 1 before idle pause")
	if err := client.Send(testPkt1); err != nil {
		t.Fatalf("client.Send failed: %v", err)
	}

	// Wait for exit to receive packet 1
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		exitMu.Lock()
		count := len(receivedByExit)
		exitMu.Unlock()
		if count >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	exitMu.Lock()
	if len(receivedByExit) < 1 {
		exitMu.Unlock()
		t.Fatalf("Exit failed to receive packet 1 before pause")
	}
	exitMu.Unlock()

	// Verify sequence is now at 2
	seqBeforePause := exit.NextDeliverSeq()
	if seqBeforePause < 2 {
		t.Fatalf("Expected nextDeliverSeq >= 2 after first packet, got %d", seqBeforePause)
	}

	// 2. Simulate idle pause > 1.2s (1500ms) with zero traffic
	time.Sleep(1500 * time.Millisecond)

	// Verify nextDeliverSeq was NOT reset back to 1!
	seqAfterPause := exit.NextDeliverSeq()
	if seqAfterPause != seqBeforePause {
		t.Fatalf("Deadlock bug detected: nextDeliverSeq changed from %d to %d during idle pause!", seqBeforePause, seqAfterPause)
	}

	// 3. Send second packet after the idle pause
	testPkt2 := []byte("Second packet after 1.5s idle pause")
	if err := client.Send(testPkt2); err != nil {
		t.Fatalf("client.Send post-pause failed: %v", err)
	}

	// Wait for exit to receive packet 2
	deadline2 := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline2) {
		exitMu.Lock()
		count := len(receivedByExit)
		exitMu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	exitMu.Lock()
	defer exitMu.Unlock()
	if len(receivedByExit) < 2 {
		t.Fatalf("Exit failed to receive packet 2 after idle pause: got %d packets, expected 2", len(receivedByExit))
	}
	if !strings.Contains(string(receivedByExit[1]), "Second packet after 1.5s") {
		t.Errorf("Unexpected second packet content: %q", string(receivedByExit[1]))
	}
}
