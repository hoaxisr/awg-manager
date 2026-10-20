package dnsroute

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Store manages DNS route domain lists storage.
type Store struct {
	path string
	mu   sync.RWMutex
	data *StoreData
}

// NewStore creates a new DNS route store.
func NewStore(dataDir string) *Store {
	return &Store{
		path: filepath.Join(dataDir, "dns-routes.json"),
	}
}

// Load reads domain lists from disk. Returns defaults if file doesn't exist.
func (s *Store) Load() (*StoreData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = defaultStoreData()
			return s.data, nil
		}
		return nil, fmt.Errorf("read dns-routes file: %w", err)
	}

	var data StoreData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse dns-routes JSON: %w", err)
	}

	if data.Lists == nil {
		data.Lists = []DomainList{}
	}
	if data.HRRuleIcons == nil {
		data.HRRuleIcons = map[string]string{}
	}
	normalizeLists(data.Lists)
	migrateLegacyExcludes(&data)
	migrateRawEditorText(&data)
	dropped := dropLegacyHRRows(&data)
	forkMigrated := migrateRuleSetSubscriptionURLs(&data)
	discordMigrated := migrateDiscordDNSSubnet(&data)
	errorsScrubbed := scrubSubscriptionErrors(&data)
	slugsAssigned := migrateGroupSlugs(&data)
	geoDropped := migrateNDMSGeoTags(&data)

	s.data = &data
	if dropped > 0 || forkMigrated || discordMigrated || errorsScrubbed || slugsAssigned || geoDropped {
		// Persist the cleanup so the file itself is cleaned, not just the
		// in-memory cache. Best-effort: on write error the cache is already
		// clean and the next Save() rewrites disk — failing startup over
		// inert-row cleanup would be worse. (Store has no logger.)
		// ponytail: swallow write error here; next real Save() surfaces a
		// persistent disk problem.
		_ = s.writeLocked(&data)
	}
	return s.data, nil
}

// scrubSubscriptionErrors reduces every address in a stored fetch error to
// scheme and host. Text saved before the refresh path did so quoted the
// list's address whole, token in the path or query included, and REST
// served it until the next successful fetch.
func scrubSubscriptionErrors(data *StoreData) bool {
	changed := false
	for i := range data.Lists {
		for j := range data.Lists[i].Subscriptions {
			sub := &data.Lists[i].Subscriptions[j]
			if scrubbed := logging.RedactURLs(sub.LastError); scrubbed != sub.LastError {
				sub.LastError = scrubbed
				changed = true
			}
		}
	}
	return changed
}

// migrateRuleSetSubscriptionURLs rewrites snapshotted vernette subscription
// URLs to the repo.hoaxisr.ru mirror (raw.githubusercontent.com заблокирован
// у части провайдеров — awgm#534). Returns true if any URL changed (caller
// persists). Idempotent — after the swap no vernette prefix remains.
func migrateRuleSetSubscriptionURLs(data *StoreData) bool {
	if data == nil {
		return false
	}
	// Якорь на начало URL: proxy-обёрнутый пользовательский workaround
	// ("https://ghproxy.com/https://github.com/vernette/...") не трогаем —
	// замена сделала бы его нерабочим гибридом.
	const old = "https://github.com/vernette/rulesets/raw/master/"
	const next = "https://repo.hoaxisr.ru/rulesets/"
	changed := false
	for i := range data.Lists {
		subs := data.Lists[i].Subscriptions
		for j := range subs {
			if rest, ok := strings.CutPrefix(subs[j].URL, old); ok {
				subs[j].URL = next + rest
				changed = true
			}
		}
	}
	return changed
}

// discordDNSSignature — домены, однозначно метящие применённый из пресета
// Discord DNS-список. Совпадение ВСЕХ → это Discord.
var discordDNSSignature = []string{"discord.com", "discordapp.com"}

