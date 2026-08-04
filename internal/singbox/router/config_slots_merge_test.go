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
	const (
		userFinal        = "user-proxy"
		sharedRuleSetTag = "geosite-x"
	)

	sharedCfg := func(mode string) *RouterConfig {
		cfg := NewEmptyConfig()
		// Наследие прежней раскладки: инбаунд в общем слоте. Его обязан
		// вычистить buildRoutingSlot — иначе в merged-конфиге окажется два
		// одинаковых тега (в режиме tproxy) либо чужой перехватчик (в
		// остальных).
		cfg.Inbounds = []Inbound{{
			Type: "tproxy", Tag: "tproxy-in", Listen: tproxyListen, ListenPort: TPROXYPort, Network: "udp",
		}}
		// Оттуда же — режимные скаляры fakeip. Ссылка на движковый резолвер
		// `real` вне fakeip-режима роняет sing-box на старте
		// («domain resolver not found»), поэтому её вычистку проверяет
		// настоящий check ниже.
		cfg.Route.DefaultDomainResolver = &DomainResolver{Server: "real"}
		cfg.DNS.Final = "real"
		cfg.Experimental = &Experimental{CacheFile: &CacheFile{Enabled: true, StoreFakeIP: true, Path: "/tmp/awgm-stale.db"}}
		cfg.Outbounds = []Outbound{
			{Type: "selector", Tag: userFinal, Outbounds: []string{"direct"}},
		}
		cfg.Route.Rules = []Rule{
			{Action: "route", DomainSuffix: []string{"example.com"}, Outbound: userFinal},
		}
		cfg.Route.RuleSet = []RuleSet{
			{Tag: sharedRuleSetTag, Type: "remote", Format: "binary", URL: "https://example.org/x.srs", UpdateInterval: "24h"},
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
			// Продовый путь fakeip — оверлей поверх содержимого слота
			// (BuildFakeIPTunConfig в проде не вызывается). Слот намеренно
			// набит ОБЩИМ содержимым с теми же тегами, что и 21-routing.json:
			// это состояние установки, пережившей прежнюю раскладку. Если
			// оверлей перестанет его вычищать, merge упадёт на дубле тегов —
			// ровно тот регресс, ради которого делалось разделение.
			name: "fakeip-tun", mode: stateFakeIPTun, modeFile: "20-fakeip.json",
			modeCfg: func(*testing.T) *RouterConfig {
				cfg := NewEmptyConfig()
				cfg.Outbounds = []Outbound{{Type: "selector", Tag: userFinal, Outbounds: []string{"direct"}}}
				cfg.Route.RuleSet = []RuleSet{
					{Tag: sharedRuleSetTag, Type: "remote", Format: "binary", URL: "https://example.org/x.srs", UpdateInterval: "24h"},
				}
				cfg.Route.Rules = []Rule{{Action: "route", DomainSuffix: []string{"stale.example"}, Outbound: userFinal}}
				cfg.Route.Final = "mode-final"
				// DNS-правило режима ссылается на набор ИЗ ОБЩЕГО слота —
				// доменное сужение fakeip; sing-box файлов не различает.
				cfg.DNS.Rules = []DNSRule{{
					Action: "route", Server: "fakeip",
					QueryType: []string{"A", "AAAA"}, RuleSet: []string{sharedRuleSetTag},
				}}
				ensureFakeIPOverlay(cfg, fakeipSpec)
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
			modeCfg := tc.modeCfg(t)
			writeSlotJSON(t, dir, tc.modeFile, modeCfg)
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
				// DNS-правило режима переживает чистку и ссылается на набор из
				// ДРУГОГО файла — так работает доменное сужение fakeip, и
				// настоящий check ниже подтверждает, что sing-box это принимает.
				narrowed := false
				for _, r := range merged.DNS.Rules {
					if r.Server == "fakeip" && len(r.RuleSet) == 1 && r.RuleSet[0] == sharedRuleSetTag {
						narrowed = true
					}
				}
				if !narrowed {
					t.Errorf("fakeip: DNS-правило с сужением по набору потеряно:\n%s", raw)
				}
			} else if hasFakeIP || hasReal {
				t.Errorf("%s: серверов fakeip/real в конфиге быть не должно:\n%s", tc.name, raw)
			}

			// Инбаунды приходят РОВНО из режимного слота: ни дубля тегов, ни
			// чужого перехватчика из общего слота.
			want := map[string]bool{}
			for _, in := range modeCfg.Inbounds {
				want[in.Tag] = true
			}
			seen := map[string]bool{}
			for _, in := range merged.Inbounds {
				if seen[in.Tag] {
					t.Errorf("дублирующийся тег инбаунда %q — sing-box откажется грузить конфиг", in.Tag)
				}
				seen[in.Tag] = true
				if !want[in.Tag] {
					t.Errorf("инбаунд %q пришёл не из режимного слота: %s", in.Tag, raw)
				}
			}
			if len(seen) != len(want) {
				t.Errorf("набор инбаундов %v не совпал с режимным слотом %v", seen, want)
			}
			// Набор и outbound объявлены ровно один раз (configmerge ловит
			// коллизию тегов как sing-box, но проверим и по факту).
			if len(merged.Route.RuleSet) != 1 || merged.Route.RuleSet[0].Tag != sharedRuleSetTag {
				t.Errorf("набор правил обязан быть объявлен ровно один раз: %+v", merged.Route.RuleSet)
			}
			outTags := map[string]int{}
			for _, o := range merged.Outbounds {
				outTags[o.Tag]++
			}
			if outTags[userFinal] != 1 {
				t.Errorf("тег outbound %q встречается %d раз(а) — дубль валит sing-box", userFinal, outTags[userFinal])
			}
			// Пользовательское правило из режимного слота (наследие прежней
			// раскладки) не должно доезжать до merged-конфига.
			for _, r := range merged.Route.Rules {
				if len(r.DomainSuffix) == 1 && r.DomainSuffix[0] == "stale.example" {
					t.Errorf("правило из режимного слота не вычищено: %+v", r)
				}
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
