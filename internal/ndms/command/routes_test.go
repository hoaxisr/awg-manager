package command

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newTestRouteCommands(_ *testing.T) (*RouteCommands, *fakePoster) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewRouteCommands(poster, sc, q), poster
}

func TestRouteCommands_SetDefaultRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.SetDefaultRoute(context.Background(), "PPPoE0")
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["default"] != true || r["interface"] != "PPPoE0" {
		t.Errorf("set default: %#v", r)
	}
	if _, ok := r["no"]; ok {
		t.Errorf("no must be absent on set")
	}
}

func TestRouteCommands_RemoveDefaultRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveDefaultRoute(context.Background(), "PPPoE0")
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["no"] != true {
		t.Errorf("remove default: %#v", r)
	}
}

func TestRouteCommands_SetIPv6DefaultRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.SetIPv6DefaultRoute(context.Background(), "PPPoE0")
	p := poster.Payloads()[0].(map[string]any)
	if _, ok := p["ipv6"]; !ok {
		t.Errorf("ipv6 key missing: %#v", p)
	}
}

func TestRouteCommands_RemoveIPv6DefaultRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveIPv6DefaultRoute(context.Background(), "PPPoE0")
	r := poster.Payloads()[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
	if r["no"] != true {
		t.Errorf("remove ipv6 default: %#v", r)
	}
}

func TestRouteCommands_RemoveHostRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveHostRoute(context.Background(), "1.2.3.4")
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["host"] != "1.2.3.4" || r["no"] != true {
		t.Errorf("remove host: %#v", r)
	}
}

// Стенд 5.01: v4-форма с v6-адресом отвергается («invalid destination host»),
// а `ipv6.route.host` целится в ::/0 — то есть в дефолтный маршрут. Снимать
// v6 host-route можно только через prefix с /128.
// F120: NDMS держит запись на КАЖДЫЙ интерфейс, и форма без interface снимает
// ровно одну, отвечая «system failed» на остатке. Стенд 5.01: две записи —
// первая команда убирает одну и отказывает, вторая убирает последнюю. Без
// повтора записи копились бы при каждой смене WAN.
func TestRouteCommands_RemoveHostRoute_ClearsEveryEntry(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	poster.SetErrorFor(1) // одна лишняя запись: первый вызов отказывает

	if err := cmds.RemoveHostRoute(context.Background(), "203.0.113.77"); err != nil {
		t.Fatalf("снятие обязано доводиться до конца: %v", err)
	}
	if n := len(poster.Payloads()); n != 2 {
		t.Fatalf("ждали два вызова (по записи на интерфейс), получили %d", n)
	}
}

// Отказ, который не кончается, не превращается в бесконечный цикл и доезжает
// до вызывающего.
func TestRouteCommands_RemoveHostRoute_GivesUpOnPersistentFailure(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	poster.SetErrorFor(100)

	if err := cmds.RemoveHostRoute(context.Background(), "203.0.113.77"); err == nil {
		t.Fatal("постоянный отказ обязан доехать до вызывающего")
	}
	// Число литералом, а не через саму константу: иначе тест проверяет код
	// против себя же и переживёт поднятие потолка, ради которого его и пишут.
	if n := len(poster.Payloads()); n != 4 {
		t.Errorf("попыток %d, ждали 4", n)
	}
}

// Настоящий отказ повторять незачем: каждая попытка стоит save.Request() и
// двух инвалидаций, а под замком оркестратора — ещё и времени.
func TestRouteCommands_RemoveHostRoute_RealErrorIsNotRetried(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	poster.SetError(errors.New("boom"))

	if err := cmds.RemoveHostRoute(context.Background(), "203.0.113.77"); err == nil {
		t.Fatal("отказ обязан доехать до вызывающего")
	}
	if n := len(poster.Payloads()); n != 1 {
		t.Fatalf("ждали одну попытку, получили %d", n)
	}
}

