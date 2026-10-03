package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
)

// Gate C4 Contract: Verify exact compile-time interface implementation.
var _ mihomo.ExactBridgeRuntime = (*mihomoBridgeRuntime)(nil)

func TestGateC4_InspectBridge_ObservedDownWhenNDMSDown(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	nodes, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443#Native", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := nodes[0].ID

	if err := store.SetBridge("proxy", nodeID, mihomonative.ProxyBridge{
		ListenPort:      12001,
		ProxyIndex:      10,
		ProxyInterface:  "Proxy10",
		KernelInterface: "t2s10",
	}); err != nil {
		t.Fatal(err)
	}

	ndms := newFakeNDMSRegistrarWithOwnership()
	gate := &gatedBridgeRegistrar{base: ndms}
	gate.setReady(true)
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
	runtime := newMihomoBridgeRuntime(store, manager, gate)

	ctx := context.Background()
	owner := mihomonative.BridgeOwnershipDescription("proxy", nodeID)

	// Ensure interface exists in NDMS
	if _, err := ndms.EnsureProxyIfOwned(ctx, 10, 12001, owner); err != nil {
		t.Fatalf("ensure proxy in NDMS failed: %v", err)
	}

	// Case 1: NDMS reports Up -> observed Up is true
	ref := mihomo.BridgeRef{
		ProxyIndex:      10,
		ProxyInterface:  "Proxy10",
		KernelInterface: "t2s10",
		ListenPort:      12001,
		OwnerUUID:       owner,
	}

	obsUp, err := runtime.InspectBridge(ctx, ref)
	if err != nil {
		t.Fatalf("InspectBridge failed: %v", err)
	}
	if !obsUp.Exists || !obsUp.Up {
		t.Errorf("expected Exists=true and Up=true, got Exists=%v Up=%v", obsUp.Exists, obsUp.Up)
	}

	// Case 2: NDMS reports Down -> observed Up is false (NDMS link state respected)
	ndms.overrideUp[10] = false
	obsDown, err := runtime.InspectBridge(ctx, ref)
	if err != nil {
		t.Fatalf("InspectBridge with NDMS Down failed: %v", err)
	}
	if !obsDown.Exists {
		t.Errorf("expected Exists=true, got %v", obsDown.Exists)
	}
	if obsDown.Up {
		t.Errorf("expected Up=false when NDMS reports Down, got %v", obsDown.Up)
	}
	if obsDown.OwnerUUID != owner {
		t.Errorf("expected OwnerUUID=%q, got %q", owner, obsDown.OwnerUUID)
	}
}

func TestGateC4_ApplyBridges_PartialFailureRollbackIsolation(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	n1, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@n1.example:443#N1", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := store.CreateProxy("vless://22222222-2222-2222-2222-222222222222@n2.example:443#N2", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	n3, err := store.CreateProxy("vless://33333333-3333-3333-3333-333333333333@n3.example:443#N3", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}

	_ = store.SetBridge("proxy", n1[0].ID, mihomonative.ProxyBridge{ListenPort: 12001, ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "t2s1"})
	_ = store.SetBridge("proxy", n2[0].ID, mihomonative.ProxyBridge{ListenPort: 12002, ProxyIndex: 2, ProxyInterface: "Proxy2", KernelInterface: "t2s2"})
	_ = store.SetBridge("proxy", n3[0].ID, mihomonative.ProxyBridge{ListenPort: 12003, ProxyIndex: 3, ProxyInterface: "Proxy3", KernelInterface: "t2s3"})

	ndms := newFakeNDMSRegistrarWithOwnership()
	gate := &gatedBridgeRegistrar{base: ndms}
	gate.setReady(true)
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
	runtime := newMihomoBridgeRuntime(store, manager, gate)

	b1 := mihomo.BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "t2s1", ListenPort: 12001, OwnerUUID: mihomonative.BridgeOwnershipDescription("proxy", n1[0].ID)}
	b2 := mihomo.BridgeRef{ProxyIndex: 2, ProxyInterface: "Proxy2", KernelInterface: "t2s2", ListenPort: 12002, OwnerUUID: mihomonative.BridgeOwnershipDescription("proxy", n2[0].ID)}
	b3 := mihomo.BridgeRef{ProxyIndex: 3, ProxyInterface: "Proxy3", KernelInterface: "t2s3", ListenPort: 12003, OwnerUUID: mihomonative.BridgeOwnershipDescription("proxy", n3[0].ID)}

	// Configure ndms to fail publishing bridge index 2
	ndms.failPublishIndex = 2

	ctx := context.Background()
	err = runtime.ApplyBridges(ctx, []mihomo.BridgeRef{b1, b2, b3})
	if err == nil {
		t.Fatal("expected ApplyBridges to fail on partial failure at index 2")
	}

	// Verification 1: B1 was published and then compensated (withdrawn)
	if ndms.occupied[1] {
		t.Errorf("expected B1 (index 1) to be withdrawn/compensated, but it is still occupied")
	}

	// Verification 2: B2 failed
	if ndms.occupied[2] {
		t.Errorf("expected B2 (index 2) to not be occupied")
	}

	// Verification 3: B3 was never touched
	if ndms.occupied[3] {
		t.Errorf("expected B3 (index 3) to never be touched")
	}
	for _, ev := range ndms.events {
		if ev == "publish:3:"+b3.OwnerUUID+":12003" {
			t.Errorf("found publish event for B3; B3 should never have been touched")
		}
	}

	// Verification 4: Total active bridges in NDMS is 0 (exact state preserved)
	if len(ndms.occupied) != 0 {
		t.Errorf("expected 0 occupied bridges in NDMS after compensation, got %d", len(ndms.occupied))
	}
}

