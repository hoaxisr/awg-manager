package firewall

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Пути по умолчанию (тесты переопределяют поля структуры).
const (
	defaultHookPath = "/opt/etc/ndm/netfilter.d/51-awgm-tunnel-fw.sh"
	defaultListPath = "/opt/etc/awg-manager/tunnel-fw.list"
)

// syncHookState поддерживает файл-список туннель-интерфейсов и netfilter.d-хук,
// который после каждой NDM-перезаписи таблиц переустанавливает наши правила.
// NDM переписывает filter/nat/mangle целиком по многу раз на флап интерфейсов
// и молча стирает всё чужое; без хука правила AddRules живут до первой
// перезаписи. ndmsManaged (OS5 OpkgTun) — no-op: filter/nat там ведёт NDMS.
// Формат строки списка: "<iface> mss" (mss — опциональный маркер клампа).
//
// Весь read-modify-write под hookMu: сериализация выше по стеку — per-tunnel,
// так что два туннеля стартуют параллельно и без лока затирали бы записи
// друг друга.
func (m *ManagerImpl) syncHookState(iface string, present bool) error {
	if m.ndmsManaged {
		return nil
	}
	m.hookMu.Lock()
	defer m.hookMu.Unlock()
	set, err := m.readList()
	if err != nil {
		return err
	}
	if present {
		set[iface] = m.mssClamp
	} else {
		delete(set, iface)
	}
	if err := m.writeList(set); err != nil {
		return err
	}
	return m.ensureHookFile()
}

func (m *ManagerImpl) hookFile() string {
	if m.hookPath != "" {
		return m.hookPath
	}
	return defaultHookPath
}

func (m *ManagerImpl) listFile() string {
	if m.listPath != "" {
		return m.listPath
	}
	return defaultListPath
}

func (m *ManagerImpl) readList() (map[string]bool, error) {
	set := map[string]bool{}
	data, err := os.ReadFile(m.listFile())
	if err != nil {
		if os.IsNotExist(err) {
			return set, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		set[f[0]] = len(f) > 1 && f[1] == "mss"
	}
	return set, nil
}

func (m *ManagerImpl) writeList(set map[string]bool) error {
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		if set[n] {
			b.WriteString(" mss")
		}
		b.WriteString("\n")
	}
	path := m.listFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *ManagerImpl) ensureHookFile() error {
	path := m.hookFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(hookScript(m.listFile())), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// hookScript генерирует тело netfilter.d-хука. Без I/O — чтобы тест мог
// прогнать результат через `sh -n`.
//
// Проверка $type инвертирована («не ip6tables») — так же, как в хуках
// sb-router (internal/singbox/router/iptables.go): при пустом $type в
// какой-нибудь прошивке положительная проверка молча убила бы хук.
func hookScript(listPath string) string {
	return fmt.Sprintf(`#!/bin/sh
# awg-manager: переустановка правил туннелей после NDM-перезаписи таблиц.
# Генерируется автоматически, не редактировать.
[ "$type" = "ip6tables" ] && exit 0
LIST=%q
[ -f "$LIST" ] || exit 0
IPT=/opt/sbin/iptables
[ -x "$IPT" ] || IPT=iptables
while read -r IF MSS; do
    [ -n "$IF" ] || continue
    case "$table" in
    filter)
        $IPT -w -C INPUT -i "$IF" -j ACCEPT 2>/dev/null || $IPT -w -A INPUT -i "$IF" -j ACCEPT
        $IPT -w -C OUTPUT -o "$IF" -j ACCEPT 2>/dev/null || $IPT -w -A OUTPUT -o "$IF" -j ACCEPT
        $IPT -w -C FORWARD -i "$IF" -j ACCEPT 2>/dev/null || $IPT -w -A FORWARD -i "$IF" -j ACCEPT
        $IPT -w -C FORWARD -o "$IF" -j ACCEPT 2>/dev/null || $IPT -w -A FORWARD -o "$IF" -j ACCEPT
        ;;
    nat)
        $IPT -w -t nat -C POSTROUTING -o "$IF" -j MASQUERADE 2>/dev/null || $IPT -w -t nat -A POSTROUTING -o "$IF" -j MASQUERADE
        ;;
    mangle)
        [ "$MSS" = "mss" ] || continue
        $IPT -w -t mangle -C FORWARD -o "$IF" -p tcp -m tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || \
            $IPT -w -t mangle -I FORWARD 1 -o "$IF" -p tcp -m tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
        ;;
    esac
done < "$LIST"
exit 0
`, listPath)
}
