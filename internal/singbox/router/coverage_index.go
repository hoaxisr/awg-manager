package router

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// CoverageSnapshot is the applied, active-slot projection used by Autolist.
// It is built together with the indexed sets, so rules and their rule_set
// references can never come from different configuration slots.
type CoverageSnapshot struct {
	Generation uint64    `json:"generation"`
	Slot       string    `json:"slot"`
	Rules      []Rule    `json:"rules"`
	RuleSets   []RuleSet `json:"rule_sets"`
}

// IndexedRuleSetMatch is the additive response of the indexed-match endpoint.
type IndexedRuleSetMatch struct {
	Generation uint64               `json:"generation"`
	Results    []RuleSetMatchResult `json:"results"`
}

const coverageIndexFailureRetryAfter = 30 * time.Second

type coverageIndexBuildFunc func(context.Context, []string) (*coverageIndexSnapshot, error)
type coverageIndexBuildContextMarker struct{}

type coverageIndexSnapshot struct {
	generation uint64
	slot       string
	rules      []Rule
	ruleSets   []RuleSet
	entries    map[string]*coverageIndexEntry
	// requested includes defined and undefined requested tags. An undefined
	// exclusion is a supported snapshot with an unsupported per-tag result, not
	// a missing generation that would rebuild forever.
	requested    map[string]struct{}
	refreshAfter time.Duration
}

func (s *coverageIndexSnapshot) hasRequested(tags []string) bool {
	for _, tag := range tags {
		if _, ok := s.requested[tag]; !ok {
			return false
		}
	}
	return true
}

func (s *coverageIndexSnapshot) projection() CoverageSnapshot {
	return CoverageSnapshot{
		Generation: s.generation,
		Slot:       s.slot,
		Rules:      slices.Clone(s.rules),
		RuleSets:   slices.Clone(s.ruleSets),
	}
}

func (s *coverageIndexSnapshot) match(input string, tags []string) IndexedRuleSetMatch {
	out := IndexedRuleSetMatch{
		Generation: s.generation,
		Results:    make([]RuleSetMatchResult, 0, len(tags)),
	}
	for _, tag := range tags {
		result := RuleSetMatchResult{Tag: tag}
		entry, ok := s.entries[tag]
		if !ok {
			result.Error = "rule-set is not defined"
			out.Results = append(out.Results, result)
			continue
		}
		result.Matched = entry.match(input)
		result.Supported = true
		out.Results = append(out.Results, result)
	}
	return out
}

// coverageIndex owns the always-current generation protocol. gate is an RW
// scheduler gate, not a data mutex: a builder holds its write side for the whole
// build, so active reads finish and queued reads cannot overtake an update.
// The ordinary mu is never held during file, network or subprocess I/O.
type coverageIndex struct {
	gate sync.RWMutex
	mu   sync.Mutex

	build coverageIndexBuildFunc
	logf  func(string)

	activated  bool
	generation uint64
	snapshot   *coverageIndexSnapshot
	extraTags  map[string]struct{}

	building     bool
	buildingGen  uint64
	buildCancel  context.CancelFunc
	buildDone    chan struct{}
	failedGen    uint64
	lastBuildErr error
	lastFailAt   time.Time
	refreshTimer *time.Timer
}

func newCoverageIndex(build coverageIndexBuildFunc, logf func(string)) *coverageIndex {
	return &coverageIndex{build: build, logf: logf, extraTags: map[string]struct{}{}}
}

// invalidate synchronously marks the current snapshot unusable and starts a
// new build once the index has been activated by its first read. Before that
// first read, invalidation is deliberately lazy: AWGM must not download and
// decompile a large working set on installations that never use Autolist.
func (i *coverageIndex) invalidate() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.activated {
		i.generation++
		i.snapshot = nil
		i.failedGen = 0
		i.lastBuildErr = nil
		return
	}
	i.startGenerationLocked()
}

func (i *coverageIndex) startGenerationLocked() {
	i.generation++
	if i.refreshTimer != nil {
		i.refreshTimer.Stop()
		i.refreshTimer = nil
	}
	i.snapshot = nil // stale fallback is not a supported mode
	i.failedGen = 0
	i.lastBuildErr = nil
	if i.buildCancel != nil {
		i.buildCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, coverageIndexBuildContextMarker{}, true)
	gen := i.generation
	done := make(chan struct{})
	tags := make([]string, 0, len(i.extraTags))
	for tag := range i.extraTags {
		tags = append(tags, tag)
	}
	i.slicesSort(tags)
	i.activated = true
	i.building = true
	i.buildingGen = gen
	i.buildCancel = cancel
	i.buildDone = done
	go i.runGeneration(ctx, gen, tags, done)
}

// slicesSort is a seam-free indirection that keeps startGenerationLocked small.
func (i *coverageIndex) slicesSort(tags []string) { slices.Sort(tags) }

