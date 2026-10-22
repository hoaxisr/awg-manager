package zapret

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// ErrNotInstalled — zapret не найден на роутере.
var ErrNotInstalled = fmt.Errorf("Zapret is not installed")

// controlTimeout ограничивает init-скрипт. Рестарт поднимает демон,
// перечитывает списки и правит firewall — на слабом MIPS это десятки
// секунд, 30 может не хватить, 90 — с запасом на списки RKN.
const controlTimeout = 90 * time.Second

// Strategies — разобранные стратегии плюс сырое тело переменной
// опций, чтобы UI мог показать нераспознанный хвост.
type Strategies struct {
	Revision string     `json:"revision"`
	OptVar   string     `json:"optVar"`
	Config   string     `json:"config"`
	Items    []Strategy `json:"items"`
	// Raw — значение переменной как есть. Нужно для «показать как есть»
	// и для отладки: парсер не обязан понимать все опции nfqws2.
	Raw string `json:"raw"`
	// ParseWarnings — проблемы уровня файла (а не отдельной стратегии).
	ParseWarnings []Warning `json:"parseWarnings,omitempty"`
}

// GetStrategies читает и разбирает стратегии из конфига.
func (s *Service) GetStrategies() (Strategies, error) {
	l, err := s.resolveLayout()
	if err != nil {
		return Strategies{}, err
	}
	raw, err := os.ReadFile(l.config)
	if err != nil {
		return Strategies{}, err
	}
	blk, ok := readOptBlockFromString(string(raw), l.optVar)
	if !ok {
		return Strategies{OptVar: l.optVar, Config: l.config, Items: []Strategy{}},
			fmt.Errorf("%s does not define %s", l.config, l.optVar)
	}
	items := parseStrategies(blk.value)
	out := Strategies{
		Revision: Revision(l.config, string(raw)),
		OptVar:   l.optVar,
		Config:   l.config,
		Items:    items,
		Raw:      blk.value,
	}
	if len(items) == 0 {
		out.ParseWarnings = append(out.ParseWarnings,
			Warning{Code: "strategies_empty", Detail: l.optVar})
	}
	if len(items) > maxProfiles {
		out.ParseWarnings = append(out.ParseWarnings,
			Warning{Code: "strategies_limit_reached", Detail: strconv.Itoa(maxProfiles)})
	}
	// Ссылки на списки резолвим в файлы: без этого UI не может показать,
	// пуст ли список, и не может предупредить, что nfqws2 не стартует.
	hydrateHostlistRefs(l, out.Items)
	for i := range out.Items {
		out.Items[i].Warnings = strategyWarnings(out.Items[i], l.optVar == optVarV2)
	}
	return out, nil
}

func hydrateHostlistRefs(l layout, items []Strategy) {
	for i := range items {
		// Плейсхолдеры разворачиваем первыми: они задают основной список
		// доменов стратегии и должны идти перед явными путями.
		for _, ph := range items[i].Placeholders {
			items[i].Hostlists = append(items[i].Hostlists,
				expandHostlistRef(l, StrategyHostlist{Path: ph})...)
		}
		items[i].Hostlists = hydrateRefs(l, items[i].Hostlists)
		items[i].HostlistExcludes = hydrateRefs(l, items[i].HostlistExcludes)
		items[i].HostlistAuto = hydrateRefs(l, items[i].HostlistAuto)
		items[i].Ipsets = hydrateRefs(l, items[i].Ipsets)
	}
}

func hydrateRefs(l layout, refs []StrategyHostlist) []StrategyHostlist {
	out := make([]StrategyHostlist, 0, len(refs))
	for _, ref := range refs {
		// Уже развёрнутый плейсхолдер пересобирать нельзя: путь теперь
		// абсолютный и реальный, а пометка происхождения из <HOSTLIST>
		// должна дожить до карточки — по ней UI объясняет пользователю,
		// откуда взялся этот список.
		if ref.FromPlaceholder != "" {
			refreshed := newHostlistRef(resolveRefPath(l, ref.Path), refIsExclude(ref.Path))
			refreshed.FromPlaceholder = ref.FromPlaceholder
			out = append(out, refreshed)
			continue
		}
		out = append(out, expandHostlistRef(l, ref)...)
	}
	return out
}

