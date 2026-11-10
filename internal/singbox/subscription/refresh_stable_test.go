package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

type stableRig struct {
	svc  *Service
	gs   *GroupStore
	dir  string
	ids  []string
	feed *mlkemFeed
}

// newStableRig: настоящий адаптер и оркестратор, две подписки и сводная
// группа над первой.
func newStableRig(t *testing.T) *stableRig {
	t.Helper()
	var b strings.Builder
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&b, "vless://3a3b1c2e-9999-4321-aaaa-1234567890a%d@h%d.example:443?security=tls&sni=h#n%d\n", i, i, i)
	}
	r := &stableRig{feed: newMLKEMFeed(t, b.String()), dir: t.TempDir()}
	orch := orchestrator.NewWithAppliedPath(r.dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	t.Cleanup(orch.Close)
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	orch.SetValidator(&countingValidator{})
	store, err := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.svc = NewService(store, NewOperatorAdapter(orch, &recProxies{}, nil))
	withLegacySetupNoop(r.svc)
	if r.gs, err = NewGroupStore(filepath.Join(t.TempDir(), "groups.json")); err != nil {
		t.Fatal(err)
	}
	r.svc.SetGroupStore(r.gs)
	ctx := context.Background()
	for i, mode := range []SubscriptionMode{ModeSelector, ModeURLTest} {
		sub, err := r.svc.Create(ctx, CreateInput{Label: string(mode), URL: r.feed.url, Enabled: true, Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		r.ids = append(r.ids, sub.ID)
		if i == 0 {
			if _, err := r.svc.CreateGroup(ctx, GroupCreateInput{Label: "g", UseSubscriptionIDs: []string{sub.ID}, Enabled: true}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return r
}

func (r *stableRig) slot(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.dir, "13-subscriptions.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (r *stableRig) tags(t *testing.T) []string {
	t.Helper()
	var cfg struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(r.slot(t), &cfg); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, ob := range cfg.Outbounds {
		tags = append(tags, ob.Tag)
	}
	return tags
}

// #1174 (F779): обновление подписки с тем же содержимым не должно менять
// слот. Иначе оркестратор перезагружает sing-box, а с tun это моргание
// интерфейса. Раньше селектор снимался и добавлялся в конец, и при двух
// подписках каждое обновление переставляло их селекторы.
func TestRefreshSameBodyKeepsSlotBytes(t *testing.T) {
	r := newStableRig(t)
	before := r.slot(t)
	for round := 0; round < 3; round++ {
		for _, id := range r.ids {
			if _, err := r.svc.Refresh(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(r.slot(t), before) {
				t.Fatalf("round %d: refresh of %s changed the slot", round, id)
			}
		}
	}
}

// Смена режима пересобирает селектор на месте, а не в конце слота.
func TestModeChangeKeepsSelectorPosition(t *testing.T) {
	r := newStableRig(t)
	before := r.tags(t)
	mode := ModeFallback
	if _, err := r.svc.Update(r.ids[0], UpdatePatch{Mode: &mode}); err != nil {
		t.Fatal(err)
	}
	if after := r.tags(t); !slices.Equal(after, before) {
		t.Fatalf("outbound order changed:\nbefore %v\nafter  %v", before, after)
	}
}
