package yandex

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

// Telemost caps what one subscriber PeerConnection receives at ~16 Mbit/s in
// total, whatever the number of streams in its slots, and a single published
// stream tops out at the same rate. Several participants in ONE room break
// that ceiling: each lane is a full participant that publishes its own camera
// and subscribes to exactly one peer lane's camera - one slot, paged onto that
// peer with setSlotsOffset - so every subscriber's 16 Mbit/s carries distinct
// data. transport.StripedTransport spreads frames over the lanes and restores
// their order on the other side.

// NewTelemostGroup creates n lanes joined to the same conference. The group
// is also a transport.LaneSource, so the stripe can grow it later.
func NewTelemostGroup(cookies, conferenceURL string, n int, isExitNode bool, config transport.TransportConfig) *TelemostGroup {
	g := &TelemostGroup{cookies: cookies, url: conferenceURL, isExit: isExitNode, config: config,
		id: uuid.New().String()[:8], start: time.Now().UnixMilli()}
	for i := 0; i < n; i++ {
		g.newLane()
	}
	// Peers become eligible for growth only after peerSettle, which no
	// description event announces - re-evaluate on a clock as well.
	go func() {
		for range time.Tick(2 * time.Second) {
			g.recompute()
		}
	}()
	return g
}

// TelemostGroup is the set of lanes (participants) one node runs in a room.
type TelemostGroup struct {
	cookies, url string
	isExit       bool
	config       transport.TransportConfig

	mu      sync.Mutex
	lanes   []*TelemostTransport
	grow    func()
	growing atomic.Bool
	silent  map[string]time.Time // peers a lane watched and got nothing from

	id    string // this run of the group, in every lane's tag
	start int64  // when it started (unix ms), to tell the newest run
}

// laneTag names a lane in its participant description: role, the group (one
// run of a node's stripe), when that run started and the lane's index. The
// SFU keeps listing a participant that vanished without a word for minutes,
// so after a reconnect or a restart the room is full of lanes that carry
// nothing; a node watches only the newest group of the other role and never
// wastes a lane, or seconds of probing, on them.
//
// The SFU passes a participant's name through untouched but drops the
// description (every peer sees "description":""), so the tag rides in the
// name: after the name everyone sees (participantName) come characters that
// take no space, two bits each (laneTag.hidden). String is the readable form
// for logs.
type laneTag struct {
	role  string // "exit" or "client"
	group string // 8 hex digits
	start int64
	index int
}

func (l laneTag) String() string {
	return fmt.Sprintf("%c%s-%s-%d", l.role[0], l.group, strconv.FormatInt(l.start, 36), l.index)
}

// tagMarks write a hidden lane tag: zero width space, zero width non-joiner,
// zero width joiner and word joiner - nothing a name shows.
var tagMarks = [4]rune{'\u200b', '\u200c', '\u200d', '\u2060'}

// hidden is the tag as name characters that take no space: a two-mark
// header, then 11 bytes - role and index, group, start (unix ms, 48 bits) -
// four marks a byte.
func (l laneTag) hidden() string {
	g, _ := strconv.ParseUint(l.group, 16, 32)
	var b [11]byte
	b[0] = byte(l.index & 0x7f)
	if l.role == "exit" {
		b[0] |= 0x80
	}
	binary.BigEndian.PutUint32(b[1:5], uint32(g))
	st := uint64(l.start)
	for i := 0; i < 6; i++ {
		b[5+i] = byte(st >> (8 * (5 - i)))
	}
	var sb strings.Builder
	sb.WriteRune(tagMarks[3])
	sb.WriteRune(tagMarks[3])
	for _, x := range b {
		for sh := 6; sh >= 0; sh -= 2 {
			sb.WriteRune(tagMarks[(x>>sh)&3])
		}
	}
	return sb.String()
}

// parseHiddenTag reads a tag hidden in a participant name.
func parseHiddenTag(name string) (laneTag, bool) {
	var d []byte
	for _, r := range name {
		for i, m := range tagMarks {
			if r == m {
				d = append(d, byte(i))
			}
		}
	}
	if len(d) != 2+11*4 || d[0] != 3 || d[1] != 3 {
		return laneTag{}, false
	}
	var b [11]byte
	for i := range b {
		b[i] = d[2+4*i]<<6 | d[3+4*i]<<4 | d[4+4*i]<<2 | d[5+4*i]
	}
	role := "client"
	if b[0]&0x80 != 0 {
		role = "exit"
	}
	var st uint64
	for i := 0; i < 6; i++ {
		st = st<<8 | uint64(b[5+i])
	}
	return laneTag{role: role, group: fmt.Sprintf("%08x", binary.BigEndian.Uint32(b[1:5])),
		start: int64(st), index: int(b[0] & 0x7f)}, st > 0
}

