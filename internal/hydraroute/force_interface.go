package hydraroute

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// forceInterfaceKey — список целей, которые HR Neo (с 3.21) считает
// интерфейсами, даже если на его старте их нет в /sys/class/net (#967).
// Без него цель awgm0 до подъёма туннеля классифицировалась как политика,
// и HR Neo заводил в NDMS пустую `ip policy awgm0`. Версии до 3.21 ключ
// молча пропускают. Не входит в canonicalKey: WriteConfig затёр бы
// пользовательское значение.
const forceInterfaceKey = "ForceInterface"

// ownIfaceFamily — kernel-имена наших интерфейсов и шаблон HR Neo для них:
// «X» в конце совпадает с любым цифровым индексом.
var ownIfaceFamily = regexp.MustCompile(`^(awgm|opkgtun|t2s)[0-9]+$`) //nolint:gochecknoglobals

// syncForceInterface дописывает в ForceInterface шаблоны семейств наших
// интерфейсов, которые встречаются среди целей, у которых есть включённая
// группа в domain.conf/ip.list (выключенные HR Neo не читает).
// Только дописывает: чужие записи и порядок сохраняются, ненужный больше
// шаблон не убирается (он безвреден). Несколько строк ключа HR Neo
// складывает, поэтому они сводятся в одну, без потери значений.
// Возвращает true, если файл изменён.
func syncForceInterface(rs RuleSet) (bool, error) {
	var want []string
	seen := map[string]bool{}
	for _, t := range rs.Targets {
		if !slices.ContainsFunc(t.Groups, Group.Enabled) {
			continue
		}
		m := ownIfaceFamily.FindStringSubmatch(t.Name)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		want = append(want, m[1]+"X")
	}
	if len(want) == 0 {
		return false, nil
	}
	slices.Sort(want)

	existing, err := os.ReadFile(hrConfPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("hydraroute: read hrneo.conf: %w", err)
	}

	var current []string
	keyLines := 0
	scanner := bufio.NewScanner(strings.NewReader(string(existing)))
	for scanner.Scan() {
		key, val, ok := cutConfLine(scanner.Text())
		if !ok || !strings.EqualFold(key, forceInterfaceKey) {
			continue
		}
		keyLines++
		for v := range strings.SplitSeq(val, ",") {
			if v = strings.TrimSpace(v); v != "" && !slices.Contains(current, v) {
				current = append(current, v)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("hydraroute: scan hrneo.conf: %w", err)
	}

	merged := current
	for _, w := range want {
		if !slices.Contains(merged, w) {
			merged = append(merged, w)
		}
	}
	if len(merged) == len(current) && keyLines <= 1 {
		return false, nil
	}

	if err := patchKeyLine(forceInterfaceKey, strings.Join(merged, ",")); err != nil {
		return false, err
	}
	return true, nil
}
