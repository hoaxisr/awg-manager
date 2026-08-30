package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
	"github.com/hoaxisr/awg-manager/internal/storage"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// GeoIPTagCounter — узкий контракт geoip-bypass: бюджет-валидация тегов и
// перечисление .dat, из которых наполняется набор. *hydraroute.GeoDataStore
// удовлетворяет обоим методам.
type GeoIPTagCounter interface {
	GeoIPTagCounts() map[string]int
	// GeoFilePaths отдаёт пути отслеживаемых geo-файлов. Наполнение обходит
	// ВСЕ geoip-файлы: GeoIPTagCounts (и UI) суммируют одноимённый тег по
	// ним всем, набор обязан покрывать ровно то же.
	GeoFilePaths() (geoIP, geoSite []string)
}

const bypassSetMaxElem = bypassset.SetMaxElem // = maxelem набора AWGM-BYPASS

// validateBypassGeoIPTags: суммарный размер выбранных geoip-тегов не
// превышает maxelem. Проверка КОНСЕРВАТИВНА: Count учитывает и IPv6-элементы
// .dat, в набор кладётся только IPv4 — ложный отказ возможен лишь вплотную
// к пределу.
func (s *ServiceImpl) validateBypassGeoIPTags(sr storage.SingboxRouterSettings) error {
	if len(sr.BypassGeoIPTags) == 0 || s.deps.GeoTagCounts == nil {
		return nil
	}
	total := 0
	counts := s.deps.GeoTagCounts.GeoIPTagCounts()
	for _, tag := range sr.BypassGeoIPTags {
		total += counts[strings.ToLower(strings.TrimSpace(tag))]
	}
	if total > bypassSetMaxElem {
		return fmt.Errorf("geoip-обход: выбрано ~%d записей при пределе %d — уберите часть тегов", total, bypassSetMaxElem)
	}
	return nil
}

// geoSourceAdapter собирает строки geoip-тега из ВСЕХ отслеживаемых .dat и
// отдаёт их bypassset.GeoSource. Обход всех файлов, а не первого попавшегося
// (как ExpandGeoTag), — потому что бюджет-валидация и UI суммируют
// одноимённый тег по всем файлам. Дубликаты строк между файлами гасит
// `ipset restore -exist`. Populate смотрит notFound РАНЬШЕ err, поэтому «тега
// нет ни в одном файле» отдаётся без ошибки рядом; ошибка разбора ЛЮБОГО
// файла — fail-closed: половина диапазонов хуже, чем явный сбой.
type geoSourceAdapter struct {
	files GeoIPTagCounter
	// extract — шов разбора одного .dat для тестов (nil =
	// hydraroute.ExtractGeoIPTagLines).
	extract func(path, tag string) ([]string, error)
}

func (g geoSourceAdapter) GeoIPTagLines(tag string) ([]string, bool, error) {
	extract := g.extract
	if extract == nil {
		extract = hydraroute.ExtractGeoIPTagLines
	}
	geoIP, _ := g.files.GeoFilePaths()
	var lines []string
	found := false
	for _, path := range geoIP {
		got, err := extract(path, tag)
		if err != nil {
			if errors.Is(err, hydraroute.ErrGeoTagNotFound) {
				continue
			}
			return nil, false, err
		}
		found = true
		lines = append(lines, got...)
	}
	if !found {
		return nil, true, nil
	}
	return lines, false, nil
}

// bypassPopulateTimeout ограничивает одну пересборку набора: разбор .dat и
// заливка сотен тысяч записей на MIPS небыстры, но конечны.
const bypassPopulateTimeout = 10 * time.Minute

// bypassSetWanted отвечает, нужен ли набор обхода ПРЯМО СЕЙЧАС, и с какими
// тегами. wanted=false — набора быть не должно (пустой список тегов, движок
// выключен, режим не tproxy). Ошибка чтения настроек отдаётся отдельно от
// wanted=false: «не смогли прочитать» не повод сносить живой набор.
func (s *ServiceImpl) bypassSetWanted() (tags []string, wanted bool, err error) {
	if s.deps.Settings == nil {
		return nil, false, errNoSettingsStore
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return nil, false, err
	}
	sr := settings.SingboxRouter
	if !sr.Enabled || len(sr.BypassGeoIPTags) == 0 ||
		(sr.RoutingMode != "" && sr.RoutingMode != "tproxy") {
		return nil, false, nil
	}
	return slices.Clone(sr.BypassGeoIPTags), true, nil
}

