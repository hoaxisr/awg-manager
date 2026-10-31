package localdeps

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestLocal_DNSRewriteUnavailable(t *testing.T) {
	l := New(Config{})
	ctx := context.Background()
	in := mcpsrv.SingboxDNSRewriteInput{Pattern: "test.invalid", IPs: []string{"192.0.2.1"}}
	if _, err := l.AddSingboxDNSRewrite(ctx, in); err == nil {
		t.Fatal("missing service accepted add")
	}
	if _, err := l.UpdateSingboxDNSRewrite(ctx, 0, "revision", in); err == nil {
		t.Fatal("missing service accepted update")
	}
	if _, err := l.DeleteSingboxDNSRewrite(ctx, 0, "revision"); err == nil {
		t.Fatal("missing service accepted delete")
	}
}

type rewriteSlot struct {
	data    []byte
	enabled bool
	saves   int
	err     error
}

func (s *rewriteSlot) Save(_ string, data []byte) error {
	if s.err != nil {
		return s.err
	}
	s.data = append([]byte(nil), data...)
	s.saves++
	return nil
}
func (s *rewriteSlot) SetEnabled(_ string, on bool) error { s.enabled = on; return nil }

func TestLocal_DNSRewriteRealService(t *testing.T) {
	ctx := context.Background()
	store := storage.NewDNSRewriteStore(filepath.Join(t.TempDir(), "rewrites.json"))
	slot := &rewriteSlot{}
	svc := dnsrewrite.NewService(store, slot)
	l := New(Config{DNSRewrites: svc, Router: &fakeRouter{settings: storage.SingboxRouterSettings{RoutingMode: "fakeip-tun"}}})
	in := mcpsrv.SingboxDNSRewriteInput{Pattern: "NODE*.Example.Invalid.", IPs: []string{"192.0.2.1", "2001:db8::1"}}
	out, err := l.AddSingboxDNSRewrite(ctx, in)
	if err != nil || !out.Saved || !out.StateKnown || len(out.Rewrites) != 1 || !slot.enabled {
		t.Fatalf("add: %+v %v", out, err)
	}
	row := out.Rewrites[0]
	if row.Pattern != "node*.example.invalid" || row.Index != 0 || len(row.Revision) != 64 {
		t.Fatalf("read-back: %+v", row)
	}
	var config struct {
		DNS struct {
			Rules []map[string]any `json:"rules"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(slot.data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.DNS.Rules) != 2 || !strings.Contains(string(slot.data), "IN A 192.0.2.1") || !strings.Contains(string(slot.data), "IN AAAA 2001:db8::1") {
		t.Fatalf("dual-stack slot: %s", slot.data)
	}
	// The web edits only IPs. A pattern-only precondition would lose this change.
	if err := svc.Update(0, dnsrewrite.DNSRewrite{Pattern: row.Pattern, IPs: []string{"192.0.2.2"}}); err != nil {
		t.Fatal(err)
	}
	calls := slot.saves
	if _, err := l.UpdateSingboxDNSRewrite(ctx, 0, row.Revision, in); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("stale update: %v", err)
	}
	if slot.saves != calls {
		t.Fatal("stale MCP edit rebuilt the slot")
	}
	items, err := l.ListSingboxDNSRewrites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err = l.UpdateSingboxDNSRewrite(ctx, 0, items[0].Revision, mcpsrv.SingboxDNSRewriteInput{Pattern: "nas.invalid", IPs: []string{"2001:db8::2"}})
	if err != nil || out.Rewrites[0].Pattern != "nas.invalid" {
		t.Fatalf("update: %+v %v", out, err)
	}
	if strings.Contains(string(slot.data), "IN A ") || !strings.Contains(string(slot.data), "IN AAAA 2001:db8::2") {
		t.Fatalf("opposite family must be NODATA: %s", slot.data)
	}
	out, err = l.DeleteSingboxDNSRewrite(ctx, 0, out.Rewrites[0].Revision)
	if err != nil || !out.StateKnown || len(out.Rewrites) != 0 || slot.enabled {
		t.Fatalf("delete last entry: %+v %v enabled=%t", out, err, slot.enabled)
	}
	// Managed entries are protected even with a fresh revision.
	if err := store.ReplaceManaged("preset", []dnsrewrite.DNSRewrite{{Pattern: "managed.invalid", IPs: []string{"192.0.2.1"}}}); err != nil {
		t.Fatal(err)
	}
	items, err = l.ListSingboxDNSRewrites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.DeleteSingboxDNSRewrite(ctx, 0, items[0].Revision); err == nil || !strings.Contains(err.Error(), "preset") {
		t.Fatalf("managed delete: %v", err)
	}
}

type readbackFailRewrites struct{ DNSRewriteService }

func (s readbackFailRewrites) List() ([]dnsrewrite.DNSRewrite, error) {
	return nil, errors.New("private-readback-path")
}

func TestLocal_DNSRewriteFailuresAreHonest(t *testing.T) {
	ctx := context.Background()
	store := storage.NewDNSRewriteStore(filepath.Join(t.TempDir(), "rewrites.json"))
	slot := &rewriteSlot{err: errors.New("private-slot-path")}
	svc := dnsrewrite.NewService(store, slot)
	journal := &recLog{}
	l := New(Config{DNSRewrites: svc, AppLog: journal})
	in := mcpsrv.SingboxDNSRewriteInput{Pattern: "nas.invalid", IPs: []string{"192.0.2.1"}}
	_, err := l.AddSingboxDNSRewrite(ctx, in)
	if err == nil || !strings.Contains(err.Error(), "may already have changed") || strings.Contains(err.Error(), "private-slot-path") {
		t.Fatalf("flush failure: %v", err)
	}
	if len(journal.lines) != 1 || !strings.Contains(journal.lines[0], "routing/singbox-router") || !strings.Contains(journal.lines[0], "(MCP)") || strings.Contains(journal.lines[0], "private-slot-path") {
		t.Fatalf("unsafe or incorrectly scoped journal: %v", journal.lines)
	}
	items, err := store.List()
	if err != nil || len(items) != 1 {
		t.Fatalf("the real service saves before flushing: %+v %v", items, err)
	}
	slot.err = nil
	l = New(Config{DNSRewrites: readbackFailRewrites{svc}})
	out, err := l.AddSingboxDNSRewrite(ctx, in)
	if err != nil || !out.Saved || out.StateKnown || out.Rewrites == nil {
		t.Fatalf("read-back failure: %+v %v", out, err)
	}
	items, err = store.List()
	if err != nil || len(items) != 2 {
		t.Fatalf("saved state: %+v %v", items, err)
	}
	// Bad caller input never reaches even the store half of the service.
	before := items
	if _, err := l.AddSingboxDNSRewrite(ctx, mcpsrv.SingboxDNSRewriteInput{Pattern: "bad\nname", IPs: in.IPs}); err == nil {
		t.Fatal("adapter accepted control characters")
	}
	after, _ := store.List()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalid input was persisted")
	}
}
