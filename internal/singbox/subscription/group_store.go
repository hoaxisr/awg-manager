package subscription

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ErrGroupNotFound возвращается Get/Update/Delete/Set*, когда группы с таким
// ID нет в store. HTTP-обработчики маппят через errors.Is на 404 (вместо
// хрупкого поиска "not found" в тексте ошибки).
var ErrGroupNotFound = errors.New("subscription: aggregate group not found")

// AggregateGroup — сводная группа: один selector/urltest outbound поверх
// членов НЕСКОЛЬКИХ подписок (#372). Группа не владеет member-outbound'ами
// (ими владеют подписки), она только ссылается на их теги; поэтому пересборка
// групп (stageGroups) обязана происходить в одном батче с любой мутацией
// подписок.
type AggregateGroup struct {
	ID         string `json:"id"`         // 12 rand bytes hex, как у подписок
	Label      string `json:"label"`      // user-facing
	Tag        string `json:"tag"`        // "agg-<id8>"
	InboundTag string `json:"inboundTag"` // "agg-<id8>-in"
	ListenPort uint16 `json:"listenPort"` // localhost-порт mixed inbound
	ProxyIndex int    `json:"proxyIndex"` // NDMS ProxyN, -1 когда нет
	// Mode — selector | urltest. Пустое значение трактуем как urltest:
	// основной кейс issue — «авто-выбор быстрейшего среди всех подписок».
	Mode    SubscriptionMode `json:"mode,omitempty"`
	URLTest *URLTestConfig   `json:"urlTest,omitempty"`
	// UseSubscriptionIDs — ID подписок-источников в пользовательском порядке
	// (определяет порядок членов в группе).
	UseSubscriptionIDs []string `json:"useSubscriptionIds"`
	FilterInclude      string   `json:"filterInclude,omitempty"`
	FilterExclude      string   `json:"filterExclude,omitempty"`
	Enabled            bool     `json:"enabled"`
}

// EffectiveMode — режим группы с back-compat шимом: пустая строка = urltest
// (в отличие от подписок, где дефолт selector — см. комментарий у Mode).
func (g AggregateGroup) EffectiveMode() SubscriptionMode {
	if g.Mode == "" {
		return ModeURLTest
	}
	return g.Mode
}

// EffectiveURLTest — URLTest группы с заполненными дефолтами.
func (g AggregateGroup) EffectiveURLTest() URLTestConfig {
	def := DefaultURLTestConfig()
	if g.URLTest == nil {
		return def
	}
	out := *g.URLTest
	if out.URL == "" {
		out.URL = def.URL
	}
	if out.IntervalSec <= 0 {
		out.IntervalSec = def.IntervalSec
	}
	if out.ToleranceMs < 0 {
		out.ToleranceMs = def.ToleranceMs
	}
	return out
}

// GroupCreateInput — вход Service.CreateGroup.
type GroupCreateInput struct {
	Label string
	// Tag — пользовательский outbound-тег (#572); "" = авто "agg-<id8>".
	// Формат валидирует Service.CreateGroup, store проверяет только
	// уникальность среди групп.
	Tag                string
	Mode               SubscriptionMode // "" = ModeURLTest
	URLTest            *URLTestConfig
	UseSubscriptionIDs []string
	FilterInclude      string
	FilterExclude      string
	Enabled            bool
}

// GroupUpdatePatch — частичный апдейт; nil-указатели = «оставить как есть».
type GroupUpdatePatch struct {
	Label              *string
	Mode               *SubscriptionMode
	URLTest            *URLTestConfig
	UseSubscriptionIDs *[]string
	FilterInclude      *string
	FilterExclude      *string
	Enabled            *bool
}

// GroupStore хранит сводные группы в отдельном JSON-файле рядом с
// subscription store (atomic-write на каждую мутацию, те же паттерны).
type GroupStore struct {
	path string
	mu   sync.RWMutex
	data map[string]*AggregateGroup
}

