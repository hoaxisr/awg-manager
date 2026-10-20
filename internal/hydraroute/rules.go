package hydraroute

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ErrRulesChanged — файлы правил изменились после того, как клиент их прочитал
// (их переписал HRweb, руки или наш же предыдущий запрос). Группа адресуется
// позицией, поэтому без совпадения ревизии правка ушла бы не той группе.
var ErrRulesChanged = errors.New("HydraRoute Neo rules changed outside this request")

// GroupRef адресует группу позицией в цели на конкретной ревизии файлов.
// В самих файлах id хранить негде: HRweb принял бы его за описание группы.
type GroupRef struct {
	Revision string
	Target   string
	Index    int
}

func (r GroupRef) String() string {
	return r.Revision + ":" + r.Target + ":" + strconv.Itoa(r.Index)
}

// ParseGroupRef разбирает "<rev>:<target>:<n>". Цель может содержать ':',
// поэтому ревизия — до первого ':', индекс — после последнего.
func ParseGroupRef(s string) (GroupRef, error) {
	first := strings.Index(s, ":")
	last := strings.LastIndex(s, ":")
	if first <= 0 || last <= first+1 {
		return GroupRef{}, fmt.Errorf("invalid HR group id %q", s)
	}
	idx, err := strconv.Atoi(s[last+1:])
	if err != nil || idx < 0 {
		return GroupRef{}, fmt.Errorf("invalid HR group id %q", s)
	}
	return GroupRef{Revision: s[:first], Target: s[first+1 : last], Index: idx}, nil
}

// ListRuleSet читает оба файла. Файлы — источник правды: каждый вызов
// перечитывает их, ручные правки видны сразу. Читает под s.mu: наша запись —
// два rename подряд, и снимок между ними дал бы ревизию, которой никогда не
// будет, а следующей мутации — ложный 409.
func (s *Service) ListRuleSet() (RuleSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, i, err := readRuleFiles()
	if err != nil {
		return RuleSet{}, err
	}
	return parseRuleFiles(d, i), nil
}

// RulesRevision — ревизия текущих файлов (для пустой цели, у которой нет
// групп, а значит и id с ревизией). Под s.mu — см. ListRuleSet.
func (s *Service) RulesRevision() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, i, err := readRuleFiles()
	if err != nil {
		return "", err
	}
	return ruleRevision(d, i), nil
}

func readRuleFiles() (string, string, error) {
	d, err := readOrEmpty(domainConfPath)
	if err != nil {
		return "", "", err
	}
	i, err := readOrEmpty(ipListPath)
	if err != nil {
		return "", "", err
	}
	return d, i, nil
}

// CreateGroup дописывает группу в конец цели. Ревизию не сверяет: ни на
// какую позицию не ссылается.
func (s *Service) CreateGroup(target string, g Group) (GroupRef, error) {
	if err := validateGroup(target, g); err != nil {
		return GroupRef{}, err
	}
	rs, err := s.mutateRules("", func(rs *RuleSet) error {
		t := rs.ensureTarget(target)
		t.Groups = append(t.Groups, g)
		return nil
	})
	if err != nil {
		return GroupRef{}, err
	}
	s.appLog.Info("create-group", target, fmt.Sprintf("description=%q domains=%d subnets=%d", g.Description, len(g.Domains), len(g.Subnets)))
	return GroupRef{Revision: rs.Revision, Target: target, Index: findGroup(rs.Target(target), g)}, nil
}

// findGroup ищет записанную группу в перечитанном документе. Позиция может
// не совпасть с той, куда группу положили: формат склеивает доменную и
// CIDR-половины с одинаковым явным `##описание` (linkGroups HRweb). Ищем
// последнюю группу цели с теми же доменами (или подсетями у группы без
// доменов).
func findGroup(t *Target, g Group) int {
	if t == nil {
		return 0
	}
	for i := len(t.Groups) - 1; i >= 0; i-- {
		have := t.Groups[i]
		if len(g.Domains) > 0 && slices.Equal(have.Domains, g.Domains) {
			return i
		}
		if len(g.Domains) == 0 && slices.Equal(have.Subnets, g.Subnets) {
			return i
		}
	}
	return len(t.Groups) - 1
}

