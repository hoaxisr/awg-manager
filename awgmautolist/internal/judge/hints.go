package judge

import (
	"net/netip"
	"time"
)

type pairKey struct {
	client netip.Addr
	real   netip.Addr
}

type hintKey struct {
	pair pairKey
	name string
}

type hintState struct {
	key  hintKey
	at   int64
	prev int
	next int
}

func (j *Judge) hintUnlink(slot int) {
	e := &j.hints[slot]
	if e.prev >= 0 {
		j.hints[e.prev].next = e.next
	} else {
		j.hintHead = e.next
	}
	if e.next >= 0 {
		j.hints[e.next].prev = e.prev
	} else {
		j.hintTail = e.prev
	}
	e.prev, e.next = -1, -1
}

func (j *Judge) hintPushTail(slot int) {
	e := &j.hints[slot]
	e.prev = j.hintTail
	e.next = -1
	if j.hintTail >= 0 {
		j.hints[j.hintTail].next = slot
	} else {
		j.hintHead = slot
	}
	j.hintTail = slot
}

func (j *Judge) hintTouch(slot int) {
	j.hintUnlink(slot)
	j.hintPushTail(slot)
}

func (j *Judge) removeHintFromPair(slot int) {
	key := j.hints[slot].key.pair
	slots := j.hintPairs[key]
	for i, candidate := range slots {
		if candidate != slot {
			continue
		}
		copy(slots[i:], slots[i+1:])
		slots = slots[:len(slots)-1]
		break
	}
	if len(slots) == 0 {
		delete(j.hintPairs, key)
	} else {
		j.hintPairs[key] = slots
	}
}

func (j *Judge) hintAdd(key hintKey) int {
	var slot int
	if j.hintCount < HintCapacity {
		slot = j.hintCount
		j.hintCount++
	} else {
		slot = j.hintHead
		delete(j.hintByKey, j.hints[slot].key)
		j.removeHintFromPair(slot)
		j.hintUnlink(slot)
	}
	j.hints[slot] = hintState{key: key, prev: -1, next: -1}
	j.hintByKey[key] = slot
	j.hintPairs[key.pair] = append(j.hintPairs[key.pair], slot)
	j.hintPushTail(slot)
	return slot
}

// Hint records DNS answers. Each entry is keyed by the client, resolved server
// address and exact name; clients cannot contaminate one another's naming.
func (j *Judge) Hint(client netip.Addr, name string, reals []netip.Addr, now time.Time) {
	if j == nil || !client.IsValid() {
		return
	}
	name = cString(name)
	if len(name) == 0 || len(name) >= NameMax {
		return
	}
	at := seconds(now)
	for _, real := range reals {
		if !real.IsValid() {
			continue
		}
		key := hintKey{pair: pairKey{client: client, real: real}, name: name}
		slot, found := j.hintByKey[key]
		if found {
			j.hintTouch(slot)
		} else {
			slot = j.hintAdd(key)
		}
		j.hints[slot].at = at
	}
}

func hintLive(e *hintState, now int64) bool {
	return now-e.at <= int64(HintTTL/time.Second)
}

func (j *Judge) pairHints(client, real netip.Addr) []int {
	return j.hintPairs[pairKey{client: client, real: real}]
}

// Hinted reports whether any unexpired name exists for client+server.
func (j *Judge) Hinted(client, server netip.Addr, now time.Time) bool {
	if j == nil || !client.IsValid() || !server.IsValid() {
		return false
	}
	at := seconds(now)
	for _, slot := range j.pairHints(client, server) {
		if hintLive(&j.hints[slot], at) {
			return true
		}
	}
	return false
}

func (j *Judge) hintNamed(client, server netip.Addr, name string, now int64) string {
	for _, slot := range j.pairHints(client, server) {
		e := &j.hints[slot]
		if hintLive(e, now) && e.key.name == name {
			return e.key.name
		}
	}
	return ""
}

// HintedName reports whether an unexpired hint has exactly name.
func (j *Judge) HintedName(client, server netip.Addr, name string, now time.Time) bool {
	if j == nil || !client.IsValid() || !server.IsValid() {
		return false
	}
	return j.hintNamed(client, server, cString(name), seconds(now)) != ""
}

type hintCount uint8

const (
	hintNone hintCount = iota
	hintOne
	hintDomain
	hintMany
)

func (j *Judge) hintOnly(client, real netip.Addr, now int64, name, domain *string) hintCount {
	*name = ""
	first := ""
	several := false
	for _, slot := range j.pairHints(client, real) {
		e := &j.hints[slot]
		if !hintLive(e, now) {
			continue
		}
		if first == "" {
			first = e.key.name
			continue
		}
		if !several {
			several = true
			var ok bool
			*domain, ok = regDomain(first)
			if !ok || regDomainShared(*domain) {
				return hintMany
			}
		}
		other, ok := regDomain(e.key.name)
		if !ok || other != *domain {
			return hintMany
		}
	}
	if first == "" {
		return hintNone
	}
	if several {
		return hintDomain
	}
	*name = first
	return hintOne
}

type seenKey struct {
	client     netip.Addr
	server     netip.Addr
	clientPort uint16
	port       uint16
	proto      uint8
}

type seenState struct {
	key  seenKey
	at   int64
	prev int
	next int
}

func (j *Judge) seenUnlink(slot int) {
	e := &j.seen[slot]
	if e.prev >= 0 {
		j.seen[e.prev].next = e.next
	} else {
		j.seenHead = e.next
	}
	if e.next >= 0 {
		j.seen[e.next].prev = e.prev
	} else {
		j.seenTail = e.prev
	}
	e.prev, e.next = -1, -1
}

func (j *Judge) seenPushTail(slot int) {
	e := &j.seen[slot]
	e.prev = j.seenTail
	e.next = -1
	if j.seenTail >= 0 {
		j.seen[j.seenTail].next = slot
	} else {
		j.seenHead = slot
	}
	j.seenTail = slot
}

func (j *Judge) seenTouch(slot int) {
	j.seenUnlink(slot)
	j.seenPushTail(slot)
}

func (j *Judge) seenAdd(key seenKey, now int64) {
	var slot int
	if j.seenCount < FlowCapacity {
		slot = j.seenCount
		j.seenCount++
	} else {
		slot = j.seenHead
		delete(j.seenByKey, j.seen[slot].key)
		j.seenUnlink(slot)
	}
	j.seen[slot] = seenState{key: key, at: now, prev: -1, next: -1}
	j.seenByKey[key] = slot
	j.seenPushTail(slot)
}

// FlowFirst implements NEW deduplication for a five-tuple. Repeated sightings
// are false until more than 300s have elapsed since the previous sighting.
// The table is a 1024-entry LRU, independent of the transport flow table.
func (j *Judge) FlowFirst(client netip.Addr, clientPort uint16, server netip.Addr, proto uint8, port uint16, now time.Time) bool {
	if j == nil || !client.IsValid() || !server.IsValid() {
		return false
	}
	key := seenKey{client: client, server: server, clientPort: clientPort, port: port, proto: proto}
	at := seconds(now)
	if slot, found := j.seenByKey[key]; found {
		e := &j.seen[slot]
		first := at-e.at > int64(FlowTTL/time.Second)
		e.at = at
		j.seenTouch(slot)
		return first
	}
	j.seenAdd(key, at)
	return true
}