// parseLaneTag reads a lane tag; ok is false for any other description.
func parseLaneTag(s string) (laneTag, bool) {
	f := strings.Split(s, "-")
	if len(f) != 3 || len(f[0]) != 9 {
		return laneTag{}, false
	}
	var role string
	switch f[0][0] {
	case 'e':
		role = "exit"
	case 'c':
		role = "client"
	default:
		return laneTag{}, false
	}
	group := f[0][1:]
	if _, err := strconv.ParseUint(group, 16, 32); err != nil {
		return laneTag{}, false
	}
	start, err1 := strconv.ParseInt(f[1], 36, 64)
	index, err2 := strconv.Atoi(f[2])
	if err1 != nil || err2 != nil || start <= 0 || index < 0 {
		return laneTag{}, false
	}
	return laneTag{role: role, group: group, start: start, index: index}, true
}

func roleName(exit bool) string {
	if exit {
		return "exit"
	}
	return "client"
}

// Lanes returns the lanes created so far.
func (g *TelemostGroup) Lanes() []transport.Transport {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]transport.Transport, len(g.lanes))
	for i, l := range g.lanes {
		out[i] = l
	}
	return out
}

func (g *TelemostGroup) newLane() *TelemostTransport {
	t := NewTelemostTransport(g.cookies, g.url, g.isExit, g.config)
	if os.Getenv("TELEMOST_PUBLISH_KIND") == "" {
		t.publishKind = kindCamera // a room has one screen share; lanes are many
	}
	g.mu.Lock()
	t.lane = &stripeLane{group: g, index: len(g.lanes), parts: map[string]tmPart{}, lastReq: -1}
	tag := laneTag{role: roleName(g.isExit), group: g.id, start: g.start, index: len(g.lanes)}
	t.laneName = participantName + tag.hidden()
	g.lanes = append(g.lanes, t)
	g.mu.Unlock()
	return t
}

// NewLane implements transport.LaneSource.
func (g *TelemostGroup) NewLane() transport.Transport {
	t := g.newLane()
	g.growing.Store(false)
	return t
}

// SetGrowHook implements transport.LaneSource.
func (g *TelemostGroup) SetGrowHook(f func()) {
	g.mu.Lock()
	g.grow = f
	g.mu.Unlock()
}

type tmPart struct {
	streaming bool      // publishes camera or screen
	gone      bool      // disconnectedAt set or removed
	since     time.Time // first seen streaming
	tag       laneTag   // the lane tag in its description, if tagged
	tagged    bool
}

// peerSettle is how long a peer must stream before we add a lane for it, so
// ghosts of killed participants (no disconnectedAt yet) do not make us grow.
const peerSettle = 15 * time.Second

// stripeLane is the per-lane state of a TelemostTransport running in a group.
type stripeLane struct {
	group *TelemostGroup
	index int

	mu       sync.Mutex
	parts    map[string]tmPart
	target   string
	targetAt time.Time // when target was set
	view     *slotsView
	shutdown bool // shutdownAllVideo last requested
	lastReq  int
	lastAt   time.Time
}

type slotsView struct {
	offset          int
	prev, cur, next []string
}