// UpdateGroup заменяет описание и содержимое группы. Флаги половин
// сохраняются; новая половина получает состояние группы. Смена цели
// переносит группу в конец новой цели.
func (s *Service) UpdateGroup(ref GroupRef, target string, g Group) (GroupRef, error) {
	if err := validateGroup(target, g); err != nil {
		return GroupRef{}, err
	}
	rs, err := s.mutateRules(ref.Revision, func(rs *RuleSet) error {
		old, err := groupAt(rs, ref)
		if err != nil {
			return err
		}
		off := !old.Enabled()
		g.DomainsDisabled, g.SubnetsDisabled = off, off
		if len(old.Domains) > 0 {
			g.DomainsDisabled = old.DomainsDisabled
		}
		if len(old.Subnets) > 0 {
			g.SubnetsDisabled = old.SubnetsDisabled
		}
		if target == ref.Target {
			*old = g
			return nil
		}
		removeGroups(rs, []GroupRef{ref})
		t := rs.ensureTarget(target)
		t.Groups = append(t.Groups, g)
		return nil
	})
	if err != nil {
		return GroupRef{}, err
	}
	s.appLog.Info("update-group", target, fmt.Sprintf("was %s", ref))
	idx := -1
	if target == ref.Target {
		idx = findGroupFrom(rs.Target(target), g, ref.Index)
	}
	if idx < 0 {
		idx = findGroup(rs.Target(target), g)
	}
	return GroupRef{Revision: rs.Revision, Target: target, Index: idx}, nil
}

// findGroupFrom — первая группа, точно совпавшая с g (описание, домены,
// подсети), начиная с позиции from: правка на месте позицию не двигает, а
// при двух одинаковых группах findGroup вернул бы последнюю. -1 — не нашли
// (группу склеило с соседней, см. findGroup).
func findGroupFrom(t *Target, g Group, from int) int {
	if t == nil {
		return -1
	}
	for i := max(from, 0); i < len(t.Groups); i++ {
		have := t.Groups[i]
		if have.Description == g.Description && slices.Equal(have.Domains, g.Domains) && slices.Equal(have.Subnets, g.Subnets) {
			return i
		}
	}
	return -1
}

// DeleteGroups удаляет группы одной записью; все ссылки обязаны нести одну
// ревизию и разрешаются по одному снимку.
func (s *Service) DeleteGroups(refs []GroupRef) error {
	if len(refs) == 0 {
		return nil
	}
	for _, r := range refs[1:] {
		if r.Revision != refs[0].Revision {
			return ErrRulesChanged
		}
	}
	_, err := s.mutateRules(refs[0].Revision, func(rs *RuleSet) error {
		for _, r := range refs {
			if _, err := groupAt(rs, r); err != nil {
				return err
			}
		}
		removeGroups(rs, refs)
		return nil
	})
	if err == nil {
		s.appLog.Info("delete-groups", "", fmt.Sprintf("deleted %d", len(refs)))
	}
	return err
}

func (s *Service) SetGroupEnabled(ref GroupRef, enabled bool) error {
	_, err := s.mutateRules(ref.Revision, func(rs *RuleSet) error {
		g, err := groupAt(rs, ref)
		if err != nil {
			return err
		}
		g.SetEnabled(enabled)
		return nil
	})
	if err == nil {
		s.appLog.Info("set-group-enabled", ref.Target, fmt.Sprintf("index=%d enabled=%v", ref.Index, enabled))
	}
	return err
}