func TestGateC4_ForeignOrEmptyDescription(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	nodes, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443#Native", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := nodes[0].ID

	_ = store.SetBridge("proxy", nodeID, mihomonative.ProxyBridge{
		ListenPort:      12015,
		ProxyIndex:      15,
		ProxyInterface:  "Proxy15",
		KernelInterface: "t2s15",
	})

	ndms := newFakeNDMSRegistrarWithOwnership()
	gate := &gatedBridgeRegistrar{base: ndms}
	gate.setReady(true)
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
	runtime := newMihomoBridgeRuntime(store, manager, gate)

	ctx := context.Background()
	awgmOwner := mihomonative.BridgeOwnershipDescription("proxy", nodeID)

	// Setup 1: Unmanaged interface (empty description) in slot 15
	ndms.occupied[15] = true
	ndms.descriptions[15] = ""

	ref15 := mihomo.BridgeRef{
		ProxyIndex:      15,
		ProxyInterface:  "Proxy15",
		KernelInterface: "t2s15",
		ListenPort:      12015,
		OwnerUUID:       awgmOwner,
	}

	// Inspect reports unmanaged (empty owner UUID)
	obs15, err := runtime.InspectBridge(ctx, ref15)
	if err != nil {
		t.Fatalf("InspectBridge on unmanaged interface failed: %v", err)
	}
	if !obs15.Exists {
		t.Errorf("expected Exists=true for unmanaged interface")
	}
	if obs15.OwnerUUID != "" {
		t.Errorf("expected empty OwnerUUID for unmanaged interface, got %q", obs15.OwnerUUID)
	}

	// Publish on unmanaged interface must fail closed with ErrForeignBridgeOwnership
	err = runtime.PublishBridge(ctx, ref15)
	if err == nil || !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
		t.Errorf("expected ErrForeignBridgeOwnership when publishing over unmanaged interface, got: %v", err)
	}

	// Setup 2: Foreign app interface in slot 16
	n2, err := store.CreateProxy("vless://22222222-2222-2222-2222-222222222222@foreign.example:443#Foreign", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	node2ID := n2[0].ID
	_ = store.SetBridge("proxy", node2ID, mihomonative.ProxyBridge{
		ListenPort:      12016,
		ProxyIndex:      16,
		ProxyInterface:  "Proxy16",
		KernelInterface: "t2s16",
	})
	awgmOwner2 := mihomonative.BridgeOwnershipDescription("proxy", node2ID)

	ndms.occupied[16] = true
	ndms.descriptions[16] = "some-foreign-vpn-interface"

	ref16 := mihomo.BridgeRef{
		ProxyIndex:      16,
		ProxyInterface:  "Proxy16",
		KernelInterface: "t2s16",
		ListenPort:      12016,
		OwnerUUID:       awgmOwner2,
	}

	// Inspect reports foreign owner
	obs16, err := runtime.InspectBridge(ctx, ref16)
	if err != nil {
		t.Fatalf("InspectBridge on foreign interface failed: %v", err)
	}
	if obs16.OwnerUUID != "some-foreign-vpn-interface" {
		t.Errorf("expected OwnerUUID='some-foreign-vpn-interface', got %q", obs16.OwnerUUID)
	}

	// Withdraw on foreign interface must NOT delete it and must fail closed with ErrForeignBridgeOwnership
	err = runtime.WithdrawBridge(ctx, ref16)
	if err == nil || !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
		t.Errorf("expected ErrForeignBridgeOwnership when attempting to withdraw foreign interface, got: %v", err)
	}
	if !ndms.occupied[16] {
		t.Errorf("foreign interface was improperly deleted from NDMS")
	}
}

