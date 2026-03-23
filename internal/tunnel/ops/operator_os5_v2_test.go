package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// OrderTrackingMocks wraps mocks with a shared call log to verify ordering.
type OrderTrackingMocks struct {
	CallLog []string
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

// === Tests for kernel-mode Stop (new: link down only) and Suspend ===

// TestOperatorOS5_Stop_ProcessKilledBeforeInterfaceDown verifies that
// Stop calls ip link set down + InterfaceDown but NOT backend.Stop.
// Interface stays as amneziawg — address and WG config preserved.
func TestOperatorOS5_Stop_ProcessKilledBeforeInterfaceDown(t *testing.T) {
	ndmsMock := &MockNDMSClient{}
	backendMock := &MockBackend{running: true}
	recorder := &ipRunRecorder{}

	op := NewOperatorOS5(ndmsMock, &MockWGClient{}, backendMock, &MockFirewall{}, nil)
	op.ipRun = recorder.run

	err := op.Stop(context.Background(), "awg0")
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// InterfaceDown must be called (sets conf: disabled).
	if len(ndmsMock.IfDownCalls) != 1 {
		t.Fatal("NDMS.InterfaceDown must be called")
	}
	// Backend.Stop must NOT be called (interface stays).
	if len(backendMock.StopCalls) != 0 {
		t.Fatalf("Backend.Stop must NOT be called by new Stop, got %d", len(backendMock.StopCalls))
	}
	// ip link set down must be called.
	foundLinkDown := false
	for _, call := range recorder.Calls {
		if strings.Contains(call, "link set down dev opkgtun0") {
			foundLinkDown = true
		}
	}
	if !foundLinkDown {
		t.Errorf("Stop must call ip link set down, got: %v", recorder.Calls)
	}
}

// TestOperatorOS5_Stop_OrderVerification verifies Stop calls
// ip link set down BEFORE InterfaceDown.
func TestOperatorOS5_Stop_OrderVerification(t *testing.T) {
	order := &OrderTrackingMocks{}

	ndmsMock := &OrderedMockNDMS{
		MockNDMSClient: MockNDMSClient{},
		order:          order,
	}
	recorder := &ipRunRecorder{}

	op := NewOperatorOS5(ndmsMock, &MockWGClient{}, &MockBackend{running: true}, &MockFirewall{}, nil)
	op.ipRun = func(ctx context.Context, name string, args ...string) (*exec.Result, error) {
		cmd := name + " " + strings.Join(args, " ")
		order.CallLog = append(order.CallLog, "ip:"+cmd)
		return recorder.run(ctx, name, args...)
	}

	err := op.Stop(context.Background(), "awg0")
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// Find positions
	linkDownIdx := -1
	ifDownIdx := -1
	for i, call := range order.CallLog {
		if strings.Contains(call, "link set down") {
			linkDownIdx = i
		}
		if call == "ndms.InterfaceDown" {
			ifDownIdx = i
		}
	}

	if linkDownIdx == -1 {
		t.Fatal("ip link set down was not called")
	}
	if ifDownIdx == -1 {
		t.Fatal("ndms.InterfaceDown was not called")
	}
	// ip link set down BEFORE InterfaceDown
	if linkDownIdx >= ifDownIdx {
		t.Errorf("ip link set down (pos %d) must be before InterfaceDown (pos %d)\nLog: %v",
			linkDownIdx, ifDownIdx, order.CallLog)
	}
}

// TestOperatorOS5_Stop_DoesCallInterfaceDown verifies that Stop
// calls InterfaceDown (conf: disabled) so NDMS remembers "admin turned this off".
func TestOperatorOS5_Stop_DoesCallInterfaceDown(t *testing.T) {
	ndmsMock := &MockNDMSClient{}

	op := newTestOperator(ndmsMock, &MockWGClient{}, &MockBackend{running: true}, &MockFirewall{})

	err := op.Stop(context.Background(), "awg0")
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if len(ndmsMock.IfDownCalls) != 1 {
		t.Errorf("Stop() must call InterfaceDown (sets conf: disabled), got %d calls",
			len(ndmsMock.IfDownCalls))
	}
}

// TestOperatorOS5_Suspend_vs_Stop_Difference verifies the key semantic difference:
// - Suspend: ip link set down only (preserves NDMS intent: conf: running) → auto-resumes
// - Stop: ip link set down + InterfaceDown (conf: disabled) → stays disabled until explicit Start
func TestOperatorOS5_Suspend_vs_Stop_Difference(t *testing.T) {
	// Suspend path
	ndms1 := &MockNDMSClient{}
	recorder1 := &ipRunRecorder{}
	op1 := NewOperatorOS5(ndms1, &MockWGClient{}, &MockBackend{running: true}, &MockFirewall{}, nil)
	op1.ipRun = recorder1.run

	_ = op1.Suspend(context.Background(), "awg0")

	// Stop path
	ndms2 := &MockNDMSClient{}
	recorder2 := &ipRunRecorder{}
	op2 := NewOperatorOS5(ndms2, &MockWGClient{}, &MockBackend{running: true}, &MockFirewall{}, nil)
	op2.ipRun = recorder2.run

	_ = op2.Stop(context.Background(), "awg0")

	// Suspend must NOT call InterfaceDown (preserves conf: running for auto-resume).
	if len(ndms1.IfDownCalls) != 0 {
		t.Errorf("Suspend must NOT call InterfaceDown, got %d calls", len(ndms1.IfDownCalls))
	}

	// Stop MUST call InterfaceDown (admin intent: disable).
	if len(ndms2.IfDownCalls) != 1 {
		t.Errorf("Stop must call InterfaceDown, got %d calls", len(ndms2.IfDownCalls))
	}

	// Both must call ip link set down.
	for name, calls := range map[string][]string{"Suspend": recorder1.Calls, "Stop": recorder2.Calls} {
		found := false
		for _, call := range calls {
			if strings.Contains(call, "link set down") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s must call ip link set down, got: %v", name, calls)
		}
	}
}
