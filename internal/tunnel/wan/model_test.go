package wan

import (
	"sort"
	"testing"
)

func TestEmptyModel_AnyUp_False(t *testing.T) {
	m := NewModel()
	if m.AnyUp() {
		t.Error("empty model should not have any WAN up")
	}
}

func TestEmptyModel_IsPopulated_False(t *testing.T) {
	m := NewModel()
	if m.IsPopulated() {
		t.Error("empty model should not be populated")
	}
}

func TestPopulate_SingleWANUp(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "PPPoE1", Type: "PPPoE", Up: true},
	})

	if !m.IsPopulated() {
		t.Error("should be populated")
	}
	if !m.AnyUp() {
		t.Error("single up WAN should make AnyUp true")
	}
}

func TestPopulate_SingleWANDown(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "PPPoE1", Type: "PPPoE", Up: false},
	})

	if m.AnyUp() {
		t.Error("single down WAN should make AnyUp false")
	}
}

func TestIsUp_KnownInterface(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "PPPoE1", Type: "PPPoE", Up: true},
		{Name: "ISP", Type: "GigabitEthernet", Up: false},
	})
	if !m.IsUp("PPPoE1") {
		t.Error("PPPoE1 should be up")
	}
	if m.IsUp("ISP") {
		t.Error("ISP should be down")
	}
}

func TestIsUp_UnknownInterface(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{})
	if m.IsUp("PPPoE1") {
		t.Error("unknown interface should return false")
	}
}

func TestSetUp_BeforePopulate_NoOp(t *testing.T) {
	m := NewModel()
	m.SetUp("PPPoE1", true) // Should not panic or create entry

	if m.AnyUp() {
		t.Error("SetUp before Populate should be no-op")
	}
}

func TestSetUp_AfterPopulate_Updates(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "PPPoE1", Type: "PPPoE", Up: false},
	})

	if m.AnyUp() {
		t.Error("PPPoE1 is down")
	}

	m.SetUp("PPPoE1", true)
	if !m.AnyUp() {
		t.Error("PPPoE1 should be up after SetUp")
	}

	m.SetUp("PPPoE1", false)
	if m.AnyUp() {
		t.Error("PPPoE1 should be down after SetUp(false)")
	}
}

func TestStatus_NoExcludedField(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "PPPoE1", Type: "PPPoE", Label: "Provider", Up: true},
	})
	status := m.Status()
	pppoe := status["PPPoE1"]
	if !pppoe.Up || pppoe.Label != "Provider" {
		t.Errorf("unexpected status: %+v", pppoe)
	}
}

func TestStatus_ShowsAllInterfaces(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ISP", Type: "GigabitEthernet", Up: true},
		{Name: "PPPoE1", Type: "PPPoE", Up: true},
	})

	status := m.Status()
	if _, ok := status["ISP"]; !ok {
		t.Error("ISP should be visible in status")
	}
	if _, ok := status["PPPoE1"]; !ok {
		t.Error("PPPoE1 should be visible in status")
	}
}

func TestPopulate_Overwrites(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "PPPoE1", Type: "PPPoE", Up: true},
	})
	if !m.AnyUp() {
		t.Error("first populate: PPPoE1 up")
	}

	// Re-populate with different data
	m.Populate([]Interface{
		{Name: "LTE0", Type: "UsbLte", Up: false},
	})
	if m.AnyUp() {
		t.Error("second populate: LTE0 down, PPPoE1 gone")
	}
}

func TestSetUp_UnknownInterface_TriggersRepopulate(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "PPPoE1", Type: "PPPoE", Up: true},
	})

	repopulateCalled := false
	m.SetRepopulateFn(func() {
		repopulateCalled = true
		// Simulate NDMS returning new interface list
		m.Populate([]Interface{
			{Name: "PPPoE1", Type: "PPPoE", Up: true},
			{Name: "UsbLte0", Type: "UsbLte", Up: false},
		})
	})

	// Hook for unknown interface triggers repopulate
	m.SetUp("UsbLte0", true)

	if !repopulateCalled {
		t.Error("repopulate should be called for unknown interface")
	}
	// After repopulate + SetUp override, UsbLte0 should be up
	if !m.AnyUp() {
		t.Error("should have WAN up")
	}
	status := m.Status()
	if !status["UsbLte0"].Up {
		t.Error("UsbLte0 should be up after repopulate + hook override")
	}
}

