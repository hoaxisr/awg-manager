// Package conntrackfeed discovers NEW five-tuples from conntrack snapshots and
// retains bounded byte/state observations. AF_PACKET is deliberately not used
// to copy all traffic for flow discovery.
package conntrackfeed

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
)

const (
	DefaultPath      = "/proc/net/nf_conntrack"
	DefaultCapacity  = 4096
	DefaultTTL       = 5 * time.Minute
	pruneInterval    = time.Second
	maxSnapshotBytes = 32 * 1024 * 1024
)

// Entry is one original-direction conntrack flow. Byte counters are cumulative
// snapshot maxima; packet observations fill gaps between proc snapshots.
type Entry struct {
	Flow            packet.Flow
	State           string
	OriginalBytes   uint64
	ReplyBytes      uint64
	OriginalPackets uint64
	ReplyPackets    uint64
	Assured         bool
	Unreplied       bool
	FirstAt         time.Time
	LastAt          time.Time
}

// Source reads one complete conntrack snapshot.
type Source interface {
	Snapshot(context.Context) ([]Entry, error)
}

// SourceFunc adapts a callback to Source.
type SourceFunc func(context.Context) ([]Entry, error)

func (f SourceFunc) Snapshot(ctx context.Context) ([]Entry, error) { return f(ctx) }

// ProcSource reads /proc directly; it never executes conntrack or firewall
// commands and therefore cannot contend for the xtables lock.
type ProcSource struct{ Path string }

func (s ProcSource) Snapshot(ctx context.Context) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := s.Path
	if path == "" {
		path = DefaultPath
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSnapshotBytes {
		return nil, errors.New("conntrackfeed: snapshot exceeds 32 MiB")
	}
	return Parse(data)
}

