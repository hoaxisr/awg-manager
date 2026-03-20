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
		{Name: "ppp0", ID: "PPPoE1", Up: true},
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
		{Name: "ppp0", ID: "PPPoE1", Up: false},
	})

	if m.AnyUp() {
		t.Error("single down WAN should make AnyUp false")
	}
}

func TestIsUp_KnownInterface(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ppp0", ID: "PPPoE1", Up: true},
		{Name: "eth3", ID: "ISP", Up: false},
	})
	if !m.IsUp("ppp0") {
		t.Error("ppp0 should be up")
	}
	if m.IsUp("eth3") {
		t.Error("eth3 should be down")
	}
}

func TestIsUp_UnknownInterface(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{})
	if m.IsUp("ppp0") {
		t.Error("unknown interface should return false")
	}
}

func TestSetUp_BeforePopulate_NoOp(t *testing.T) {
	m := NewModel()
	m.SetUp("ppp0", true) // Should not panic or create entry

	if m.AnyUp() {
		t.Error("SetUp before Populate should be no-op")
	}
}

func TestSetUp_AfterPopulate_Updates(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ppp0", ID: "PPPoE1", Up: false},
	})

	if m.AnyUp() {
		t.Error("ppp0 is down")
	}

	m.SetUp("ppp0", true)
	if !m.AnyUp() {
		t.Error("ppp0 should be up after SetUp")
	}

	m.SetUp("ppp0", false)
	if m.AnyUp() {
		t.Error("ppp0 should be down after SetUp(false)")
	}
}

func TestStatus_NoExcludedField(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ppp0", ID: "PPPoE1", Label: "Provider", Up: true},
	})
	status := m.Status()
	pppoe := status["ppp0"]
	if !pppoe.Up || pppoe.Label != "Provider" {
		t.Errorf("unexpected status: %+v", pppoe)
	}
}

func TestStatus_ShowsAllInterfaces(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true},
		{Name: "ppp0", ID: "PPPoE1", Up: true},
	})

	status := m.Status()
	if _, ok := status["eth3"]; !ok {
		t.Error("eth3 should be visible in status")
	}
	if _, ok := status["ppp0"]; !ok {
		t.Error("ppp0 should be visible in status")
	}
}

func TestPopulate_Overwrites(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ppp0", ID: "PPPoE1", Up: true},
	})
	if !m.AnyUp() {
		t.Error("first populate: ppp0 up")
	}

	// Re-populate with different data
	m.Populate([]Interface{
		{Name: "usb0", ID: "LTE0", Up: false},
	})
	if m.AnyUp() {
		t.Error("second populate: usb0 down, ppp0 gone")
	}
}

func TestSetUp_UnknownInterface_TriggersRepopulate(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ppp0", ID: "PPPoE1", Up: true},
	})

	repopulateCalled := false
	m.SetRepopulateFn(func() {
		repopulateCalled = true
		// Simulate NDMS returning new interface list
		m.Populate([]Interface{
			{Name: "ppp0", ID: "PPPoE1", Up: true},
			{Name: "usb0", ID: "UsbLte0", Up: false},
		})
	})

	// Hook for unknown interface triggers repopulate
	m.SetUp("usb0", true)

	if !repopulateCalled {
		t.Error("repopulate should be called for unknown interface")
	}
	// After repopulate + SetUp override, usb0 should be up
	if !m.AnyUp() {
		t.Error("should have WAN up")
	}
	status := m.Status()
	if !status["usb0"].Up {
		t.Error("usb0 should be up after repopulate + hook override")
	}
}

func TestSetUp_UnknownInterface_BeforePopulate_NoRepopulate(t *testing.T) {
	m := NewModel()

	repopulateCalled := false
	m.SetRepopulateFn(func() {
		repopulateCalled = true
	})

	// Before Populate, unknown interface should NOT trigger repopulate
	m.SetUp("usb0", true)

	if repopulateCalled {
		t.Error("repopulate should not be called before initial Populate")
	}
}

func TestSetUp_UnknownInterface_RepopulateAddsNewInterface(t *testing.T) {
	m := NewModel()
	// Boot: eth3 standalone
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true},
	})

	m.SetRepopulateFn(func() {
		// User configured PPPoE1 over ISP after boot
		m.Populate([]Interface{
			{Name: "eth3", ID: "ISP", Up: true},
			{Name: "ppp0", ID: "PPPoE1", Up: false},
		})
	})

	// ppp0 comes up — triggers repopulate since it's unknown
	m.SetUp("ppp0", true)

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
	if names[0] != "eth3" || names[1] != "ppp0" {
		t.Errorf("want [eth3 ppp0], got %v", names)
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
		{Name: "eth3", ID: "ISP", Up: false, Priority: 58977},
		{Name: "usb0", ID: "LTE0", Up: false, Priority: 52424},
	})
	if _, ok := m.PreferredUp(); ok {
		t.Error("all down should return false")
	}
}

func TestPreferredUp_SingleUp(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: false, Priority: 58977},
		{Name: "usb0", ID: "LTE0", Up: true, Priority: 52424},
	})
	name, ok := m.PreferredUp()
	if !ok || name != "usb0" {
		t.Errorf("want usb0, got %q (ok=%v)", name, ok)
	}
}

func TestPreferredUp_HigherPriorityWins(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true, Priority: 58977},
		{Name: "usb0", ID: "LTE0", Up: true, Priority: 52424},
	})
	name, ok := m.PreferredUp()
	if !ok || name != "eth3" {
		t.Errorf("want eth3 (priority 58977), got %q (ok=%v)", name, ok)
	}
}

