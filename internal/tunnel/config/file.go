package config

import (
	"fmt"
	"os"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// WriteFile материализует запись туннеля в .conf — единственный писатель этого
// файла в проекте.
//
// Раньше их было три с идентичными телами: у хендлера, у сервиса и у
// оркестратора; каждый собирал путь по-своему, а два держали собственную
// переменную с каталогом. Поводы у вызывающих разные — сервис пишет, когда
// меняется запись, оркестратор перегенерирует файл перед каждым запуском, —
// но сама операция одна, и политика «когда звать» остаётся у вызывающего.
//
// Путь берётся из tunnel.NewNames, то есть из того же источника, по которому
// файл потом применяется через `awg setconf` и удаляется.
func WriteFile(stored *storage.AWGTunnel) error {
	path := tunnel.NewNames(stored.ID).ConfPath
	if err := os.MkdirAll(tunnel.ConfDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(Generate(stored)), 0600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

// RemoveFile убирает .conf туннеля. Отсутствие файла — не ошибка: удаление
// зовут и там, где файла заведомо может не быть.
func RemoveFile(tunnelID string) {
	_ = os.Remove(tunnel.NewNames(tunnelID).ConfPath)
}
