package nwg

import (
	"path/filepath"
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
			// #953: Titan SE не было в карте SoC — NativeWG отказывал с
			// «не значится в карте SoC». Его сборка совпадает с KN-1812, под
			// которую и собран aarch64 arch-default. SoC — из настоящей карты.
			name:     "Titan SE (NC-4210) gets the mt7988 arch default",
			model:    "KN-4210",
			soc:      kmod.ParseModelToSoC("NC-4210"),
			exists:   have(),
			wantPath: defaultKoPath,
		},
		{
			name:     "unknown hardware still gets the arch default",
			model:    "",
			soc:      kmod.SoCUnknown,
			exists:   have(),
			wantPath: defaultKoPath,
		},
		{
			// Keenetic, которого нет в карте SoC: NDMS ответил, значит модель
			// настоящая, а под какое ядро её кормить — неизвестно. Угадывать
			// здесь и есть F342.
			name:    "known-model-unknown-SoC — refuse, not arch default",
			model:   "KN-9999",
			soc:     kmod.SoCUnknown,
			exists:  have(),
			wantErr: "KN-9999",
		},
		{
			// KN-1011 — mt7621 с HIGHMEM: сборка SoC ему не годится, а mt7621
			// сидит в socsCoveredByArchDefault, так что без своего гейта
			// провал был бы тихим.
			name:    "model with its own build, file missing — refuse",
			model:   "KN-1011",
			soc:     kmod.SoCMT7621,
			exists:  have("awg_proxy-mt7621.ko"),
			wantErr: "KN-1011",
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
// иметь собственную сборку в kmod/awg-proxy/out/. Списки не дублируются: SoC
// берутся из карты моделей, сборки — с диска, поэтому тест валится и когда в
// soc.go заводят новый SoC без сборки, и когда сборка пропала из out/.
func TestEverySoCHasABuild(t *testing.T) {
	const outDir = "../../../kmod/awg-proxy/out"

	files, err := filepath.Glob(filepath.Join(outDir, "awg_proxy-*.ko"))
	if err != nil {
		t.Fatalf("glob сборок: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("в %s нет ни одной сборки awg_proxy — проверять нечего", outDir)
	}

	built := map[string]bool{}
	for _, f := range files {
		built[strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "awg_proxy-"), ".ko")] = true
	}

	socs := kmod.KnownSoCs()
	if len(socs) == 0 {
		t.Fatal("карта моделей пуста — тест ничего не проверяет")
	}
	for _, soc := range socs {
		if !built[string(soc)] && !socsCoveredByArchDefault[soc] {
			t.Errorf("SoC %s: нет ни сборки awg_proxy-%s.ko в %s, ни покрытия arch-default — роутеры на нём останутся без прокси-пути", soc, soc, outDir)
		}
	}

	// Модели со своей сборкой — тот же уговор: файл обязан лежать на месте.
	for model := range modelsWithOwnBuild {
		if !built[model] {
			t.Errorf("модель %s: нет сборки awg_proxy-%s.ko в %s, а сборка её SoC ей не годится", model, model, outDir)
		}
	}
}
