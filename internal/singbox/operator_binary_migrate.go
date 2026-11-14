package singbox

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
)

// migrateLegacyBinary переносит бинарь, поставленный под прежним именем
// sing-box, на путь binary (awgm-box). Процесс со старым именем не опознаётся
// как наш (matchesBinary сверяет имя), поэтому его сначала гасим: вочдог
// поднимет движок уже по новому пути. Версия не меняется — обновление до пина
// по-прежнему за баннером, скачивания здесь нет.
func migrateLegacyBinary(binary, configPath, pidPath string, log *slog.Logger) {
	legacy := filepath.Join(filepath.Dir(binary), installer.LegacyBinaryName)
	if legacy == binary {
		return
	}
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	old := NewProcess(legacy, configPath, pidPath)
	if running, pid := old.IsRunning(); running {
		if err := old.Stop(); err != nil {
			// Переименование под живым процессом оставило бы сироту с портами.
			log.Warn("singbox legacy binary: stop failed, migration postponed", "pid", pid, "err", err)
			return
		}
		log.Info("singbox legacy binary: stopped for rename", "pid", pid)
	}
	pairs := [][2]string{{legacy, binary}, {legacy + ".meta.json", binary + ".meta.json"}}
	_, statErr := os.Stat(binary)
	keep := errors.Is(statErr, os.ErrNotExist)
	for _, p := range pairs {
		var err error
		if keep {
			err = os.Rename(p[0], p[1])
		} else {
			err = os.Remove(p[0])
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn("singbox legacy binary migration", "path", p[0], "err", err)
		}
	}
	_ = os.Remove(legacy + ".tmp")
	log.Info("singbox legacy binary migrated", "from", legacy, "to", binary, "renamed", keep)
}