func TestGateC4_ExactBridgeSync_AdjacentIsolation(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	n1, _ := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@n1.example:443#N1", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	n2, _ := store.CreateProxy("vless://22222222-2222-2222-2222-222222222222@n2.example:443#N2", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	n3, _ := store.CreateProxy("vless://33333333-3333-3333-3333-333333333333@n3.example:443#N3", mihomonative.EngineMihomo, mihomonative.EngineMihomo)

	_ = store.SetBridge("proxy", n1[0].ID, mihomonative.ProxyBridge{ListenPort: 12001, ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "t2s1"})
	_ = store.SetBridge("proxy", n2[0].ID, mihomonative.ProxyBridge{ListenPort: 12002, ProxyIndex: 2, ProxyInterface: "Proxy2", KernelInterface: "t2s2"})
	_ = store.SetBridge("proxy", n3[0].ID, mihomonative.ProxyBridge{ListenPort: 12003, ProxyIndex: 3, ProxyInterface: "Proxy3", KernelInterface: "t2s3"})

	ndms := newFakeNDMSRegistrarWithOwnership()
	gate := &gatedBridgeRegistrar{base: ndms}
	gate.setReady(true)
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
	runtime := newMihomoBridgeRuntime(store, manager, gate)

	b1 := mihomo.BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "t2s1", ListenPort: 12001, OwnerUUID: mihomonative.BridgeOwnershipDescription("proxy", n1[0].ID)}
	b2 := mihomo.BridgeRef{ProxyIndex: 2, ProxyInterface: "Proxy2", KernelInterface: "t2s2", ListenPort: 12002, OwnerUUID: mihomonative.BridgeOwnershipDescription("proxy", n2[0].ID)}
	b3 := mihomo.BridgeRef{ProxyIndex: 3, ProxyInterface: "Proxy3", KernelInterface: "t2s3", ListenPort: 12003, OwnerUUID: mihomonative.BridgeOwnershipDescription("proxy", n3[0].ID)}

	ctx := context.Background()

	// Initially publish B1 and B2
	if err := runtime.ApplyBridges(ctx, []mihomo.BridgeRef{b1, b2}); err != nil {
		t.Fatalf("initial ApplyBridges failed: %v", err)
	}

	initialPubCalls := ndms.publishCalls
	if initialPubCalls != 2 {
		t.Fatalf("expected 2 publish calls, got %d", initialPubCalls)
	}

	// 1. Publish only B3: B1 and B2 must NOT be republished
	if err := runtime.ApplyBridges(ctx, []mihomo.BridgeRef{b3}); err != nil {
		t.Fatalf("ApplyBridges for B3 failed: %v", err)
	}
	if ndms.publishCalls != initialPubCalls+1 {
		t.Errorf("expected exactly 1 additional publish call for B3, got total %d", ndms.publishCalls)
	}
	if !ndms.occupied[1] || !ndms.occupied[2] || !ndms.occupied[3] {
		t.Errorf("expected all 3 bridges to remain occupied")
	}

	// 2. Withdraw only B1: B2 and B3 must remain untouched
	initialWithdrawCalls := ndms.withdrawCalls
	if err := runtime.WithdrawBridges(ctx, []mihomo.BridgeRef{b1}); err != nil {
		t.Fatalf("WithdrawBridges for B1 failed: %v", err)
	}
	if ndms.withdrawCalls != initialWithdrawCalls+1 {
		t.Errorf("expected exactly 1 withdraw call, got total %d", ndms.withdrawCalls)
	}
	if ndms.occupied[1] {
		t.Errorf("expected B1 to be withdrawn")
	}
	if !ndms.occupied[2] || !ndms.occupied[3] {
		t.Errorf("expected B2 and B3 to remain completely occupied and untouched")
	}
}