// expandHostlistRef разворачивает ссылку на список.
//
// Ссылка бывает двух видов. Обычная — путь к файлу, и тогда мы просто
// считаем его содержимое. Но zapret и его форки часто пишут в конфиг
// плейсхолдер <HOSTLIST>, который init-скрипт разворачивает при старте в
// несколько реальных списков (common/list.sh: filter_apply_hostlist_target).
// Такой «путь» файлом не является: если не развернуть его, карточка
// показала бы «файла нет» на работающей стратегии и ругалась бы на то,
// на что ругаться нечего.
func expandHostlistRef(l layout, ref StrategyHostlist) []StrategyHostlist {
	specs, isPlaceholder := placeholderTargets[ref.Path]
	if !isPlaceholder {
		// Ссылки могут быть относительными — nfqws2 резолвит их
		// относительно каталога демона, и мы обязаны повторить именно
		// это, иначе покажем «файл не найден» для существующего списка.
		return []StrategyHostlist{newHostlistRef(resolveRefPath(l, ref.Path), refIsExclude(ref.Path))}
	}

	out := make([]StrategyHostlist, 0, len(specs)+1)
	for _, sp := range specs {
		// Плейсхолдер всегда указывает на существующий набор файлов, но
		// указывает не на один: пометка explains, что список на карточке
		// пришёл из <HOSTLIST>, а не из явного пути.
		resolved := newHostlistRef(resolveHostlistRef(l, sp.filename), sp.exclude)
		resolved.FromPlaceholder = ref.Path
		out = append(out, resolved)
	}
	return out
}

// resolveHostlistRef ищет файл списка с учётом .gz, как это делает
// find_hostlists() в common/list.sh.
func resolveHostlistRef(l layout, filename string) string {
	path, _ := resolveHostlistFile(l.ipset, filename)
	return path
}

// placeholderSpec — один список, в который разворачивается плейсхолдер.
type placeholderSpec struct {
	filename string
	exclude  bool
}

// placeholderTargets раскрывает плейсхолдер в список целей.
//
// Состав взят из filter_apply_hostlist_target в common/list.sh:
//
//	--hostlist=<user> --hostlist=<main> --hostlist-exclude=<exclude>
//
// а <HOSTLIST_NOAUTO> отличается лишь тем, что не добавляет автосписок
// (--hostlist-auto=<auto>), который дописывает сам nfqws2.
var placeholderTargets = map[string][]placeholderSpec{
	"<HOSTLIST>": {
		{filename: "zapret-hosts-user.txt"},
		{filename: "zapret-hosts.txt"},
		{filename: "zapret-hosts-user-exclude.txt", exclude: true},
		{filename: "zapret-hosts-auto.txt"},
	},
	"<HOSTLIST_NOAUTO>": {
		{filename: "zapret-hosts-user.txt"},
		{filename: "zapret-hosts.txt"},
		{filename: "zapret-hosts-user-exclude.txt", exclude: true},
	},
}

// resolveRefPath превращает относительный путь из конфига в абсолютный
// относительно базы zapret. Абсолютные возвращает как есть.
func resolveRefPath(l layout, p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return filepath.Join(l.base, p)
}

// saveStrategies записывает карточки обратно в конфиг. Вызывающий держит opMu.
//
// Пишется ТОЛЬКО блок переменной опций (см. writeOptBlock): остальные
// ключи config'а — NFQWS2_ENABLE, QNUM, IFACE_WAN и прочие — остаются
// нетронутыми, потому что мы их не понимаем и затирать их не имеем
// права. Пользователь после этого обязан перезапустить демон: nfqws2
// читает опции ровно один раз, при старте процесса.
func (s *Service) saveStrategies(items []Strategy, revision string) (Strategies, error) {
	l, err := s.resolveLayout()
	if err != nil {
		return Strategies{}, err
	}
	if err := checkWriteTarget(l, l.config); err != nil {
		return Strategies{}, err
	}
	if err := checkRevision(l.config, revision); err != nil {
		return Strategies{}, err
	}

	// Карточки приходят из браузера; пересобираем тело из Raw, а не из
	// разобранных полей — иначе любое поле, которое мы не покрыли
	// парсером, молча потерялось бы при сохранении.
	body := strategiesToBody(items)
	if err := validateProfiles(splitProfiles(body)); err != nil {
		return Strategies{}, err
	}
	if err := checkRevision(l.config, revision); err != nil {
		return Strategies{}, err
	}
	if err := s.checkSelected(l); err != nil {
		return Strategies{}, err
	}
	if err := writeOptBlock(l.config, l.optVar, body, fileMode(l.config)); err != nil {
		return Strategies{}, err
	}
	s.appLog.Info("strategies", l.config, fmt.Sprintf("записано стратегий: %d", len(items)))
	return s.GetStrategies()
}

