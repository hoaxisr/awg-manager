package kmod

import "testing"

func TestParseModelToSoC_Table(t *testing.T) {
	cases := map[string]SoC{
		"KN-1810": "mt7621", "NC-1810": "mt7621", "kn-1810": "mt7621",
		"KN-1811": "mt7622", "KN-2710": "mt7622", "KN-1812": "mt7988",
		"ki_rb": "mt7628", "kng_re": "mt7621", "ku_rd": "mt7621",
		"KI_RB": "", "KN-9999": "", "": "", "Keenetic Giga": "",
		// две записи из больших групп — по одной на MT7628/MT7981
		// (номера из карты soc.go:27-140; MT7621 уже покрыт KN-1810 выше).
		"KN-1110": "mt7628", "KN-1012": "mt7981",
	}
	for model, want := range cases {
		if got := ParseModelToSoC(model); got != want {
			t.Errorf("ParseModelToSoC(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestSoC_ModulePathAndAARCH64(t *testing.T) {
	dir := t.TempDir()
	setModulesDir(t, dir)
	if got := SoC("mt7622").ModulePath(); got != dir+"/mt7622/amneziawg.ko" {
		t.Fatalf("ModulePath = %q", got)
	}
	if SoCUnknown.ModulePath() != "" {
		t.Fatal("unknown → пустой путь")
	}
	for s, want := range map[SoC]bool{"mt7622": true, "mt7981": true, "mt7988": true, "mt7621": false, "mt7628": false} {
		if s.IsAARCH64() != want {
			t.Errorf("IsAARCH64(%s) = %v", s, !want)
		}
	}
}
