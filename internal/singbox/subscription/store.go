package subscription

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

func (s *Store) List() []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Subscription, 0, len(s.data))
	for _, sub := range s.data {
		out = append(out, *sub)
	}
	return out
}

func (s *Store) Update(id string, patch UpdatePatch) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return nil, fmt.Errorf("subscription %q not found", id)
	}
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
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	cp := *sub
	return &cp, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[id]; !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
	delete(s.data, id)
	return s.saveLocked()
}

func (s *Store) UpdateState(id string, res RefreshResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
	sub.LastFetched = res.When
	if res.Err != nil {
		sub.LastError = MaskURL(res.Err.Error(), sub.URL)
	} else {
		sub.LastError = ""
	}
	return s.saveLocked()
}

// SetMembers replaces the Members slice and mirrors tags into MemberTags so
// existing consumers that iterate by tag still work. Also updates ActiveMember
// when the current active is no longer present.
func (s *Store) SetMembers(id string, members []MemberInfo, orphans []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
	sub.Members = members
	tags := make([]string, len(members))
	for i, m := range members {
		tags[i] = m.Tag
	}
	sub.MemberTags = tags
	sub.OrphanTags = orphans
	reconcileActiveMember(sub, tags)
	return s.saveLocked()
}

// SetMembersExtras updates members, orphans, rejected, and info in one write.
func (s *Store) SetMembersExtras(id string, members []MemberInfo, orphans []string, rejected []RejectedMember, info []SubscriptionInfoItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
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
	reconcileActiveMember(sub, tags)
	return s.saveLocked()
}

// RemoveInfoItem moves one info banner to rejectedMembers and dismisses it on refresh.
func (s *Store) RemoveInfoItem(id, itemID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
	idx := findInfoItem(sub.InfoItems, itemID)
	if idx < 0 {
		return ErrInfoItemNotFound
	}
	item := sub.InfoItems[idx]
	removedID := strings.TrimSpace(item.ID)
	sub.InfoItems = append(sub.InfoItems[:idx], sub.InfoItems[idx+1:]...)
	sub.DismissedInfoIDs = appendDismissedID(sub.DismissedInfoIDs, removedID)
	sub.RejectedMembers = appendRejectedUnique(sub.RejectedMembers, rejectedFromInfoItem(item))
	return s.saveLocked()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
	next := removeDismissedID(sub.DismissedInfoIDs, infoID)
	if len(next) == len(sub.DismissedInfoIDs) {
		return nil
	}
	sub.DismissedInfoIDs = next
	return s.saveLocked()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
	sub.RejectedMembers = rejected
	if info != nil {
		sub.InfoItems = info
	}
	return s.saveLocked()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
	sub.ProxyIndex = idx
	return s.saveLocked()
}

// SetMembership replaces MemberTags + OrphanTags atomically. Used by Service.Refresh.
// Auto-defaults ActiveMember to the first member when empty, and falls back to the
// first remaining member when the current active becomes orphan.
func (s *Store) SetMembership(id string, members, orphans []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
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
	return s.saveLocked()
}

func (s *Store) SetActiveMember(id, memberTag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
	sub.ActiveMember = memberTag
	return s.saveLocked()
}

func (s *Store) SetListenPort(id string, port uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data[id]
	if !ok {
		return fmt.Errorf("subscription %q not found", id)
	}
	sub.ListenPort = port
	return s.saveLocked()
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