func TestRouteCommands_RemoveHostRoute_V6UsesPrefix(t *testing.T) {
	for _, host := range []string{
		"2001:db8::1",
		"2001:db8:0:0:0:0:0:1", // развёрнутая форма — без «::» в строке
		"fe80::1",
	} {
		t.Run(host, func(t *testing.T) {
			cmds, poster := newTestRouteCommands(t)
			_ = cmds.RemoveHostRoute(context.Background(), host)
			if n := len(poster.Payloads()); n != 1 {
				t.Fatalf("ждали ровно один запрос, получили %d: %#v", n, poster.Payloads())
			}
			payload := poster.Payloads()[0].(map[string]any)
			if _, v4 := payload["ip"]; v4 {
				t.Fatalf("v6-адрес ушёл v4-формой: %#v", payload)
			}
			r := payload["ipv6"].(map[string]any)["route"].(map[string]any)
			if r["prefix"] != host+"/128" || r["no"] != true {
				t.Errorf("remove ipv6 host: %#v", r)
			}
			// Ровно два ключа: `host` в v6-форме удаляет ::/0, а снятие без
			// интерфейса стенд 5.01 принял («deleted static route:
			// 2001:db8::2/128 via PPPoE0») — интерфейс здесь не нужен.
			if len(r) != 2 {
				t.Errorf("лишние ключи в v6-форме: %#v", r)
			}
		})
	}
}

// Не-IP уходит v4-формой: отказ NDMS виден в журнале, а не превращается в
// «/128» из мусора.
func TestRouteCommands_RemoveHostRoute_UnparsableStaysV4(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveHostRoute(context.Background(), "vpn.example.com:51820")
	payload := poster.Payloads()[0].(map[string]any)
	if _, v6 := payload["ipv6"]; v6 {
		t.Fatalf("неразобранный адрес ушёл v6-формой: %#v", payload)
	}
}

