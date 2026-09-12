package main

import (
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/obfuscator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// applyDataDir перенацеливает пакеты, чьи каталоги производны от каталога
// данных. Пакеты держат их отдельными переменными (их же подменяют тесты), и
// без этой раздачи флаг `-data-dir` соблюдался наполовину: запуск «в
// песочнице» всё равно писал в /opt/etc/awg-manager.
//
// Сюда попадает только то, что ДАННЫЕ демона. Хук ndm
// (/opt/etc/ndm/netfilter.d/…), бинари Entware (/opt/bin) и белый список
// путей файлового редактора к каталогу данных отношения не имеют.
func applyDataDir(dataDir string) {
	if dataDir == "" {
		return
	}
	tunnel.ConfDir = dataDir
	obfuscator.ConfDir = filepath.Join(dataDir, "obfuscator")
	kmod.ModulesDir = filepath.Join(dataDir, "modules")
	router.SetDataDir(dataDir)
}
