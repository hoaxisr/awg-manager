package clientsdb

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestClientsDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "clients.json")

	db, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create db: %v", err)
	}

	if err = db.Add("client-123", "Test 1"); err != nil {
		t.Fatalf("Failed to add client: %v", err)
	}

	if !db.IsAuthorized("client-123") {
		t.Errorf("Expected client-123 to be authorized")
	}

	if db.IsAuthorized("client-456") {
		t.Errorf("Expected client-456 not to be authorized")
	}

	if err = db.Remove("client-123"); err != nil {
		t.Fatalf("Failed to remove client: %v", err)
	}

	if db.IsAuthorized("client-123") {
		t.Errorf("Expected client-123 to be removed")
	}

	// Test persistence
	_ = db.Add("client-789", "Test Persistence")

	db2, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create db2: %v", err)
	}

	if !db2.IsAuthorized("client-789") {
		t.Errorf("Expected client-789 to be persisted")
	}

	// Test hot reload manually
	db2.mu.Lock()
	db2.lastModified = db2.lastModified.Add(-1 * time.Second)
	db2.mu.Unlock()

	_ = db.Add("client-999", "Hot reload test")
	db2.loadIfModified()

	if !db2.IsAuthorized("client-999") {
		t.Errorf("Expected client-999 to be loaded via hot reload")
	}
}

func TestClientIDRoundTrip(t *testing.T) {
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
	serverConn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer func() { _ = serverConn.Close() }()

	udpAddr, ok := serverConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("LocalAddr is not *net.UDPAddr")
	}
	clientConn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer func() { _ = clientConn.Close() }()

	expectedID := "client-test-uuid-123"

	// Client writes
	go func() {
		if werr := WriteClientID(clientConn, expectedID); werr != nil {
			t.Errorf("WriteClientID failed: %v", werr)
		}
	}()

	// Server reads
	readID, err := ReadClientID(serverConn)
	if err != nil {
		t.Fatalf("ReadClientID: %v", err)
	}

	if readID != expectedID {
		t.Errorf("expected %q, got %q", expectedID, readID)
	}
}
