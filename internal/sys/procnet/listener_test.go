package procnet

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIPToProcHex(t *testing.T) {
	if hex := IPToProcHex("127.0.0.1"); hex != "0100007F" {
		t.Errorf("expected 0100007F for 127.0.0.1, got %s", hex)
	}
	if hex := IPToProcHex("0.0.0.0"); hex != "00000000" {
		t.Errorf("expected 00000000 for 0.0.0.0, got %s", hex)
	}
	if hex := IPToProcHex("::1"); hex != "00000000000000000000000001000000" {
		t.Errorf("expected 00000000000000000000000001000000 for ::1, got %s", hex)
	}
	if hex := IPToProcHex("invalid"); hex != "" {
		t.Errorf("expected empty string for invalid IP, got %s", hex)
	}
}

func TestIsIPv6Target(t *testing.T) {
	if IsIPv6Target("127.0.0.1") {
		t.Errorf("expected 127.0.0.1 to NOT be IPv6")
	}
	if IsIPv6Target("0.0.0.0") {
		t.Errorf("expected 0.0.0.0 to NOT be IPv6")
	}
	if IsIPv6Target("") {
		t.Errorf("expected empty to NOT be IPv6")
	}
	if !IsIPv6Target("::1") {
		t.Errorf("expected ::1 to be IPv6")
	}
	if !IsIPv6Target("::") {
		t.Errorf("expected :: to be IPv6")
	}
	if !IsIPv6Target("2001:db8::1") {
		t.Errorf("expected 2001:db8::1 to be IPv6")
	}
}

func TestFindListeningProcess_FailClosed(t *testing.T) {
	t.Run("MissingProcTableReturnsError", func(t *testing.T) {
		emptyProc := t.TempDir()
		_, err := FindListeningProcess(emptyProc, "127.0.0.1", 443)
		if err == nil || !strings.Contains(err.Error(), "ipv4 socket table") {
			t.Errorf("expected error for missing proc table, got: %v", err)
		}
	})

	t.Run("DirectoryAsSocketTableReturnsError", func(t *testing.T) {
		procDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(procDir, "net", "tcp"), 0755); err != nil {
			t.Fatal(err)
		}
		_, err := FindListeningProcess(procDir, "127.0.0.1", 443)
		if err == nil || !strings.Contains(err.Error(), "is a directory") {
			t.Errorf("expected is a directory error, got: %v", err)
		}
	})

	t.Run("IPv6MissingTCP6ReturnsError", func(t *testing.T) {
		procDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := FindListeningProcess(procDir, "::1", 443)
		if err == nil || !strings.Contains(err.Error(), "ipv6 socket table") {
			t.Errorf("expected ipv6 socket table unavailable error, got: %v", err)
		}
	})

	t.Run("SocketPresentWithoutPID", func(t *testing.T) {
		procDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
			t.Fatal(err)
		}
		tcpContent := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
			"   0: 0100007F:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 88888 1 0000000000000000 100 0 0 10 0\n"
		if err := os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(tcpContent), 0644); err != nil {
			t.Fatal(err)
		}
		lookup, err := FindListeningProcess(procDir, "127.0.0.1", 443)
		if err == nil || !errors.Is(err, ErrUnresolvedSocketOwner) {
			t.Fatalf("expected ErrUnresolvedSocketOwner when socket owner cannot be proven, got: %v", err)
		}
		if !lookup.SocketFound {
			t.Errorf("expected SocketFound to be true")
		}
		if lookup.SocketInode != "88888" {
			t.Errorf("expected inode 88888, got %s", lookup.SocketInode)
		}
		if lookup.PID != 0 {
			t.Errorf("expected PID 0 when unmapped, got %d", lookup.PID)
		}
	})

	t.Run("CleanAbsence", func(t *testing.T) {
		procDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
		lookup, err := FindListeningProcess(procDir, "127.0.0.1", 443)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if lookup.SocketFound {
			t.Errorf("expected SocketFound=false on empty table")
		}
		if lookup.PID != 0 {
			t.Errorf("expected PID 0")
		}
	})
}

