package firc

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

type capturedAppLog struct{ warns []string }

func (l *capturedAppLog) AppLog(level logging.Level, _, _, _, _, message string) {
	if level == logging.LevelWarn {
		l.warns = append(l.warns, message)
	}
}

func TestSyncGroups_ServerPolicyDenies(t *testing.T) {
	base := func() RouterPolicies {
		return RouterPolicies{
			Policies: []RouterPolicy{
				{Name: "Policy2", Description: "Guests"},
				{Name: "Policy4", Description: "VPN only"},
				{Name: "Policy5"},
				{Name: "Policy6", Description: "VPN only"},
			},
			Interfaces: map[string]string{"Wireguard5": "Policy4", "OpkgTun17": "Policy5", "OpkgTun18": "Policy5", "Bridge1": "Policy2"},
			Hosts:      map[string]string{"aa:bb:cc:dd:ee:01": "Policy2"},
			WDTT:       []WDTTServerPolicy{{Policy: "Policy5", Interfaces: []string{"OpkgTun17", "OpkgTun18"}}},
		}
	}
	for _, tc := range []struct {
		name    string
		servers []string
		rp      func() (RouterPolicies, bool)
		deny    []string
		want    []string
		warns   []string // подстроки предупреждений; nil — журнал молчит
	}{
		{"политика WDTT — из её конфигурации", []string{"wdtt_server"}, nil,
			[]string{"policy:Policy3", "policy:Policy5"}, []string{"policy:Policy3"}, nil},
		{"WDTT без политики — не снимается ничего", []string{"wdtt_server"}, func() (RouterPolicies, bool) {
			rp := base()
			rp.WDTT[0].Policy = "none"
			return rp, true
		}, []string{"policy:Policy3", "policy:Policy5", "policy:none"}, []string{"policy:Policy3", "policy:Policy5", "policy:none"}, nil},
		{"данных о политиках нет — не снимается ничего", []string{"wdtt_server", "managed:Wireguard5"}, func() (RouterPolicies, bool) {
			return base(), false
		}, []string{"policy:Policy4", "policy:Policy5"}, []string{"policy:Policy4", "policy:Policy5"}, nil},
		{"сервер выключен — его политика остаётся", []string{"wdtt_server"}, nil,
			[]string{"policy:Policy4"}, []string{"policy:Policy4"}, nil},
		{"первая политика с описанием", []string{"managed:Wireguard5"}, nil,
			[]string{"policy:VPN only", "policy:Guests"}, []string{"policy:Guests"}, nil},
		{"имя, занятое описанием другой политики", []string{"managed:Wireguard5"}, func() (RouterPolicies, bool) {
			rp := base()
			rp.Policies[0].Description = "Policy4"
			return rp, true
		}, []string{"policy:Policy4"}, []string{"policy:Policy4"}, nil},
		{"в политике LAN-устройство", []string{"managed:Wireguard5"}, func() (RouterPolicies, bool) {
			rp := base()
			rp.Hosts["aa:bb:cc:dd:ee:02"] = "Policy4"
			return rp, true
		}, []string{"policy:VPN only", "10.88.0.0/24"}, []string{"policy:VPN only"}, []string{"«YouTube»: исключение политики Policy4 оставлено — кроме VPN-сервера в ней состоят aa:bb:cc:dd:ee:02."}},
		{"в политике LAN-сегмент", []string{"managed:Wireguard5"}, func() (RouterPolicies, bool) {
			rp := base()
			rp.Interfaces["Bridge0"] = "Policy4"
			return rp, true
		}, []string{"policy:Policy4"}, []string{"policy:Policy4"}, []string{"Bridge0"}},
		{"в политике сервер, не включённый в маршрутизацию", []string{"managed:Wireguard5"}, func() (RouterPolicies, bool) {
			rp := base()
			rp.Interfaces["OpkgTun17"] = "Policy4"
			return rp, true
		}, []string{"policy:Policy4"}, []string{"policy:Policy4"}, []string{"OpkgTun17"}},
		{"оба сервера политики включены", []string{"managed:Wireguard5", "wdtt_server"}, func() (RouterPolicies, bool) {
			rp := base()
			rp.Interfaces["OpkgTun17"], rp.Interfaces["OpkgTun18"] = "Policy4", "Policy4"
			rp.WDTT[0].Policy = "Policy4"
			return rp, true
		}, []string{"policy:Policy4"}, []string{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			appLog := &capturedAppLog{}
			op := NewOperator(Options{ConfigDir: t.TempDir(), AppLog: appLog})
			op.SetWGServersProvider(func(context.Context) []DynamicWGServer {
				return []DynamicWGServer{{ID: "managed:Wireguard5", NDMSName: "Wireguard5", Category: "awgm", Subnet: "10.88.0.0/24", Policy: "Policy4"}}
			})
			op.SetRouterPoliciesProvider(func(context.Context) (RouterPolicies, bool) {
				if tc.rp != nil {
					return tc.rp()
				}
				return base(), true
			})
			ext := ExtConfig{ServerRouting: ServerRoutingConfig{EnabledServers: tc.servers}}
			groups := []Group{{ID: "g", Name: "YouTube", Devices: GroupDevices{Deny: tc.deny}}}
			var out []Group
			for range 2 { // повтор того же состояния журнал не дублирует
				out, _ = op.syncGroupsWithExt(context.Background(), groups, ext)
			}
			if !slices.Equal(out[0].Devices.Deny, tc.want) {
				t.Errorf("deny = %v, want %v", out[0].Devices.Deny, tc.want)
			}
			if len(appLog.warns) != len(tc.warns) {
				t.Fatalf("warnings = %q, want %d", appLog.warns, len(tc.warns))
			}
			for i, want := range tc.warns {
				if !strings.Contains(appLog.warns[i], want) {
					t.Errorf("warning %q does not mention %q", appLog.warns[i], want)
				}
			}
		})
	}
}

// Источник политик ходит на роутер мимо кэша — без повода его не зовём.
func TestSyncGroups_RouterPoliciesAskedOnlyWhenNeeded(t *testing.T) {
	for name, tc := range map[string]struct {
		servers []string
		deny    []string
		want    int
	}{
		"серверы выключены":  {nil, []string{"policy:Policy4"}, 0},
		"политик в deny нет": {[]string{"wdtt_server"}, []string{"192.168.1.5", "mac:aa:bb:cc:dd:ee:01"}, 0},
		"есть что разбирать": {[]string{"wdtt_server"}, []string{"policy:Policy4"}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			op := NewOperator(Options{ConfigDir: t.TempDir()})
			op.SetRouterPoliciesProvider(func(context.Context) (RouterPolicies, bool) {
				calls++
				return RouterPolicies{}, true
			})
			op.syncGroupsWithExt(context.Background(), []Group{{ID: "g", Devices: GroupDevices{Deny: tc.deny}}},
				ExtConfig{ServerRouting: ServerRoutingConfig{EnabledServers: tc.servers}})
			if calls != tc.want {
				t.Fatalf("provider calls = %d, want %d", calls, tc.want)
			}
		})
	}
}