func (i *coverageIndex) runGeneration(ctx context.Context, gen uint64, tags []string, done chan struct{}) {
	started := time.Now()
	// Writer preference of sync.RWMutex gives pending updates priority over
	// reads that arrive after this Lock starts waiting.
	i.gate.Lock()
	snapshot, err := i.build(ctx, tags)
	if ctxErr := ctx.Err(); err == nil && ctxErr != nil {
		err = ctxErr
	}

	i.mu.Lock()
	if gen == i.generation && i.buildingGen == gen && i.building {
		i.building = false
		i.buildCancel = nil
		if err != nil {
			i.failedGen = gen
			i.lastBuildErr = err
			i.lastFailAt = time.Now()
		} else if snapshot == nil {
			i.failedGen = gen
			i.lastBuildErr = fmt.Errorf("coverage index builder returned nil snapshot")
			i.lastFailAt = time.Now()
			err = i.lastBuildErr
		} else {
			snapshot.generation = gen
			i.snapshot = snapshot
			if snapshot.refreshAfter > 0 {
				i.refreshTimer = time.AfterFunc(snapshot.refreshAfter, i.invalidate)
			}
		}
	}
	close(done)
	current := gen == i.generation
	logf := i.logf
	i.mu.Unlock()
	i.gate.Unlock()
	if logf != nil && current {
		if err != nil {
			logf(fmt.Sprintf("coverage index generation %d failed after %s: %v", gen, time.Since(started).Round(time.Millisecond), err))
		} else {
			logf(fmt.Sprintf("coverage index generation %d ready after %s", gen, time.Since(started).Round(time.Millisecond)))
		}
	}
}

func (i *coverageIndex) snapshotRead(ctx context.Context, exclusionTags []string) (CoverageSnapshot, error) {
	var out CoverageSnapshot
	err := i.use(ctx, exclusionTags, true, func(snapshot *coverageIndexSnapshot) {
		out = snapshot.projection()
	})
	return out, err
}

func (i *coverageIndex) indexedMatch(ctx context.Context, input string, tags []string) (IndexedRuleSetMatch, error) {
	var out IndexedRuleSetMatch
	err := i.use(ctx, tags, false, func(snapshot *coverageIndexSnapshot) {
		out = snapshot.match(input, tags)
	})
	return out, err
}

// use waits for the latest complete generation containing tags and executes fn
// under the read gate. A failed generation is returned as an error. A later
// independent read may retry it once; concurrent waiters share the same error
// instead of launching a rebuild storm.
func (i *coverageIndex) use(ctx context.Context, tags []string, replaceTags bool, fn func(*coverageIndexSnapshot)) error {
	retryFailure := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		i.mu.Lock()
		added := false
		if replaceTags {
			added = i.replaceTagsLocked(tags)
		} else if !i.building {
			// While a snapshot build is running, wait and inspect its complete
			// active+exclusion working set first. Adding every active routing tag
			// here would turn each first match into a redundant new generation.
			added = i.addTagsLocked(tags)
		}
		if !added && i.snapshot != nil && i.snapshot.generation == i.generation && i.snapshot.hasRequested(tags) {
			i.mu.Unlock()
			i.gate.RLock()
			i.mu.Lock()
			if !added && i.snapshot != nil && i.snapshot.generation == i.generation && i.snapshot.hasRequested(tags) {
				snapshot := i.snapshot
				i.mu.Unlock()
				fn(snapshot) // immutable; write-side updates wait for this read
				i.gate.RUnlock()
				return nil
			}
			i.mu.Unlock()
			i.gate.RUnlock()
			continue
		}
		if i.building {
			if added {
				// The old build cannot satisfy this request. Cancel it and make
				// the expanded working set the newest generation.
				i.startGenerationLocked()
			}
			gen, done := i.buildingGen, i.buildDone
			i.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
			i.mu.Lock()
			failed := gen == i.generation && i.failedGen == gen && i.lastBuildErr != nil
			err := i.lastBuildErr
			i.mu.Unlock()
			if failed {
				return err
			}
			continue
		}
		if i.failedGen == i.generation && i.lastBuildErr != nil {
			if !retryFailure || time.Since(i.lastFailAt) < coverageIndexFailureRetryAfter {
				err := i.lastBuildErr
				i.mu.Unlock()
				return err
			}
			retryFailure = false
		}
		i.startGenerationLocked()
		i.mu.Unlock()
	}
}

func (i *coverageIndex) addTagsLocked(tags []string) bool {
	added := false
	for _, tag := range tags {
		if _, ok := i.extraTags[tag]; ok {
			continue
		}
		if i.snapshot != nil && i.snapshot.generation == i.generation {
			if _, ok := i.snapshot.requested[tag]; ok {
				continue
			}
		}
		i.extraTags[tag] = struct{}{}
		added = true
	}
	return added
}

