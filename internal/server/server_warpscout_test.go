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