// ReorderStrategies двигает карточку в списке. Порядок — приоритет:
// nfqws2 проверяет фильтры строго слева направо и выигрывает первый
// подошедший, поэтому это не косметика.
func (s *Service) ReorderStrategies(from, to int, revision string) (Strategies, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	cur, err := s.GetStrategies()
	if err != nil {
		return Strategies{}, err
	}
	items := append([]Strategy(nil), cur.Items...)
	if from < 0 || from >= len(items) {
		return Strategies{}, fmt.Errorf("strategy %d does not exist", from+1)
	}
	if to < 0 || to >= len(items) {
		return Strategies{}, fmt.Errorf("cannot move strategy to position %d; there are %d strategies", to+1, len(items))
	}
	moved := items[from]
	items = append(items[:from], items[from+1:]...)
	items = append(items, Strategy{})
	copy(items[to+1:], items[to:])
	items[to] = moved

	if _, err := s.saveStrategies(reindex(items), revision); err != nil {
		return Strategies{}, err
	}
	s.appLog.Info("strategies", cur.Config, fmt.Sprintf("порядок изменён: %d → %d", from+1, to+1))
	return s.GetStrategies()
}

// GetHostlists возвращает описания списков.
func (s *Service) GetHostlists() ([]HostlistInfo, error) {
	l, err := s.resolveLayout()
	if err != nil {
		return nil, err
	}
	return listHostlists(l), nil
}

// ReadHostlist отдаёт содержимое пользовательского списка.
func (s *Service) ReadHostlist(id HostlistID) (HostlistID, string, string, error) {
	l, err := s.resolveLayout()
	if err != nil {
		return "", "", "", err
	}
	path, content, err := readHostlist(l, id)
	return id, path, content, err
}

// HostlistApplyError means the file was saved but the daemon could not reload it.
// Callers must not report the write itself as failed or discard the saved state.
type HostlistApplyError struct{ Err error }

func (e *HostlistApplyError) Error() string { return e.Err.Error() }
func (e *HostlistApplyError) Unwrap() error { return e.Err }

// WriteHostlist saves the list and reapplies it only when this installation is running.
func (s *Service) WriteHostlist(id HostlistID, content, revision string) (HostlistID, string, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	l, err := s.resolveLayout()
	if err != nil {
		return "", "", err
	}
	path, err := s.writeHostlist(l, id, content, revision)
	if err != nil {
		return id, path, err
	}
	_, pids := runningPIDsForBase(l.base)
	pids = managedDaemonPIDs(l, pids)
	if len(pids) > 0 {
		if err := s.control("restart-daemons", l.base); err != nil {
			return id, path, &HostlistApplyError{Err: err}
		}
	}
	return id, path, nil
}

// Control запускает/останавливает/перезапускает zapret через его
// собственный init-скрипт. base — установка, которую видел клиент: если
// выбор сменился, команда уйдёт не туда, поэтому это конфликт. Для
// применения правок используем только restart-daemons, чтобы не
// пересоздавать правила firewall.
func (s *Service) Control(action, base string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.control(action, base)
}