// Mandatory regression for P0-1:
// Seed bridge A as already active before ApplyBridges([A, B]). Make publication of B fail.
// Prove A still exists unchanged after compensation.
// Also inject a compensation failure and prove both the original and compensation errors are observable.
func TestGateC4_ApplyBridges_PreExistingBridgePreservedOnBatchFailure(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	nA, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@a.example:443#A", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	nB, err := store.CreateProxy("vless://22222222-2222-2222-2222-222222222222@b.example:443#B", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	nC, err := store.CreateProxy("vless://33333333-3333-3333-3333-333333333333@c.example:443#C", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}

	_ = store.SetBridge("proxy", nA[0].ID, mihomonative.ProxyBridge{ListenPort: 12001, ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "t2s1"})
	_ = store.SetBridge("proxy", nB[0].ID, mihomonative.ProxyBridge{ListenPort: 12002, ProxyIndex: 2, ProxyInterface: "Proxy2", KernelInterface: "t2s2"})
	_ = store.SetBridge("proxy", nC[0].ID, mihomonative.ProxyBridge{ListenPort: 12003, ProxyIndex: 3, ProxyInterface: "Proxy3", KernelInterface: "t2s3"})

	ndms := newFakeNDMSRegistrarWithOwnership()
	gate := &gatedBridgeRegistrar{base: ndms}
	gate.setReady(true)
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
	runtime := newMihomoBridgeRuntime(store, manager, gate)

	bA := mihomo.BridgeRef{ProxyIndex: 1, ProxyInterface: "Proxy1", KernelInterface: "t2s1", ListenPort: 12001, OwnerUUID: mihomonative.BridgeOwnershipDescription("proxy", nA[0].ID)}
	bB := mihomo.BridgeRef{ProxyIndex: 2, ProxyInterface: "Proxy2", KernelInterface: "t2s2", ListenPort: 12002, OwnerUUID: mihomonative.BridgeOwnershipDescription("proxy", nB[0].ID)}
	bC := mihomo.BridgeRef{ProxyIndex: 3, ProxyInterface: "Proxy3", KernelInterface: "t2s3", ListenPort: 12003, OwnerUUID: mihomonative.BridgeOwnershipDescription("proxy", nC[0].ID)}

	ctx := context.Background()

	// 1. Seed bridge A as already active before ApplyBridges([A, B])
	if err := runtime.ApplyBridges(ctx, []mihomo.BridgeRef{bA}); err != nil {
		t.Fatalf("seed bridge A failed: %v", err)
	}
	if !ndms.occupied[1] {
		t.Fatal("expected bridge A to be active")
	}

	// 2. Apply batch [A, B] where publication of B fails
	ndms.failPublishIndex = 2
	err = runtime.ApplyBridges(ctx, []mihomo.BridgeRef{bA, bB})
	if err == nil {
		t.Fatal("expected ApplyBridges([A, B]) to fail on B")
	}

	// PROVE: Bridge A still exists unchanged after compensation!
	if !ndms.occupied[1] {
		t.Fatalf("VIOLATION: pre-existing bridge A was improperly withdrawn during compensation!")
	}
	obsA, err := runtime.InspectBridge(ctx, bA)
	if err != nil || !obsA.Exists || obsA.OwnerUUID != bA.OwnerUUID {
		t.Fatalf("bridge A corrupted after compensation: obs=%+v, err=%v", obsA, err)
	}

	// 3. Inject a compensation failure on a new bridge C when subsequent bridge B fails
	// Batch: [C, B], where C is new, B fails to publish, and C fails to withdraw during compensation.
	ndms.failPublishIndex = 2
	ndms.failWithdrawIndex = 3
	compErr := runtime.ApplyBridges(ctx, []mihomo.BridgeRef{bC, bB})
	if compErr == nil {
		t.Fatal("expected batch to fail")
	}
	// Prove BOTH original and compensation errors are observable in returned error
	errStr := compErr.Error()
	if !strings.Contains(errStr, "publish bridge Proxy2") {
		t.Errorf("expected original error in %q", errStr)
	}
	if !strings.Contains(errStr, "compensation withdraw") || !strings.Contains(errStr, "withdraw bridge Proxy3") {
		t.Errorf("expected compensation error in %q", errStr)
	}
}

