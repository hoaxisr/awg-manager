package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// #567: CRUD композитов fakeip обязан валидировать member-теги — молча
// сохранённый мёртвый член валит enable fakeip-tun кросс-слот валидацией
// («unknown-outbound») с откатом в «Выключен», и пользователь не понимает,
// что чинить.
func TestFakeIPCompositeOutbound_RejectsUnknownMembers(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()

	// fakeipWithConfig требует provisioned-состояния.
	all, err0 := svc.deps.Settings.Load()
	if err0 != nil {
		t.Fatal(err0)
	}
	all.OpkgTun = &storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 0, FakeIP: &storage.OpkgTunFakeIPData{Inet4Range: "198.18.0.0/15"}}
	if err0 := svc.deps.Settings.Save(all); err0 != nil {
		t.Fatal(err0)
	}

	// Атомарный выход в fakeip-слоте — валидный член.
	if err := svc.fakeipWithConfig(ctx, "outbounds", func(c *RouterConfig) error {
		c.Outbounds = append(c.Outbounds,
			Outbound{Tag: "have", Type: "socks", Server: "1.2.3.4"},
			Outbound{Tag: "have2", Type: "socks", Server: "5.6.7.8"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err := svc.FakeIPAddCompositeOutbound(ctx, Outbound{
		Tag: "combo", Type: "urltest", Outbounds: []string{"have", "ghost-1"},
	})
	if err == nil {
		t.Fatal("composite with unknown member must be rejected")
	}
	if !errors.Is(err, ErrCompositeMemberUnknown) {
		t.Fatalf("error must wrap ErrCompositeMemberUnknown (API maps it to 400), got: %v", err)
	}
	if !strings.Contains(err.Error(), "ghost-1") {
		t.Fatalf("error must name the unknown member, got: %v", err)
	}

	// Все члены известны (слотовые выходы) — принимается.
	if err := svc.FakeIPAddCompositeOutbound(ctx, Outbound{
		Tag: "combo", Type: "urltest", Outbounds: []string{"have", "have2"},
	}); err != nil {
		t.Fatalf("valid composite rejected: %v", err)
	}

	// Update с мёртвым членом — тоже отказ.
	err = svc.FakeIPUpdateCompositeOutbound(ctx, "combo", Outbound{
		Tag: "combo", Type: "urltest", Outbounds: []string{"ghost-2", "ghost-3"},
	})
	if err == nil {
		t.Fatal("update with unknown members must be rejected")
	}
	if !strings.Contains(err.Error(), "ghost-2") || !strings.Contains(err.Error(), "ghost-3") {
		t.Fatalf("error must name ALL unknown members, got: %v", err)
	}
}

// #567: переименование внешнего выхода (тег туннеля) обязано чинить ссылки и
// в fakeip-слоте — раньше переписывался только 20-router.json, и член
// композита в 21-fakeip.json повисал.
func TestRenameExternalOutboundTag_RewritesFakeIPSlot(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	ctx := context.Background()

	fakeipCfg := `{"outbounds":[{"tag":"FI+LV","type":"urltest","outbounds":["old-tun","direct"]}],"route":{"final":"direct","rules":[{"action":"route","outbound":"old-tun","domain":["example.com"]}]}}`

	t.Run("active slot", func(t *testing.T) {
		if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotFakeIP, true); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "21-fakeip.json"), []byte(fakeipCfg), 0644); err != nil {
			t.Fatal(err)
		}
		if err := svc.RenameExternalOutboundTag(ctx, "old-tun", "new-tun"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "21-fakeip.json"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "old-tun") || !strings.Contains(string(data), "new-tun") {
			t.Fatalf("active fakeip slot not rewritten: %s", data)
		}
	})

	t.Run("parked slot", func(t *testing.T) {
		if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotFakeIP, false); err != nil {
			t.Fatal(err)
		}
		parked := filepath.Join(dir, "disabled", "21-fakeip.json")
		if err := os.WriteFile(parked, []byte(fakeipCfg), 0644); err != nil {
			t.Fatal(err)
		}
		if err := svc.RenameExternalOutboundTag(ctx, "old-tun", "new-tun"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(parked)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "old-tun") || !strings.Contains(string(data), "new-tun") {
			t.Fatalf("parked fakeip slot not rewritten: %s", data)
		}
	})
}

// #567: guard удаления туннеля должен видеть ссылки из fakeip-слота —
// раньше он смотрел только 20-router.json, и туннель удалялся, оставляя
// висячий член композита в 21-fakeip.json.
func TestOutboundReferenceLocations_IncludesFakeIPSlot(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	fakeipCfg := `{"outbounds":[{"tag":"FI+LV","type":"urltest","outbounds":["tun-x","direct"]}],"route":{"final":"direct","rules":[{"action":"route","outbound":"tun-x","domain":["example.com"]}]}}`
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotFakeIP, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "21-fakeip.json"), []byte(fakeipCfg), 0644); err != nil {
		t.Fatal(err)
	}

	// Точные строки, а не Contains: формат — контракт с фронтом.
	// frontend/src/lib/utils/tunnelRefs.ts разбирает префикс слота и
	// route.rules[N] регулярками, молча падая в raw-путь при расхождении.
	locs := svc.OutboundReferenceLocations("tun-x")
	want := []string{
		`[fakeip] route.rules[0]`,
		`[fakeip] outbounds[0="FI+LV"].outbounds[0]`,
	}
	if !slices.Equal(locs, want) {
		t.Fatalf("fakeip references = %q, want %q", locs, want)
	}
	if locs := svc.OutboundReferenceLocations("absent-tag"); len(locs) != 0 {
		t.Fatalf("unreferenced tag must give no locations, got %v", locs)
	}
}

// #567 (follow-up): tproxy-CRUD композитов валидирует member-теги тем же
// хелпером — симметрично fakeip, чтобы мёртвый член не доезжал до конфига.
func TestCompositeOutbound_TproxyRejectsUnknownMembers(t *testing.T) {
	svc, dir := newOrchedTestService(t)
	ctx := context.Background()

	// Router-слот с двумя атомарными выходами — валидные члены.
	routerCfg := `{"outbounds":[{"tag":"have","type":"socks","server":"1.2.3.4"},{"tag":"have2","type":"socks","server":"5.6.7.8"},{"tag":"direct","type":"direct"}],"route":{"final":"direct","rules":[]}}`
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"), []byte(routerCfg), 0644); err != nil {
		t.Fatal(err)
	}

	err := svc.AddCompositeOutbound(ctx, Outbound{
		Tag: "combo", Type: "urltest", Outbounds: []string{"have", "ghost-1"},
	})
	if err == nil {
		t.Fatal("tproxy composite with unknown member must be rejected")
	}
	if !errors.Is(err, ErrCompositeMemberUnknown) {
		t.Fatalf("error must wrap ErrCompositeMemberUnknown, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ghost-1") {
		t.Fatalf("error must name the unknown member, got: %v", err)
	}

	// Все члены известны — принимается.
	if err := svc.AddCompositeOutbound(ctx, Outbound{
		Tag: "combo", Type: "urltest", Outbounds: []string{"have", "have2"},
	}); err != nil {
		t.Fatalf("valid tproxy composite rejected: %v", err)
	}

	// Update с мёртвым членом — отказ.
	err = svc.UpdateCompositeOutbound(ctx, "combo", Outbound{
		Tag: "combo", Type: "urltest", Outbounds: []string{"ghost-2"},
	})
	if err == nil || !errors.Is(err, ErrCompositeMemberUnknown) {
		t.Fatalf("tproxy update with unknown member must be rejected, got: %v", err)
	}
}
