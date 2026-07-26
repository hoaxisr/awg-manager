package subscription

// Перенос исключений на новую схему тегов (issues #614/#625).
//
// Тег члена выводится из ключа идентичности, а ключ расширился полями
// транспорта. У групп, где раньше разные эндпоинты схлопывались в один тег,
// теги изменились — и сохранённые ExcludedTags перестали бы совпадать. Это
// данные пользователя: без переноса исключённые серверы молча вернулись бы в
// строй.
//
// Сопоставление идёт по метаданным (MemberInfo), а не пересчётом прежних
// ключей: иначе в коде пришлось бы вечно держать копию старой схемы. Побочная
// выгода — механизм переживёт и следующую смену ключей.

// remapStaleTags переносит исключения и активного члена на текущую схему тегов.
// Возвращает новый список исключённых, нового активного и признак изменения.
//
// known — источник метаданных для протухших тегов: ExcludedMembers + Members.
func remapStaleTags(excluded []string, known []MemberInfo, diff DiffResult, active string) ([]string, string, bool) {
	all := make([]TaggedOutbound, 0, len(diff.New)+len(diff.Existing))
	all = append(all, diff.New...)
	all = append(all, diff.Existing...)

	current := make(map[string]bool, len(all))
	for _, t := range all {
		current[t.Tag] = true
	}
	infoByTag := make(map[string]MemberInfo, len(known))
	for _, m := range known {
		infoByTag[m.Tag] = m
	}

	// matches ищет в текущем фиде серверы, отвечающие метаданным протухшего
	// тега. Сравнение заведомо широкое: в MemberInfo нет ни credential, ни
	// reality short_id, поэтому под один старый тег может попасть больше
	// серверов, чем стояло за ним раньше. Направление выбрано сознательно —
	// лучше оставить исключённым лишнее, чем самовольно включить то, что
	// пользователь выключил.
	matches := func(mi MemberInfo) []string {
		var out []string
		for _, t := range all {
			c := toMemberInfo(t.Tag, t.Out)
			if c.Protocol == mi.Protocol && c.Server == mi.Server &&
				c.Port == mi.Port && c.SNI == mi.SNI && c.Transport == mi.Transport {
				out = append(out, t.Tag)
			}
		}
		return out
	}

	result := make([]string, 0, len(excluded))
	seen := make(map[string]bool, len(excluded))
	add := func(tag string) {
		if !seen[tag] {
			seen[tag] = true
			result = append(result, tag)
		}
	}

	changed := false
	for _, tag := range excluded {
		if current[tag] {
			add(tag)
			continue
		}
		mi, ok := infoByTag[tag]
		if !ok {
			add(tag) // нет метаданных — судить не о чем
			continue
		}
		found := matches(mi)
		if len(found) == 0 {
			// Обычный случай «сервер ушёл от провайдера». Тег обязан
			// сохраниться: когда сервер вернётся, он останется исключённым.
			add(tag)
			continue
		}
		for _, t := range found {
			add(t)
		}
		changed = true
	}

	// Guard: расширение «один тег → N» способно исключить вообще всех.
	// Ручной путь такое запрещает (ErrAllMembersExcluded), значит refresh не
	// имеет права создавать это состояние сам — откатываемся к исходному.
	if changed {
		remaining := 0
		for _, t := range all {
			if !seen[t.Tag] {
				remaining++
			}
		}
		if remaining == 0 {
			return excluded, active, false
		}
	}

	newActive := active
	if active != "" && !current[active] {
		if mi, ok := infoByTag[active]; ok {
			if found := matches(mi); len(found) > 0 {
				newActive = found[0]
				changed = true
			}
		}
	}

	return result, newActive, changed
}