// ponytail: одноразовый data-specific хардкод. CIDR продублирован в трёх местах
// (форк .srs, defaults.json после регена, эта константа) — расходятся
// независимо. Это закрывает разрыв propagation для уже-применённых DNS-списков
// Discord; следующая правка данных потребует своей такой миграции. Upgrade-path:
// добавить provenance (presetId) в DomainList и общий reconcile catalog→applied,
// тогда эти разовые миграции не нужны.
const discordNewCIDR = "104.29.0.0/16"

// migrateDiscordDNSSubnet дописывает новый CIDR Discord в применённые DNS-списки,
// которые его ещё не содержат. Матч по доменной сигнатуре (provenance к пресету
// нет). Additive: ничего не удаляет. Обновляет Subnets (активно сразу) и
// ManualDomains/ManualText (переживёт ре-сейв редактора). Возвращает true, если
// что-то изменено (caller персистит). Идемпотентно.
func migrateDiscordDNSSubnet(data *StoreData) bool {
	if data == nil {
		return false
	}
	changed := false
	for i := range data.Lists {
		list := &data.Lists[i]
		// isHydraRoute-гард в текущем порядке Load недостижим (dropLegacyHRRows
		// уже удалил HR-строки) — оставлен defensive на случай изменения порядка
		// миграций или прямого вызова.
		if isHydraRoute(list.Backend) || !hasDiscordSignature(list) {
			continue
		}
		if !slices.Contains(list.Subnets, discordNewCIDR) && len(list.Subnets) < MaxSubnetsPerList {
			list.Subnets = append(list.Subnets, discordNewCIDR)
			changed = true
		}
		if !slices.Contains(list.ManualDomains, discordNewCIDR) {
			list.ManualDomains = append(list.ManualDomains, discordNewCIDR)
			changed = true
		}
		if list.ManualText != nil && !strings.Contains(*list.ManualText, discordNewCIDR) {
			updated := strings.TrimRight(*list.ManualText, "\n")
			if updated == "" {
				updated = discordNewCIDR
			} else {
				updated += "\n" + discordNewCIDR
			}
			list.ManualText = &updated
			changed = true
		}
	}
	return changed
}

// hasDiscordSignature: домены списка (Domains ∪ ManualDomains ∪ строки ManualText)
// содержат все сигнатурные домены Discord.
func hasDiscordSignature(list *DomainList) bool {
	have := make(map[string]bool, len(list.Domains)+len(list.ManualDomains))
	for _, d := range list.Domains {
		have[d] = true
	}
	for _, d := range list.ManualDomains {
		have[d] = true
	}
	if list.ManualText != nil {
		for _, line := range strings.Split(*list.ManualText, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			have[line] = true
		}
	}
	for _, sig := range discordDNSSignature {
		if !have[sig] {
			return false
		}
	}
	return true
}

// migrateLegacyExcludes splits any CIDR-shaped entries out of Excludes
// (legacy: Excludes used to be a free-form list and may contain CIDRs)
// and merges them into ExcludeSubnets. In-memory only — written back
// to disk only on the next Save().
func migrateLegacyExcludes(data *StoreData) {
	if data == nil {
		return
	}
	for i := range data.Lists {
		list := &data.Lists[i]
		if len(list.Excludes) == 0 {
			continue
		}
		domains, subnets := splitDomainsAndSubnets(list.Excludes)
		if len(subnets) == 0 {
			continue
		}
		list.Excludes = domains
		// Merge subnets into ExcludeSubnets, dedup union.
		seen := make(map[string]bool, len(list.ExcludeSubnets)+len(subnets))
		merged := make([]string, 0, len(list.ExcludeSubnets)+len(subnets))
		for _, s := range list.ExcludeSubnets {
			if !seen[s] {
				seen[s] = true
				merged = append(merged, s)
			}
		}
		for _, s := range subnets {
			if !seen[s] {
				seen[s] = true
				merged = append(merged, s)
			}
		}
		list.ExcludeSubnets = merged
	}
}