// DeleteTarget удаляет правило HRweb целиком: все группы цели и её место в
// PolicyOrder. Политику NDMS не трогает — она живёт на вкладке политик.
func (s *Service) DeleteTarget(target, revision string) error {
	_, err := s.mutateRulesThen(revision, func(rs *RuleSet) error {
		rs.Targets = slices.DeleteFunc(rs.Targets, func(t Target) bool { return t.Name == target })
		return nil
	}, func() error {
		cfg, err := ReadConfig()
		if err != nil {
			return err
		}
		order := slices.DeleteFunc(slices.Clone(cfg.PolicyOrder), func(n string) bool { return n == target })
		if len(order) == len(cfg.PolicyOrder) {
			return nil
		}
		return WritePolicyOrderOnly(order)
	})
	if err == nil {
		s.appLog.Info("delete-target", target, "rule deleted with its groups")
	}
	return err
}

// mutateRules — единственный путь записи правил. Под s.mu: перечитать файлы,
// сверить ревизию (пустая — не сверять), применить fn и, если текст
// изменился, записать оба файла, синхронизировать ForceInterface, назначить
// рестарт. Возвращает документ, перечитанный из записанного текста: позиции
// и ревизия в нём те, что увидит следующий List.
//
// Отката нет: сбой между записью domain.conf и ip.list (или в after)
// оставляет файлы частично новыми. Каждый файл пишется атомарно, оба
// остаются валидными для демона; следующий List покажет фактическое
// состояние, а ревизия клиента устареет (409, а не правка не той группы).
func (s *Service) mutateRules(want string, fn func(*RuleSet) error) (RuleSet, error) {
	return s.mutateRulesThen(want, fn, nil)
}

func (s *Service) mutateRulesThen(want string, fn func(*RuleSet) error, after func() error) (RuleSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.status.Installed {
		return RuleSet{}, ErrNotInstalled
	}
	d, i, err := readRuleFiles()
	if err != nil {
		return RuleSet{}, err
	}
	rs := parseRuleFiles(d, i)
	if want != "" && want != rs.Revision {
		return RuleSet{}, ErrRulesChanged
	}
	if err := fn(&rs); err != nil {
		return RuleSet{}, err
	}
	nd, ni := renderRuleFiles(rs)
	// Текст не изменился (например, включить уже включённую группу) — файлы
	// не трогаем и демон не перезапускаем; after выполняется всё равно:
	// у пустой цели DeleteTarget меняет только PolicyOrder.
	unchanged := nd == d && ni == i
	if !unchanged {
		if err := WriteWholeFile(domainConfPath, nd); err != nil {
			return RuleSet{}, err
		}
		if err := WriteWholeFile(ipListPath, ni); err != nil {
			return RuleSet{}, err
		}
	}
	if after != nil {
		if err := after(); err != nil {
			return RuleSet{}, err
		}
	}
	written := parseRuleFiles(nd, ni)
	if !unchanged {
		s.updateForceInterface(written)
		s.scheduleRestart("rules-write")
	}
	return written, nil
}

func groupAt(rs *RuleSet, ref GroupRef) (*Group, error) {
	t := rs.Target(ref.Target)
	if t == nil || ref.Index < 0 || ref.Index >= len(t.Groups) {
		// Ревизия совпала, а позиции нет — id собран руками.
		return nil, fmt.Errorf("HR group %s not found", ref)
	}
	return &t.Groups[ref.Index], nil
}

// removeGroups удаляет группы по позициям одного снимка; цель без групп
// остаётся в PolicyOrder и из файлов просто пропадает.
func removeGroups(rs *RuleSet, refs []GroupRef) {
	drop := map[string]map[int]bool{}
	for _, r := range refs {
		if drop[r.Target] == nil {
			drop[r.Target] = map[int]bool{}
		}
		drop[r.Target][r.Index] = true
	}
	for ti := range rs.Targets {
		t := &rs.Targets[ti]
		kept := t.Groups[:0]
		for gi, g := range t.Groups {
			if !drop[t.Name][gi] {
				kept = append(kept, g)
			}
		}
		t.Groups = kept
	}
}