// F236: host-маршрут у v6 выражается как prefix с /128 — стенд 5.01 принял
// `{prefix, interface, auto, comment}` и сохранил запись
// `ipv6 route 2001:db8::2/128 PPPoE0 auto !awgm-test`. Раньше v6-ветка ключ
// Host игнорировала, и для обфусцированного туннеля с v6-таргетом маршрут не
// вставал вовсе.
func TestRouteCommands_AddStaticRoute_V6Host(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.AddStaticRoute(context.Background(), StaticRouteSpec{
		Host: "2001:db8::1", Interface: "PPPoE0", Comment: "awgm-obfuscator awg10", V6: true,
	})
	r := poster.Payloads()[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
	if r["prefix"] != "2001:db8::1/128" || r["interface"] != "PPPoE0" || r["auto"] != true {
		t.Errorf("add ipv6 host: %#v", r)
	}
	if r["comment"] != "awgm-obfuscator awg10" {
		t.Errorf("комментарий владения потерян: %#v", r)
	}
	if _, ok := r["host"]; ok {
		t.Errorf("ключ host в v6-форме NDMS молча отбрасывает: %#v", r)
	}
}

func TestRouteCommands_RemoveStaticRoute_V6Host(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveStaticRoute(context.Background(), StaticRouteSpec{
		Host: "2001:db8::1", Interface: "PPPoE0", V6: true,
	})
	r := poster.Payloads()[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
	if r["prefix"] != "2001:db8::1/128" || r["no"] != true {
		t.Errorf("remove ipv6 host: %#v", r)
	}
}

// F238, стенд 5.01: `ipv6 route 2001:db8:bb::/48 PPPoE0 auto reject`.
func TestRouteCommands_AddStaticRoute_V6Reject(t *testing.T) {
	for _, tc := range []struct {
		name       string
		spec       StaticRouteSpec
		wantPrefix string
	}{
		{"сеть", StaticRouteSpec{Network: "2001:db8:bb::/48", Interface: "PPPoE0", Reject: true, V6: true}, "2001:db8:bb::/48"},
		{"хост", StaticRouteSpec{Host: "2001:db8::1", Interface: "PPPoE0", Reject: true, V6: true}, "2001:db8::1/128"},
		{"с меткой владения", StaticRouteSpec{Network: "2001:db8:bb::/48", Interface: "PPPoE0", Reject: true, Comment: "awgm-drain", V6: true}, "2001:db8:bb::/48"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds, poster := newTestRouteCommands(t)
			if err := cmds.AddStaticRoute(context.Background(), tc.spec); err != nil {
				t.Fatalf("AddStaticRoute: %v", err)
			}
			r := poster.Payloads()[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
			if r["auto"] != true {
				t.Errorf("reject-маршрут без auto NDMS не переигрывает на iface up: %#v", r)
			}
			if r["reject"] != true {
				t.Errorf("reject потерян — kill-switch стал обычным маршрутом: %#v", r)
			}
			if r["prefix"] != tc.wantPrefix || r["interface"] != "PPPoE0" {
				t.Errorf("add ipv6 reject: %#v", r)
			}
			if tc.spec.Comment != "" && r["comment"] != tc.spec.Comment {
				// Без метки владения маршрут не подберёт стартовый sweep.
				t.Errorf("метка владения потеряна рядом с reject: %#v", r)
			}
		})
	}
}

// Стенд 5.01: ЛЮБОЙ v6-маршрут без интерфейса роутер отвергает («no input») —
// и host-форма, и сетевая. Отказываем сами, а не отправляем заведомо мёртвый
// запрос; для reject это ещё и разница между kill-switch и утечкой.
func TestRouteCommands_V6WithoutInterface_Refused(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec StaticRouteSpec
	}{
		{"reject", StaticRouteSpec{Network: "2001:db8:bb::/48", Reject: true, V6: true}},
		{"сеть", StaticRouteSpec{Network: "2001:db8:bb::/48", V6: true}},
		{"хост", StaticRouteSpec{Host: "2001:db8::1", V6: true}},
		{"с меткой владения", StaticRouteSpec{Network: "2001:db8:bb::/48", Comment: "awgm-drain", V6: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds, poster := newTestRouteCommands(t)
			if err := cmds.AddStaticRoute(context.Background(), tc.spec); err == nil {
				t.Fatal("v6-маршрут без интерфейса обязан отказывать")
			}
			if n := len(poster.Payloads()); n != 0 {
				t.Fatalf("в NDMS ушёл заведомо отвергаемый запрос: %#v", poster.Payloads())
			}
		})
	}
}

// Запрос без подсети не отправляется вовсе: NDMS молча отбрасывает неизвестное
// поле, и снятие вырождается в удаление ::/0 — дефолтного маршрута.
// Заполнены оба поля — побеждает Network: у v6 сеть и хост выражаются одним
// ключом, и «сеть плюс хост» разошлись бы молча.
func TestRouteCommands_V6NetworkWinsOverHost(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.AddStaticRoute(context.Background(), StaticRouteSpec{
		Network: "2001:db8::/64", Host: "2001:db8::1", Interface: "PPPoE0", V6: true,
	})
	r := poster.Payloads()[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
	if r["prefix"] != "2001:db8::/64" {
		t.Errorf("приоритет полей разъехался: %#v", r)
	}
}

func TestRouteCommands_V6WithoutPrefix_Refused(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*RouteCommands) error
	}{
		{"add", func(c *RouteCommands) error {
			return c.AddStaticRoute(context.Background(), StaticRouteSpec{Interface: "PPPoE0", V6: true})
		}},
		{"remove", func(c *RouteCommands) error {
			return c.RemoveStaticRoute(context.Background(), StaticRouteSpec{Interface: "PPPoE0", V6: true})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds, poster := newTestRouteCommands(t)
			if err := tc.call(cmds); err == nil {
				t.Fatal("v6-маршрут без подсети обязан отказывать, а не уходить в NDMS")
			}
			if n := len(poster.Payloads()); n != 0 {
				t.Fatalf("в NDMS ушёл запрос без подсети: %#v", poster.Payloads())
			}
		})
	}
}

