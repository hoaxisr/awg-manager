package firc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// foreignConfigPrefix — каталог внутри /opt/etc/firc, куда откладываются файлы
// снятого стороннего firc.
const foreignConfigPrefix = "firc-config-"

// setAsideForeignConfig откладывает в сторону конфигурацию стороннего firc,
// оставшуюся после снятия его пакета: opkg её не трогает, а формат у рантайма
// другой — с failback он не загружает туннели вовсе, списки групп (lists) панель
// потеряла бы при первой же записи. Зовётся при установке без прежнего рантайма,
// до opkg: умолчания кладёт postinst. Признак — ключи, которых ни рантайм, ни
// панель не пишут; свои файлы после снятия рантайма под него не попадают.
func (i *OnDemandInstaller) setAsideForeignConfig() (string, error) {
	tunnels := i.rootPath(DefaultTunnelsFilePath)
	if !yamlListHasKey(tunnels, "tunnels", "failback") && !yamlListHasKey(i.layout.GroupsFile, "groups", "lists") {
		return "", nil
	}
	dir := filepath.Join(filepath.Dir(i.layout.GroupsFile), foreignConfigPrefix+time.Now().UTC().Format("20060102-150405"))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("set aside firc configuration: %w", err)
	}
	for _, p := range []string{tunnels, i.layout.GroupsFile, i.layout.ConfigFile} {
		if err := os.Rename(p, filepath.Join(dir, filepath.Base(p))); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("set aside firc configuration: %w", err)
		}
	}
	return dir, nil
}

// foreignConfigDir — последний отложенный каталог; пусто, если такого нет.
func (i *OnDemandInstaller) foreignConfigDir() string {
	dirs, _ := filepath.Glob(filepath.Join(filepath.Dir(i.layout.GroupsFile), foreignConfigPrefix+"*"))
	if len(dirs) == 0 {
		return ""
	}
	sort.Strings(dirs)
	return dirs[len(dirs)-1]
}

// yamlListHasKey — есть ли key хотя бы у одного элемента списка list в файле.
// Нечитаемый файл — «нет»: трогать то, чего не поняли, нельзя.
func yamlListHasKey(path, list, key string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc map[string]any
	if yaml.Unmarshal(raw, &doc) != nil {
		return false
	}
	items, _ := doc[list].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			if _, has := m[key]; has {
				return true
			}
		}
	}
	return false
}