func (s *Service) control(action, base string) error {
	r, err := s.resolveLayoutState()
	if err != nil {
		return err
	}
	l := r.layout
	if base == "" || base != l.base {
		return ErrConflict
	}
	// A broad nfqws process match can also include independent scanner runs.
	// The helper falls back to the full live set for legacy init layouts.
	r.selectedPIDs = managedDaemonPIDs(l, r.selectedPIDs)
	if l.init == "" {
		return fmt.Errorf("Zapret init script was not found in %s; restart the daemon manually", strings.Join(initDirs, ", "))
	}
	switch action {
	case "start", "stop", "restart", "restart-daemons":
	default:
		return fmt.Errorf("unknown action %q; available actions are start, stop, restart, and restart-daemons", action)
	}

	verb := action
	if action == "restart-daemons" {
		if len(r.selectedPIDs) == 0 {
			return nil
		}
		body, err := os.ReadFile(l.init)
		if err != nil {
			return err
		}
		// Never fall back to a full restart: it can reorder firewall rules.
		if verb = daemonRestartVerb(body); verb == "" {
			return fmt.Errorf("daemon-only restart is not supported by %s", l.init)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	if err := s.checkSelected(l); err != nil {
		return err
	}
	result, err := sysexec.RunWithOptions(ctx, l.init, []string{verb}, sysexec.Options{Timeout: controlTimeout})
	if err != nil {
		// Init-скрипты zapret печатаютUsage в stderr и кодом 1 при
		// неизвестном аргументе, а часть из них (stock sysv zapret2) вообще
		// не знает verb `status`. Поэтому текст ошибки показываем
		// пользователю целиком — он самый полезный диагностический
		// инструмент в этой ситуации.
		return fmt.Errorf("zapret %s: %w", action, sysexec.FormatError(result, err))
	}
	if err := s.confirmControlState(ctx, l, action, r.selectedPIDs); err != nil {
		return err
	}
	s.appLog.Info(action, l.init, "zapret: команда выполнена")
	s.mu.Lock()
	s.versionAt = time.Time{}
	s.mu.Unlock()
	return nil
}

// The init helper records managed workers under its own PIDDIR. Other nfqws
// processes from this installation can belong to scans and must survive reload.
// Read literal settings only; never source or evaluate installed shell files.
func managedDaemonPIDs(l layout, live []int) []int {
	init, err := filepath.EvalSymlinks(l.init)
	if err != nil {
		return live
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(init), "functions"))
	if err != nil {
		return live
	}
	dir := strings.Trim(findAssignment(string(body), "PIDDIR"), "\"'")
	if !filepath.IsAbs(dir) || strings.ContainsAny(dir, "$`\n") {
		return live
	}
	liveSet := make(map[int]bool, len(live))
	for _, pid := range live {
		liveSet[pid] = true
	}
	files, err := filepath.Glob(filepath.Join(dir, filepath.Base(l.binary)+"_*.pid"))
	if err != nil {
		return live
	}
	var managed []int
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil && liveSet[pid] {
			managed = append(managed, pid)
		}
	}
	return managed
}

var caseLabelRe = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_|-]+)\s*\)`)

// daemonRestartVerb returns the daemon-only restart verb as the init script's
// case statement spells it: stock zapret accepts restart-daemons, z2k only
// restart_daemons. Empty means the script has no such verb.
func daemonRestartVerb(body []byte) string {
	for _, m := range caseLabelRe.FindAllSubmatch(body, -1) {
		for _, label := range strings.Split(string(m[1]), "|") {
			if label == "restart-daemons" || label == "restart_daemons" {
				return label
			}
		}
	}
	return ""
}

// Init scripts can exit successfully without changing the daemon. Confirm the
// selected installation's live processes twice before reporting success. A
// restart must also replace the old processes so pending config is not treated
// as applied after a no-op script.
func (s *Service) confirmControlState(ctx context.Context, want layout, action string, previous []int) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	matched := false
	for {
		current, err := s.resolveLayoutState()
		if err != nil {
			return err
		}
		if canonicalBase(current.layout.base) != canonicalBase(want.base) {
			return ErrConflict
		}
		current.selectedPIDs = managedDaemonPIDs(want, current.selectedPIDs)
		running := len(current.selectedPIDs) > 0
		ready := running == (action != "stop")
		if ready && (action == "restart" || action == "restart-daemons") {
			for _, pid := range previous {
				for _, live := range current.selectedPIDs {
					if pid == live {
						ready = false
					}
				}
			}
		}
		if ready && matched {
			return nil
		}
		matched = ready
		select {
		case <-ctx.Done():
			return fmt.Errorf("zapret %s: the requested process state was not confirmed: %w", action, ctx.Err())
		case <-ticker.C:
		}
	}
}