func TestFindListeningProcessNetwork_UDP(t *testing.T) {
	procDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
		t.Fatal(err)
	}
	// UDP bound state is "07"
	udpContent := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 55555 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(procDir, "net", "udp"), []byte(udpContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create fake PID 42 owning inode 55555
	fdDir := filepath.Join(procDir, "42", "fd")
	if err := os.MkdirAll(fdDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[55555]", filepath.Join(fdDir, "3")); err != nil {
		t.Fatal(err)
	}

	lookup, err := FindListeningProcessNetwork(procDir, "udp", "0.0.0.0", 53)
	if err != nil {
		t.Fatalf("FindListeningProcessNetwork error: %v", err)
	}
	if !lookup.SocketFound {
		t.Errorf("expected SocketFound=true for UDP listener")
	}
	if lookup.SocketInode != "55555" {
		t.Errorf("expected inode 55555, got %s", lookup.SocketInode)
	}
	if lookup.PID != 42 {
		t.Errorf("expected PID 42, got %d", lookup.PID)
	}
}

func TestFindListeningProcessNetwork_ProtocolSeparation(t *testing.T) {
	procDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
		t.Fatal(err)
	}
	tcpContent := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 11111 1 0000000000000000 100 0 0 10 0\n"
	udpContent := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 22222 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(tcpContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "net", "udp"), []byte(udpContent), 0644); err != nil {
		t.Fatal(err)
	}

	// PID 101 owns TCP 11111, PID 102 owns UDP 22222
	for pid, inode := range map[string]string{"101": "11111", "102": "22222"} {
		fdDir := filepath.Join(procDir, pid, "fd")
		if err := os.MkdirAll(fdDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("socket:["+inode+"]", filepath.Join(fdDir, "3")); err != nil {
			t.Fatal(err)
		}
	}

	// Check TCP
	tcpLookup, err := FindListeningProcessNetwork(procDir, "tcp", "127.0.0.1", 53)
	if err != nil {
		t.Fatalf("TCP lookup failed: %v", err)
	}
	if tcpLookup.PID != 101 {
		t.Errorf("expected TCP PID 101, got %d", tcpLookup.PID)
	}

	// Check UDP
	udpLookup, err := FindListeningProcessNetwork(procDir, "udp", "127.0.0.1", 53)
	if err != nil {
		t.Fatalf("UDP lookup failed: %v", err)
	}
	if udpLookup.PID != 102 {
		t.Errorf("expected UDP PID 102, got %d", udpLookup.PID)
	}
}

func TestFindListeningProcessNetwork_Ambiguity(t *testing.T) {
	procDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
		t.Fatal(err)
	}
	tcpContent := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 33333 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(tcpContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Two distinct PIDs claim the same socket inode (e.g. fork without exec)
	for _, pid := range []string{"201", "202"} {
		fdDir := filepath.Join(procDir, pid, "fd")
		if err := os.MkdirAll(fdDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("socket:[33333]", filepath.Join(fdDir, "4")); err != nil {
			t.Fatal(err)
		}
	}

	_, err := FindListeningProcessNetwork(procDir, "tcp", "127.0.0.1", 80)
	if err == nil || !strings.Contains(err.Error(), "ambiguous socket ownership") {
		t.Errorf("expected ambiguous socket ownership error, got: %v", err)
	}
}

func TestFindListeningProcessNetwork_UnsupportedProtocol(t *testing.T) {
	_, err := FindListeningProcessNetwork(t.TempDir(), "sctp", "127.0.0.1", 80)
	if err == nil || !strings.Contains(err.Error(), "unsupported network protocol") {
		t.Errorf("expected unsupported network protocol error, got: %v", err)
	}
}

// Mandatory regressions for Gate C P1-2:
// - matching fd directory unreadable -> error;
// - specific IPv4 target plus only IPv6 wildcard -> not accepted as IPv4 owner;
// - malformed target row -> observation error;
// - IPv4-only system without optional IPv6 table remains supported.

func TestFindListeningProcess_UnreadableFDDirectoryFailsClosed(t *testing.T) {
	procDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
		t.Fatal(err)
	}
	tcpContent := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 44444 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(tcpContent), 0644); err != nil {
		t.Fatal(err)
	}

	// PID 301 directory exists, but fd is a file (unreadable as a directory)
	pidDir := filepath.Join(procDir, "301")
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "fd"), []byte("not-a-directory"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := FindListeningProcess(procDir, "127.0.0.1", 80)
	if err == nil || !errors.Is(err, ErrUnresolvedSocketOwner) {
		t.Fatalf("expected ErrUnresolvedSocketOwner when fd directory is unreadable, got: %v", err)
	}
}