// Mandatory regression for P0-2:
// Live observation with empty SystemName and desired t2s10 must not return observed t2s10
// and must not pass exact publication verification.
func TestGateC4_InspectBridge_EmptySystemNameFailsClosed(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	nodes, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@empty.example:443#Empty", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := nodes[0].ID

	if err := store.SetBridge("proxy", nodeID, mihomonative.ProxyBridge{
		ListenPort:      12010,
		ProxyIndex:      10,
		ProxyInterface:  "Proxy10",
		KernelInterface: "t2s10",
	}); err != nil {
		t.Fatal(err)
	}

	ndms := newFakeNDMSRegistrarWithOwnership()
	gate := &gatedBridgeRegistrar{base: ndms}
	gate.setReady(true)
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
	runtime := newMihomoBridgeRuntime(store, manager, gate)

	ctx := context.Background()
	owner := mihomonative.BridgeOwnershipDescription("proxy", nodeID)

	ref := mihomo.BridgeRef{
		ProxyIndex:      10,
		ProxyInterface:  "Proxy10",
		KernelInterface: "t2s10",
		ListenPort:      12010,
		OwnerUUID:       owner,
	}

	// 1. Live observation with empty SystemName
	ndms.occupied[10] = true
	ndms.descriptions[10] = owner
	ndms.overrideSystemName[10] = "" // empty SystemName in live NDMS

	obs, err := runtime.InspectBridge(ctx, ref)
	if err != nil {
		t.Fatalf("InspectBridge failed: %v", err)
	}
	// MUST NOT return observed t2s10:
	if obs.KernelInterface == "t2s10" {
		t.Fatalf("VIOLATION: live observation fabricated desired kernel interface 't2s10' when SystemName was empty!")
	}
	if obs.KernelInterface != "" {
		t.Fatalf("expected empty KernelInterface when live SystemName is empty, got %q", obs.KernelInterface)
	}

	// 2. Publish postcondition must not pass exact publication verification
	pubErr := runtime.PublishBridge(ctx, ref)
	if pubErr == nil {
		t.Fatalf("VIOLATION: PublishBridge succeeded despite missing kernel interface (empty SystemName)!")
	}
	if !strings.Contains(pubErr.Error(), "kernel interface mismatch") {
		t.Errorf("expected kernel interface mismatch error, got: %v", pubErr)
	}
}

