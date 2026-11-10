package judge

import (
	"net/netip"
	"sort"
	"time"
)

const (
	quotaFailed = iota
	quotaBypass
	noAddressRow = -1
)

type hiddenAddress struct {
	server netip.Addr
	proto  uint8
	port   uint16
	until  int64
}

func addressKey(r *addressRow, server netip.Addr, proto uint8, port uint16) bool {
	return r.proto == proto && r.port == port && r.server == server
}

func (j *Judge) addrLive(at int, now int64) bool {
	return j.addrUsed[at] && now-j.addr[at].lastAt < int64(AddressTTL/time.Second)
}

func (j *Judge) addrFind(quota int, server netip.Addr, proto uint8, port uint16) int {
	from := quota * AddressCapacity
	for i := from; i < from+AddressCapacity; i++ {
		if j.addrUsed[i] && addressKey(&j.addr[i], server, proto, port) {
			return i
		}
	}
	return noAddressRow
}

func (j *Judge) addrSpare(quota int, now int64) int {
	from := quota * AddressCapacity
	spare := from
	for i := from; i < from+AddressCapacity; i++ {
		if !j.addrLive(i, now) {
			return i
		}
		if j.addr[i].lastAt < j.addr[spare].lastAt {
			spare = i
		}
	}
	return spare
}

func (j *Judge) addrStart(at int, server netip.Addr, proto uint8, port uint16, now int64) *addressRow {
	j.addr[at] = addressRow{server: server, proto: proto, port: port, firstAt: now}
	j.addrUsed[at] = true
	return &j.addr[at]
}

func (j *Judge) addrTake(quota int, server netip.Addr, proto uint8, port uint16, now int64) (int, bool) {
	at := j.addrFind(quota, server, proto, port)
	fresh := at == noAddressRow || !j.addrLive(at, now)
	if at == noAddressRow {
		at = j.addrSpare(quota, now)
	}
	return at, fresh
}

func (j *Judge) addrNote(r *addressRow, client netip.Addr, name string, now int64) uint32 {
	r.fails++
	r.lastAt = now
	name = clipName(name)
	if name != "" {
		r.name = name
	}
	known := false
	for i := 0; i < int(r.nClient); i++ {
		known = known || r.clients[i] == client
	}
	if !known && r.nClient < AddressClientCapacity {
		r.clients[r.nClient] = client
		r.nClient++
	}
	return r.fails
}

func (j *Judge) bypassCount(f *flowState, why Why, now int64) uint32 {
	server := f.key.Server
	port := f.key.ServerPort
	from := j.addrFind(quotaBypass, server, ProtocolTCP, port)
	at, fresh := j.addrTake(quotaFailed, server, ProtocolTCP, port, now)
	if fresh && from != noAddressRow && j.addrLive(from, now) {
		j.addr[at] = j.addr[from]
		j.addrUsed[at] = true
	} else if fresh {
		j.addrStart(at, server, ProtocolTCP, port, now)
	}
	if from != noAddressRow {
		j.addrUsed[from] = false
	}
	r := &j.addr[at]
	r.outcome = OutcomeFailed
	r.why = why
	return j.addrNote(r, f.key.Client, f.sni, now)
}

// BypassSeen counts a connection that went past autolist. A live failed row with
// the same address/protocol/port is updated in place; otherwise the bypass
// quota is used. Each quota independently retains its newest 64 keys.
func (j *Judge) BypassSeen(client, server netip.Addr, proto uint8, port uint16, name, group string, how How, now time.Time) {
	if j == nil || !client.IsValid() || !server.IsValid() {
		return
	}
	at := seconds(now)
	row := j.addrFind(quotaFailed, server, proto, port)
	if row == noAddressRow || !j.addrLive(row, at) {
		var fresh bool
		row, fresh = j.addrTake(quotaBypass, server, proto, port, at)
		if fresh {
			j.addrStart(row, server, proto, port, at)
		}
	}
	r := &j.addr[row]
	r.outcome = OutcomeBypass
	r.how = how
	r.group = clip(cString(group), GroupMax)
	j.addrNote(r, client, name, at)
}

func (j *Judge) addrHidden(r *addressRow, now int64) bool {
	for i := range j.hidden {
		h := &j.hidden[i]
		if h.until > now && h.proto == r.proto && h.port == r.port && h.server == r.server {
			return true
		}
	}
	return false
}

// Addresses returns visible rows from both quotas, newest LastAt first. Rows
// become stale at 24h. Returned slices and client lists are independent copies.
func (j *Judge) Addresses(now time.Time) []Address {
	if j == nil {
		return nil
	}
	at := seconds(now)
	order := make([]int, 0, addressRows)
	for i := range j.addr {
		if j.addrLive(i, at) && !j.addrHidden(&j.addr[i], at) {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		return j.addr[order[a]].lastAt > j.addr[order[b]].lastAt
	})
	out := make([]Address, len(order))
	for i, row := range order {
		out[i] = j.addr[row].snapshot()
	}
	return out
}

// DismissAddress hides every quota's visible row for the key for 24h. It
// returns false when the key has no visible live row.
func (j *Judge) DismissAddress(server netip.Addr, proto uint8, port uint16, now time.Time) bool {
	if j == nil || !server.IsValid() {
		return false
	}
	at := seconds(now)
	visible := false
	for i := range j.addr {
		if j.addrLive(i, at) && addressKey(&j.addr[i], server, proto, port) && !j.addrHidden(&j.addr[i], at) {
			visible = true
			break
		}
	}
	if !visible {
		return false
	}
	j.hidden[j.hiddenAt] = hiddenAddress{server: server, proto: proto, port: port, until: at + int64(AddressTTL/time.Second)}
	j.hiddenAt = (j.hiddenAt + 1) % addressRows
	return true
}

var localV4 = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("0.0.0.0/8"),
}

func isLocalV4(ip netip.Addr) bool {
	for _, network := range localV4 {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// IsLocal reports addresses that cannot name a public site. Optional pools
// extend the built-in IPv4, IPv4-mapped, ULA, link-local and unspecified ranges.
func IsLocal(ip netip.Addr, pools ...netip.Prefix) bool {
	if !ip.IsValid() {
		return false
	}
	for _, pool := range pools {
		if pool.IsValid() && pool.Contains(ip) {
			return true
		}
	}
	if ip.Is4() {
		return isLocalV4(ip)
	}
	if ip.Is4In6() {
		return isLocalV4(ip.Unmap())
	}
	if !ip.Is6() {
		return false
	}
	if ip.IsUnspecified() || ip.IsLoopback() {
		return true
	}
	return netip.MustParsePrefix("fc00::/7").Contains(ip) || netip.MustParsePrefix("fe80::/10").Contains(ip)
}
