package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/backup"
	"github.com/hoaxisr/awg-manager/internal/warpscout"
)

// После восстановления из резервной копии (маркер post-restore) «WARP Сканер»
// не пишет warpscout.json: до перезапуска демона состояние из памяти легло бы
// поверх восстановленного файла.
func TestWarpScout_NoWritesAfterRestore(t *testing.T) {
	s, _ := newGuardServer(t)
	s.registerRoutes(http.NewServeMux())
	if s.warpScout == nil {
		t.Fatal("сервис WARP Сканер не собран")
	}
	t.Cleanup(s.warpScout.Stop)
	cfg := warpscout.Config{AWG: warpscout.AWGParams{SignatureProfile: "none"}}
	if _, err := s.warpScout.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("до восстановления запись должна проходить: %v", err)
	}
	if err := backup.WritePostRestoreMarker(s.settings.DataDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.warpScout.SaveConfig(context.Background(), cfg); !errors.Is(err, warpscout.ErrFrozen) {
		t.Fatalf("после восстановления запись должна отклоняться, err=%v", err)
	}
}

func TestKmodVersionAtLeast(t *testing.T) {
	cases := []struct {
		ver  string
		want bool
	}{
		{"3.1.20260906", true},
		{"v3.1.0", true},
		{"3.2", true},
		{"4.0.0", true},
		{"3.1-rc1", true},
		{"3.0.20260812", false},
		{"3.0", false},
		{"2.0.1", false},
		{"1.0.0", false},
		{"", false},
		{"invalid", false},
		{"3.x", false},
	}
	for _, tc := range cases {
		if got := kmodVersionAtLeast(tc.ver, 3, 1); got != tc.want {
			t.Errorf("kmodVersionAtLeast(%q, 3, 1) = %v, want %v", tc.ver, got, tc.want)
		}
	}
}