func TestSetUp_UnknownInterface_BeforePopulate_NoRepopulate(t *testing.T) {
	m := NewModel()

	repopulateCalled := false
	m.SetRepopulateFn(func() {
		repopulateCalled = true
	})

	// Before Populate, unknown interface should NOT trigger repopulate
	m.SetUp("UsbLte0", true)

	if repopulateCalled {
		t.Error("repopulate should not be called before initial Populate")
	}
}

func TestSetUp_UnknownInterface_RepopulateAddsNewInterface(t *testing.T) {
	m := NewModel()
	// Boot: ISP standalone
	m.Populate([]Interface{
		{Name: "ISP", Type: "GigabitEthernet", Up: true},
	})

	m.SetRepopulateFn(func() {
		// User configured PPPoE1 over ISP after boot
		m.Populate([]Interface{
			{Name: "ISP", Type: "GigabitEthernet", Up: true},
			{Name: "PPPoE1", Type: "PPPoE", Up: false},
		})
	})

	// PPPoE1 comes up — triggers repopulate since it's unknown
	m.SetUp("PPPoE1", true)

	// Both interfaces should be visible
	ui := m.ForUI()
	names := make([]string, len(ui))
	for i, iface := range ui {
		names[i] = iface.Name
	}
	sort.Strings(names)

	if len(names) != 2 {
		t.Fatalf("want 2 interfaces, got %d: %v", len(names), names)
	}
	if names[0] != "ISP" || names[1] != "PPPoE1" {
		t.Errorf("want [ISP PPPoE1], got %v", names)
	}
}

// === PreferredUp tests ===

func TestPreferredUp_EmptyModel(t *testing.T) {
	m := NewModel()
	if _, ok := m.PreferredUp(); ok {
		t.Error("empty model should return false")
	}
}

func TestPreferredUp_AllDown(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ISP", Type: "GigabitEthernet", Up: false, Priority: 58977},
		{Name: "LTE0", Type: "UsbLte", Up: false, Priority: 52424},
	})
	if _, ok := m.PreferredUp(); ok {
		t.Error("all down should return false")
	}
}

func TestPreferredUp_SingleUp(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ISP", Type: "GigabitEthernet", Up: false, Priority: 58977},
		{Name: "LTE0", Type: "UsbLte", Up: true, Priority: 52424},
	})
	name, ok := m.PreferredUp()
	if !ok || name != "LTE0" {
		t.Errorf("want LTE0, got %q (ok=%v)", name, ok)
	}
}

func TestPreferredUp_HigherPriorityWins(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ISP", Type: "GigabitEthernet", Up: true, Priority: 58977},
		{Name: "LTE0", Type: "UsbLte", Up: true, Priority: 52424},
	})
	name, ok := m.PreferredUp()
	if !ok || name != "ISP" {
		t.Errorf("want ISP (priority 58977), got %q (ok=%v)", name, ok)
	}
}

func TestPreferredUp_PriorityUpdatesWithSetUp(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ISP", Type: "GigabitEthernet", Up: true, Priority: 58977},
		{Name: "LTE0", Type: "UsbLte", Up: true, Priority: 52424},
	})

	// ISP goes down — LTE becomes preferred
	m.SetUp("ISP", false)
	name, ok := m.PreferredUp()
	if !ok || name != "LTE0" {
		t.Errorf("after ISP down: want LTE0, got %q (ok=%v)", name, ok)
	}

	// ISP comes back — ISP preferred again
	m.SetUp("ISP", true)
	name, ok = m.PreferredUp()
	if !ok || name != "ISP" {
		t.Errorf("after ISP up: want ISP, got %q (ok=%v)", name, ok)
	}
}