// migrateRawEditorText backfills raw editor text fields for configs created
// before manualText/excludesText existed. In-memory only — written back to disk
// on the next Save(), matching migrateLegacyExcludes behavior.
func migrateRawEditorText(data *StoreData) {
	if data == nil {
		return
	}

	for i := range data.Lists {
		list := &data.Lists[i]

		// HR Neo rules are sourced from HR files; keep this migration scoped to
		// NDMS/stored DNS routes only.
		if isHydraRoute(list.Backend) {
			continue
		}

		if list.ManualText == nil && len(list.ManualDomains) > 0 {
			raw := strings.Join(list.ManualDomains, "\n")
			list.ManualText = &raw
		}

		if list.ExcludesText == nil && (len(list.Excludes) > 0 || len(list.ExcludeSubnets) > 0) {
			lines := make([]string, 0, len(list.Excludes)+len(list.ExcludeSubnets))
			lines = append(lines, list.Excludes...)
			lines = append(lines, list.ExcludeSubnets...)
			raw := strings.Join(lines, "\n")
			list.ExcludesText = &raw
		}
	}
}

// migrateNDMSGeoTags снимает geo-теги (geosite:/geoip:, любой регистр) со
// всех хранимых списков — после dropLegacyHRRows это только NDMS-списки.
// NDMS тегов не принимает (стенд KN-1810 5.02.B.1, 20.10: error 101582220
// «invalid FQDN or IP address», E в журнале ndm), запись их уже отвергает
// (F700); здесь — списки, записанные раньше. Строки снимаются и из сырого
// текста редактора, иначе следующий Update вернул бы их через
// applyManualText. Комментарии и прочие строки не трогаются. Идемпотентна.
func migrateNDMSGeoTags(data *StoreData) bool {
	changed := false
	drop := func(entries *[]string) {
		if n := len(*entries); n > 0 {
			*entries = slices.DeleteFunc(*entries, hasGeoPrefix)
			changed = changed || len(*entries) != n
		}
	}
	dropLines := func(text *string) {
		if text == nil {
			return
		}
		lines := strings.Split(*text, "\n")
		kept := slices.DeleteFunc(slices.Clone(lines), func(l string) bool {
			return hasGeoPrefix(strings.TrimSuffix(l, "\r"))
		})
		if len(kept) != len(lines) {
			*text = strings.Join(kept, "\n")
			changed = true
		}
	}
	for i := range data.Lists {
		l := &data.Lists[i]
		drop(&l.ManualDomains)
		drop(&l.Domains)
		drop(&l.Subnets)
		drop(&l.Excludes)
		drop(&l.ExcludeSubnets)
		dropLines(l.ManualText)
		dropLines(l.ExcludesText)
	}
	return changed
}

// writeLocked marshals and atomically writes data, updating the cache.
// Caller MUST already hold s.mu.
func (s *Store) writeLocked(data *StoreData) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dns-routes: %w", err)
	}
	if err := storage.AtomicWrite(s.path, raw); err != nil {
		return fmt.Errorf("write dns-routes file: %w", err)
	}
	s.data = data
	return nil
}

// dropLegacyHRRows removes HydraRoute-backed rows from data.Lists. HR rules
// live in HR config files (source of truth); any hydraroute-backed row left in
// dns-routes.json is a legacy leftover — hidden from List(), never reconciled,
// and only pollutes the dedup index. Returns how many rows were removed.
func dropLegacyHRRows(data *StoreData) int {
	if data == nil || len(data.Lists) == 0 {
		return 0
	}
	kept := data.Lists[:0]
	removed := 0
	for _, l := range data.Lists {
		if isHydraRoute(l.Backend) {
			removed++
			continue
		}
		kept = append(kept, l)
	}
	data.Lists = kept
	return removed
}

// Save writes domain list data to disk atomically.
func (s *Store) Save(data *StoreData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(data)
}

// GetCached returns cached data with read lock. Returns nil if not loaded yet.
func (s *Store) GetCached() *StoreData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

