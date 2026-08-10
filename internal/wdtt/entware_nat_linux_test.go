//go:build linux

package wdtt

import (
	"reflect"
	"testing"
)

func TestMasqueradeMatchArgs(t *testing.T) {
	plan := entwareNATPlan{Iface: "wdttraw0", CIDR: "10.70.0.0/16"}
	full := masqueradeMatchArgs(plan, "full", "eth3")
	wantFull := []string{"-s", "10.70.0.0/16", "!", "-o", "wdttraw0",
		"-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE"}
	if !reflect.DeepEqual(full, wantFull) {
		t.Fatalf("full: %v, want %v", full, wantFull)
	}
	inet := masqueradeMatchArgs(plan, "internet-only", "eth3")
	wantInet := []string{"-s", "10.70.0.0/16", "-o", "eth3",
		"-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE"}
	if !reflect.DeepEqual(inet, wantInet) {
		t.Fatalf("internet-only: %v, want %v", inet, wantInet)
	}
	// internet-only без известного WAN — деградация в full-форму, не в слепой -o ""
	if got := masqueradeMatchArgs(plan, "internet-only", ""); !reflect.DeepEqual(got, wantFull) {
		t.Fatalf("internet-only без WAN: %v, want %v", got, wantFull)
	}
}
