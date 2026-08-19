package subscription

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Store persists subscriptions to disk as JSON, atomic-writes on every mutation.
type Store struct {
	path string
	mu   sync.RWMutex
	data map[string]*Subscription
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, data: make(map[string]*Subscription)}
	if err := s.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var list []*Subscription
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("subscription store: parse %s: %w", s.path, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	needsSave := false
	for _, item := range list {
		if sanitizeLegacySubscriptionLastError(item) {
			needsSave = true
		}
		s.data[item.ID] = item
	}
	if needsSave {
		_ = s.saveLocked()
	}
	return nil
}

func sanitizeLegacySubscriptionLastError(sub *Subscription) bool {
	if sub == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(sub.LastError))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "download via") && strings.Contains(msg, "(subscription)") {
		sub.LastError = ""
		return true
	}
	return false
}

func (s *Store) saveLocked() error {
	list := make([]*Subscription, 0, len(s.data))
	for _, item := range s.data {
		list = append(list, item)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return storage.AtomicWrite(s.path, b)
}

// cloneSlice копирует срез с сохранением nil-ности: nil остаётся nil (важно
// для omitempty/JSON-формы), пустой не-nil срез остаётся пустым не-nil.
func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}

// clone — глубокая копия подписки: все slice-поля и указатель URLTest
// копируются, чтобы правки клона не были видны сквозь общие backing-массивы.
// Элементы срезов (Header, MemberInfo, RejectedMember, SubscriptionInfoItem)
// — плоские value-типы без вложенных срезов/указателей, поэтому поэлементного
// копирования достаточно. Используется copy-on-write мутацией Store.mutate.
func (s *Subscription) clone() *Subscription {
	cp := *s
	cp.Headers = cloneSlice(s.Headers)
	cp.MemberTags = cloneSlice(s.MemberTags)
	cp.Members = cloneSlice(s.Members)
	cp.OrphanTags = cloneSlice(s.OrphanTags)
	cp.RejectedMembers = cloneSlice(s.RejectedMembers)
	cp.InfoItems = cloneSlice(s.InfoItems)
	cp.DismissedInfoIDs = cloneSlice(s.DismissedInfoIDs)
	cp.ExcludedTags = cloneSlice(s.ExcludedTags)
	cp.ExcludedMembers = cloneSlice(s.ExcludedMembers)
	cp.FilteredMembers = cloneSlice(s.FilteredMembers)
	if s.URLTest != nil {
		ut := *s.URLTest
		cp.URLTest = &ut
	}
	return &cp
}

// errMutateNoop сигнализирует mutate из fn, что изменений нет: своп и запись
// на диск пропускаются, вызывающему возвращается текущее состояние без ошибки.
var errMutateNoop = errors.New("subscription store: no-op mutation")

// mutate — copy-on-write мутация одной подписки: fn правит глубокий клон,
// клон кладётся в map и состояние сохраняется на диск. При ошибке записи в
// map возвращается ОРИГИНАЛ и наружу отдаётся ошибка — память никогда не
// расходится с диском (иначе следующая успешная запись чего угодно молча
// унесла бы несохранённое изменение). Успех возвращает новый клон.
func (s *Store) mutate(id string, fn func(*Subscription) error) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orig, ok := s.data[id]
	if !ok {
		return nil, fmt.Errorf("subscription %q not found", id)
	}
	next := orig.clone()
	if err := fn(next); err != nil {
		if errors.Is(err, errMutateNoop) {
			return orig, nil
		}
		return nil, err
	}
	s.data[id] = next
	if err := s.saveLocked(); err != nil {
		s.data[id] = orig
		return nil, err
	}
	return next, nil
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand should never fail
	}
	return hex.EncodeToString(b[:])
}

func (s *Store) Create(in CreateInput) (*Subscription, error) {
	id := newID()
	short := id[:8]
	mode := in.Mode
	if mode == "" {
		mode = ModeSelector
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
			// ToleranceMs == 0 is meaningful (always switch on any
			// latency advantage); only negative values fall through
			// to the default. Matches EffectiveURLTest contract.
			if in.URLTest.ToleranceMs >= 0 {
				cfg.ToleranceMs = in.URLTest.ToleranceMs
			}
		}
		urlTest = &cfg
	}
	sub := &Subscription{
		ID:               id,
		Label:            in.Label,
		URL:              in.URL,
		Inline:           in.Inline,
		Headers:          in.Headers,
		RefreshHours:     in.RefreshHours,
		Enabled:          in.Enabled,
		FilterInclude:    in.FilterInclude,
		FilterExclude:    in.FilterExclude,
		BindInterface:    strings.TrimSpace(in.BindInterface),
		SelectorTag:      "sub-" + short,
		InboundTag:       "sub-" + short + "-in",
		ProxyIndex:       -1,
		MemberTags:       []string{},
		Members:          []MemberInfo{},
		OrphanTags:       []string{},
		RejectedMembers:  []RejectedMember{},
		InfoItems:        []SubscriptionInfoItem{},
		DismissedInfoIDs: []string{},
		Mode:             mode,
		URLTest:          urlTest,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[id] = sub
	if err := s.saveLocked(); err != nil {
		delete(s.data, id)
		return nil, err
	}
	return sub, nil
}

