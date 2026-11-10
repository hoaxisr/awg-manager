package judge

import (
	"math"
	"net/netip"
	"strings"
	"time"
)

type flowState struct {
	used        bool
	judged      bool
	bypass      bool
	ambiguous   bool
	finned      bool
	syn         bool
	domainNamed bool
	sent        uint8
	seq         uint32
	ack0        uint32
	seen        int64
	helloAt     int64
	synAt       int64
	host        int
	hostSerial  uint32
	key         Flow
	sni         string
}

type tally struct {
	n    uint32
	last int64
}

type notes struct {
	at [StallNotesThreshold]int64
	n  uint8
}

type hostState struct {
	used      bool
	serial    uint32
	fails     tally
	noConnect tally
	seen      int64
	bad       [badAddressCapacity]netip.Addr
	nBad      uint8
	nextBad   uint8
	notes     notes
	name      string
}

type countKind uint8

const (
	kindFail countKind = iota
	kindNoConnect
	kindNote
	kindCount
)

type domainHost struct {
	host   int
	serial uint32
	n      [kindCount]uint32
}

type domainState struct {
	used      bool
	serial    uint32
	seen      int64
	fails     tally
	noConnect tally
	notes     notes
	hostless  [kindCount]uint32
	hosts     [domainHostCapacity]domainHost
	nHosts    uint8
	name      string
}

// Judge owns all bounded autolist state. Use New or NewWithParams to
// initialize one; the zero value is not ready for use.
type Judge struct {
	params Params
	serial uint32

	flows   [FlowCapacity]flowState
	hosts   [HostCapacity]hostState
	domains [DomainCapacity]domainState

	hints     [HintCapacity]hintState
	hintByKey map[hintKey]int
	hintPairs map[pairKey][]int
	hintHead  int
	hintTail  int
	hintCount int

	seen      [FlowCapacity]seenState
	seenByKey map[seenKey]int
	seenHead  int
	seenTail  int
	seenCount int

	addr     [addressRows]addressRow
	addrUsed [addressRows]bool
	hidden   [addressRows]hiddenAddress
	hiddenAt int

	dayHour        [DayHours]int64
	dayCount       [DayHours]uint32
	ambiguousHour  [DayHours]int64
	ambiguousCount [DayHours]uint32
}

// New constructs a Judge with autolist's default thresholds.
func New() *Judge { return NewWithParams(Params{}) }

// NewWithParams constructs a Judge. Zero or negative timing values select the
// defaults, as do zero thresholds.
func NewWithParams(p Params) *Judge {
	j := &Judge{
		params:    normalizeParams(p),
		hintByKey: make(map[hintKey]int),
		hintPairs: make(map[pairKey][]int),
		hintHead:  -1,
		hintTail:  -1,
		seenByKey: make(map[seenKey]int),
		seenHead:  -1,
		seenTail:  -1,
	}
	return j
}

// SetParams changes thresholds without clearing existing state.
func (j *Judge) SetParams(p Params) {
	if j != nil {
		j.params = normalizeParams(p)
	}
}

func seconds(now time.Time) int64 { return now.Unix() }

func cString(s string) string {
	if at := strings.IndexByte(s, 0); at >= 0 {
		return s[:at]
	}
	return s
}

func clip(s string, limit int) string {
	if len(s) > limit {
		return s[:limit]
	}
	return s
}

func clipName(s string) string { return clip(cString(s), NameMax-1) }

func (j *Judge) tallyLive(t *tally, now int64) bool {
	return t.n > 0 && now-t.last <= int64(j.params.FailWindow/time.Second)
}

func (j *Judge) tallyAdd(t *tally, now int64) uint32 {
	if !j.tallyLive(t, now) {
		t.n = 0
	}
	t.n++
	t.last = now
	return t.n
}

func notesLive(n *notes, now int64) bool {
	return n.n > 0 && now-n.at[n.n-1] <= int64(StallNoteWindow/time.Second)
}

func notesAdd(n *notes, now int64) uint8 {
	kept := 0
	for i := 0; i < int(n.n); i++ {
		if now-n.at[i] <= int64(StallNoteWindow/time.Second) {
			n.at[kept] = n.at[i]
			kept++
		}
	}
	if kept == StallNotesThreshold {
		copy(n.at[:], n.at[1:])
		kept--
	}
	n.at[kept] = now
	kept++
	n.n = uint8(kept)
	return n.n
}

func hostRestart(h *hostState) {
	h.fails.n = 0
	h.nBad = 0
	h.nextBad = 0
}

func (j *Judge) hostCounting(h *hostState, now int64) bool {
	return h.used && (j.tallyLive(&h.fails, now) || j.tallyLive(&h.noConnect, now))
}