// Parse parses both IPv4 and IPv6 nf_conntrack text. The first direction is the
// original client-to-server tuple; reply tuple/NAT fields never replace it.
func Parse(data []byte) ([]Entry, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var out []Entry
	for line := 1; scanner.Scan(); line++ {
		entry, ok, err := ParseLine(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("conntrackfeed: line %d: %w", line, err)
		}
		if ok {
			out = append(out, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ParseLine parses one entry. Empty and unsupported-protocol lines return
// ok=false; malformed TCP/UDP endpoint fields return an error instead of a
// guessed flow.
func ParseLine(line string) (entry Entry, ok bool, err error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return Entry{}, false, nil
	}
	var protocol uint8
	for _, field := range fields[:min(len(fields), 6)] {
		switch field {
		case "tcp":
			protocol = packet.ProtocolTCP
		case "udp":
			protocol = packet.ProtocolUDP
		}
	}
	if protocol == 0 {
		return Entry{}, false, nil
	}
	firstSrc := -1
	for i, field := range fields {
		if strings.HasPrefix(field, "src=") {
			firstSrc = i
			break
		}
	}
	if firstSrc < 0 {
		return Entry{}, false, errors.New("missing original src")
	}
	if protocol == packet.ProtocolTCP && firstSrc > 5 {
		entry.State = fields[5]
	}
	entry.Assured = strings.Contains(line, "[ASSURED]")
	entry.Unreplied = strings.Contains(line, "UNREPLIED")

	direction := 0
	seenPorts := [2]bool{}
	for _, field := range fields[firstSrc:] {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		switch key {
		case "src":
			if direction == 0 && entry.Flow.Src.IsValid() {
				direction = 1 // second src begins the reply tuple
			}
			if direction > 1 {
				continue
			}
			addr, parseErr := netip.ParseAddr(value)
			if parseErr != nil {
				return Entry{}, false, fmt.Errorf("src: %w", parseErr)
			}
			if direction == 0 {
				entry.Flow.Src = addr
			} else {
				_ = addr // reply src must not overwrite original destination
			}
		case "dst":
			if direction > 1 {
				continue
			}
			addr, parseErr := netip.ParseAddr(value)
			if parseErr != nil {
				return Entry{}, false, fmt.Errorf("dst: %w", parseErr)
			}
			if direction == 0 {
				entry.Flow.Dst = addr
			} else {
				_ = addr
			}
		case "sport", "dport":
			if direction > 1 {
				continue
			}
			port, parseErr := strconv.ParseUint(value, 10, 16)
			if parseErr != nil || port == 0 {
				return Entry{}, false, fmt.Errorf("%s: invalid port %q", key, value)
			}
			if direction == 0 {
				if key == "sport" {
					entry.Flow.SrcPort = uint16(port)
				} else {
					entry.Flow.DstPort = uint16(port)
				}
			}
		case "packets", "bytes":
			if direction > 1 {
				continue
			}
			count, parseErr := strconv.ParseUint(value, 10, 64)
			if parseErr != nil {
				return Entry{}, false, fmt.Errorf("%s: %w", key, parseErr)
			}
			if key == "bytes" {
				if direction == 0 {
					entry.OriginalBytes = count
				} else {
					entry.ReplyBytes = count
				}
			} else if direction == 0 {
				entry.OriginalPackets = count
			} else {
				entry.ReplyPackets = count
			}
		}
		if key == "dport" {
			seenPorts[direction] = true
		}
	}
	entry.Flow.Protocol = protocol
	if !entry.Flow.Src.IsValid() || !entry.Flow.Dst.IsValid() || !seenPorts[0] || entry.Flow.SrcPort == 0 || entry.Flow.DstPort == 0 {
		return Entry{}, false, errors.New("incomplete original five-tuple")
	}
	return entry, true, nil
}

type tracked struct {
	entry     Entry
	announced bool
}

// Tracker owns bounded first-seen, expiry, byte, and state data. It is safe for
// concurrent diagnostics, although packetruntime normally confines it to the
// judge-owner goroutine.
type Tracker struct {
	mu        sync.Mutex
	flows     map[packet.Flow]*tracked
	capacity  int
	ttl       time.Duration
	lastPrune time.Time
}

func NewTracker(capacity int, ttl time.Duration) *Tracker {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Tracker{flows: make(map[packet.Flow]*tracked), capacity: capacity, ttl: ttl}
}

// ApplySnapshot merges cumulative conntrack data and returns only flows whose
// NEW observation has not previously been announced. Disappearance does not
// instantly erase recent byte/state data; expiry permits a later first-seen.
func (t *Tracker) ApplySnapshot(entries []Entry, now time.Time) []Entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(now)
	var fresh []Entry
	for _, entry := range entries {
		if !validFlow(entry.Flow) {
			continue
		}
		entry.Flow = canonicalFlow(entry.Flow)
		row := t.take(entry.Flow, now)
		if row.entry.FirstAt.IsZero() {
			row.entry.FirstAt = now
		}
		entry.FirstAt = row.entry.FirstAt
		entry.LastAt = now
		if entry.OriginalBytes < row.entry.OriginalBytes {
			entry.OriginalBytes = row.entry.OriginalBytes
		}
		if entry.ReplyBytes < row.entry.ReplyBytes {
			entry.ReplyBytes = row.entry.ReplyBytes
		}
		if entry.OriginalPackets < row.entry.OriginalPackets {
			entry.OriginalPackets = row.entry.OriginalPackets
		}
		if entry.ReplyPackets < row.entry.ReplyPackets {
			entry.ReplyPackets = row.entry.ReplyPackets
		}
		if entry.State == "" {
			entry.State = row.entry.State
		}
		row.entry = entry
		if !row.announced {
			row.announced = true
			fresh = append(fresh, entry)
		}
	}
	return fresh
}

// ObservePacket merges one AF_PACKET packet into byte/state tracking without
// announcing NEW. The before/after reply counters let the runtime reproduce the
// early-reset and 512-byte answered boundaries between conntrack snapshots.
func (t *Tracker) ObservePacket(p packet.Packet, now time.Time) (before, after Entry) {
	flow := p.Flow()
	reply := false
	if p.Protocol == packet.ProtocolTCP || p.Protocol == packet.ProtocolUDP {
		if p.SrcPort == 443 || p.SrcPort == 53 {
			flow = flow.Reverse()
			reply = true
		}
	}
	if !validFlow(flow) {
		return Entry{}, Entry{}
	}
	flow = canonicalFlow(flow)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(now)
	row := t.take(flow, now)
	before = row.entry
	entry := row.entry
	if entry.FirstAt.IsZero() {
		entry.FirstAt = now
	}
	entry.Flow = flow
	entry.LastAt = now
	count := uint64(len(p.Payload))
	if !reply {
		entry.OriginalBytes += count
	} else {
		entry.ReplyBytes += count
	}
	if p.Protocol == packet.ProtocolTCP {
		switch {
		case p.Flags&packet.TCPRST != 0:
			entry.State = "RESET"
		case p.Flags&packet.TCPFIN != 0:
			entry.State = "FIN"
		case p.Flags&packet.TCPSYN != 0:
			entry.State = "SYN"
		case p.Flags&packet.TCPACK != 0:
			entry.State = "ESTABLISHED"
		}
	}
	row.entry = entry
	return before, entry
}

// Lookup returns current byte/state data for correlation and diagnostics.
func (t *Tracker) Lookup(flow packet.Flow, now time.Time) (Entry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	flow = canonicalFlow(flow)
	row := t.flows[flow]
	if row == nil || expired(row.entry.LastAt, now, t.ttl) {
		if row != nil {
			delete(t.flows, flow)
		}
		return Entry{}, false
	}
	return row.entry, true
}

func (t *Tracker) take(flow packet.Flow, now time.Time) *tracked {
	if row := t.flows[flow]; row != nil {
		return row
	}
	if len(t.flows) >= t.capacity {
		var oldest packet.Flow
		var at time.Time
		first := true
		for key, row := range t.flows {
			if first || row.entry.LastAt.Before(at) {
				oldest, at, first = key, row.entry.LastAt, false
			}
		}
		if !first {
			delete(t.flows, oldest)
		}
	}
	row := &tracked{entry: Entry{Flow: flow, FirstAt: now, LastAt: now}}
	t.flows[flow] = row
	return row
}

func (t *Tracker) prune(now time.Time) {
	// Lookup всё равно проверяет TTL точно, а capacity ограничен. Не сканируем
	// всю таблицу на каждом пакете: это квадратичная нагрузка на роутере.
	if !t.lastPrune.IsZero() && now.Sub(t.lastPrune) < pruneInterval {
		return
	}
	t.lastPrune = now
	for flow, row := range t.flows {
		if expired(row.entry.LastAt, now, t.ttl) {
			delete(t.flows, flow)
		}
	}
}

func expired(at, now time.Time, ttl time.Duration) bool {
	return !now.Before(at) && now.Sub(at) > ttl
}

func validFlow(flow packet.Flow) bool {
	return flow.Src.IsValid() && flow.Dst.IsValid() && flow.SrcPort != 0 && flow.DstPort != 0 &&
		(flow.Protocol == packet.ProtocolTCP || flow.Protocol == packet.ProtocolUDP)
}

func canonicalFlow(flow packet.Flow) packet.Flow {
	flow.Src, flow.Dst = flow.Src.Unmap(), flow.Dst.Unmap()
	return flow
}