func TestRouteCommands_AddStaticRoute_Network(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.AddStaticRoute(context.Background(), StaticRouteSpec{
		Interface: "Wireguard0",
		Network:   "10.0.0.0",
		Mask:      "255.255.255.0",
		Reject:    true,
		Comment:   "test route",
	})
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["network"] != "10.0.0.0" || r["mask"] != "255.255.255.0" {
		t.Errorf("network/mask: %#v", r)
	}
	if r["auto"] != true || r["reject"] != true {
		t.Errorf("flags: %#v", r)
	}
	if r["comment"] != "test route" {
		t.Errorf("comment: %#v", r)
	}
	if _, ok := r["host"]; ok {
		t.Errorf("host must be absent for network route")
	}
}

func TestRouteCommands_AddStaticRoute_Host(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.AddStaticRoute(context.Background(), StaticRouteSpec{
		Interface: "Wireguard0",
		Host:      "8.8.8.8",
	})
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["host"] != "8.8.8.8" {
		t.Errorf("host: %#v", r)
	}
	if _, ok := r["network"]; ok {
		t.Errorf("network must be absent for host route")
	}
}

func TestRouteCommands_RemoveStaticRoute(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveStaticRoute(context.Background(), StaticRouteSpec{
		Interface: "Wireguard0",
		Network:   "10.0.0.0",
		Mask:      "255.255.255.0",
	})
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["no"] != true || r["network"] != "10.0.0.0" {
		t.Errorf("remove static: %#v", r)
	}
}

// TestRouteCommands_RemoveStaticRoute_RejectPayload documents the CURRENT
// behavior of RemoveStaticRoute when given a Reject:true spec (the fakeip drain
// removal): RemoveStaticRoute drops the `reject` key entirely and emits only
// {network, mask, no:true}. This is the payload the fakeip drain sends to take
// down the temporary reject route.
//
// STAND-GATE (Task 1F.1): whether this no:true / reject-key-less form actually
// MATCHES a reject:true route on live Keenetic RCI is UNVERIFIED and MUST be
// checked at the stand. If it does NOT match, the startup sweep in
// ReapOrphanedFakeIPTun (router Fix 1) is the safety net that removes the stale
// reject route on the next boot. This test pins current behavior only — do NOT
// change RemoveStaticRoute's payload speculatively; that is a stand decision.
func TestRouteCommands_RemoveStaticRoute_RejectPayload(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	_ = cmds.RemoveStaticRoute(context.Background(), StaticRouteSpec{
		Network: "10.128.0.0",
		Mask:    "255.192.0.0",
		Reject:  true,
	})
	r := poster.Payloads()[0].(map[string]any)["ip"].(map[string]any)["route"].(map[string]any)
	if r["network"] != "10.128.0.0" || r["mask"] != "255.192.0.0" {
		t.Errorf("network/mask: %#v", r)
	}
	if r["no"] != true {
		t.Errorf("no must be true on remove: %#v", r)
	}
	// Current behavior: the reject key is DROPPED on remove (UNVERIFIED match —
	// see the stand-gate note above).
	if _, ok := r["reject"]; ok {
		t.Errorf("reject key is currently expected to be ABSENT on remove (documents current behavior): %#v", r)
	}
}