func (j *Judge) domainCounting(d *domainState, now int64) bool {
	return d.used && (j.tallyLive(&d.fails, now) || j.tallyLive(&d.noConnect, now) || notesLive(&d.notes, now))
}

func (j *Judge) hostTake(name string, now int64) int {
	if len(name) == 0 || len(name) >= NameMax {
		return -1
	}
	freeAt, idle, oldest := -1, -1, -1
	for i := range j.hosts {
		h := &j.hosts[i]
		if h.used && h.name == name {
			h.seen = now
			return i
		}
		if !h.used {
			if freeAt < 0 {
				freeAt = i
			}
		} else if !j.hostCounting(h, now) {
			if idle < 0 || h.seen < j.hosts[idle].seen {
				idle = i
			}
		} else if oldest < 0 || h.seen < j.hosts[oldest].seen {
			oldest = i
		}
	}
	at := freeAt
	if at < 0 {
		at = idle
	}
	if at < 0 {
		at = oldest
	}
	if at < 0 {
		return -1
	}
	h := &j.hosts[at]
	*h = hostState{
		used:   true,
		serial: j.nextSerial(),
		seen:   now,
		name:   name,
	}
	return at
}

func (j *Judge) nextSerial() uint32 {
	j.serial++
	return j.serial
}

func (j *Judge) hostOf(f *flowState) *hostState {
	if f.host < 0 || f.host >= len(j.hosts) {
		return nil
	}
	h := &j.hosts[f.host]
	if !h.used || h.serial != f.hostSerial {
		return nil
	}
	return h
}

func (j *Judge) flowFind(key Flow, now int64) *flowState {
	for i := range j.flows {
		f := &j.flows[i]
		if f.used && now-f.seen <= int64(FlowTTL/time.Second) && f.key == key {
			return f
		}
	}
	return nil
}

func (j *Judge) flowSlot(now int64) *flowState {
	oldest := -1
	for i := range j.flows {
		f := &j.flows[i]
		if !f.used || now-f.seen > int64(FlowTTL/time.Second) {
			return f
		}
		if oldest < 0 || f.seen < j.flows[oldest].seen {
			oldest = i
		}
	}
	return &j.flows[oldest]
}

func flowStart(f *flowState, key Flow, seq uint32, now int64) {
	*f = flowState{
		used: true,
		key:  key,
		seq:  seq,
		seen: now,
		host: -1,
	}
}

func (j *Judge) flowName(f *flowState, sni string, now int64) {
	sni = cString(sni)
	f.sni = clipName(sni)
	name := ""
	if sni != "" {
		name = j.hintNamed(f.key.Client, f.key.Server, sni, now)
		f.bypass = name == ""
	} else {
		var domain string
		count := j.hintOnly(f.key.Client, f.key.Server, now, &name, &domain)
		f.bypass = count == hintNone
		f.ambiguous = count == hintMany
		f.domainNamed = count == hintDomain
	}
	if name != "" {
		f.host = j.hostTake(name, now)
		if f.host >= 0 {
			f.hostSerial = j.hosts[f.host].serial
		}
	}
}

// Hello observes an original ClientHello or a retransmission of the same
// sequence number. With defaults, the original plus three retransmissions
// produces one Failure; further retransmissions of that flow are ignored.
func (j *Judge) Hello(key Flow, seq, ack uint32, sni string, now time.Time) (Failure, bool) {
	if j == nil || !key.Client.IsValid() || !key.Server.IsValid() {
		return Failure{}, false
	}
	at := seconds(now)
	f := j.flowFind(key, at)
	if f != nil && f.seq == seq {
		f.seen = at
		if f.judged {
			return Failure{}, false
		}
		if f.sent < math.MaxUint8 {
			f.sent++
		}
		if uint32(f.sent)-1 < j.params.RetransThreshold {
			return Failure{}, false
		}
		return j.fail(f, WhyRetrans, at)
	}
	if f == nil {
		f = j.flowSlot(at)
	}
	flowStart(f, key, seq, at)
	f.ack0 = ack
	f.sent = 1
	f.helloAt = at
	j.flowName(f, sni, at)
	return Failure{}, false
}

// Answered observes a non-reset server reply. It judges the flow successful,
// clears the host's failure history only when this exact server was previously
// bad, and clears the domain failure tally without disturbing other counters.
func (j *Judge) Answered(key Flow, now time.Time) {
	j.reply(key, false, now)
}

// Reset observes a TCP reset on a HELLO flow and emits its single Failure.
func (j *Judge) Reset(key Flow, now time.Time) (Failure, bool) {
	return j.reply(key, true, now)
}

