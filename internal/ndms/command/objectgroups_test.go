package command

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newTestObjectGroupCommands(_ *testing.T) (*ObjectGroupCommands, *fakePoster) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	return NewObjectGroupCommands(poster, sc, q), poster
}

func TestObjectGroupCommands_DeleteGroups(t *testing.T) {
	cmds, poster := newTestObjectGroupCommands(t)
	_ = cmds.DeleteGroups(context.Background(), []string{"g1", "g2"})
	fqdn := poster.Payloads()[0].(map[string]any)["object-group"].(map[string]any)["fqdn"].(map[string]any)
	if fqdn["g1"].(map[string]any)["no"] != true {
		t.Errorf("g1 no: %#v", fqdn["g1"])
	}
	if fqdn["g2"].(map[string]any)["no"] != true {
		t.Errorf("g2 no: %#v", fqdn["g2"])
	}
}

func TestObjectGroupCommands_DeleteGroups_EmptyNoOp(t *testing.T) {
	cmds, poster := newTestObjectGroupCommands(t)
	if err := cmds.DeleteGroups(context.Background(), nil); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if poster.Calls() != 0 {
		t.Errorf("POST on empty: %d", poster.Calls())
	}
}

func TestObjectGroupCommands_UpsertGroup_AddsAndRemoves(t *testing.T) {
	cmds, poster := newTestObjectGroupCommands(t)
	_ = cmds.UpsertGroup(context.Background(), FQDNGroupMutation{
		Name:           "my-group",
		AddIncludes:    []string{"example.com"},
		RemoveIncludes: []string{"gone.com"},
		AddExcludes:    []string{"bad.com"},
	})
	fqdn := poster.Payloads()[0].(map[string]any)["object-group"].(map[string]any)["fqdn"].(map[string]any)
	g := fqdn["my-group"].(map[string]any)
	includes := g["include"].([]any)
	if len(includes) != 2 {
		t.Fatalf("includes len: %d", len(includes))
	}
	rm := includes[0].(map[string]any)
	if rm["no"] != true || rm["address"] != "gone.com" {
		t.Errorf("remove[0]: %#v", rm)
	}
	ad := includes[1].(map[string]any)
	if ad["address"] != "example.com" {
		t.Errorf("add[0]: %#v", ad)
	}
	if _, ok := ad["no"]; ok {
		t.Errorf("add must not have 'no': %#v", ad)
	}
	excludes := g["exclude"].([]any)
	if len(excludes) != 1 {
		t.Errorf("excludes len: %d", len(excludes))
	}
}

func TestObjectGroupCommands_UpsertGroup_EmptyNoOp(t *testing.T) {
	cmds, poster := newTestObjectGroupCommands(t)
	if err := cmds.UpsertGroup(context.Background(), FQDNGroupMutation{Name: "g"}); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if poster.Calls() != 0 {
		t.Errorf("POST on empty: %d", poster.Calls())
	}
}

func TestObjectGroupCommands_UpsertGroup_RejectsEmptyName(t *testing.T) {
	cmds, _ := newTestObjectGroupCommands(t)
	err := cmds.UpsertGroup(context.Background(), FQDNGroupMutation{
		AddIncludes: []string{"example.com"},
	})
	if err == nil {
		t.Errorf("empty name: want error, got nil")
	}
}