var errNoSettingsStore = errors.New("хранилище настроек не подключено")

// TriggerBypassSetPopulate асинхронно наполняет AWGM-BYPASS из выбранных
// geoip-тегов. No-op: пустой список, режим не tproxy, движок выключен. Пока
// наполнение идёт, повторный триггер только взводит признак повтора — по
// завершении прогон выполняется ещё раз с уже актуальными тегами (иначе
// уведомление о смене .dat или тегов проглатывалось бы без ретрая). По
// завершении публикует resource:invalidated (bypass-set) и пишет итог в
// журнал.
func (s *ServiceImpl) TriggerBypassSetPopulate() {
	tags, wanted, err := s.bypassSetWanted()
	if err != nil || !wanted {
		return
	}
	if !s.bypassPopulating.CompareAndSwap(false, true) {
		s.bypassRerunPending.Store(true)
		return
	}
	go func() {
		defer s.bypassPopulating.Store(false)
		for {
			s.runBypassPopulate(tags)
			// Триггер, пришедший ровно между этой проверкой и снятием
			// признака занятости, теряется — окно в наносекунды, следующая
			// смена настроек/.dat запустит наполнение заново.
			if !s.bypassRerunPending.CompareAndSwap(true, false) {
				return
			}
			next, wanted, err := s.bypassSetWanted()
			if err != nil || !wanted {
				return
			}
			tags = next
		}
	}()
}

// runBypassPopulate — один прогон наполнения. Если к его концу набор больше
// не нужен (пользователь снял теги / выключил движок / сменил режим), teardown
// уже прошёл и снёс набор с дампом, а наш swap+save их воскресил: сносим
// сироту сами и НЕ публикуем итог как актуальное состояние — иначе хук вечно
// восстанавливал бы набор, которого быть не должно.
func (s *ServiceImpl) runBypassPopulate(tags []string) {
	ctx, cancel := context.WithTimeout(context.Background(), bypassPopulateTimeout)
	defer cancel()
	res, err := s.populateBypassSetOnce(ctx, tags)
	if _, wanted, wErr := s.bypassSetWanted(); wErr == nil && !wanted {
		// ctx мог истечь вместе с наполнением — снос идёт по своему.
		s.teardownBypassSet(context.Background())
		return
	}
	s.storeBypassSetOutcome(res, err)
}

// populateBypassSetOnce — продакшн-наполнение (или тест-шов, если задан).
func (s *ServiceImpl) populateBypassSetOnce(ctx context.Context, tags []string) (bypassset.PopulateResult, error) {
	if s.populateBypassSet != nil {
		return s.populateBypassSet(ctx, tags)
	}
	if s.deps.GeoTagCounts == nil {
		return bypassset.PopulateResult{}, fmt.Errorf("geo-данные не подключены")
	}
	return bypassset.Populate(ctx, geoSourceAdapter{files: s.deps.GeoTagCounts}, bypassset.PopulateInput{
		GeoIPTags: tags,
		SavePath:  bypassSavePath,
	})
}

