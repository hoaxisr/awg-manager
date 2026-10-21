package singbox

import (
	"fmt"
	"os"
	"path/filepath"
)

// bindingSlotRenames — слоты с привязками «inbound → свой выход», переехавшие
// с позиций после 20/21 на позиции до них (F701, см. orchestrator.KnownSlots).
var bindingSlotRenames = [][2]string{
	{"30-deviceproxy.json", "12-deviceproxy.json"},
	{"40-subscriptions.json", "13-subscriptions.json"},
}

// MigrateBindingSlotOrder переименовывает слоты со старыми именами в новые,
// в config.d и в config.d/disabled. Обязана идти до любого чтения/записи этих
// слотов — поэтому её зовёт NewOperator до reconcileConfigSteps: иначе на
// диске окажутся оба имени — дубль тегов inbound'ов и outbound'ов, sing-box
// не стартует.
//
// Старое имя рядом с новым бывает только после даунгрейда и повторного
// апгрейда: старая версия писала в старое имя, значит оно свежее и
// перезаписывает новое.
//
// Возвращает признак «файлы переименованы» — по нему демон перечитывает
// конфиг пережившего рестарт sing-box (см. MigrateDeviceProxyOutOfTunnels).
func MigrateBindingSlotOrder(configDir string) (bool, error) {
	migrated := false
	for _, dir := range []string{configDir, filepath.Join(configDir, "disabled")} {
		for _, r := range bindingSlotRenames {
			oldPath := filepath.Join(dir, r[0])
			if _, err := os.Stat(oldPath); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return migrated, fmt.Errorf("stat %s: %w", oldPath, err)
			}
			if err := os.Rename(oldPath, filepath.Join(dir, r[1])); err != nil {
				return migrated, fmt.Errorf("rename %s: %w", oldPath, err)
			}
			migrated = true
		}
	}
	return migrated, nil
}
