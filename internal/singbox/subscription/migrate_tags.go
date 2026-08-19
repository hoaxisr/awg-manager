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
// allows — предикат regex-фильтра по имени сервера (flt.Allows). Guard обязан
// его учитывать: applyDiff скрывает члена по «исключён ИЛИ отфильтрован» и на
// пустом наборе возвращает ErrAllMembersFiltered (service.go:689,709).
func remapStaleTags(excluded []string, known []MemberInfo, diff DiffResult, active string, allows func(label string) bool) ([]string, string, bool) {
	if allows == nil {
		allows = func(string) bool { return true }
	}
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
	// тега.
	//
	// TransportKey сравнивается, когда он есть: без этого исключение одного
	// эндпоинта расползалось бы на соседние. Тег мог протухнуть не только от
	// смены схемы — уровень ключа зависит от состава фида, поэтому группа
	// схлопывается и расширяется обратно по мере того, как провайдер убирает
	// и возвращает эндпоинты. В такой момент широкое сравнение исключило бы и
	// тот эндпоинт, которого пользователь не трогал.
	//
	// У записей, сделанных до появления поля, оно пустое — для них остаётся
	// прежнее грубое сравнение (credential и reality short_id в MemberInfo нет
	// в любом случае). Направление выбрано сознательно: лучше оставить
	// исключённым лишнее, чем самовольно включить то, что пользователь выключил.
	matches := func(mi MemberInfo) []string {
		var out []string
		for _, t := range all {
			if !sameServer(mi, toMemberInfo(t.Tag, t.Out)) {
				continue
			}
			out = append(out, t.Tag)
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
			// Обычный случай «сервер ушёл от провайдера»: тег сохраняем.
			//
			// Оговорка: он защищает только пока схема тегов не менялась.
			// MemberInfo исключённых пересобирается из фида (SetMembersExtras),
			// поэтому у сервера, отсутствовавшего в момент refresh, метаданных
			// не остаётся — вернувшись уже с новой схемой, он получит другой
			// тег, сопоставить будет не по чему, и он материализуется активным.
			// Данных для более точного поведения на диске просто нет.
			add(tag)
			continue
		}
		for _, t := range found {
			add(t)
		}
		changed = true
	}

	// Guard: расширение «один тег → N» способно не оставить ни одного живого
	// члена. Ручной путь такое запрещает (ErrAllMembersExcluded), а applyDiff
	// на пустом наборе валит весь refresh (ErrAllMembersFiltered) — и валил бы
	// его на каждом тике, потому что remap пересчитывается заново. Считаем
	// оставшихся так же, как applyDiff: член жив, если не исключён И не срезан
	// фильтром.
	if changed {
		remaining := 0
		for _, t := range all {
			if !seen[t.Tag] && allows(t.Out.Label) {
				remaining++
			}
		}
		if remaining == 0 {
			return excluded, active, false
		}
	}

	// Активный член не может оказаться исключённым: seen к этому моменту
	// содержит итоговый набор исключений.
	newActive := active
	if active != "" && !current[active] {
		if mi, ok := infoByTag[active]; ok {
			for _, candidate := range matches(mi) {
				if !seen[candidate] && allows(labelOf(all, candidate)) {
					newActive = candidate
					changed = true
					break
				}
			}
		}
	}

	return result, newActive, changed
}

// labelOf возвращает имя сервера по тегу — для проверки его фильтром.
func labelOf(all []TaggedOutbound, tag string) string {
	for _, t := range all {
		if t.Tag == tag {
			return t.Out.Label
		}
	}
	return ""
}
