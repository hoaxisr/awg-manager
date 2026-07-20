package awg3endpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Store хранит AWG3-endpoint записи в одном JSON-файле ([]Record).
type Store struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	records []Record
}

// NewStore создаёт store поверх файла path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// load лениво читает файл. Пустой/отсутствующий → пустой список.
// Вызывать под удержанием s.mu.
func (s *Store) load() error {
	if s.loaded {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.records = nil
			s.loaded = true
			return nil
		}
		return fmt.Errorf("read awg3 store: %w", err)
	}
	if len(data) == 0 {
		s.records = nil
		s.loaded = true
		return nil
	}
	var recs []Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return fmt.Errorf("parse awg3 store: %w", err)
	}
	s.records = recs
	s.loaded = true
	return nil
}

// save пишет текущий слайс атомарно. Вызывать под удержанием s.mu.
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal awg3 store: %w", err)
	}
	// 0600: awg3.json содержит приватные ключи endpoint'ов — не отдаём их
	// в мир (остальные store'ы пишутся 0644, их права не трогаем).
	if err := storage.AtomicWritePerm(s.path, data, 0600); err != nil {
		return fmt.Errorf("write awg3 store: %w", err)
	}
	return nil
}

// List возвращает копию всех записей в порядке добавления.
func (s *Store) List() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return nil, err
	}
	out := make([]Record, len(s.records))
	copy(out, s.records)
	return out, nil
}

// Add добавляет запись в конец и сохраняет.
func (s *Store) Add(rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	s.records = append(s.records, rec)
	return s.save()
}

// Delete удаляет запись по id и сохраняет.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	for i, r := range s.records {
		if r.ID == id {
			s.records = append(s.records[:i], s.records[i+1:]...)
			return s.save()
		}
	}
	return fmt.Errorf("awg3 endpoint not found: %s", id)
}

// Rename меняет тег записи id на newTag. Ошибка, если newTag занят ДРУГОЙ записью.
func (s *Store) Rename(id, newTag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	idx := -1
	for i, r := range s.records {
		if r.ID == id {
			idx = i
			continue
		}
		if r.Tag == newTag {
			return fmt.Errorf("%w: %q", ErrTag, newTag)
		}
	}
	if idx == -1 {
		return fmt.Errorf("awg3 endpoint not found: %s", id)
	}
	s.records[idx].Tag = newTag
	return s.save()
}

// Get возвращает запись по id.
func (s *Store) Get(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return Record{}, false
	}
	for _, r := range s.records {
		if r.ID == id {
			return r, true
		}
	}
	return Record{}, false
}

// Tags возвращает множество занятых тегов. Сигнатура без error намеренная
// (Parse и HasContent-замыкание зовут её как чистую функцию): битый файл →
// пустой результат. Fail-closed обеспечивают Add/List — они читают тот же
// файл и вернут ошибку раньше, чем пустой Tags() приведёт к дублю.
func (s *Store) Tags() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return map[string]bool{}
	}
	tags := make(map[string]bool, len(s.records))
	for _, r := range s.records {
		tags[r.Tag] = true
	}
	return tags
}

// Len возвращает число записей (для HasContent). Сигнатура без error
// намеренная (используется в HasContent-замыкании): битый файл → 0.
// Fail-closed обеспечивают Add/List, читающие тот же файл (см. Tags).
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return 0
	}
	return len(s.records)
}