// recompute hands each lane a distinct peer to watch: every streaming
// participant of the room that is not one of our own lanes, in a stable order.
func (g *TelemostGroup) recompute() {
	g.mu.Lock()
	own := map[string]bool{}
	for _, l := range g.lanes {
		if l.peerID != "" {
			own[l.peerID] = true
		}
	}
	seen := map[string]tmPart{}
	for _, l := range g.lanes {
		l.lane.mu.Lock()
		for id, p := range l.lane.parts {
			q := seen[id]
			q.streaming = q.streaming || p.streaming
			q.gone = q.gone || p.gone
			if p.tagged {
				q.tag, q.tagged = p.tag, true
			}
			if !p.since.IsZero() && (q.since.IsZero() || p.since.Before(q.since)) {
				q.since = p.since
			}
			seen[id] = q
		}
		l.lane.mu.Unlock()
	}
	for id, at := range g.silent {
		if time.Since(at) > silentFor {
			delete(g.silent, id)
		}
	}
	// The newest group of the other role, when the peers tag their lanes.
	var best laneTag
	tagged := false
	for id, p := range seen {
		if !p.streaming || p.gone || own[id] || !p.tagged || p.tag.role == roleName(g.isExit) {
			continue
		}
		if !tagged || p.tag.start > best.start || (p.tag.start == best.start && p.tag.group > best.group) {
			best, tagged = p.tag, true
		}
	}
	var peers, quiet []string
	settled := 0
	for id, p := range seen {
		if !p.streaming || p.gone || own[id] {
			continue
		}
		if tagged && (!p.tagged || p.tag.group != best.group) {
			continue // a lane of an earlier run, someone else, or our own role
		}
		if _, silent := g.silent[id]; silent && !tagged {
			quiet = append(quiet, id)
			continue
		}
		peers = append(peers, id)
		if time.Since(p.since) >= peerSettle || tagged {
			settled++
		}
	}
	if tagged {
		// Lane i of ours to lane i of theirs where it can: a stable order.
		sort.Slice(peers, func(i, j int) bool { return seen[peers[i]].tag.index < seen[peers[j]].tag.index })
	} else {
		sort.Strings(peers)
	}
	sort.Strings(quiet)
	peers = append(peers, quiet...) // the silent ones only if nothing else is left
	lanes := append([]*TelemostTransport(nil), g.lanes...)
	grow := g.grow
	g.mu.Unlock()

	// More peer lanes than ours: one of them is unwatched, so its frames
	// would be lost. Ask the stripe for another lane (one at a time).
	if settled > len(lanes) && grow != nil && g.growing.CompareAndSwap(false, true) {
		grow()
	}

	// Stable assignment: a lane keeps its peer while that peer is alive (a
	// re-steer costs that lane a resubscription), and only free lanes pick
	// up peers nobody watches yet.
	alive := map[string]bool{}
	for _, p := range peers[:len(peers)-len(quiet)] {
		alive[p] = true
	}
	taken := map[string]bool{}
	targets := make([]string, len(lanes))
	for i, l := range lanes {
		l.lane.mu.Lock()
		cur := l.lane.target
		l.lane.mu.Unlock()
		if alive[cur] && !taken[cur] {
			targets[i] = cur
			taken[cur] = true
		}
	}
	free := peers[:0:0]
	for _, p := range peers {
		if !taken[p] {
			free = append(free, p)
		}
	}
	for i := range lanes {
		if targets[i] == "" && len(free) > 0 {
			targets[i], free = free[0], free[1:]
		}
	}
	for i, l := range lanes {
		l.setStripeTarget(targets[i])
	}
	// A lane that has watched its peer for a while and got nothing: the peer
	// is most likely the ghost of a participant that left without a word
	// (the SFU keeps those listed for a minute). Put it aside and let the
	// lane take another one.
	var silent []string
	for _, l := range lanes {
		l.lane.mu.Lock()
		if !tagged && l.lane.target != "" && !l.receiving() && time.Since(l.lane.targetAt) > silentAfter {
			silent = append(silent, l.lane.target)
		}
		l.lane.mu.Unlock()
	}
	marked := false
	g.mu.Lock()
	for _, id := range silent {
		if _, ok := g.silent[id]; ok {
			continue
		}
		if g.silent == nil {
			g.silent = map[string]time.Time{}
		}
		utils.Infof("[Telemost] nothing arrives from %q: watching another peer", short(id))
		g.silent[id], marked = time.Now(), true
	}
	g.mu.Unlock()
	if marked {
		g.recompute()
	}
}

// silentAfter is how long a lane may watch a peer and receive nothing before
// the peer is put aside for silentFor.
const (
	silentAfter = 6 * time.Second
	silentFor   = 90 * time.Second
)

func (t *TelemostTransport) setStripeTarget(target string) {
	ln := t.lane
	ln.mu.Lock()
	changed := ln.target != target
	ln.target = target
	if changed {
		ln.targetAt = time.Now()
	}
	needSlots := (target == "") != ln.shutdown
	ln.mu.Unlock()
	if !changed {
		return
	}
	utils.Infof("[Telemost] lane %d -> watching %q", ln.index, short(target))
	if needSlots {
		_ = t.sendSetSlots()
	}
	t.steerSlots()
}

func shortAll(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = short(id)
	}
	return out
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// onDescriptions records participants from update/upsert/removeDescription.
func (t *TelemostTransport) onDescriptions(v interface{}, removed bool) {
	ln := t.lane
	if ln == nil {
		return
	}
	var list []interface{}
	switch x := v.(type) {
	case map[string]interface{}:
		if d, ok := x["description"].([]interface{}); ok {
			list = d
		} else {
			list = []interface{}{x}
		}
	case []interface{}:
		list = x
	}
	ln.mu.Lock()
	for _, e := range list {
		var id string
		var m map[string]interface{}
		switch y := e.(type) {
		case string:
			id = y
		case map[string]interface{}:
			m = y
			id, _ = y["id"].(string)
			if id == "" {
				id, _ = y["participantId"].(string)
			}
		}
		if id == "" {
			continue
		}
		p := ln.parts[id]
		if removed {
			p.gone = true
		} else if m != nil {
			sv, _ := m["sendVideo"].(bool)
			ss, _ := m["sendSharing"].(bool)
			p.streaming = sv || ss
			if meta, ok := m["meta"].(map[string]interface{}); ok {
				if n, ok := meta["name"].(string); ok {
					p.tag, p.tagged = parseHiddenTag(n)
				}
				if d, ok := meta["description"].(string); ok && !p.tagged {
					p.tag, p.tagged = parseLaneTag(d)
				}
			}
			_, dis := m["disconnectedAt"]
			p.gone = dis
			if p.streaming && p.since.IsZero() {
				p.since = time.Now()
			}
		}
		ln.parts[id] = p
	}
	ln.mu.Unlock()
	ln.group.recompute()
}