func (s *Store) Get(id string) (*Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.data[id]
	if !ok {
		return nil, fmt.Errorf("subscription %q not found", id)
	}
	cp := *sub
	return &cp, nil
}

// List возвращает подписки в детерминированном порядке (label без регистра,
// tie-break по ID) — тем же, что GroupStore.List. s.data — map, её порядок
// итерации рандомен на каждый вызов: карточки подписок в UI перепрыгивали на
// каждом 30-секундном поллинге (#525).
func (s *Store) List() []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Subscription, 0, len(s.data))
	for _, sub := range s.data {
		out = append(out, *sub)
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

func (s *Store) Update(id string, patch UpdatePatch) (*Subscription, error) {
	sub, err := s.mutate(id, func(sub *Subscription) error {
		if patch.Label != nil {
			sub.Label = *patch.Label
		}
		if patch.URL != nil {
			sub.URL = *patch.URL
		}
		if patch.Headers != nil {
			sub.Headers = *patch.Headers
		}
		if patch.RefreshHours != nil {
			sub.RefreshHours = *patch.RefreshHours
		}
		if patch.Enabled != nil {
			sub.Enabled = *patch.Enabled
		}
		if patch.Mode != nil {
			sub.Mode = *patch.Mode
			if sub.Mode == ModeURLTest && sub.URLTest == nil {
				cfg := DefaultURLTestConfig()
				sub.URLTest = &cfg
			}
		}
		if patch.URLTest != nil {
			cp := *patch.URLTest
			sub.URLTest = &cp
		}
		if patch.FilterInclude != nil {
			sub.FilterInclude = *patch.FilterInclude
		}
		if patch.FilterExclude != nil {
			sub.FilterExclude = *patch.FilterExclude
		}
		if patch.BindInterface != nil {
			// Trim before persisting so storage state matches the
			// validated value: subscription/service.go trims and
			// validates before reaching the store, but a direct
			// Store.Update call (tests, future programmatic
			// paths) would otherwise persist a value with stray
			// whitespace and silently fail the next kernel check
			// (#709, PR #732 review non-blocker #11).
			sub.BindInterface = strings.TrimSpace(*patch.BindInterface)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	cp := *sub
	return &cp, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
	delete(s.data, id)
	if err := s.saveLocked(); err != nil {
		// Запись не прошла — возвращаем строку, чтобы память не разошлась
		// с диском (на диске подписка всё ещё есть).
		s.data[id] = sub
		return err
	}
	return nil
}

func (s *Store) UpdateState(id string, res RefreshResult) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		sub.LastFetched = res.When
		if res.Err != nil {
			sub.LastError = MaskURL(res.Err.Error(), sub.URL)
		} else {
			sub.LastError = ""
		}
		return nil
	})
	return err
}

// SetMembers replaces the Members slice and mirrors tags into MemberTags so
// existing consumers that iterate by tag still work. Also updates ActiveMember
// when the current active is no longer present.
func (s *Store) SetMembers(id string, members []MemberInfo, orphans []string) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		sub.Members = members
		tags := make([]string, len(members))
		for i, m := range members {
			tags[i] = m.Tag
		}
		sub.MemberTags = tags
		sub.OrphanTags = orphans
		reconcileActiveMember(sub, tags)
		return nil
	})
	return err
}

// MoveToExcluded атомарно: активные члены = keepMembers, excluded набор обновлён.
func (s *Store) MoveToExcluded(id string, keepMembers []MemberInfo, excludedTags []string, excludedMembers []MemberInfo) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		sub.Members = keepMembers
		tags := make([]string, 0, len(keepMembers)) // inline, как в SetMembers
		for _, m := range keepMembers {
			tags = append(tags, m.Tag)
		}
		sub.MemberTags = tags
		sub.ExcludedTags = excludedTags
		sub.ExcludedMembers = excludedMembers
		reconcileActiveMember(sub, sub.MemberTags)
		return nil
	})
	return err
}

// SetExcludedTags перезаписывает только excluded-поля (restore-path: уменьшение набора).
func (s *Store) SetExcludedTags(id string, excludedTags []string, excludedMembers []MemberInfo) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		sub.ExcludedTags = excludedTags
		sub.ExcludedMembers = excludedMembers
		return nil
	})
	return err
}

// SetMembersExtras updates members, orphans, rejected, info, excluded and
// filtered display mirrors in one write.
func (s *Store) SetMembersExtras(id string, members []MemberInfo, orphans []string, rejected []RejectedMember, info []SubscriptionInfoItem, excludedMembers, filteredMembers []MemberInfo) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		sub.Members = members
		tags := make([]string, len(members))
		for i, m := range members {
			tags[i] = m.Tag
		}
		sub.MemberTags = tags
		sub.OrphanTags = orphans
		if rejected == nil {
			rejected = []RejectedMember{}
		}
		if info == nil {
			info = []SubscriptionInfoItem{}
		}
		sub.RejectedMembers = rejected
		sub.InfoItems = info
		sub.ExcludedMembers = excludedMembers
		sub.FilteredMembers = filteredMembers
		reconcileActiveMember(sub, tags)
		return nil
	})
	return err
}