// replaceTagsLocked installs the complete current exclusion set. Both adding
// and removing an exclusion therefore create a generation; the next snapshot
// self-replaces rather than accumulating historical Autolist selections.
func (i *coverageIndex) replaceTagsLocked(tags []string) bool {
	changed := len(i.extraTags) != len(tags)
	next := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		next[tag] = struct{}{}
		if _, ok := i.extraTags[tag]; !ok {
			changed = true
		}
	}
	if changed {
		i.extraTags = next
	}
	return changed
}

// ---- Immutable headless-rule representation ----

type coverageIndexEntry struct {
	rules []coverageIndexRule
}

func (e *coverageIndexEntry) match(input string) bool {
	for i := range e.rules {
		if e.rules[i].match(input) {
			return true
		}
	}
	return false
}

type coverageIndexRule struct {
	logical      bool
	mode         string
	children     []coverageIndexRule
	invert       bool
	needsContext bool
	dest         coverageDestination
}

func (r *coverageIndexRule) match(input string) bool {
	var matched bool
	if r.logical {
		switch r.mode {
		case "and":
			matched = true
			for i := range r.children {
				if !r.children[i].match(input) {
					matched = false
					break
				}
			}
		case "or":
			for i := range r.children {
				if r.children[i].match(input) {
					matched = true
					break
				}
			}
		}
	} else {
		// sing-box rule-set match receives only a domain/IP. Network and port
		// conditions cannot hold without that metadata, including under invert.
		matched = !r.needsContext && r.dest.match(input)
	}
	if r.invert {
		return !matched
	}
	return matched
}

type coverageDestination struct {
	exact     map[string]struct{}
	suffix    map[string]struct{}
	keywords  []string
	regexes   []*regexp.Regexp
	prefixes  *coveragePrefixTrie
	hasFields bool
}

func (d *coverageDestination) match(input string) bool {
	input = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input), "."))
	if addr, err := netip.ParseAddr(input); err == nil {
		addr = addr.Unmap()
		return d.prefixes != nil && d.prefixes.contains(addr)
	}
	if input == "" {
		return false
	}
	if _, ok := d.exact[input]; ok {
		return true
	}
	candidate := input
	for {
		if _, ok := d.suffix[candidate]; ok {
			return true
		}
		dot := strings.IndexByte(candidate, '.')
		if dot < 0 || dot == len(candidate)-1 {
			break
		}
		candidate = candidate[dot+1:]
	}
	for _, keyword := range d.keywords {
		if strings.Contains(input, keyword) {
			return true
		}
	}
	for _, expression := range d.regexes {
		if expression.MatchString(input) {
			return true
		}
	}
	return false
}

// coveragePrefixTrie is a binary prefix trie. It avoids a linear scan of large
// geoip sets while keeping one compact array per address family.
type coveragePrefixTrie struct {
	v4 []coverageTrieNode
	v6 []coverageTrieNode
}

type coverageTrieNode struct {
	zero     int32
	one      int32
	terminal bool
}

func newCoverageTrieNode() coverageTrieNode { return coverageTrieNode{zero: -1, one: -1} }

func (t *coveragePrefixTrie) insert(prefix netip.Prefix) error {
	prefix = prefix.Masked()
	addr := prefix.Addr().Unmap()
	if !addr.IsValid() {
		return fmt.Errorf("invalid prefix address")
	}
	var nodes *[]coverageTrieNode
	var bytes []byte
	if addr.Is4() {
		nodes = &t.v4
		value := addr.As4()
		bytes = value[:]
	} else {
		nodes = &t.v6
		value := addr.As16()
		bytes = value[:]
	}
	if len(*nodes) == 0 {
		*nodes = append(*nodes, newCoverageTrieNode())
	}
	at := int32(0)
	for bit := 0; bit < prefix.Bits(); bit++ {
		one := bytes[bit/8]&(1<<uint(7-bit%8)) != 0
		next := (*nodes)[at].zero
		if one {
			next = (*nodes)[at].one
		}
		if next < 0 {
			*nodes = append(*nodes, newCoverageTrieNode())
			next = int32(len(*nodes) - 1)
			// Assign after append: append may move the backing array.
			if one {
				(*nodes)[at].one = next
			} else {
				(*nodes)[at].zero = next
			}
		}
		at = next
	}
	(*nodes)[at].terminal = true
	return nil
}

func (t *coveragePrefixTrie) contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	var nodes []coverageTrieNode
	var bytes []byte
	if addr.Is4() {
		nodes = t.v4
		value := addr.As4()
		bytes = value[:]
	} else {
		nodes = t.v6
		value := addr.As16()
		bytes = value[:]
	}
	if len(nodes) == 0 {
		return false
	}
	at := int32(0)
	for bit := 0; bit < addr.BitLen(); bit++ {
		if nodes[at].terminal {
			return true
		}
		one := bytes[bit/8]&(1<<uint(7-bit%8)) != 0
		next := nodes[at].zero
		if one {
			next = nodes[at].one
		}
		if next < 0 {
			return false
		}
		at = next
	}
	return nodes[at].terminal
}
