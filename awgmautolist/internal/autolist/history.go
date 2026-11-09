package autolist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const historyLimit = 512

// HistoryEntry — завершённое действие автосписка. At сериализуется time.Time
// в RFC3339 JSON; Host и Why отсутствуют, если строки кандидата уже не было.
type HistoryEntry struct {
	Domain string    `json:"domain"`
	Host   string    `json:"host,omitempty"`
	Action string    `json:"action"`
	Why    string    `json:"why,omitempty"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

// historyStore хранит записи в хронологическом порядке. path пуст для
// тестового in-memory Engine; ошибки записи не отменяют уже сделанное действие.
type historyStore struct {
	mu      sync.Mutex
	path    string
	entries []HistoryEntry
}

func newHistoryStore() *historyStore {
	return &historyStore{entries: make([]HistoryEntry, 0)}
}

// relevantHistory оставляет только пользовательские скрытия и исключения.
// Текущее содержимое туннеля читается из самого rule-set, а не из журнала.
func relevantHistory(entries []HistoryEntry) []HistoryEntry {
	out := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Action == "dismissed" || entry.Action == "excluded" {
			out = append(out, entry)
		}
	}
	return out
}

func historyPathFromConfig(configPath string) string {
	if configPath == "" {
		return ""
	}
	if strings.HasSuffix(configPath, ".json") {
		return strings.TrimSuffix(configPath, ".json") + "-history.json"
	}
	return configPath + "-history.json"
}

// load заменяет память содержимым sibling-файла. Отсутствие файла означает
// пустую историю; malformed JSON возвращается вызывающему для журнала.
func (h *historyStore) load(path string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.path = path
	h.entries = make([]HistoryEntry, 0)
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read history: %w", err)
	}
	var entries []HistoryEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return fmt.Errorf("decode history: %w", err)
	}
	entries = relevantHistory(entries)
	if len(entries) > historyLimit {
		entries = entries[len(entries)-historyLimit:]
	}
	h.entries = append(h.entries, entries...)
	return nil
}

// record добавляет один accepted batch и сохраняет его одной атомарной записью.
// В памяти действие остаётся даже при ошибке диска, чтобы /status не потерял его.
func (h *historyStore) record(entries []HistoryEntry) error {
	entries = relevantHistory(entries)
	if len(entries) == 0 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, entries...)
	if overflow := len(h.entries) - historyLimit; overflow > 0 {
		kept := make([]HistoryEntry, historyLimit)
		copy(kept, h.entries[overflow:])
		h.entries = kept
	}
	if h.path == "" {
		return nil
	}
	return writeHistoryAtomic(h.path, h.entries)
}

// removeHidden удаляет все записи скрытия домена после его восстановления.
// Журнал содержит только временные и постоянные скрытия.
func (h *historyStore) removeHidden(domain string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	kept := make([]HistoryEntry, 0, len(h.entries))
	for _, entry := range h.entries {
		if entry.Domain == domain && (entry.Action == "dismissed" || entry.Action == "excluded") {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == len(h.entries) {
		return nil
	}
	h.entries = kept
	if h.path == "" {
		return nil
	}
	return writeHistoryAtomic(h.path, h.entries)
}

// list возвращает новые записи первыми и никогда не возвращает nil для JSON.
func (h *historyStore) list() []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries := relevantHistory(h.entries)
	out := make([]HistoryEntry, len(entries))
	for i, entry := range entries {
		out[len(entries)-1-i] = entry
	}
	return out
}

func writeHistoryAtomic(path string, entries []HistoryEntry) error {
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func historyEntryFromCandidate(domain, action, source string, at time.Time, row Candidate, exists bool) HistoryEntry {
	entry := HistoryEntry{Domain: domain, Action: action, Source: source, At: at}
	if exists {
		entry.Host = row.Host
		entry.Why = row.Why
	}
	return entry
}
