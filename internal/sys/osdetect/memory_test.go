package osdetect

import (
	"slices"
	"testing"
)

func TestGCEnvForTotalMemoryMB_TierTable(t *testing.T) {
	cases := []struct {
		name string
		mem  int
		want []string
	}{
		{"unknown memory", 0, nil},
		{"negative (defensive)", -1, nil},
		{"32MB", 32, []string{"GOGC=50", "GOMEMLIMIT=16MiB"}},
		{"tiny boundary 49", 49, []string{"GOGC=50", "GOMEMLIMIT=16MiB"}},
		{"64MB", 64, []string{"GOGC=50", "GOMEMLIMIT=24MiB"}},
		{"128MB router (~123 reported)", 123, []string{"GOGC=50", "GOMEMLIMIT=32MiB"}},
		{"low boundary 199", 199, []string{"GOGC=50", "GOMEMLIMIT=32MiB"}},
		// Mid tier: 256MB and 512MB routers previously got NO tuning at all
		// and the selective rebuild could be OOM-killed.
		{"low boundary 200", 200, []string{"GOGC=100", "GOMEMLIMIT=96MiB"}},
		{"256MB router (~248 reported)", 248, []string{"GOGC=100", "GOMEMLIMIT=96MiB"}},
		{"512MB router (~500 reported)", 500, []string{"GOGC=100", "GOMEMLIMIT=96MiB"}},
		{"mid boundary 699", 699, []string{"GOGC=100", "GOMEMLIMIT=96MiB"}},
		// High tier (#562): 1GB+ модели раньше жили вовсе без потолка —
		// аллокационный шторм доезжал до 90%+ ОЗУ роутера. Мягкий
		// GOMEMLIMIT без смены GOGC: идл ~30-60МБ, легитимные пики
		// ~100-150МБ — трэша нет, runaway гасится.
		{"mid boundary 700", 700, []string{"GOMEMLIMIT=256MiB"}},
		{"1GB router (~950 reported)", 950, []string{"GOMEMLIMIT=256MiB"}},
		{"2GB (hypothetical)", 2000, []string{"GOMEMLIMIT=256MiB"}},
	}
	for _, tc := range cases {
		if got := gcEnvForTotalMemoryMB(tc.mem); !slices.Equal(got, tc.want) {
			t.Errorf("%s: gcEnvForTotalMemoryMB(%d) = %v, want %v", tc.name, tc.mem, got, tc.want)
		}
	}
}

func TestGetGCEnv_DisableMemorySavingWinsOverTiers(t *testing.T) {
	if got := GetGCEnv(true); !slices.Equal(got, []string{"GOGC=100"}) {
		t.Fatalf("GetGCEnv(true) = %v, want [GOGC=100]", got)
	}
}