func NewGroupStore(path string) (*GroupStore, error) {
	s := &GroupStore{path: path, data: make(map[string]*AggregateGroup)}
	if err := s.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *GroupStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var list []*AggregateGroup
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("subscription group store: parse %s: %w", s.path, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range list {
		s.data[item.ID] = item
	}
	return nil
}

func (s *GroupStore) saveLocked() error {
	list := make([]*AggregateGroup, 0, len(s.data))
	for _, item := range s.data {
		list = append(list, item)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return storage.AtomicWrite(s.path, b)
}

// clone — глубокая копия группы: срез UseSubscriptionIDs и указатель URLTest
// копируются, чтобы правки клона не были видны сквозь общий backing-массив.
// Используется copy-on-write мутацией GroupStore.mutate.
func (g *AggregateGroup) clone() *AggregateGroup {
	cp := *g
	cp.UseSubscriptionIDs = cloneSlice(g.UseSubscriptionIDs)
	if g.URLTest != nil {
		ut := *g.URLTest
		cp.URLTest = &ut
	}
	return &cp
}

// mutate — copy-on-write мутация одной группы: fn правит глубокий клон, клон
// кладётся в map и состояние сохраняется на диск. При ошибке записи в map
// возвращается ОРИГИНАЛ — память никогда не расходится с диском (зеркалит
// Store.mutate). Успех возвращает новый клон.
func (s *GroupStore) mutate(id string, fn func(*AggregateGroup) error) (*AggregateGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orig, ok := s.data[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrGroupNotFound, id)
	}
	next := orig.clone()
	if err := fn(next); err != nil {
		return nil, err
	}
	s.data[id] = next
	if err := s.saveLocked(); err != nil {
		s.data[id] = orig
		return nil, err
	}
	return next, nil
}

func (s *GroupStore) Create(in GroupCreateInput) (*AggregateGroup, error) {
	id := newID()
	short := id[:8]
	tag := "agg-" + short
	if in.Tag != "" {
		tag = in.Tag
	}
	inboundTag := tag + "-in"
	mode := in.Mode
	if mode == "" {
		mode = ModeURLTest
	}
	var urlTest *URLTestConfig
	if mode == ModeURLTest {
		cfg := DefaultURLTestConfig()
		if in.URLTest != nil {
			if in.URLTest.URL != "" {
				cfg.URL = in.URLTest.URL
			}
			if in.URLTest.IntervalSec > 0 {
				cfg.IntervalSec = in.URLTest.IntervalSec
			}
			if in.URLTest.ToleranceMs >= 0 {
				cfg.ToleranceMs = in.URLTest.ToleranceMs
			}
		}
		urlTest = &cfg
	}
	useIDs := in.UseSubscriptionIDs
	if useIDs == nil {
		useIDs = []string{}
	}
	g := &AggregateGroup{
		ID:                 id,
		Label:              in.Label,
		Tag:                tag,
		InboundTag:         inboundTag,
		ProxyIndex:         -1,
		Mode:               mode,
		URLTest:            urlTest,
		UseSubscriptionIDs: useIDs,
		FilterInclude:      in.FilterInclude,
		FilterExclude:      in.FilterExclude,
		Enabled:            in.Enabled,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, other := range s.data {
		if other.Tag == tag || other.InboundTag == tag ||
			other.Tag == inboundTag || other.InboundTag == inboundTag {
			return nil, fmt.Errorf("%w: тег %q уже занят группой %q", ErrValidation, tag, other.Label)
		}
	}
	s.data[id] = g
	if err := s.saveLocked(); err != nil {
		delete(s.data, id)
		return nil, err
	}
	cp := *g
	return &cp, nil
}

func (s *GroupStore) Get(id string) (*AggregateGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.data[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrGroupNotFound, id)
	}
	cp := *g
	return &cp, nil
}

// List возвращает все группы в детерминированном порядке: по Label без
// учёта регистра, при равенстве — по ID. Map-обход недетерминирован, а
// стабильный порядок нужен и API-списку, и повторяемости пересборок.
func (s *GroupStore) List() []AggregateGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AggregateGroup, 0, len(s.data))
	for _, g := range s.data {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Label), strings.ToLower(out[j].Label)
		if li != lj {
			return li < lj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *GroupStore) Update(id string, patch GroupUpdatePatch) (*AggregateGroup, error) {
	g, err := s.mutate(id, func(g *AggregateGroup) error {
		applyGroupPatch(g, patch)
		return nil
	})
	if err != nil {
		return nil, err
	}
	cp := *g
	return &cp, nil
}

