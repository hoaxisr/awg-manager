package ops

import (
	"context"
	"strings"
	"testing"
)

// === Tests for kernel-mode Stop order and KillLink ===

// TestOperatorOS5_Stop_ProcessKilledBeforeInterfaceDown verifies that both
// InterfaceDown and Backend.Stop are called during Stop.
// Kernel stop order: InterfaceDown FIRST (device still present so NDMS can
// bring it down cleanly), then Backend.Stop removes the kernel interface.
func TestOperatorOS5_Stop_ProcessKilledBeforeInterfaceDown(t *testing.T) {
	ndmsMock := &MockNDMSClient{}
	backendMock := &MockBackend{running: true}
	fw := &MockFirewall{}

	op := newTestOperator(ndmsMock, &MockWGClient{}, backendMock, fw)

	err := op.Stop(context.Background(), "awg0")
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// Verify both were called
	if len(backendMock.StopCalls) != 1 {
		t.Fatal("Backend.Stop must be called")
	}
	if len(ndmsMock.IfDownCalls) != 1 {
		t.Fatal("NDMS.InterfaceDown must be called")
	}
}

// OrderTrackingMocks wraps mocks with a shared call log to verify ordering.
type OrderTrackingMocks struct {
	CallLog []string
}

// OrderedMockBackend tracks call order.
type OrderedMockBackend struct {
	MockBackend
	order *OrderTrackingMocks
}

func (m *OrderedMockBackend) Stop(ctx context.Context, ifaceName string) error {
	m.order.CallLog = append(m.order.CallLog, "backend.Stop")
	return m.MockBackend.Stop(ctx, ifaceName)
}

// OrderedMockNDMS tracks call order.
type OrderedMockNDMS struct {
	MockNDMSClient
	order *OrderTrackingMocks
}

func (m *OrderedMockNDMS) InterfaceDown(ctx context.Context, name string) error {
	m.order.CallLog = append(m.order.CallLog, "ndms.InterfaceDown")
	return m.MockNDMSClient.InterfaceDown(ctx, name)
}

// TestOperatorOS5_Stop_OrderVerification uses ordered mocks to verify
// kernel stop order: NDMS.InterfaceDown BEFORE Backend.Stop.
// InterfaceDown needs the device present; Backend.Stop (ip link del) removes it.
func TestOperatorOS5_Stop_OrderVerification(t *testing.T) {
	order := &OrderTrackingMocks{}

	ndmsMock := &OrderedMockNDMS{
		MockNDMSClient: MockNDMSClient{},
		order:          order,
	}
	backendMock := &OrderedMockBackend{
		MockBackend: MockBackend{running: true},
		order:       order,
	}
	fw := &MockFirewall{}

	op := NewOperatorOS5(ndmsMock, &MockWGClient{}, backendMock, fw, nil)
	op.ipRun = mockIPRun

	err := op.Stop(context.Background(), "awg0")
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// Find positions of the two critical calls
	stopIdx := -1
	ifDownIdx := -1
	for i, call := range order.CallLog {
		if call == "backend.Stop" {
			stopIdx = i
		}
		if call == "ndms.InterfaceDown" {
			ifDownIdx = i
		}
	}

	if stopIdx == -1 {
		t.Fatal("backend.Stop was not called")
	}
	if ifDownIdx == -1 {
		t.Fatal("ndms.InterfaceDown was not called")
	}
	// Kernel order: InterfaceDown BEFORE Backend.Stop
	if ifDownIdx >= stopIdx {
		t.Errorf("ndms.InterfaceDown (pos %d) must be called BEFORE backend.Stop (pos %d)\n"+
			"Call log: %v", ifDownIdx, stopIdx, order.CallLog)
	}
}