// validateGroup отсекает то, что сломало бы формат файлов. Описание
// необязательно и может повторяться (модель HRweb).
func validateGroup(target string, g Group) error {
	if target == "" || strings.ContainsAny(target, "/,#\r\n \t") {
		return fmt.Errorf("invalid rule target %q", target)
	}
	if strings.ContainsAny(g.Description, "\r\n") {
		return fmt.Errorf("group name must be a single line")
	}
	if strings.EqualFold(strings.TrimSpace(g.Description), oversizedSectionName) {
		return fmt.Errorf("group name %q is reserved by HydraRoute Neo", oversizedSectionName)
	}
	if len(g.Domains) == 0 && len(g.Subnets) == 0 {
		return fmt.Errorf("group must have at least one domain or subnet")
	}
	for _, d := range g.Domains {
		// `#` в начале первого домена выключил бы строку, `##` — сделал бы её заголовком.
		// Пробелы по краям парсер срежет при чтении: записанное не совпало бы с прочитанным.
		if d == "" || d != strings.TrimSpace(d) || strings.ContainsAny(d, "/,\r\n") || strings.HasPrefix(d, "#") {
			return fmt.Errorf("invalid domain %q", d)
		}
		if err := hrwebTokenError(d); err != nil {
			return err
		}
	}
	for _, c := range g.Subnets {
		// Пустая строка закрыла бы блок ip.list, `/X` открыл бы блок другой цели.
		if c == "" || c != strings.TrimSpace(c) || strings.ContainsAny(c, "\r\n") || strings.HasPrefix(c, "#") || strings.HasPrefix(c, "/") {
			return fmt.Errorf("invalid subnet %q", c)
		}
		if err := hrwebTokenError(c); err != nil {
			return err
		}
	}
	return nil
}

var (
	hrwebBadChar  = regexp.MustCompile(`[^A-Za-z0-9.\-:/]`)            //nolint:gochecknoglobals
	hrwebEdge     = regexp.MustCompile(`^[.\-:/]|[.\-:/]$`)            //nolint:gochecknoglobals
	hrwebAdjacent = regexp.MustCompile(`[.:/]{2}|-\.|\.-|-/|/-|:-|-:`) //nolint:gochecknoglobals
	hrwebIPv4     = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)      //nolint:gochecknoglobals

	errHRwebMask  = errors.New("IP address needs a network mask (/32 or /128)")                                                        //nolint:gochecknoglobals
	errHRwebChars = errors.New("HydraRoute Neo allows only latin letters, digits, '.', '-', ':' and '/', not at the edges or doubled") //nolint:gochecknoglobals
)

// hrwebTokenError повторяет classifyLine + InputValidator.isValidToken из
// HRweb (JS вшит в /opt/bin/hrweb, сверено с 1.68.0). HRweb при
// сохранении прогоняет этим ВСЕ токены обоих файлов, и один отвергнутый токен
// в любой группе запрещает сохранить в HRweb любое правило (#1045).
func hrwebTokenError(t string) error {
	geosite := strings.HasPrefix(t, "geosite:")
	cidr := strings.HasPrefix(t, "geoip:") || (!geosite && strings.Contains(t, "/"))
	maskless := !cidr && !geosite && (hrwebIPv4.MatchString(t) || strings.Contains(t, ":"))
	if maskless && hrwebValid(t, true) {
		return fmt.Errorf("%q: %w", t, errHRwebMask)
	}
	if maskless || !hrwebValid(t, cidr) {
		return fmt.Errorf("%q: %w", t, errHRwebChars)
	}
	return nil
}

func hrwebValid(t string, cidr bool) bool {
	adj := t
	if cidr {
		adj = strings.ReplaceAll(t, "::", "x")
	}
	return !hrwebBadChar.MatchString(t) && !hrwebEdge.MatchString(t) && !hrwebAdjacent.MatchString(adj) &&
		(!cidr || strings.HasPrefix(t, "geoip:") || strings.HasPrefix(t, "geosite:") || !strings.Contains(t, "-"))
}