// Draft возвращает копию кэша для правки: мутатор меняет её и отдаёт в
// Save, а кэш заменяется только успешной записью (writeLocked). Правка
// живого кэша при отказе записи оставалась в нём, и её довозили до роутера
// следующая сверка и до диска следующий успешный Save (F695). Nil, если
// store не загружен.
//
// Глубина — ровно то, что пути правки пакета пишут на месте:
//   - Lists — свой массив: присваивание Lists[i] и полей элемента
//     (Update, SetEnabled, OnTunnelDelete, refreshSubscriptions), сдвиг
//     append(Lists[:i], Lists[i+1:]...) в Delete, append в Create/CreateBatch;
//   - Subscriptions каждого списка — refreshSubscriptions пишет поля
//     Subscriptions[j] через указатель;
//   - Routes каждого списка — Update без присланных маршрутов берёт срез
//     записи, и resolveRouteInterfaces пишет в него Interface на месте;
//   - HRRuleIcons — запись и delete в map (setHRIcon).
//
// Остальные срезы и указатели элемента (Domains, Excludes, ManualText, …)
// общие с кэшем: пути правки их только читают или заменяют присваиванием
// поля. Путь, пишущий внутрь такого среза на месте, обязан углубить копию.
// Полная копия (DomainList.clone) не нужна: списки доменов бывают в тысячи
// строк, а роутеры — MIPS с малой памятью.
func (s *Store) Draft() *StoreData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil {
		return nil
	}
	d := *s.data
	d.Lists = slices.Clone(d.Lists)
	for i := range d.Lists {
		d.Lists[i].Subscriptions = slices.Clone(d.Lists[i].Subscriptions)
		d.Lists[i].Routes = slices.Clone(d.Lists[i].Routes)
	}
	d.HRRuleIcons = maps.Clone(d.HRRuleIcons)
	return &d
}

// defaultStoreData returns empty store data with initialized collections.
func defaultStoreData() *StoreData {
	return &StoreData{
		Lists:       []DomainList{},
		HRRuleIcons: map[string]string{},
	}
}

// EmptyStoreData returns empty store data for cleanup (reconcile will remove all AWG_* objects).
func EmptyStoreData() *StoreData {
	return defaultStoreData()
}

// normalizeLists ensures no nil slices in DomainList fields (Go nil → JSON null → JS crash).
func normalizeLists(lists []DomainList) {
	for i := range lists {
		if lists[i].Domains == nil {
			lists[i].Domains = []string{}
		}
		if lists[i].ManualDomains == nil {
			lists[i].ManualDomains = []string{}
		}
		if lists[i].Routes == nil {
			lists[i].Routes = []RouteTarget{}
		}
	}
}

// migrateGroupSlugs назначает GroupSlug спискам, записанным до F694.
// Уже заданные слаги заняты. Два прохода по спискам без слага, в порядке
// хранилища:
//  1. список с содержимым получает нынешнюю основу GroupSlug(ID, Name), если
//     она ещё свободна, — его группа на роутере не меняется. Пустой список
//     пропускается: группы у него нет (buildTargetState), и основу он не
//     отнимает у списка, который группой владеет;
//  2. оставшиеся — uniqueGroupSlug с суффиксом. Суффиксы раздаются после
//     того, как все основы зарезервированы, иначе суффикс коллидирующего
//     списка мог бы занять нынешнее имя более позднего («youtube» + «_2» =
//     основа «youtube 2») и увести его группу в хвост (класс #1023).
//
// Возвращает true, если что-то изменено (caller персистит). Идемпотентно.
func migrateGroupSlugs(data *StoreData) bool {
	if data == nil {
		return false
	}
	taken := make(map[string]bool, len(data.Lists))
	for _, l := range data.Lists {
		if l.GroupSlug != "" {
			taken[l.GroupSlug] = true
		}
	}
	changed := false
	for i := range data.Lists {
		l := &data.Lists[i]
		if l.GroupSlug != "" || len(l.Domains)+len(l.Subnets) == 0 {
			continue
		}
		if base := GroupSlug(l.ID, l.Name); !taken[base] {
			l.GroupSlug = base
			taken[base] = true
			changed = true
		}
	}
	for i := range data.Lists {
		l := &data.Lists[i]
		if l.GroupSlug != "" {
			continue
		}
		l.GroupSlug = uniqueGroupSlug(l.ID, l.Name, taken)
		taken[l.GroupSlug] = true
		changed = true
	}
	return changed
}