// applyGroupPatch применяет частичный патч к группе (nil-указатели = «оставить
// как есть»). Вынесен из Update, чтобы патч работал по клону в mutate.
func applyGroupPatch(g *AggregateGroup, patch GroupUpdatePatch) {
	if patch.Label != nil {
		g.Label = *patch.Label
	}
	if patch.Mode != nil {
		g.Mode = *patch.Mode
		if g.Mode == ModeURLTest && g.URLTest == nil {
			cfg := DefaultURLTestConfig()
			g.URLTest = &cfg
		}
	}
	if patch.URLTest != nil {
		cp := *patch.URLTest
		g.URLTest = &cp
	}
	if patch.UseSubscriptionIDs != nil {
		ids := append([]string{}, (*patch.UseSubscriptionIDs)...)
		g.UseSubscriptionIDs = ids
	}
	if patch.FilterInclude != nil {
		g.FilterInclude = *patch.FilterInclude
	}
	if patch.FilterExclude != nil {
		g.FilterExclude = *patch.FilterExclude
	}
	if patch.Enabled != nil {
		g.Enabled = *patch.Enabled
	}
}

func (s *GroupStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.data[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrGroupNotFound, id)
	}
	delete(s.data, id)
	if err := s.saveLocked(); err != nil {
		// Запись не прошла — возвращаем строку, чтобы память не разошлась
		// с диском (на диске группа всё ещё есть).
		s.data[id] = g
		return err
	}
	return nil
}

// SetListenPort persists the allocated mixed-inbound port.
func (s *GroupStore) SetListenPort(id string, port uint16) error {
	_, err := s.mutate(id, func(g *AggregateGroup) error {
		g.ListenPort = port
		return nil
	})
	return err
}

// SetProxyIndex persists the NDMS ProxyN index for this group.
func (s *GroupStore) SetProxyIndex(id string, idx int) error {
	_, err := s.mutate(id, func(g *AggregateGroup) error {
		g.ProxyIndex = idx
		return nil
	})
	return err
}

// RemoveSubscriptionRef выкидывает subID из useSubscriptionIds всех групп
// (вызывается при удалении подписки; сами группы остаются). Copy-on-write:
// затронутые группы заменяются клонами, при ошибке записи на диск оригиналы
// возвращаются в map — память не расходится с диском.
func (s *GroupStore) RemoveSubscriptionRef(subID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	restore := map[string]*AggregateGroup{} // id → оригинал для отката
	for id, g := range s.data {
		refs := false
		for _, sid := range g.UseSubscriptionIDs {
			if sid == subID {
				refs = true
				break
			}
		}
		if !refs {
			continue
		}
		next := g.clone()
		kept := make([]string, 0, len(next.UseSubscriptionIDs))
		for _, sid := range next.UseSubscriptionIDs {
			if sid != subID {
				kept = append(kept, sid)
			}
		}
		next.UseSubscriptionIDs = kept
		restore[id] = g
		s.data[id] = next
	}
	if len(restore) == 0 {
		return nil
	}
	if err := s.saveLocked(); err != nil {
		for id, orig := range restore {
			s.data[id] = orig
		}
		return err
	}
	return nil
}
