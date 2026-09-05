package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// fakeToggler и journalProxies пишут в ОДИН журнал: инвариант MigrateOff —
// межобъектный порядок (флаг раньше сноса ProxyN), а два раздельных среза
// вызовов его не видят.
type fakeToggler struct{ events *[]string }

func (f *fakeToggler) SetSingboxCreateNDMSProxy(v bool) error {
	*f.events = append(*f.events, fmt.Sprintf("flag:%t", v))
	return nil
}

// MigrateOff геттер не зовёт — он тут только ради интерфейса SettingsToggler.
func (f *fakeToggler) IsSingboxNDMSProxyEnabled() bool {
	panic("не используется в MigrateOff")
}

type journalProxies struct {
	fakeProxies
	events *[]string
}

func (j *journalProxies) RemoveProxy(ctx context.Context, index int) error {
	*j.events = append(*j.events, fmt.Sprintf("proxy:%d", index))
	return j.fakeProxies.RemoveProxy(ctx, index)
}

// fakeSubProxies — минимальный SubscriptionProxySet: MigrateOff должен снять
// и подписочные ProxyN (`op.subscriptionProxies()`), не только те, что
// пришли из Tunnels().
type fakeSubProxies struct{ proxies []SubscriptionProxy }

func (f *fakeSubProxies) SubscriptionProxies() []SubscriptionProxy { return f.proxies }

// MigrateOff: флаг выключается ПЕРВЫМ (обрыв на шаге 2 — orphan-cleanup подберёт),
// ProxyN каждого туннеля снимается по индексу из listen_port, а следом —
// подписочные composite-прокси (отдельный managed-набор, invisible к Tunnels()).
func TestMigrator_MigrateOff_FlipsFlagThenRemovesEveryProxy(t *testing.T) {
	op, _ := newOrchedOperator(t)
	var events []string
	proxies := &journalProxies{events: &events}
	op.proxyMgr = proxies
	op.SetSubscriptionProxySet(&fakeSubProxies{proxies: []SubscriptionProxy{{Index: 7, Port: 21000, Label: "sub-A"}}})
	ctx := context.Background()

	cfg := NewConfig()
	for _, tc := range []struct {
		tag  string
		port int
	}{{"A", 1084}, {"B", 1091}} {
		if err := cfg.AddTunnelWithListenPort(tc.tag, "vless", tc.tag+".example", 443, tc.port,
			json.RawMessage(`{"type":"vless","tag":"`+tc.tag+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := op.ApplyConfig(ctx, cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	tog := &fakeToggler{events: &events}
	if err := NewMigrator(op, tog, nil).MigrateOff(ctx); err != nil {
		t.Fatalf("MigrateOff: %v", err)
	}
	if want := []string{"flag:false", "proxy:4", "proxy:11", "proxy:7"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("порядок событий = %v, want %v", events, want)
	}
	if !reflect.DeepEqual(proxies.removed, []int{4, 11, 7}) {
		t.Fatalf("RemoveProxy indexes = %v, want [4 11 7]", proxies.removed)
	}
}