// TestOperatorOS5_KillLink verifies that KillLink in kernel mode:
// - Calls ip link set down (not ip link del) to preserve the interface
// - Does NOT call Backend.Stop (would destroy the interface)
// - Does NOT call InterfaceDown (preserves NDMS intent: conf: running)
func TestOperatorOS5_KillLink(t *testing.T) {
	ndmsMock := &MockNDMSClient{}
	backendMock := &MockBackend{running: true}
	recorder := &ipRunRecorder{}

	op := NewOperatorOS5(ndmsMock, &MockWGClient{}, backendMock, &MockFirewall{}, nil)
	op.ipRun = recorder.run

	err := op.KillLink(context.Background(), "awg0")
	if err != nil {
		t.Fatalf("KillLink() error = %v", err)
	}

	// ip link set down must be called (bring link down but preserve interface)
	foundLinkDown := false
	for _, call := range recorder.Calls {
		if strings.Contains(call, "link set down dev opkgtun0") {
			foundLinkDown = true
		}
	}
	if !foundLinkDown {
		t.Errorf("KillLink must call ip link set down, got: %v", recorder.Calls)
	}

	// Backend.Stop must NOT be called (ip link del would destroy the interface)
	if len(backendMock.StopCalls) != 0 {
		t.Errorf("Backend.Stop must NOT be called by KillLink (preserves interface), got %d calls",
			len(backendMock.StopCalls))
	}

	// InterfaceDown must NOT be called (preserves NDMS intent: conf: running)
	if len(ndmsMock.IfDownCalls) != 0 {
		t.Errorf("NDMS.InterfaceDown must NOT be called by KillLink (preserves intent), got %d calls",
			len(ndmsMock.IfDownCalls))
	}

	// InterfaceUp must NOT be called
	if len(ndmsMock.IfUpCalls) != 0 {
		t.Errorf("NDMS.InterfaceUp must NOT be called by KillLink, got %d calls",
			len(ndmsMock.IfUpCalls))
	}
}

// TestOperatorOS5_Stop_DoesCallInterfaceDown verifies that full Stop
// (user toggle OFF) DOES call InterfaceDown -- unlike KillLink.
// This sets conf: disabled so NDMS remembers "admin turned this off".
func TestOperatorOS5_Stop_DoesCallInterfaceDown(t *testing.T) {
	ndmsMock := &MockNDMSClient{}
	backendMock := &MockBackend{running: true}

	op := newTestOperator(ndmsMock, &MockWGClient{}, backendMock, &MockFirewall{})

	err := op.Stop(context.Background(), "awg0")
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// Stop MUST call InterfaceDown (admin intent: disable)
	if len(ndmsMock.IfDownCalls) != 1 {
		t.Errorf("Stop() must call InterfaceDown (sets conf: disabled), got %d calls",
			len(ndmsMock.IfDownCalls))
	}

	// Stop MUST also call Backend.Stop (ip link del)
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Stop() must call Backend.Stop, got %d calls",
			len(backendMock.StopCalls))
	}
}

// TestOperatorOS5_KillLink_vs_Stop_Difference verifies the key semantic difference:
// - KillLink: ip link set down (preserves interface + NDMS intent) -> auto-starts on WAN recovery
// - Stop: InterfaceDown + Backend.Stop (ip link del) -> tunnel stays disabled
func TestOperatorOS5_KillLink_vs_Stop_Difference(t *testing.T) {
	// KillLink path
	ndms1 := &MockNDMSClient{}
	be1 := &MockBackend{running: true}
	recorder1 := &ipRunRecorder{}
	op1 := NewOperatorOS5(ndms1, &MockWGClient{}, be1, &MockFirewall{}, nil)
	op1.ipRun = recorder1.run

	_ = op1.KillLink(context.Background(), "awg0")

	// Stop path
	ndms2 := &MockNDMSClient{}
	be2 := &MockBackend{running: true}
	op2 := newTestOperator(ndms2, &MockWGClient{}, be2, &MockFirewall{})

	_ = op2.Stop(context.Background(), "awg0")

	// KillLink must NOT call InterfaceDown (preserves intent for auto-restart)
	if len(ndms1.IfDownCalls) != 0 {
		t.Errorf("KillLink must NOT call InterfaceDown (preserves intent), got %d calls", len(ndms1.IfDownCalls))
	}
	// KillLink must NOT call Backend.Stop (would destroy the interface)
	if len(be1.StopCalls) != 0 {
		t.Errorf("KillLink must NOT call Backend.Stop (preserves interface), got %d calls", len(be1.StopCalls))
	}
	// KillLink MUST call ip link set down
	foundLinkDown := false
	for _, call := range recorder1.Calls {
		if strings.Contains(call, "link set down") {
			foundLinkDown = true
		}
	}
	if !foundLinkDown {
		t.Errorf("KillLink must call ip link set down, got: %v", recorder1.Calls)
	}

	// Stop MUST call InterfaceDown (admin intent: disable)
	if len(ndms2.IfDownCalls) != 1 {
		t.Errorf("Stop must call InterfaceDown, got %d calls", len(ndms2.IfDownCalls))
	}
	// Stop MUST call Backend.Stop (ip link del)
	if len(be2.StopCalls) != 1 {
		t.Errorf("Stop must call Backend.Stop, got %d calls", len(be2.StopCalls))
	}
}