// Reply is the autolist-compatible form of Answered (rst=false) and Reset
// (rst=true). The boolean reports whether the event produced a Failure.
func (j *Judge) Reply(key Flow, rst bool, now time.Time) (Failure, bool) {
	return j.reply(key, rst, now)
}

func (j *Judge) reply(key Flow, rst bool, now time.Time) (Failure, bool) {
	if j == nil || !key.Client.IsValid() || !key.Server.IsValid() {
		return Failure{}, false
	}
	at := seconds(now)
	f := j.flowFind(key, at)
	if f == nil || f.syn || f.judged {
		return Failure{}, false
	}
	f.seen = at
	if rst {
		return j.fail(f, WhyReset, at)
	}
	f.judged = true
	h := j.hostOf(f)
	if h != nil && hostBadAt(h, f.key.Server) {
		hostRestart(h)
	}
	var d *domainState
	if h != nil {
		d = j.domainFind(h.name, false, at)
	} else {
		d = j.flowNamedDomain(f, false, at)
	}
	if d != nil {
		d.fails.n = 0
	}
	return Failure{}, false
}

func (j *Judge) fail(f *flowState, why Why, now int64) (Failure, bool) {
	f.judged = true
	if f.ambiguous {
		dayBump(j.ambiguousHour[:], j.ambiguousCount[:], now)
		return Failure{}, false
	}
	if f.bypass {
		fails := j.bypassCount(f, why, now)
		return Failure{
			Host:   f.sni,
			Client: f.key.Client,
			Server: f.key.Server,
			Fails:  fails,
			Why:    why,
			Bypass: true,
		}, true
	}
	h := j.hostOf(f)
	d := j.domainCredit(f, h, kindFail, now)
	if h == nil && d == nil {
		j.unnamed(f, now)
		return Failure{}, false
	}
	dayBump(j.dayHour[:], j.dayCount[:], now)
	var fails uint32
	if h != nil {
		if !j.tallyLive(&h.fails, now) {
			hostRestart(h)
		}
		fails = j.tallyAdd(&h.fails, now)
		hostNoteBad(h, f.key.Server)
	}
	var domainFails uint32
	if d != nil {
		domainFails = j.tallyAdd(&d.fails, now)
	}
	byHost := h != nil && fails >= j.params.FailThreshold
	byDomain := !byHost && d != nil && domainFails >= j.params.FailThreshold
	named := ""
	switch {
	case byDomain:
		named = j.domainMarker(d, kindFail)
	case h != nil:
		named = h.name
	default:
		named = d.name
	}
	out := Failure{
		Host:   clipName(named),
		Client: f.key.Client,
		Why:    why,
		Listed: byHost || byDomain,
		Fails:  domainFails,
	}
	if h != nil && !byDomain {
		out.Fails = fails
	}
	if byDomain {
		out.Domain = d.name
	}
	if byHost {
		hostRestart(h)
	}
	if byDomain {
		j.domainFire(d, kindFail)
	} else if byHost && d != nil {
		domainRestartKind(d, kindFail)
	}
	return out, true
}

func (j *Judge) unnamed(f *flowState, now int64) {
	if f.domainNamed {
		dayBump(j.ambiguousHour[:], j.ambiguousCount[:], now)
	}
}

// Close observes a FIN/close acknowledgment. A flow is examined at most once;
// only 8192..32768 received bytes and age >=10s create a stall note. Two notes
// within 120s nominate the host or its domain.
func (j *Judge) Close(key Flow, ack uint32, now time.Time) (Nomination, bool) {
	if j == nil || !key.Client.IsValid() || !key.Server.IsValid() {
		return Nomination{}, false
	}
	at := seconds(now)
	f := j.flowFind(key, at)
	if f == nil || f.syn || f.finned {
		return Nomination{}, false
	}
	f.finned = true
	f.seen = at
	received := ack - f.ack0
	if received < StallMinBytes || received > StallMaxBytes {
		return Nomination{}, false
	}
	if at-f.helloAt < int64(StallMinAge/time.Second) {
		return Nomination{}, false
	}
	h := j.hostOf(f)
	d := j.domainCredit(f, h, kindNote, at)
	if h == nil && d == nil {
		return Nomination{}, false
	}
	byHost := h != nil && uint32(notesAdd(&h.notes, at)) >= j.params.StallNotesThreshold
	byDomain := d != nil && uint32(notesAdd(&d.notes, at)) >= j.params.StallNotesThreshold
	if byHost {
		h.notes.n = 0
	}
	return j.nominate(f, h, d, byHost, byDomain, NominationStall, received)
}

// FIN is an alias for Close.
func (j *Judge) FIN(key Flow, ack uint32, now time.Time) (Nomination, bool) {
	return j.Close(key, ack, now)
}

