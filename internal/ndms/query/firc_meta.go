package query

import (
	"context"
	"sort"
	"strings"
)

// FircInterfaceMeta — метаданные NDMS одного интерфейса ядра для ответа
// GET /api/firc/v1/system/interfaces: подпись и роль WAN, когда fircd своих
// метаданных RCI не получил или его рантайм старше.
type FircInterfaceMeta struct {
	KernelName  string
	NDMSName    string
	Description string
	IsWAN       bool
	IsNonISP    bool
}

// Правила повторяют firc_kn_parse_interface_list в
// c-core/firc/src/backend-c/src/interfaces/keenetic_rci.c: панель дописывает
// роль за fircd, и расходиться с ним она не должна.
func isNonISPNDMSType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "wireguard", "opkgtun", "proxy", "openvpn", "sstp", "ipsec", "ike", "virtualip", "zerotier":
		return true
	}
	return false
}

func isLocalOnlyNDMSType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "bridge", "accesspoint", "wifimaster", "port":
		return true
	}
	return false
}

func isWANNDMSType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "gigabitethernet", "fastethernet", "ethernet", "pppoe",
		"usbmodem", "usblte", "usbqmi", "cdcethernet", "usbdsl",
		"ipoe", "wifistation", "mwsmobile":
		return true
	}
	return false
}

// ListFircMeta — проекция кэша стора для обогащения списка интерфейсов fircd.
// Ключ — имя ядра; записи с одним именем сводятся как в ListWAN: побеждает
// поднятая, затем WAN, затем с описанием, затем меньший id.
func (s *InterfaceStore) ListFircMeta(ctx context.Context) ([]FircInterfaceMeta, error) {
	listed, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		meta FircInterfaceMeta
		id   string
		up   bool
	}
	better := func(c, w candidate) bool {
		if c.up != w.up {
			return c.up
		}
		if c.meta.IsWAN != w.meta.IsWAN {
			return c.meta.IsWAN
		}
		if (c.meta.Description != "") != (w.meta.Description != "") {
			return c.meta.Description != ""
		}
		return c.id < w.id
	}
	best := make(map[string]candidate, len(listed))
	for _, iface := range listed {
		// Порт резолвится в имя родителя (eth3) и затёр бы его подпись;
		// его {"role":"inet","for":X} уже перенесён на X в fetchListMap.
		if strings.EqualFold(iface.Type, "Port") {
			continue
		}
		kernelName := strings.TrimSpace(s.ResolveSystemName(ctx, iface.ID))
		if kernelName == "" {
			kernelName = strings.TrimSpace(iface.SystemName)
		}
		if kernelName == "" {
			continue
		}
		ndmsName := strings.TrimSpace(iface.InterfaceName)
		if ndmsName == "" || ndmsName == kernelName {
			ndmsName = strings.TrimSpace(iface.ID)
		}
		if ndmsName == kernelName {
			ndmsName = ""
		}
		desc := strings.TrimSpace(iface.Description)
		if desc == kernelName {
			desc = ""
		}
		sl := strings.TrimSpace(iface.SecurityLevel)
		isNonISP := IsNonISPInterface(kernelName) || isNonISPNDMSType(iface.Type) || iface.VPNTrait
		excluded := isNonISP || isLocalOnlyNDMSType(iface.Type) || iface.LocalTrait ||
			(sl != "" && !strings.EqualFold(sl, "public"))
		isWAN := !excluded && (iface.RoleInet || (iface.Global && (iface.WANTrait || isWANNDMSType(iface.Type))))

		c := candidate{
			meta: FircInterfaceMeta{KernelName: kernelName, NDMSName: ndmsName, Description: desc, IsWAN: isWAN, IsNonISP: isNonISP},
			id:   iface.ID,
			up:   iface.State == "up" && iface.IPv4 == "running",
		}
		key := strings.ToLower(kernelName)
		if w, dup := best[key]; dup && !better(c, w) {
			continue
		}
		best[key] = c
	}
	out := make([]FircInterfaceMeta, 0, len(best))
	for _, c := range best {
		out = append(out, c.meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KernelName < out[j].KernelName })
	return out, nil
}