func TestFindListeningProcess_IPv4TargetIPv6WildcardNotAccepted(t *testing.T) {
	procDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
		t.Fatal(err)
	}
	// Empty IPv4 tcp table
	if err := os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// IPv6 tcp6 table contains only IPv6 wildcard :::8080 (all zeroes)
	tcp6Content := "  sl  local_address                         rem_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 66666 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(procDir, "net", "tcp6"), []byte(tcp6Content), 0644); err != nil {
		t.Fatal(err)
	}

	// Fake PID 401 owning 66666
	fdDir := filepath.Join(procDir, "401", "fd")
	if err := os.MkdirAll(fdDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[66666]", filepath.Join(fdDir, "3")); err != nil {
		t.Fatal(err)
	}

	// Specific IPv4 target 127.0.0.1 must NOT accept IPv6 wildcard
	lookup, err := FindListeningProcess(procDir, "127.0.0.1", 8080)
	if err != nil {
		t.Fatalf("unexpected lookup error: %v", err)
	}
	if lookup.SocketFound {
		t.Fatalf("VIOLATION: specific IPv4 target 127.0.0.1 improperly matched IPv6 wildcard listener!")
	}
	if lookup.PID != 0 {
		t.Errorf("expected PID 0, got %d", lookup.PID)
	}
}

func TestFindListeningProcess_MalformedTargetRowFailsClosed(t *testing.T) {
	procDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
		t.Fatal(err)
	}
	// Row contains target port 9090 (2382 in hex) but has fewer than 10 fields
	malformedContent := "  sl  local_address rem_address   st\n" +
		"   0: 0100007F:2382 00000000:0000 0A\n"
	if err := os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(malformedContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := FindListeningProcess(procDir, "127.0.0.1", 9090)
	if err == nil || !errors.Is(err, ErrMalformedSocketTable) {
		t.Fatalf("expected ErrMalformedSocketTable on malformed target row, got: %v", err)
	}
}

func TestFindListeningProcess_IPv4OnlySystemSupported(t *testing.T) {
	procDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
		t.Fatal(err)
	}
	// tcp exists, but tcp6 does NOT exist on the system
	tcpContent := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:04D2 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 77777 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(tcpContent), 0644); err != nil {
		t.Fatal(err)
	}

	fdDir := filepath.Join(procDir, "501", "fd")
	if err := os.MkdirAll(fdDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[77777]", filepath.Join(fdDir, "3")); err != nil {
		t.Fatal(err)
	}

	// Probing port 1234 (04D2 hex) on IPv4-only system must succeed without failing on missing tcp6
	lookup, err := FindListeningProcess(procDir, "127.0.0.1", 1234)
	if err != nil {
		t.Fatalf("IPv4-only system lookup failed: %v", err)
	}
	if !lookup.SocketFound || lookup.PID != 501 {
		t.Errorf("expected SocketFound=true and PID=501 on IPv4-only system, got %+v", lookup)
	}
}
