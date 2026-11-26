package firc

import (
	"maps"
	"slices"
)

// Связи групп с прокси-группами (ExtConfig.GroupLinks): чью проверку наследует
// группа. Хранятся у нас: fircd незнакомых полей группы не держит. У KeenDNS и Global
// связь значит «проверку группе задала эта прокси-группа» и ведётся по их настройкам.
// Пишет связи только updateGroupLinks; SaveExtConfig сохраняет лежащие на диске.

func isSpecialGroup(id string) bool { return id == KeenDNSGroupID || id == globalGroupID }

// linkedProxyGroupCheck — проверка прокси-группы, с которой группа связана; nil —
// связи нет, проверка не задана или цепочка группы уже другая.
func linkedProxyGroupCheck(ext ExtConfig, g Group) *GroupCheck {
	pg, ok := findProxyGroupByID(ext, ext.GroupLinks[g.ID])
	if !ok || len(g.Interface) < 2 || !slices.Equal(pg.Interfaces, g.Interface) {
		return nil
	}
	return normalizeProxyGroupCheck(pg.Check, len(g.Interface))
}

// syncLinkedProxyGroupInterfaces переносит смену цепочки туннелей из изменённой
// прокси-группы в явно связанные с ней правила (ext.GroupLinks). Обновляются только
// группы, чья текущая цепочка совпадала с прежней цепочкой прокси-группы, чтобы не
// перезаписать группы, отредактированные вручную или извне.
func syncLinkedProxyGroupInterfaces(groups []Group, prevExt, ext ExtConfig) ([]Group, bool) {
	if len(ext.GroupLinks) == 0 || len(prevExt.ProxyGroups) == 0 || len(ext.ProxyGroups) == 0 {
		return groups, false
	}
	var out []Group
	changed := false
	for i, g := range groups {
		if isSpecialGroup(g.ID) {
			continue
		}
		pgID := ext.GroupLinks[g.ID]
		if pgID == "" {
			continue
		}
		prevPg, hasPrev := findProxyGroupByID(prevExt, pgID)
		newPg, hasNew := findProxyGroupByID(ext, pgID)
		if !hasPrev || !hasNew || len(newPg.Interfaces) == 0 {
			continue
		}
		if slices.Equal(prevPg.Interfaces, newPg.Interfaces) || !slices.Equal(prevPg.Interfaces, g.Interface) {
			continue
		}
		if !changed {
			out = slices.Clone(groups)
			changed = true
		}
		out[i].Interface = GroupInterface(slices.Clone(newPg.Interfaces))
		if len(newPg.Interfaces) < 2 && out[i].Check != nil && normalizeGroupCheck(out[i].Check).Mode != "none" {
			out[i].Check = &GroupCheck{Mode: "none"}
		}
	}
	if !changed {
		return groups, false
	}
	return out, true
}

// proxyGroupInterfacesChanged сообщает, изменилась ли цепочка интерфейсов хотя бы
// у одной существующей прокси-группы между prev и next.
func proxyGroupInterfacesChanged(prev, next []ProxyGroup) bool {
	for _, p := range prev {
		for _, n := range next {
			if n.ID == p.ID && !slices.Equal(p.Interfaces, n.Interfaces) {
				return true
			}
		}
	}
	return false
}

// specialGroupLinks — прокси-группы, задающие проверку KeenDNS и Global.
func specialGroupLinks(ext ExtConfig) map[string]string {
	links := map[string]string{}
	kd := ext.KeenDNSCloud
	if pg, ok := findProxyGroupByID(ext, kd.ProxyGroupID); ok && pg.Check != nil &&
		kd.Enabled && !kd.CloudHealthCheck && len(pg.Interfaces) >= 2 {
		links[KeenDNSGroupID] = pg.ID
	}
	if g, ok := globalGroup(ext); ok && g.Check != nil {
		links[globalGroupID] = ext.GlobalTarget[len("pg:"):]
	}
	return links
}