// RemoveInfoItem moves one info banner to rejectedMembers and dismisses it on refresh.
func (s *Store) RemoveInfoItem(id, itemID string) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		idx := findInfoItem(sub.InfoItems, itemID)
		if idx < 0 {
			return ErrInfoItemNotFound
		}
		item := sub.InfoItems[idx]
		removedID := strings.TrimSpace(item.ID)
		sub.InfoItems = append(sub.InfoItems[:idx], sub.InfoItems[idx+1:]...)
		sub.DismissedInfoIDs = appendDismissedID(sub.DismissedInfoIDs, removedID)
		sub.RejectedMembers = appendRejectedUnique(sub.RejectedMembers, rejectedFromInfoItem(item))
		return nil
	})
	return err
}

func removeDismissedID(dismissed []string, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return dismissed
	}
	out := make([]string, 0, len(dismissed))
	for _, d := range dismissed {
		if strings.TrimSpace(d) != id {
			out = append(out, d)
		}
	}
	return out
}

// UnmarkDismissedInfoID allows a previously hidden info line to appear again after refresh.
func (s *Store) UnmarkDismissedInfoID(id, infoID string) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		next := removeDismissedID(sub.DismissedInfoIDs, infoID)
		if len(next) == len(sub.DismissedInfoIDs) {
			return errMutateNoop // без изменений — не переписываем диск
		}
		sub.DismissedInfoIDs = next
		return nil
	})
	return err
}

func appendDismissedID(dismissed []string, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return dismissed
	}
	for _, d := range dismissed {
		if strings.TrimSpace(d) == id {
			return dismissed
		}
	}
	return append(dismissed, id)
}

// SetRejectedAndInfo updates rejected/info slices without touching members.
// info nil means leave unchanged (used by ClearRejected).
func (s *Store) SetRejectedAndInfo(id string, rejected []RejectedMember, info []SubscriptionInfoItem) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		sub.RejectedMembers = rejected
		if info != nil {
			sub.InfoItems = info
		}
		return nil
	})
	return err
}

func reconcileActiveMember(sub *Subscription, tags []string) {
	if sub.ActiveMember == "" && len(tags) > 0 {
		sub.ActiveMember = tags[0]
	}
	if sub.ActiveMember == "" {
		return
	}
	for _, t := range tags {
		if t == sub.ActiveMember {
			return
		}
	}
	if len(tags) > 0 {
		sub.ActiveMember = tags[0]
	}
}

// SetProxyIndex persists the NDMS ProxyN index for this subscription.
func (s *Store) SetProxyIndex(id string, idx int) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		sub.ProxyIndex = idx
		return nil
	})
	return err
}

// SetMembership replaces MemberTags + OrphanTags atomically. Used by Service.Refresh.
// Auto-defaults ActiveMember to the first member when empty, and falls back to the
// first remaining member when the current active becomes orphan.
func (s *Store) SetMembership(id string, members, orphans []string) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		sub.MemberTags = members
		sub.OrphanTags = orphans
		if sub.ActiveMember == "" && len(members) > 0 {
			sub.ActiveMember = members[0]
		}
		if sub.ActiveMember != "" {
			found := false
			for _, m := range members {
				if m == sub.ActiveMember {
					found = true
					break
				}
			}
			if !found && len(members) > 0 {
				sub.ActiveMember = members[0]
			}
		}
		return nil
	})
	return err
}

func (s *Store) SetActiveMember(id, memberTag string) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		sub.ActiveMember = memberTag
		return nil
	})
	return err
}

func (s *Store) SetListenPort(id string, port uint16) error {
	_, err := s.mutate(id, func(sub *Subscription) error {
		sub.ListenPort = port
		return nil
	})
	return err
}

// MaskURL replaces the subscription URL in an error message with a placeholder
// so logs and API responses never leak provider tokens / paths.
func MaskURL(msg, url string) string {
	if url == "" || msg == "" {
		return msg
	}
	return strings.ReplaceAll(msg, url, "<subscription-url>")
}

// MaybeRefresh returns subscriptions whose RefreshHours interval has
// elapsed since LastFetched. Used by the scheduler to pick due items.
func (s *Store) MaybeRefresh(now time.Time) []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Subscription{}
	for _, sub := range s.data {
		if !sub.Enabled || sub.RefreshHours <= 0 {
			continue
		}
		// Inline subscriptions have no remote source — auto-refresh
		// would just re-parse the same paste. Skip; user can still
		// trigger a manual refresh from the UI if they want a re-parse.
		if sub.IsInline() {
			continue
		}
		if sub.LastFetched.IsZero() ||
			now.Sub(sub.LastFetched) >= time.Duration(sub.RefreshHours)*time.Hour {
			out = append(out, *sub)
		}
	}
	return out
}