// storeBypassSetOutcome фиксирует итог наполнения, журналирует его и зовёт
// фронт перечитать статус. Текст ошибки отдаётся как есть: «ipset save» после
// удавшегося swap значит «набор живой, но дампа для хука нет» — это не то же
// самое, что «набор не собран».
func (s *ServiceImpl) storeBypassSetOutcome(res bypassset.PopulateResult, err error) {
	s.mu.Lock()
	s.bypassLastPopulate = time.Now()
	s.bypassMissingTags = res.MissingTags
	s.bypassEntryCount, s.bypassCountOK = res.EntryCount, res.CountOK
	if !res.CountOK {
		s.bypassEntryCount = 0
	}
	if err != nil {
		s.bypassLastError = err.Error()
	} else {
		s.bypassLastError = ""
	}
	s.mu.Unlock()

	switch {
	case err != nil:
		s.bypassLog.Warn("bypass-set", "", "geoip-обход: "+err.Error())
	case res.CountOK:
		s.bypassLog.Info("bypass-set", "", fmt.Sprintf("geoip-обход: набор наполнен, записей %d", res.EntryCount))
	default:
		s.bypassLog.Info("bypass-set", "", "geoip-обход: набор наполнен, размер набора получить не удалось")
	}
	if len(res.MissingTags) > 0 {
		s.bypassLog.Warn("bypass-set", "", "geoip-обход: теги не найдены в .dat: "+strings.Join(res.MissingTags, ", "))
	}
	if s.deps.Bus != nil {
		events.PublishInvalidatedTo(s.deps.Bus, events.ResourceBypassSet, "bypass-set-filled")
	}
}

// BypassSetStatus отдаёт итог последнего наполнения. countOK=false — размер
// набора неизвестен (счётчик не получен), и entryCount=0 в этом случае НЕ
// значит «набор пуст».
func (s *ServiceImpl) BypassSetStatus() (entryCount int, countOK bool, lastPopulate, lastError string, missingTags []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.bypassLastPopulate.IsZero() {
		lastPopulate = s.bypassLastPopulate.UTC().Format(time.RFC3339)
	}
	return s.bypassEntryCount, s.bypassCountOK, lastPopulate, s.bypassLastError, slices.Clone(s.bypassMissingTags)
}

// ensureBypassSetExists создаёт пустой AWGM-BYPASS до установки правил:
// правило `-m set --match-set` на несуществующий набор роняет весь
// iptables-restore. Best-effort — реальная причина всплывёт на Install.
func (s *ServiceImpl) ensureBypassSetExists(ctx context.Context) {
	if err := bypassset.EnsureXtSetModule(ctx); err != nil {
		s.bypassLog.Warn("bypass-set", "", "загрузка xt_set: "+err.Error())
	}
	if bypassset.IPSetBinary() == "" {
		s.bypassLog.Warn("bypass-set", "", bypassset.ErrIPSetNotAvailable.Error())
		return
	}
	if err := bypassset.CreateSet(ctx); err != nil {
		s.bypassLog.Warn("bypass-set", "", "создание набора: "+err.Error())
	}
}

// teardownBypassSet сносит набор, staging-близнеца и дамп для хука. Вызывать
// ТОЛЬКО после переустановки правил без ссылки на набор — иначе ipset ответит
// «set is in use by a kernel component» и набор переживёт снос. Staging
// правилами не занят никогда, но без сноса остаётся сиротой с записями
// последней пересборки в памяти ядра.
func (s *ServiceImpl) teardownBypassSet(ctx context.Context) {
	if s.teardownBypassSetFn != nil {
		s.teardownBypassSetFn(ctx)
		return
	}
	if bypassset.IPSetBinary() != "" {
		if err := bypassset.DestroySet(ctx); err != nil {
			s.bypassLog.Warn("bypass-set", "", "снос набора: "+err.Error())
		}
		if err := bypassset.DestroyStagingSet(ctx); err != nil {
			s.bypassLog.Warn("bypass-set", "", "снос staging-набора: "+err.Error())
		}
	}
	if err := os.Remove(bypassSavePath); err != nil && !os.IsNotExist(err) {
		s.bypassLog.Warn("bypass-set", "", "удаление дампа набора: "+err.Error())
	}
}

// legacySelectiveSetName / legacySelectiveStagingSetName — ipset'ы
// выпиленного динамического селектива: живой и его staging-близнец.
const (
	legacySelectiveSetName        = "AWGM-SELECTIVE"
	legacySelectiveStagingSetName = "AWGM-SELECTIVE-STG"
)

// legacySelectiveSlotFile — слот выпиленного селектива в config.d.
const legacySelectiveSlotFile = "19-selective-routes.json"

