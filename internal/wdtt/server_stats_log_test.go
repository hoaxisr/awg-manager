package wdtt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func TestNormalizeStatsLogMode(t *testing.T) {
	if got := normalizeStatsLogMode(""); got != statsLogModeRAM {
		t.Fatalf("empty=%q", got)
	}
	if got := normalizeStatsLogMode("OFF"); got != statsLogModeOff {
		t.Fatalf("off=%q", got)
	}
}

func TestRedirectServerStatsLog_RAM(t *testing.T) {
	dir := t.TempDir()
	if err := redirectServerStatsLog(dir, "default", statsLogModeRAM); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	if link != "/tmp/awg-wdtt-server-default.log" {
		t.Fatalf("link=%q", link)
	}
}

func TestRedirectServerStatsLog_Off(t *testing.T) {
	dir := t.TempDir()
	if err := redirectServerStatsLog(dir, "srv1", statsLogModeOff); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	if link != "/dev/null" {
		t.Fatalf("link=%q", link)
	}
}

func TestOpkgPolicyNeedsDefault(t *testing.T) {
	sole := ndms.Policy{Name: "Policy3", Interfaces: []ndms.PermittedIface{{Name: "OpkgTun18", Order: 0}}}
	if !opkgPolicyNeedsDefault(sole, "OpkgTun18") {
		t.Fatal("sole opkg should need default")
	}
	multi := ndms.Policy{Name: "Policy0", Interfaces: []ndms.PermittedIface{
		{Name: "ISP", Order: 0},
		{Name: "OpkgTun18", Order: 1},
	}}
	if opkgPolicyNeedsDefault(multi, "OpkgTun18") {
		t.Fatal("second opkg should not auto-default")
	}
	first := ndms.Policy{Name: "Policy0", Interfaces: []ndms.PermittedIface{
		{Name: "OpkgTun18", Order: 0},
		{Name: "ISP", Order: 1},
	}}
	if !opkgPolicyNeedsDefault(first, "OpkgTun18") {
		t.Fatal("first opkg should need default")
	}
	multiWG := ndms.Policy{Name: "Policy3", Interfaces: []ndms.PermittedIface{
		{Name: "Wireguard2", Order: 0},
		{Name: "OpkgTun17", Order: 1},
	}}
	if opkgPolicySoleUplink(multiWG, "OpkgTun17") {
		t.Fatal("multi-uplink opkg is not sole")
	}
}
