package nwg

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
)

// Выбор .ko: своя сборка модели → своя сборка SoC → arch-default, и ОТКАЗ,
// если у SoC своей сборки нет, а arch-default собран не под него. Отказ — суть
// F342: vermagic у этих ядер одинаков, CONFIG_MODVERSIONS выключен, поэтому
// insmod чужого модуля проходит молча и вешает роутер.
func TestResolveKoPathFor(t *testing.T) {
	const dir = "/opt/etc/awg-manager/modules/"

	have := func(names ...string) func(string) bool {
		set := map[string]bool{}
		for _, n := range names {
			set[dir+n] = true
		}
		return func(p string) bool { return set[p] }
	}

	tests := []struct {
		name     string
		model    string
		soc      kmod.SoC
		exists   func(string) bool
		wantPath string
		wantErr  string
	}{
		{
			name:     "model build wins over SoC build",
			model:    "KN-1011",
			soc:      kmod.SoCMT7621,
			exists:   have("awg_proxy-KN-1011.ko", "awg_proxy-mt7621.ko"),
			wantPath: dir + "awg_proxy-KN-1011.ko",
		},
		{
			name:     "SoC build when no model build",
			model:    "KN-2112",
			soc:      kmod.SoCEN7516,
			exists:   have("awg_proxy-en7516.ko"),
			wantPath: dir + "awg_proxy-en7516.ko",
		},
		{
			name:     "arch default for the SoC it is built from",
			model:    "KN-1810",
			soc:      kmod.SoCMT7621,
			exists:   have(),
			wantPath: defaultKoPath,
		},
		{
			name:    "no SoC build and arch default is foreign — refuse",
			model:   "KN-2112",
			soc:     kmod.SoCEN7516,
			exists:  have(),
			wantErr: "en7516",
		},
		{
			name:    "aarch64 groups that are not mt7988 — refuse",
			model:   "KN-3811",
			soc:     kmod.SoCMT7981,
			exists:  have(),
			wantErr: "mt7981",
		},
		{
			name:     "unknown hardware still gets the arch default",
			model:    "",
			soc:      kmod.SoCUnknown,
			exists:   have(),
			wantPath: defaultKoPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, _, err := resolveKoPathFor(tt.model, tt.soc, tt.exists)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ожидался отказ, получен путь %q", path)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("в тексте отказа нет %q: %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданный отказ: %v", err)
			}
			if path != tt.wantPath {
				t.Fatalf("выбран %q, ожидался %q", path, tt.wantPath)
			}
		})
	}
}

// Каждый SoC из карты моделей обязан быть либо покрыт arch-default'ом, либо
// иметь собственную сборку в build-all-awg-proxy.sh. Тест ловит добавление
// нового SoC, под который сборку завести забыли: выбор тогда молча уедет в
// отказ на живом роутере.
func TestEverySoCHasABuild(t *testing.T) {
	// Имена файлов, которые собирает keenetic-sdk/build-all-awg-proxy.sh
	// (SoC-сборки; arch-default'ы mips/arm64/mt7621 покрыты картой выше).
	built := map[kmod.SoC]bool{
		kmod.SoCMT7628: true,
		kmod.SoCEN7528: true,
		kmod.SoCEN7516: true,
		kmod.SoCMT7622: true,
		kmod.SoCMT7981: true,
	}

	for _, soc := range []kmod.SoC{
		kmod.SoCMT7621, kmod.SoCMT7628, kmod.SoCEN7512, kmod.SoCEN7516,
		kmod.SoCEN7528, kmod.SoCMT7622, kmod.SoCMT7981, kmod.SoCMT7988,
	} {
		if !built[soc] && !socsCoveredByArchDefault[soc] {
			t.Errorf("SoC %s: нет ни своей сборки awg_proxy, ни покрытия arch-default — роутеры на нём останутся без прокси-пути", soc)
		}
	}
}