// removeLegacySelectiveFiles удаляет с диска артефакты выпиленного селектива:
// слот 19 (активный и припаркованный), снапшоты пересборки и маркер последней
// пересборки. Осиротевший слот 19 у апгрейдящихся пользователей мержится
// sing-box'ом в конфиг, поэтому его убираем в первую очередь.
func removeLegacySelectiveFiles(configDir string) {
	if configDir == "" {
		return
	}
	doomed := []string{
		filepath.Join(configDir, legacySelectiveSlotFile),
		filepath.Join(configDir, "disabled", legacySelectiveSlotFile),
		filepath.Join(configDir, "selective-last-rebuild"),
	}
	snapshots, _ := filepath.Glob(filepath.Join(configDir, "selective-snapshot*"))
	for _, p := range append(doomed, snapshots...) {
		_ = os.Remove(p)
	}
}

// cleanupLegacySelectiveOnce — однократная стартовая зачистка наследия:
// файлы селектива + managed-правила «selective-ip», осевшие в применённом
// 20-router.json (раньше их прятал отдельный фильтр, теперь они всплыли бы
// в UI как пользовательские).
func (s *ServiceImpl) cleanupLegacySelectiveOnce(ctx context.Context) {
	s.legacySelectiveOnce.Do(func() {
		removeLegacySelectiveFiles(s.configDir())
		if err := s.dropLegacySelectiveManagedRules(ctx); err != nil {
			s.bypassLog.Warn("bypass-set", "", "зачистка managed-правил селектива: "+err.Error())
		}
	})
}

// configDir возвращает каталог config.d sing-box (оркестратор — источник
// правды, Singbox — легаси-путь тестов).
func (s *ServiceImpl) configDir() string {
	if s.deps.Orch != nil {
		return s.deps.Orch.ConfigDir()
	}
	if s.deps.Singbox != nil {
		return s.deps.Singbox.ConfigDir()
	}
	return ""
}

func (s *ServiceImpl) dropLegacySelectiveManagedRules(ctx context.Context) error {
	cfg, err := s.loadAppliedRouterConfig()
	if err != nil {
		return err
	}
	kept := make([]Rule, 0, len(cfg.Route.Rules))
	for _, r := range cfg.Route.Rules {
		if r.AwgmManaged == "selective-ip" {
			continue
		}
		kept = append(kept, r)
	}
	removed := len(cfg.Route.Rules) - len(kept)
	if removed == 0 {
		return nil
	}
	cfg.Route.Rules = kept
	if err := s.persistConfigDirect(ctx, cfg); err != nil {
		return err
	}
	s.bypassLog.Info("bypass-set", "", fmt.Sprintf("удалены managed-правила выпиленного селектива: %d", removed))
	s.emitRulesEvent()
	return nil
}

// destroyLegacySelectiveSetOnce сносит ipset'ы выпиленного селектива (живой и
// staging) — один раз за жизнь процесса и ТОЛЬКО после успешной установки
// правил: до неё живой набор ещё может быть занят старыми правилами в ядре.
func (s *ServiceImpl) destroyLegacySelectiveSetOnce(ctx context.Context) {
	s.legacySelectiveSetOnce.Do(func() {
		bin := bypassset.IPSetBinary()
		if bin == "" {
			return
		}
		for _, name := range []string{legacySelectiveSetName, legacySelectiveStagingSetName} {
			res, err := sysexec.Run(ctx, bin, "destroy", name)
			if err == nil {
				s.bypassLog.Info("bypass-set", name, "удалён набор выпиленного селектива")
				continue
			}
			out := ""
			if res != nil {
				out = res.Stdout + res.Stderr
			}
			switch {
			case strings.Contains(out, "does not exist") || strings.Contains(out, "not found"):
				// Набора нет — зачищать нечего.
			case strings.Contains(out, "in use"):
				s.bypassLog.Warn("bypass-set", name, "набор ещё занят правилами ядра — удалится после перезагрузки роутера")
			default:
				s.bypassLog.Warn("bypass-set", name, "снос набора селектива: "+sysexec.FormatError(res, err).Error())
			}
		}
	})
}