func TestPreferredUp_PriorityUpdatesWithSetUp(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true, Priority: 58977},
		{Name: "usb0", ID: "LTE0", Up: true, Priority: 52424},
	})

	// eth3 goes down — usb0 becomes preferred
	m.SetUp("eth3", false)
	name, ok := m.PreferredUp()
	if !ok || name != "usb0" {
		t.Errorf("after eth3 down: want usb0, got %q (ok=%v)", name, ok)
	}

	// eth3 comes back — eth3 preferred again
	m.SetUp("eth3", true)
	name, ok = m.PreferredUp()
	if !ok || name != "eth3" {
		t.Errorf("after eth3 up: want eth3, got %q (ok=%v)", name, ok)
	}
}

// === ForUI tests ===

func TestForUI_ShowsAllInterfaces(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ppp0", ID: "PPPoE1", Up: true},
		{Name: "eth3", ID: "ISP", Up: false},
		{Name: "usb0", ID: "LTE0", Up: true},
	})

	ui := m.ForUI()
	if len(ui) != 3 {
		t.Fatalf("ForUI: want 3 interfaces, got %d", len(ui))
	}
	// Should be sorted by name
	if ui[0].Name != "eth3" || ui[1].Name != "ppp0" || ui[2].Name != "usb0" {
		t.Errorf("ForUI: want [eth3 ppp0 usb0], got [%s %s %s]", ui[0].Name, ui[1].Name, ui[2].Name)
	}
}

// === GetLabel tests ===

func TestGetLabel_Known(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "ppp0", ID: "PPPoE1", Label: "Provider", Up: true},
	})

	if label := m.GetLabel("ppp0"); label != "Provider" {
		t.Errorf("GetLabel(ppp0) = %q, want %q", label, "Provider")
	}
}

func TestGetLabel_Unknown(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true},
	})

	if label := m.GetLabel("usb0"); label != "" {
		t.Errorf("GetLabel(usb0) = %q, want empty string", label)
	}
}

func TestGetLabel_EmptyLabel(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Label: "", Up: true},
	})

	if label := m.GetLabel("eth3"); label != "" {
		t.Errorf("GetLabel(eth3) = %q, want empty string", label)
	}
}

// === PreferredUp edge cases ===

func TestPreferredUp_EqualPriority_ReturnsOneOfThem(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true, Priority: 50000},
		{Name: "usb0", ID: "LTE0", Up: true, Priority: 50000},
	})

	name, ok := m.PreferredUp()
	if !ok {
		t.Fatal("PreferredUp should return ok=true when interfaces are up")
	}
	if name != "eth3" && name != "usb0" {
		t.Errorf("PreferredUp = %q, want eth3 or usb0", name)
	}
}

// === IDFor tests ===

func TestIDFor_Found(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true},
		{Name: "ppp0", ID: "PPPoE1", Up: true},
	})

	if id := m.IDFor("eth3"); id != "ISP" {
		t.Errorf("IDFor(eth3) = %q, want %q", id, "ISP")
	}
	if id := m.IDFor("ppp0"); id != "PPPoE1" {
		t.Errorf("IDFor(ppp0) = %q, want %q", id, "PPPoE1")
	}
}

func TestIDFor_NotFound(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true},
	})

	if id := m.IDFor("usb0"); id != "" {
		t.Errorf("IDFor(usb0) = %q, want empty for unknown interface", id)
	}
}

func TestIDFor_EmptyModel(t *testing.T) {
	m := NewModel()
	if id := m.IDFor("eth3"); id != "" {
		t.Errorf("IDFor on empty model = %q, want empty", id)
	}
}

// === NameForID tests ===

func TestNameForID_Found(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true},
		{Name: "ppp0", ID: "PPPoE1", Up: true},
	})

	if name := m.NameForID("ISP"); name != "eth3" {
		t.Errorf("NameForID(ISP) = %q, want %q", name, "eth3")
	}
	if name := m.NameForID("PPPoE1"); name != "ppp0" {
		t.Errorf("NameForID(PPPoE1) = %q, want %q", name, "ppp0")
	}
}

func TestNameForID_NotFound(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true},
	})

	if name := m.NameForID("LTE0"); name != "" {
		t.Errorf("NameForID(LTE0) = %q, want empty for unknown ID", name)
	}
}

func TestNameForID_EmptyModel(t *testing.T) {
	m := NewModel()
	if name := m.NameForID("ISP"); name != "" {
		t.Errorf("NameForID on empty model = %q, want empty", name)
	}
}

// === SetUp repopulate edge cases ===

func TestSetUp_RepopulateStillUnknown(t *testing.T) {
	m := NewModel()
	m.Populate([]Interface{
		{Name: "eth3", ID: "ISP", Up: true},
	})

	repopulateCalled := false
	m.SetRepopulateFn(func() {
		repopulateCalled = true
		// Repopulate does NOT add the unknown interface
		m.Populate([]Interface{
			{Name: "eth3", ID: "ISP", Up: true},
		})
	})

	m.SetUp("ghost0", true)

	if !repopulateCalled {
		t.Error("repopulate should be called for unknown interface")
	}
	// ghost0 should not exist in the model
	if m.IsUp("ghost0") {
		t.Error("ghost0 should not be up — repopulate did not add it")
	}
	status := m.Status()
	if _, ok := status["ghost0"]; ok {
		t.Error("ghost0 should not appear in status")
	}
}