// === ForUI tests ===

func TestForUI_ShowsAllInterfaces(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "PPPoE1", Type: "PPPoE", Up: true},
		{Name: "ISP", Type: "GigabitEthernet", Up: false},
		{Name: "LTE0", Type: "UsbLte", Up: true},
	})

	ui := m.ForUI()
	if len(ui) != 3 {
		t.Fatalf("ForUI: want 3 interfaces, got %d", len(ui))
	}
	// Should be sorted by name
	if ui[0].Name != "ISP" || ui[1].Name != "LTE0" || ui[2].Name != "PPPoE1" {
		t.Errorf("ForUI: want [ISP LTE0 PPPoE1], got [%s %s %s]", ui[0].Name, ui[1].Name, ui[2].Name)
	}
}

// === IsNonISPInterface tests ===

func TestIsNonISPInterface(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Wireguard0", true},
		{"OpenVPN1", true},
		{"IPSec0", true},
		{"SSTP1", true},
		{"PPTP0", true},
		{"EoIP0", true},
		{"GRE0", true},
		{"IPIP0", true},
		{"Wireguard", true},
		{"ISP", false},
		{"PPPoE1", false},
		{"GigabitEthernet0", false},
		{"eth3", false},
		{"ppp0", false},
		{"br0", false},
		{"", false},
		{"Wi", false},
		{"GR", false},
		{"wireguard0", false},
		{"WIREGUARD0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNonISPInterface(tt.name); got != tt.want {
				t.Errorf("IsNonISPInterface(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// === GetLabel tests ===

func TestGetLabel_Known(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "PPPoE1", Type: "PPPoE", Label: "Provider", Up: true},
	})

	if label := m.GetLabel("PPPoE1"); label != "Provider" {
		t.Errorf("GetLabel(PPPoE1) = %q, want %q", label, "Provider")
	}
}

func TestGetLabel_Unknown(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ISP", Type: "GigabitEthernet", Up: true},
	})

	if label := m.GetLabel("LTE0"); label != "" {
		t.Errorf("GetLabel(LTE0) = %q, want empty string", label)
	}
}

func TestGetLabel_EmptyLabel(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ISP", Type: "GigabitEthernet", Label: "", Up: true},
	})

	if label := m.GetLabel("ISP"); label != "" {
		t.Errorf("GetLabel(ISP) = %q, want empty string", label)
	}
}

// === PreferredUp edge cases ===

func TestPreferredUp_EqualPriority_ReturnsOneOfThem(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ISP", Type: "GigabitEthernet", Up: true, Priority: 50000},
		{Name: "LTE0", Type: "UsbLte", Up: true, Priority: 50000},
	})

	name, ok := m.PreferredUp()
	if !ok {
		t.Fatal("PreferredUp should return ok=true when interfaces are up")
	}
	if name != "ISP" && name != "LTE0" {
		t.Errorf("PreferredUp = %q, want ISP or LTE0", name)
	}
}

// === SetUp repopulate edge cases ===

func TestSetUp_RepopulateStillUnknown(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ISP", Type: "GigabitEthernet", Up: true},
	})

	repopulateCalled := false
	m.SetRepopulateFn(func() {
		repopulateCalled = true
		// Repopulate does NOT add the unknown interface
		m.Populate([]Interface{
			{Name: "ISP", Type: "GigabitEthernet", Up: true},
		})
	})

	m.SetUp("Ghost0", true)

	if !repopulateCalled {
		t.Error("repopulate should be called for unknown interface")
	}
	// Ghost0 should not exist in the model
	if m.IsUp("Ghost0") {
		t.Error("Ghost0 should not be up — repopulate did not add it")
	}
	status := m.Status()
	if _, ok := status["Ghost0"]; ok {
		t.Error("Ghost0 should not appear in status")
	}
}
