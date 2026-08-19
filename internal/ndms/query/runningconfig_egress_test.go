package query

import (
	"context"
	"reflect"
	"testing"
)

func TestGlobalEgressInterfaces(t *testing.T) {
	fg := NewFakeGetter()
	fg.SetJSON("/show/running-config", `{"message":[
		"interface PPPoE0",
		"    description WAN",
		"    ip global 32767",
		"interface Wireguard0",
		"    ip address 10.10.0.1 255.255.255.0",
		"interface Wireguard2",
		"    ip global auto",
		"interface OpkgTun0",
		"    ip global 100",
		"ip nat Bridge0"
	]}`)
	s := NewRunningConfigStore(fg, NopLogger())

	got, err := s.GlobalEgressInterfaces(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"PPPoE0", "Wireguard2", "OpkgTun0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
