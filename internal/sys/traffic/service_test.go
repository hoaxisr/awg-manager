package traffic

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
)

type mockPolicyService struct {
	accesspolicy.Service
	devices []accesspolicy.Device
}

func (m *mockPolicyService) ListDevices(ctx context.Context) ([]accesspolicy.Device, error) {
	return m.devices, nil
}

func TestParseFlowLine(t *testing.T) {
	line := "ipv4     2 tcp      6 431999 ESTABLISHED src=192.168.1.100 dst=142.250.74.206 sport=54321 dport=443 packets=15 bytes=4096 src=142.250.74.206 dst=192.168.1.100 sport=443 dport=54321 packets=20 bytes=12000 [ASSURED] mark=0 use=2"
	flow, ok := parseFlowLine(line, "192.168.1.100")
	if !ok {
		t.Fatalf("expected flow to parse successfully")
	}

	if flow.Protocol != "tcp" {
		t.Errorf("expected proto tcp, got %s", flow.Protocol)
	}
	if flow.SrcIP != "192.168.1.100" || flow.DstIP != "142.250.74.206" {
		t.Errorf("unexpected IPs: %s -> %s", flow.SrcIP, flow.DstIP)
	}
	if flow.SrcPort != 54321 || flow.DstPort != 443 {
		t.Errorf("unexpected ports: %d -> %d", flow.SrcPort, flow.DstPort)
	}
	if flow.State != "ESTABLISHED" {
		t.Errorf("expected state ESTABLISHED, got %s", flow.State)
	}
	if flow.BytesOut != 4096 || flow.BytesIn != 12000 {
		t.Errorf("unexpected bytes: in=%d, out=%d", flow.BytesIn, flow.BytesOut)
	}
}

func TestDetectService(t *testing.T) {
	tests := []struct {
		domain   string
		ip       string
		wantName string
	}{
		{domain: "rr2---sn-4g5edn6r.googlevideo.com", ip: "1.2.3.4", wantName: "YouTube"},
		{domain: "audio-ak-spotify-com.akamaized.net", ip: "1.2.3.4", wantName: "Spotify"},
		{domain: "api.telegram.org", ip: "149.154.167.220", wantName: "Telegram"},
		{domain: "gateway.discord.gg", ip: "1.2.3.4", wantName: "Discord"},
		{domain: "unknown.example.org", ip: "1.2.3.4", wantName: ""},
	}

	for _, tt := range tests {
		name, _ := detectService(tt.domain, tt.ip)
		if tt.wantName != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(tt.wantName)) {
			t.Errorf("detectService(%s) = %s; want substring %s", tt.domain, name, tt.wantName)
		}
	}
}

func TestServiceListDevices(t *testing.T) {
	mockPol := &mockPolicyService{
		devices: []accesspolicy.Device{
			{IP: "192.168.1.100", Name: "Smart TV", MAC: "AA:BB:CC:DD:EE:01", Active: true},
			{IP: "192.168.1.101", Name: "iPhone", MAC: "AA:BB:CC:DD:EE:02", Active: true},
		},
	}

	svc := NewService(mockPol, nil, nil)
	devices, err := svc.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices failed: %v", err)
	}

	if len(devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devices))
	}
}

func TestEnsureSniffer_ConcurrencyAndGenerationSafe(t *testing.T) {
	svc := NewService(nil, nil, nil)
	var openCalls int32
	var closeCalls int32
	closedFd := make(chan int, 1)
	opened := make(chan struct{}, 1)

	svc.openPacketSocket = func() (int, error) {
		atomic.AddInt32(&openCalls, 1)
		select {
		case opened <- struct{}{}:
		default:
		}
		return 999, nil
	}
	svc.closePacketSocket = func(fd int) error {
		atomic.AddInt32(&closeCalls, 1)
		select {
		case closedFd <- fd:
		default:
		}
		return nil
	}
	svc.recvPacket = func(fd int, buf []byte) (int, error) {
		time.Sleep(10 * time.Millisecond)
		return 0, errors.New("timeout")
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.EnsureSniffer()
		}()
	}
	wg.Wait()

	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("openPacketSocket was not called within 1s")
	}

	calls := atomic.LoadInt32(&openCalls)
	if calls != 1 {
		t.Fatalf("concurrent EnsureSniffer launched %d socket openers, want 1", calls)
	}

	svc.snifferMu.Lock()
	active := svc.snifferActive
	svc.snifferMu.Unlock()
	if !active {
		t.Fatal("expected snifferActive to be true")
	}

	svc.Close()

	select {
	case fd := <-closedFd:
		if fd != 999 {
			t.Fatalf("unexpected fd closed: %d", fd)
		}
	case <-time.After(time.Second):
		t.Fatal("closePacketSocket was not called after Close()")
	}

	if closed := atomic.LoadInt32(&closeCalls); closed != 1 {
		t.Fatalf("expected 1 close call, got %d", closed)
	}

	svc.snifferMu.Lock()
	activeAfterClose := svc.snifferActive
	cancelNil := svc.snifferCancel == nil
	svc.snifferMu.Unlock()

	if activeAfterClose || !cancelNil {
		t.Fatalf("Close() failed to reset state: active=%v cancelNil=%v", activeAfterClose, cancelNil)
	}
}

func TestEnsureSniffer_SocketOpenFailureClearsActive(t *testing.T) {
	svc := NewService(nil, nil, nil)
	var openCalls int32
	svc.openPacketSocket = func() (int, error) {
		atomic.AddInt32(&openCalls, 1)
		return 0, errors.New("socket permission denied")
	}

	svc.EnsureSniffer()

	timeout := time.After(time.Second)
	for {
		svc.snifferMu.Lock()
		active := svc.snifferActive
		svc.snifferMu.Unlock()
		if !active {
			break
		}
		select {
		case <-timeout:
			t.Fatal("timeout waiting for snifferActive to be cleared on failure")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if got := atomic.LoadInt32(&openCalls); got != 1 {
		t.Fatalf("expected 1 open attempt, got %d", got)
	}

	// Calling EnsureSniffer again should retry
	svc.EnsureSniffer()

	for {
		svc.snifferMu.Lock()
		active := svc.snifferActive
		svc.snifferMu.Unlock()
		if !active {
			break
		}
		select {
		case <-timeout:
			t.Fatal("timeout waiting for second snifferActive to be cleared")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if got := atomic.LoadInt32(&openCalls); got != 2 {
		t.Fatalf("expected 2 open attempts, got %d", got)
	}
}

type mockNativeRuleStore struct {
	rules []NativeRule
}

func (m *mockNativeRuleStore) ListRules() []NativeRule {
	return m.rules
}

func TestTrafficExport_MihomoUninitializedBatchSaverFailsExplicitly(t *testing.T) {
	svc := NewService(nil, nil, nil)
	svc.SetNativeStore(&mockNativeRuleStore{})

	_, err := svc.Export(context.Background(), TrafficExportRequest{
		Target:  "mihomo",
		Domains: []string{"example.com"},
	})
	if err == nil || !strings.Contains(err.Error(), "batch rule saver is not initialized") {
		t.Fatalf("expected uninitialized saver error, got: %v", err)
	}
}
