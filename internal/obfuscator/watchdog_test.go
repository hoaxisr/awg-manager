package obfuscator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setWatchdogPaths(t *testing.T, bootID string) string {
	dir := t.TempDir()
	oldA, oldB, oldO := ArmPath, BootIDPath, OopsPath
	ArmPath, BootIDPath, OopsPath = filepath.Join(dir, "arming"), filepath.Join(dir, "boot_id"), filepath.Join(dir, "oops")
	t.Cleanup(func() {
		DisarmAfter(0)
		ArmPath, BootIDPath, OopsPath = oldA, oldB, oldO
	})
	_ = os.WriteFile(BootIDPath, []byte(bootID+"\n"), 0o644)
	return dir
}

func TestWatchdog_OtherBootTrips(t *testing.T) {
	setWatchdogPaths(t, "boot-1")
	if err := Arm(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(BootIDPath, []byte("boot-2\n"), 0o644)
	if reason, _ := WatchdogCheck(""); reason == "" {
		t.Fatal("перезагрузка в окне не поймана")
	}
}

func TestWatchdog_SameBootDoesNotTrip(t *testing.T) {
	setWatchdogPaths(t, "boot-1")
	_ = Arm()
	if reason, _ := WatchdogCheck(""); reason != "" {
		t.Fatalf("ложное срабатывание: %s", reason)
	}
}

const oopsTrace = `{"version":1,"board":"KN-1810","timestamp":1,"hash":"H1","content":"Modules linked in: awgm_relay udp_tunnel\nepc   : 8e20a1b4 relay_stop+0x4c/0x1a0 [awgm_relay]\nra    : 8e20a3c0 awgmr_relay_del+0x60/0x90 [awgm_relay]\n"}`

func TestWatchdog_OopsInOurTraceTrips(t *testing.T) {
	setWatchdogPaths(t, "boot-1")
	_ = os.WriteFile(OopsPath, []byte(oopsTrace+"\x00\x00"), 0o644)
	reason, hash := WatchdogCheck("")
	if reason == "" || hash != "H1" {
		t.Fatalf("reason=%q hash=%q", reason, hash)
	}
	if reason2, _ := WatchdogCheck("H1"); reason2 != "" {
		t.Fatal("уже обработанный oops сработал повторно")
	}
}

// Review Focus 5: oops другого модуля при загруженном awgm_relay — не наш.
func TestOopsOurs_ModulesLinkedInOnly(t *testing.T) {
	c := "Modules linked in: awgm_relay awg_proxy\nepc   : 8e30 foo+0x4/0x10 [awg_proxy]\nCall Trace:\n[<8e30>] foo+0x4/0x10 [awg_proxy]\n"
	if OopsOurs(c) {
		t.Fatal("чужой oops принят за наш")
	}
	if !OopsOurs(strings.Replace(c, "foo+0x4/0x10 [awg_proxy]\nCall", "foo+0x4/0x10 [awgm_relay]\nCall", 1)) {
		t.Fatal("наш кадр в epc не пойман")
	}
	if !OopsOurs("PC is at relay_stop+0x4c/0x1a0 [awgm_relay]\n") {
		t.Fatal("arm64-кадр не пойман")
	}
}

// Каталога метки может не быть — Arm обязан его создать, иначе метка не
// пишется и сторож слеп к перезагрузке в окне (M1).
func TestArm_CreatesMissingDir(t *testing.T) {
	dir := setWatchdogPaths(t, "boot-1")
	ArmPath = filepath.Join(dir, "modules", "awgm_relay.arming")
	if err := Arm(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(ArmPath); err != nil || strings.TrimSpace(string(b)) != "boot-1" {
		t.Fatalf("метка: %q %v", b, err)
	}
}

// F477 M7: метка давнего сеанса (демон не снял её — упал до таймера) не
// повод выключать ядро при любой будущей перезагрузке.
func TestWatchdog_StaleMarkDoesNotTrip(t *testing.T) {
	setWatchdogPaths(t, "boot-1")
	if err := Arm(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(ArmPath, old, old); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(BootIDPath, []byte("boot-2\n"), 0o644)
	if reason, _ := WatchdogCheck(""); reason != "" {
		t.Fatalf("ложное срабатывание по старой метке: %s", reason)
	}
	if _, err := os.Stat(ArmPath); !os.IsNotExist(err) {
		t.Fatal("старая метка не снята")
	}
}

// F477: не прочитанный boot_id — не доказательство перезагрузки; и метку без
// boot_id писать нельзя.
func TestWatchdog_UnreadableBootID(t *testing.T) {
	setWatchdogPaths(t, "boot-1")
	if err := Arm(); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(BootIDPath)
	if reason, _ := WatchdogCheck(""); reason != "" {
		t.Fatalf("срабатывание без boot_id: %s", reason)
	}
	if err := Arm(); err == nil {
		t.Fatal("метка без boot_id записана молча")
	}
}
