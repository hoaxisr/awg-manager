package command

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func TestDHCPCommands_SetPoolDNS(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	cmds := NewDHCPCommands(poster, sc, q)
	if err := cmds.SetPoolDNS(context.Background(), "_WEBADMIN", []string{"172.18.0.2", "192.168.0.1"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	parse := poster.Payloads()[0].(map[string]any)["parse"].(string)
	if parse != "ip dhcp pool _WEBADMIN dns-server 172.18.0.2 192.168.0.1" {
		t.Errorf("parse=%q", parse)
	}
}

func TestDHCPCommands_ClearPoolDNS(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	cmds := NewDHCPCommands(poster, sc, q)
	if err := cmds.ClearPoolDNS(context.Background(), "_WEBADMIN"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	parse := poster.Payloads()[0].(map[string]any)["parse"].(string)
	if parse != "ip dhcp pool _WEBADMIN no dns-server" {
		t.Errorf("parse=%q", parse)
	}
}