// SYN observes an original SYN or its retransmission. A same-sequence SYN at
// least two seconds after the first is one no-connect observation per flow.
func (j *Judge) SYN(key Flow, seq uint32, now time.Time) (Nomination, bool) {
	if j == nil || !key.Client.IsValid() || !key.Server.IsValid() {
		return Nomination{}, false
	}
	at := seconds(now)
	f := j.flowFind(key, at)
	if f != nil && !f.syn && seq+1 == f.seq {
		return Nomination{}, false
	}
	if f != nil && f.syn && f.seq == seq {
		f.seen = at
		if f.judged || at-f.synAt < int64(SYNRetransDelay/time.Second) {
			return Nomination{}, false
		}
		return j.noConnect(f, at)
	}
	if f == nil {
		f = j.flowSlot(at)
	}
	flowStart(f, key, seq, at)
	f.syn = true
	f.synAt = at
	j.flowName(f, "", at)
	return Nomination{}, false
}

func (j *Judge) noConnect(f *flowState, now int64) (Nomination, bool) {
	f.judged = true
	if f.ambiguous {
		dayBump(j.ambiguousHour[:], j.ambiguousCount[:], now)
		return Nomination{}, false
	}
	if f.bypass {
		j.bypassCount(f, WhyNoConnect, now)
		return Nomination{
			Client: f.key.Client,
			Server: f.key.Server,
			Kind:   NominationNoConnect,
			Bypass: true,
		}, true
	}
	h := j.hostOf(f)
	d := j.domainCredit(f, h, kindNoConnect, now)
	if h == nil && d == nil {
		j.unnamed(f, now)
		return Nomination{}, false
	}
	byHost := h != nil && j.tallyAdd(&h.noConnect, now) >= j.params.FailThreshold
	byDomain := d != nil && j.tallyAdd(&d.noConnect, now) >= j.params.FailThreshold
	if byHost {
		h.noConnect.n = 0
	}
	return j.nominate(f, h, d, byHost, byDomain, NominationNoConnect, 0)
}

func (j *Judge) nominate(f *flowState, h *hostState, d *domainState, byHost, byDomain bool, kind NominationKind, received uint32) (Nomination, bool) {
	if !byHost && !byDomain {
		return Nomination{}, false
	}
	counted := kindNote
	if kind == NominationNoConnect {
		counted = kindNoConnect
	}
	named := ""
	if byHost {
		named = h.name
	} else {
		named = j.domainMarker(d, counted)
	}
	out := Nomination{
		Host:     clipName(named),
		Client:   f.key.Client,
		Received: received,
		Kind:     kind,
	}
	if !byHost {
		out.Domain = d.name
	}
	if !byHost {
		j.domainFire(d, counted)
	} else if d != nil {
		domainRestartKind(d, counted)
	}
	return out, true
}

func hourOf(now int64) int64 {
	hour := now / 3600
	if now%3600 < 0 {
		hour--
	}
	return hour
}

func dayBump(hours []int64, counts []uint32, now int64) {
	hour := hourOf(now)
	at := int((hour%DayHours + DayHours) % DayHours)
	if hours[at] != hour {
		hours[at] = hour
		counts[at] = 0
	}
	counts[at]++
}

func daySum(hours []int64, counts []uint32, now int64) uint32 {
	hour := hourOf(now)
	var n uint32
	for i := 0; i < DayHours; i++ {
		if hours[i] <= hour && hour-hours[i] < DayHours {
			n += counts[i]
		}
	}
	return n
}

// FailuresDay returns attributed failures in the current and preceding 23
// hour buckets. Bypass and ambiguous observations are not included.
func (j *Judge) FailuresDay(now time.Time) uint32 {
	if j == nil {
		return 0
	}
	return daySum(j.dayHour[:], j.dayCount[:], seconds(now))
}

// AmbiguousDay returns rejected ambiguous observations in the same rolling
// 24-hour buckets as FailuresDay.
func (j *Judge) AmbiguousDay(now time.Time) uint32 {
	if j == nil {
		return 0
	}
	return daySum(j.ambiguousHour[:], j.ambiguousCount[:], seconds(now))
}

func hostBadAt(h *hostState, server netip.Addr) bool {
	for i := 0; i < int(h.nBad); i++ {
		if h.bad[i] == server {
			return true
		}
	}
	return false
}

func hostNoteBad(h *hostState, server netip.Addr) {
	if hostBadAt(h, server) {
		return
	}
	if h.nBad < badAddressCapacity {
		h.bad[h.nBad] = server
		h.nBad++
		return
	}
	h.bad[h.nextBad] = server
	h.nextBad = (h.nextBad + 1) % badAddressCapacity
}
