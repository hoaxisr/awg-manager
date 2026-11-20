package uplink

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Store persists only desired policy. Runtime observations are never trusted
// across restarts. A failed disk write must not publish an in-memory revision.
type Store struct {
	mu            sync.Mutex
	path          string
	policies      map[string]Policy
	pendingRename *RenameIntent
}

type diskPolicies struct {
	PendingRename *RenameIntent `json:"pendingRename,omitempty"`
	SchemaVersion int           `json:"schemaVersion"`
	Policies      []Policy      `json:"policies"`
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, policies: map[string]Policy{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var d diskPolicies
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	if d.SchemaVersion != 1 && d.SchemaVersion != 2 && d.SchemaVersion != 3 {
		return nil, fmt.Errorf("unsupported uplink schema %d", d.SchemaVersion)
	}
	targets := map[Target]bool{}
	for _, p := range d.Policies {
		p.Defaults()
		if d.SchemaVersion == 1 && p.Target.Kind != "" {
			return nil, fmt.Errorf("target kind requires uplink schema 2")
		}
		if err := p.Validate(); err != nil {
			return nil, err
		}
		if p.Revision == 0 || s.policies[p.ID].ID != "" || targets[p.Target] {
			return nil, fmt.Errorf("duplicate or invalid policy %q", p.ID)
		}
		targets[p.Target] = true
		s.policies[p.ID] = p
	}
	if d.PendingRename != nil {
		if d.SchemaVersion < 3 || d.PendingRename.From == d.PendingRename.To || d.PendingRename.From.Engine != d.PendingRename.To.Engine || d.PendingRename.From.Kind != d.PendingRename.To.Kind || d.PendingRename.To.Ref == "" || targets[d.PendingRename.To] {
			return nil, fmt.Errorf("invalid pending uplink rename")
		}
		found := false
		for _, p := range s.policies {
			if p.Target == d.PendingRename.From {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("pending rename has no owning policy")
		}
		s.pendingRename = d.PendingRename
	}
	return s, nil
}

func copyPolicy(p Policy) Policy { p.Providers = append([]string(nil), p.Providers...); return p }

func (s *Store) List() []Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]Policy, 0, len(s.policies))
	for _, p := range s.policies {
		list = append(list, copyPolicy(p))
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

func (s *Store) Put(p Policy) (Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingRename != nil {
		return Policy{}, fmt.Errorf("%w: uplink target rename needs recovery", ErrConflict)
	}
	p.Defaults()
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	old := s.policies[p.ID]
	if old.Revision != p.Revision {
		return Policy{}, ErrConflict
	}
	for id, existing := range s.policies {
		if id != p.ID && existing.Target == p.Target {
			return Policy{}, fmt.Errorf("target already owned by policy %q", id)
		}
	}
	p.Revision++
	next := make(map[string]Policy, len(s.policies)+1)
	for id, policy := range s.policies {
		next[id] = policy
	}
	next[p.ID] = copyPolicy(p)
	if err := s.save(next); err != nil {
		return Policy{}, err
	}
	s.policies = next
	return copyPolicy(p), nil
}

func (s *Store) CheckPut(p Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingRename != nil {
		return ErrConflict
	}
	if s.policies[p.ID].Revision != p.Revision {
		return ErrConflict
	}
	for id, existing := range s.policies {
		if id != p.ID && existing.Target == p.Target {
			return fmt.Errorf("%w: target already owned by policy %q", ErrConflict, id)
		}
	}
	return nil
}

func (s *Store) Delete(id string, revision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingRename != nil {
		return fmt.Errorf("%w: uplink target rename needs recovery", ErrConflict)
	}
	p, ok := s.policies[id]
	if !ok {
		return ErrNotFound
	}
	if p.Revision != revision {
		return ErrConflict
	}
	next := make(map[string]Policy, len(s.policies)-1)
	for key, policy := range s.policies {
		if key != id {
			next[key] = policy
		}
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.policies = next
	return nil
}

func (s *Store) save(policies map[string]Policy) error {
	d := diskPolicies{SchemaVersion: 3, Policies: make([]Policy, 0, len(policies))}
	d.PendingRename = s.pendingRename
	for _, p := range policies {
		d.Policies = append(d.Policies, p)
	}
	sort.Slice(d.Policies, func(i, j int) bool { return d.Policies[i].ID < d.Policies[j].ID })
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	return storage.AtomicWritePerm(s.path, b, 0600)
}