// Mandatory regression for P0-2:
// Seed an owned live ProxyN with old port A, apply desired port B, fail publication of a later bridge,
// and assert the live proxy is restored to A using a genuinely observed complete before-image.
// Also assert that when live port observation fails (ListenPort <= 0), the mutation is rejected before any mutation.
func TestGateC4_ApplyBridges_ObservedBeforeImagePortRestoredOnBatchFailure(t *testing.T) {
	t.Run("ObservedPortRestoredOnBatchFailure", func(t *testing.T) {
		store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
		if err != nil {
			t.Fatal(err)
		}

		n1, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@n1.example:443#N1", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
		if err != nil {
			t.Fatal(err)
		}
		n2, err := store.CreateProxy("vless://22222222-2222-2222-2222-222222222222@n2.example:443#N2", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
		if err != nil {
			t.Fatal(err)
		}

		owner1 := mihomonative.BridgeOwnershipDescription("proxy", n1[0].ID)
		owner2 := mihomonative.BridgeOwnershipDescription("proxy", n2[0].ID)

		// Seed live NDMS with old port A (12001) for Proxy 1
		const portA = 12001
		const portB = 12099
		const port2 = 12002

		ndms := newFakeNDMSRegistrarWithOwnership()
		ndms.occupied[1] = true
		ndms.descriptions[1] = owner1
		ndms.ports[1] = portA

		gate := &gatedBridgeRegistrar{base: ndms}
		gate.setReady(true)
		manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
		runtime := newMihomoBridgeRuntime(store, manager, gate)

		// Configure store with desired port B (12099) for Proxy 1, and port 12002 for Proxy 2
		if err := store.SetBridge("proxy", n1[0].ID, mihomonative.ProxyBridge{
			ListenPort:      portB,
			ProxyIndex:      1,
			ProxyInterface:  "Proxy1",
			KernelInterface: "t2s1",
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetBridge("proxy", n2[0].ID, mihomonative.ProxyBridge{
			ListenPort:      port2,
			ProxyIndex:      2,
			ProxyInterface:  "Proxy2",
			KernelInterface: "t2s2",
		}); err != nil {
			t.Fatal(err)
		}

		b1Desired := mihomo.BridgeRef{
			ProxyIndex:      1,
			ProxyInterface:  "Proxy1",
			KernelInterface: "t2s1",
			ListenPort:      portB,
			OwnerUUID:       owner1,
		}
		b2Desired := mihomo.BridgeRef{
			ProxyIndex:      2,
			ProxyInterface:  "Proxy2",
			KernelInterface: "t2s2",
			ListenPort:      port2,
			OwnerUUID:       owner2,
		}

		// Inject failure on publication of bridge 2
		ndms.failPublishIndex = 2

		ctx := context.Background()
		applyErr := runtime.ApplyBridges(ctx, []mihomo.BridgeRef{b1Desired, b2Desired})
		if applyErr == nil {
			t.Fatal("expected ApplyBridges to fail on bridge 2 publish failure")
		}

		// PROVE: Live Proxy 1 was restored to port A (12001), NOT left at port B (12099)
		if !ndms.occupied[1] {
			t.Errorf("expected Proxy 1 to remain occupied after restore")
		}
		if ndms.ports[1] != portA {
			t.Fatalf("VIOLATION: Proxy 1 live port not restored to port A (%d); got %d", portA, ndms.ports[1])
		}
	})

	t.Run("UnobservablePortRejectedBeforeMutation", func(t *testing.T) {
		store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
		if err != nil {
			t.Fatal(err)
		}

		n1, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@n1.example:443#N1", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
		if err != nil {
			t.Fatal(err)
		}
		owner1 := mihomonative.BridgeOwnershipDescription("proxy", n1[0].ID)

		// Seed live NDMS with Proxy 1 occupied, but port cannot be observed (port <= 0)
		ndms := newFakeNDMSRegistrarWithOwnership()
		ndms.occupied[1] = true
		ndms.descriptions[1] = owner1
		ndms.ports[1] = 0 // unobservable live port

		gate := &gatedBridgeRegistrar{base: ndms}
		gate.setReady(true)
		manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
		runtime := newMihomoBridgeRuntime(store, manager, gate)

		if err := store.SetBridge("proxy", n1[0].ID, mihomonative.ProxyBridge{
			ListenPort:      12099,
			ProxyIndex:      1,
			ProxyInterface:  "Proxy1",
			KernelInterface: "t2s1",
		}); err != nil {
			t.Fatal(err)
		}

		b1Desired := mihomo.BridgeRef{
			ProxyIndex:      1,
			ProxyInterface:  "Proxy1",
			KernelInterface: "t2s1",
			ListenPort:      12099,
			OwnerUUID:       owner1,
		}

		ctx := context.Background()
		err = runtime.ApplyBridges(ctx, []mihomo.BridgeRef{b1Desired})
		if err == nil {
			t.Fatal("expected ApplyBridges to fail closed when live port is unobservable")
		}
		if !errors.Is(err, mihomo.ErrVerificationFailed) {
			t.Errorf("expected error wrapping ErrVerificationFailed, got: %v", err)
		}
		// PROVE: NDMS was never mutated
		if ndms.ports[1] != 0 {
			t.Errorf("NDMS port was mutated to %d despite unobservable before-image", ndms.ports[1])
		}
	})
}
