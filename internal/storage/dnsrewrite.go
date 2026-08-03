package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
)

type dnsRewriteData struct {
	Rewrites []dnsrewrite.DNSRewrite `json:"rewrites"`
}

// DNSRewriteStore — канонический стор перезаписей (отдельно от RouterConfig).
type DNSRewriteStore struct {
	path string
	mu   sync.RWMutex
}

func NewDNSRewriteStore(path string) *DNSRewriteStore {
	return &DNSRewriteStore{path: path}
}

func (s *DNSRewriteStore) loadUnlocked() (*dnsRewriteData, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &dnsRewriteData{Rewrites: []dnsrewrite.DNSRewrite{}}, nil
		}
		return nil, fmt.Errorf("read dns rewrites: %w", err)
	}
	var d dnsRewriteData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("parse dns rewrites: %w", err)
	}
	if d.Rewrites == nil {
		d.Rewrites = []dnsrewrite.DNSRewrite{}
	}
	return &d, nil
}

func (s *DNSRewriteStore) saveUnlocked(d *dnsRewriteData) error {
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dns rewrites: %w", err)
	}
	if err := AtomicWrite(s.path, raw); err != nil {
		return fmt.Errorf("write dns rewrites: %w", err)
	}
	return nil
}

func (s *DNSRewriteStore) List() ([]dnsrewrite.DNSRewrite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, err := s.loadUnlocked()
	if err != nil {
		return nil, err
	}
	return d.Rewrites, nil
}

func (s *DNSRewriteStore) Add(r dnsrewrite.DNSRewrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	d.Rewrites = append(d.Rewrites, r)
	return s.saveUnlocked(d)
}

func (s *DNSRewriteStore) Update(index int, r dnsrewrite.DNSRewrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if index < 0 || index >= len(d.Rewrites) {
		return fmt.Errorf("dns rewrite index %d out of range", index)
	}
	d.Rewrites[index] = r
	return s.saveUnlocked(d)
}

func (s *DNSRewriteStore) Delete(index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if index < 0 || index >= len(d.Rewrites) {
		return fmt.Errorf("dns rewrite index %d out of range", index)
	}
	d.Rewrites = append(d.Rewrites[:index], d.Rewrites[index+1:]...)
	return s.saveUnlocked(d)
}

func (s *DNSRewriteStore) Move(from, to int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	n := len(d.Rewrites)
	if from < 0 || from >= n || to < 0 || to >= n {
		return fmt.Errorf("dns rewrite move index out of range")
	}
	if from == to {
		return nil
	}
	r := d.Rewrites[from]
	d.Rewrites = append(d.Rewrites[:from], d.Rewrites[from+1:]...)
	d.Rewrites = append(d.Rewrites[:to], append([]dnsrewrite.DNSRewrite{r}, d.Rewrites[to:]...)...)
	return s.saveUnlocked(d)
}

// ReplaceManaged swaps every rewrite with Managed==id for items (may be
// empty = drop managed entries only). Non-managed rewrites are preserved
// in order. Each item is stamped with Managed=id.
func (s *DNSRewriteStore) ReplaceManaged(id string, items []dnsrewrite.DNSRewrite) error {
	if id == "" {
		return fmt.Errorf("ReplaceManaged: empty managed id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	kept := make([]dnsrewrite.DNSRewrite, 0, len(d.Rewrites))
	for _, r := range d.Rewrites {
		if r.Managed == id {
			continue
		}
		kept = append(kept, r)
	}
	for _, r := range items {
		r.Managed = id
		kept = append(kept, r)
	}
	d.Rewrites = kept
	return s.saveUnlocked(d)
}