// onSlotsConfig remembers the current layout page and steers toward the target.
func (t *TelemostTransport) onSlotsConfig(sc interface{}) {
	ln := t.lane
	if ln == nil {
		return
	}
	b, _ := json.Marshal(sc)
	var raw struct {
		Offset    int                      `json:"offset"`
		Slots     []map[string]interface{} `json:"slots"`
		PrevSlots []map[string]interface{} `json:"prevSlots"`
		NextSlots []map[string]interface{} `json:"nextSlots"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return
	}
	ids := func(es []map[string]interface{}) []string {
		out := make([]string, len(es))
		for i, e := range es {
			for _, k := range []string{"participantVideoByMid", "participantScreenSharingByMid", "participant"} {
				if p, ok := e[k].(map[string]interface{}); ok {
					out[i], _ = p["participantId"].(string)
					break
				}
			}
		}
		return out
	}
	ln.mu.Lock()
	ln.view = &slotsView{offset: raw.Offset, prev: ids(raw.PrevSlots), cur: ids(raw.Slots), next: ids(raw.NextSlots)}
	utils.Debugf("[Telemost] lane %d slots: offset=%d cur=%v prev=%d next=%d target=%q", ln.index, raw.Offset,
		shortAll(ln.view.cur), len(ln.view.prev), len(ln.view.next), short(ln.target))
	ln.mu.Unlock()
	t.steerSlots()
}

// steerSlots pages the single slot onto the target participant.
func (t *TelemostTransport) steerSlots() {
	ln := t.lane
	ln.mu.Lock()
	v, target := ln.view, ln.target
	if v == nil || target == "" {
		ln.mu.Unlock()
		return
	}
	want := -1
	for i, id := range v.cur {
		if id == target {
			want = v.offset + i
		}
	}
	if want == v.offset {
		ln.mu.Unlock()
		return // already on it
	}
	if want < 0 {
		prevStart := v.offset - len(v.prev)
		if prevStart < 0 {
			prevStart = 0
		}
		for i, id := range v.prev {
			if id == target {
				want = prevStart + i
			}
		}
		for i, id := range v.next {
			if id == target {
				want = v.offset + len(v.cur) + i
			}
		}
	}
	if want < 0 {
		// Not on this page: scan forward, wrap to the start at the end.
		if len(v.next) > 0 {
			want = v.offset + len(v.cur) + len(v.next)
		} else {
			want = 0
		}
	}
	if want == ln.lastReq && time.Since(ln.lastAt) < 700*time.Millisecond {
		ln.mu.Unlock()
		return
	}
	ln.lastReq, ln.lastAt = want, time.Now()
	ln.mu.Unlock()
	_ = t.sendSetSlotsOffset(want)
}

// SetFrameSource implements transport.FramePuller: every tick of the publish
// clock takes up to max frames from src, after anything queued by Send.
func (t *TelemostTransport) SetFrameSource(src func(max int) [][]byte) { t.frameSource.Store(&src) }

// UplinkLoss is the fraction of our published packets the SFU reported lost
// in its latest Receiver Report.
func (t *TelemostTransport) UplinkLoss() float64 { return float64(t.uplinkLoss.Load()) / 256 }

// LaneID is this lane's participant id in the room.
func (t *TelemostTransport) LaneID() string { return t.peerID }

// Watching lists the peer lanes our lanes are currently subscribed to.
func (g *TelemostGroup) Watching() []string {
	g.mu.Lock()
	lanes := append([]*TelemostTransport(nil), g.lanes...)
	g.mu.Unlock()
	var out []string
	for _, l := range lanes {
		l.lane.mu.Lock()
		// Only a peer whose video really arrives: a subscription the SFU
		// does not serve would make the peer send into nowhere.
		if l.lane.target != "" && l.receiving() {
			out = append(out, l.lane.target)
		}
		l.lane.mu.Unlock()
	}
	return out
}

// receiving reports whether the lane's subscription delivered anything lately
// (a watched peer sends filler many times a second even when idle).
func (t *TelemostTransport) receiving() bool {
	return time.Since(time.Unix(0, t.videoAt.Load())) < 3*time.Second
}

// laneIndex is the lane's number in its group, -1 outside a group.
func (t *TelemostTransport) laneIndex() int {
	if t.lane == nil {
		return -1
	}
	return t.lane.index
}