// nextGroupLinks — связи для сохраняемых групп. links — присланные вместе с группами;
// nil — клиент о связях не знает: остаются те, у чьих групп цепочка прежняя.
func nextGroupLinks(ext ExtConfig, groups, prev []Group, links map[string]string) map[string]string {
	out := map[string]string{}
	for id, pgID := range ext.GroupLinks {
		if isSpecialGroup(id) {
			out[id] = pgID
		}
	}
	for _, g := range groups {
		if isSpecialGroup(g.ID) {
			continue
		}
		pgID := links[g.ID]
		if links == nil && slices.ContainsFunc(prev, func(p Group) bool {
			return p.ID == g.ID && slices.Equal(p.Interface, g.Interface)
		}) {
			pgID = ext.GroupLinks[g.ID]
		}
		if pgID != "" {
			out[g.ID] = pgID
		}
	}
	return out
}

func withNextGroupLinks(ext ExtConfig, groups, prev []Group, links map[string]string) ExtConfig {
	if ext.GroupLinks != nil || links != nil {
		ext.GroupLinks = nextGroupLinks(ext, groups, prev, links)
	}
	return ext
}

// settledGroupLinks — links после применения групп: без исчезнувших прокси-групп, у
// KeenDNS и Global — по действующим настройкам. Писать раньше нельзя: до применения
// связь говорит, что проверка группы ещё от прежней прокси-группы.
func settledGroupLinks(ext ExtConfig, links map[string]string) map[string]string {
	if links == nil {
		return nil
	}
	out := specialGroupLinks(ext)
	for id, pgID := range links {
		if _, ok := findProxyGroupByID(ext, pgID); ok && !isSpecialGroup(id) {
			out[id] = pgID
		}
	}
	return out
}

// settleGroupLinks пишет связи сохранённых групп (ext.GroupLinks).
func (op *Operator) settleGroupLinks(ext ExtConfig) error {
	return op.updateGroupLinks(func(map[string]string) map[string]string {
		return settledGroupLinks(ext, ext.GroupLinks)
	})
}

// refreshGroupLinks — то же, когда сами группы никто не сохранял: за основу лежащее на диске.
func (op *Operator) refreshGroupLinks(ext ExtConfig) error {
	return op.updateGroupLinks(func(cur map[string]string) map[string]string {
		return settledGroupLinks(ext, cur)
	})
}

// inferGroupLinks выводит связи для установки, где их ещё не было: только если с
// цепочкой группы совпадает ровно одна прокси-группа и её проверка уже действует.
func inferGroupLinks(ext ExtConfig, groups []Group) map[string]string {
	special := specialGroupLinks(ext)
	links := map[string]string{}
	for _, g := range groups {
		pgID := special[g.ID]
		if !isSpecialGroup(g.ID) && len(g.Interface) >= 2 {
			var same []string
			for _, pg := range ext.ProxyGroups {
				if slices.Equal(pg.Interfaces, g.Interface) {
					same = append(same, pg.ID)
				}
			}
			if len(same) == 1 {
				pgID = same[0]
			}
		}
		pg, ok := findProxyGroupByID(ext, pgID)
		if ok && pg.Check != nil && groupCheckEqual(normalizeProxyGroupCheck(pg.Check, len(pg.Interfaces)), g.Check) {
			links[g.ID] = pgID
		}
	}
	return links
}

// migrateGroupLinks однократно выводит связи по сохранённым группам. Ошибка чтения
// групп — не повод записать «связей нет»: попробуем в следующий раз.
func (op *Operator) migrateGroupLinks() {
	ext := op.LoadExtConfig()
	if ext.GroupLinks != nil {
		return
	}
	groups, err := op.LoadGroupsFile()
	if err != nil {
		return
	}
	_ = op.updateGroupLinks(func(cur map[string]string) map[string]string {
		if cur != nil {
			return cur
		}
		return inferGroupLinks(ext, groups)
	})
}

// updateGroupLinks меняет связи на диске, не трогая остальной конфиг; nil — нечего
// писать. Поколение не меняется: на netfilter и облако связи не влияют.
func (op *Operator) updateGroupLinks(next func(cur map[string]string) map[string]string) error {
	op.extConfigMu.Lock()
	defer op.extConfigMu.Unlock()
	ext, _ := op.loadExtConfigUnlocked()
	links := next(ext.GroupLinks)
	if links == nil || (ext.GroupLinks != nil && maps.Equal(ext.GroupLinks, links)) {
		return nil
	}
	ext.GroupLinks = links
	return op.writeExtConfigLocked(ext)
}