func TestRouteCommands_AddStaticRoute_V6(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	if err := cmds.AddStaticRoute(context.Background(), StaticRouteSpec{
		V6: true, Network: "3f80::/10", Interface: "OpkgTun10",
	}); err != nil {
		t.Fatalf("add6: %v", err)
	}
	r := poster.Payloads()[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
	// Ключ подсети у v6 — prefix (у v4 network). С network NDMS отдаёт ложный
	// «no input»: обязательного поля в запросе нет — стенд 2026-08-24.
	if r["prefix"] != "3f80::/10" || r["interface"] != "OpkgTun10" || r["auto"] != true {
		t.Errorf("ipv6 route: %#v", r)
	}
	// v6 add для этой spec эмитит {prefix, interface, auto} — без network/mask/host
	// (их у v6-формы нет вовсе) и без reject/comment, которых spec не задаёт.
	for _, k := range []string{"network", "mask", "host", "reject", "comment", "no"} {
		if _, ok := r[k]; ok {
			t.Errorf("v6 add must not emit %q: %#v", k, r)
		}
	}
}

func TestRouteCommands_RemoveStaticRoute_V6(t *testing.T) {
	cmds, poster := newTestRouteCommands(t)
	if err := cmds.RemoveStaticRoute(context.Background(), StaticRouteSpec{
		V6: true, Network: "3f80::/10", Interface: "OpkgTun10",
	}); err != nil {
		t.Fatalf("rm6: %v", err)
	}
	r := poster.Payloads()[0].(map[string]any)["ipv6"].(map[string]any)["route"].(map[string]any)
	// prefix обязателен и здесь: без него NDMS понимает запрос как снятие
	// ДЕФОЛТНОГО ::/0 у интерфейса («no such route: ::/0 via OpkgTun10» на
	// стенде 2026-08-24), то есть целится не в тот маршрут.
	if r["prefix"] != "3f80::/10" || r["interface"] != "OpkgTun10" || r["no"] != true {
		t.Errorf("ipv6 route remove: %#v", r)
	}
	// v6 remove emits ONLY {prefix, interface, no} — no network/auto/mask/host/reject/comment.
	for _, k := range []string{"network", "auto", "mask", "host", "reject", "comment"} {
		if _, ok := r[k]; ok {
			t.Errorf("v6 remove must not emit %q: %#v", k, r)
		}
	}
}

// TestRouteCommands_ExactPayloads pins the EXACT marshaled JSON of all four
// static-route forms through the unified AddStaticRoute/RemoveStaticRoute path,
// so the wire bytes are provably unchanged by the v6-unification refactor. The
// v6 forms were stand-verified on a live router; a byte change would silently
// break NDMS, so these assert the full marshaled string.
func TestRouteCommands_ExactPayloads(t *testing.T) {
	marshal := func(payload any) string {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}
	cases := []struct {
		name string
		run  func(*RouteCommands) error
		want string
	}{
		{
			name: "v4 add (fakeip pool)",
			run: func(c *RouteCommands) error {
				return c.AddStaticRoute(context.Background(), StaticRouteSpec{
					Network: "10.128.0.0", Mask: "255.192.0.0", Interface: "OpkgTun10", Comment: "awgm fakeip pool",
				})
			},
			want: `{"ip":{"route":{"auto":true,"comment":"awgm fakeip pool","interface":"OpkgTun10","mask":"255.192.0.0","network":"10.128.0.0"}}}`,
		},
		{
			name: "v4 remove (drain)",
			run: func(c *RouteCommands) error {
				return c.RemoveStaticRoute(context.Background(), StaticRouteSpec{
					Network: "10.128.0.0", Mask: "255.192.0.0", Interface: "OpkgTun10", Comment: "awgm fakeip drain",
				})
			},
			want: `{"ip":{"route":{"interface":"OpkgTun10","mask":"255.192.0.0","network":"10.128.0.0","no":true}}}`,
		},
		{
			name: "v6 add (pool)",
			run: func(c *RouteCommands) error {
				return c.AddStaticRoute(context.Background(), StaticRouteSpec{
					V6: true, Network: "3f80::/10", Interface: "OpkgTun10",
				})
			},
			want: `{"ipv6":{"route":{"auto":true,"interface":"OpkgTun10","prefix":"3f80::/10"}}}`,
		},
		{
			name: "v6 remove (pool)",
			run: func(c *RouteCommands) error {
				return c.RemoveStaticRoute(context.Background(), StaticRouteSpec{
					V6: true, Network: "3f80::/10", Interface: "OpkgTun10",
				})
			},
			want: `{"ipv6":{"route":{"interface":"OpkgTun10","no":true,"prefix":"3f80::/10"}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmds, poster := newTestRouteCommands(t)
			if err := tc.run(cmds); err != nil {
				t.Fatalf("run: %v", err)
			}
			got := marshal(poster.Payloads()[0])
			if got != tc.want {
				t.Errorf("payload mismatch:\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}
