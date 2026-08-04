package router

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/configmerge"
)

// Единственная автоматическая защита от регресса, который иначе виден только на
// роутере: merged-конфиг собирается из режимного и общего слотов, и порядок
// слияния (массивы конкатенируются, скаляры — first-file-wins) решает,
// перехватится ли DNS и чей route.final доживёт до sing-box.
//
// Проверяется во всех трёх режимах:
//   - системное правило режима стоит ПЕРВЫМ (иначе пользовательское правило
//     обгонит hijack-dns и DNS перестанет перехватываться);
//   - route.final — пользовательский из общего слота, а не значение режима;
//   - серверы fakeip/real существуют ТОЛЬКО в fakeip-режиме (в остальных
//     ссылка на них повисла бы).
//
// Когда рядом есть бинарь sing-box — тот же каталог дополнительно проходит
// настоящий `sing-box check -C`.
func TestMergedConfigPerMode(t *testing.T) {
	const userFinal = "user-proxy"

	sharedCfg := func(mode string) *RouterConfig {
		cfg := NewEmptyConfig()
		cfg.Outbounds = []Outbound{
			{Type: "selector", Tag: userFinal, Outbounds: []string{"direct"}},
		}
		cfg.Route.Rules = []Rule{
			{Action: "route", DomainSuffix: []string{"example.com"}, Outbound: userFinal},
		}
		cfg.Route.RuleSet = []RuleSet{
			{Tag: "geosite-x", Type: "remote", Format: "binary", URL: "https://example.org/x.srs", UpdateInterval: "24h"},
		}
		cfg.Route.Final = userFinal
		buildRoutingSlot(cfg, RoutingSlotParams{Mode: mode, WANAutoDetect: true})
		return cfg
	}

	fakeipSpec := FakeIPTunSpec{
		Iface: "opkgtun0", TunAddr4: "172.18.0.1/30", MTU: 1500,
		Inet4Range: "198.18.0.0/15", CachePath: "/tmp/awgm-cache.db", RealServer: "1.1.1.1",
	}

	cases := []struct {
		name     string
		mode     string
		modeFile string
		modeCfg  func(t *testing.T) *RouterConfig
	}{
		{
			name: "tproxy", mode: stateTProxy, modeFile: "20-tproxy.json",
			modeCfg: func(*testing.T) *RouterConfig {
				return buildTProxySlot(TProxyParams{SnifferEnabled: true})
			},
		},
		{
			name: "policy-tun", mode: statePolicyTun, modeFile: "20-policytun.json",
			modeCfg: func(*testing.T) *RouterConfig {
				return buildPolicyTunSlot(PolicyTunInboundSpec{
					Iface: "opkgtun0", TunAddr4: "172.18.0.1/30", MTU: 1500,
				}, true, nil)
			},
		},
		{
			name: "fakeip-tun", mode: stateFakeIPTun, modeFile: "20-fakeip.json",
			modeCfg: func(t *testing.T) *RouterConfig {
				cfg, err := BuildFakeIPTunConfig(fakeipSpec)
				if err != nil {
					t.Fatalf("build fakeip slot: %v", err)
				}
				return cfg
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSlotJSON(t, dir, "00-base.json", map[string]any{
				"log":       map[string]any{"level": "warn"},
				"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
			})
			writeSlotJSON(t, dir, tc.modeFile, tc.modeCfg(t))
			writeSlotJSON(t, dir, "21-routing.json", sharedCfg(tc.mode))

			raw, err := configmerge.MergeDir(dir)
			if err != nil {
				t.Fatalf("merge: %v", err)
			}
			var merged RouterConfig
			if err := json.Unmarshal([]byte(raw), &merged); err != nil {
				t.Fatalf("unmarshal merged: %v\n%s", err, raw)
			}

			if len(merged.Route.Rules) == 0 {
				t.Fatalf("merged route.rules пуст:\n%s", raw)
			}
			first := merged.Route.Rules[0]
			if !isSystemRule(first) {
				t.Errorf("route.rules[0] обязан быть системным правилом режима, получено %+v", first)
			}
			if tc.mode == stateFakeIPTun && first.Action != "hijack-dns" {
				t.Errorf("fakeip: route.rules[0].action = %q, want hijack-dns", first.Action)
			}
			if merged.Route.Final != userFinal {
				t.Errorf("route.final = %q, want пользовательский %q (скаляр общего слота)", merged.Route.Final, userFinal)
			}
			// Пользовательское правило пережило слияние и стоит ПОСЛЕ системных.
			userIdx := -1
			for i, r := range merged.Route.Rules {
				if len(r.DomainSuffix) == 1 && r.DomainSuffix[0] == "example.com" {
					userIdx = i
				}
			}
			if userIdx <= 0 {
				t.Errorf("пользовательское правило потеряно или обогнало системные (index=%d):\n%s", userIdx, raw)
			}

			hasFakeIP, hasReal := false, false
			for _, sv := range merged.DNS.Servers {
				switch {
				case sv.Type == "fakeip":
					hasFakeIP = true
				case sv.Tag == "real":
					hasReal = true
				}
			}
			if tc.mode == stateFakeIPTun {
				if !hasFakeIP || !hasReal {
					t.Errorf("fakeip: серверы fakeip/real обязаны быть в merged-конфиге:\n%s", raw)
				}
				if merged.DNS.Final != "real" {
					t.Errorf("fakeip: dns.final = %q, want real", merged.DNS.Final)
				}
			} else if hasFakeIP || hasReal {
				t.Errorf("%s: серверов fakeip/real в конфиге быть не должно:\n%s", tc.name, raw)
			}

			// Инбаунды приходят ровно из режимного слота — дубля тегов нет.
			seen := map[string]bool{}
			for _, in := range merged.Inbounds {
				if seen[in.Tag] {
					t.Errorf("дублирующийся тег инбаунда %q — sing-box откажется грузить конфиг", in.Tag)
				}
				seen[in.Tag] = true
			}

			singboxCheck(t, dir)
		})
	}
}

func writeSlotJSON(t *testing.T, dir, name string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// singboxCheck прогоняет настоящий `sing-box check -C dir`, если бинарь
// доступен. Юнит-проверка порядка слияния его не заменяет: сам факт, что
// merged-конфиг ЗАГРУЖАЕТСЯ, ловит ошибки схемы, которых Go-структуры не видят.
func singboxCheck(t *testing.T, dir string) {
	t.Helper()
	bin := locateSingboxForCheck()
	if bin == "" {
		t.Log("sing-box не найден — настоящая проверка конфига пропущена")
		return
	}
	cmd := exec.Command(bin, "check", "-C", dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("sing-box check: %v\nstderr: %s", err, stderr.String())
	}
}

// locateSingboxForCheck ищет бинарь sing-box: сначала переменная окружения
// (ей пользуется ручной прогон), затем PATH, затем артефакты сборки в dist/.
func locateSingboxForCheck() string {
	if p := os.Getenv("AWGM_SINGBOX_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("sing-box"); err == nil {
		return p
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return ""
	}
	matches, err := filepath.Glob(filepath.FromSlash(
		"../../../dist/singbox-binaries/*/sing-box-*-linux-" + runtime.GOARCH + "*/sing-box"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}
