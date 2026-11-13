package capture

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"time"

	domainpolicy "github.com/hoaxisr/awg-manager/awgmautolist/internal/domain"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

const (
	suspectDelay = 2 * time.Second
	bodyLimit    = 30 * 1024
	maxFlows     = 2048
	maxDNSIPs    = 1024
)

// Suspect is the only passive evidence emitted by capture.
type Suspect struct {
	Target    model.Target `json:"target"`
	Reason    string       `json:"reason"`
	Protocol  string       `json:"protocol,omitempty"`
	ClientIP  string       `json:"clientIP,omitempty"`
	ClientMAC string       `json:"clientMAC,omitempty"`
	FirstAt   time.Time    `json:"firstAt"`
	LastAt    time.Time    `json:"lastAt"`
}

type flowKey struct {
	network    uint8
	client     netip.Addr
	server     netip.Addr
	clientPort uint16
	serverPort uint16
}

type flow struct {
	target          model.Target
	created         time.Time
	last            time.Time
	synAt           time.Time
	helloAt         time.Time
	lastClientData  time.Time
	lastServerData  time.Time
	clientEnd       uint32
	serverEnd       uint32
	clientData      bool
	serverData      bool
	clientDatagrams int
	serverBytes     int
	serverResponded bool
	sawSYN          bool
	sawHello        bool
	stun            bool
	nameFromSNI     bool
	finished        bool
	submitted       bool
	clientMAC       string
	clientIP        netip.Addr
}

type dnsHintKey struct {
	client netip.Addr
	server netip.Addr
}

type dnsHint struct {
	names map[string]time.Time
}

// FlowTable owns all mutable passive state. Reset is used on every WAN pause,
// and a resumed capture receives a fresh table with no old queued evidence.
type FlowTable struct {
	mu     sync.Mutex
	flows  map[flowKey]*flow
	dns    map[dnsHintKey]dnsHint
	udpAll bool
	drops  uint64
}

type FlowStats struct {
	Flows   int    `json:"flows"`
	DNSIPs  int    `json:"dnsIPs"`
	Dropped uint64 `json:"dropped"`
	UDPAll  bool   `json:"udpAll"`
}

func NewFlowTable(udpAll bool) *FlowTable {
	return &FlowTable{flows: map[flowKey]*flow{}, dns: map[dnsHintKey]dnsHint{}, udpAll: udpAll}
}

func (t *FlowTable) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.flows = map[flowKey]*flow{}
	t.dns = map[dnsHintKey]dnsHint{}
	t.drops = 0
}

func (t *FlowTable) Stats() FlowStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return FlowStats{Flows: len(t.flows), DNSIPs: len(t.dns), Dropped: t.drops, UDPAll: t.udpAll}
}

// Handle processes one frame. A server RST is emitted immediately; timer-based
// reasons are returned by Scan. The returned pointer is independent of the
// mutable flow and safe for a caller to enqueue after Handle returns.
func (t *FlowTable) Handle(raw []byte, now time.Time) *Suspect {
	frame, ok := ParseFrame(raw)
	if !ok {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rememberDNS(frame, now)
	switch frame.Network {
	case networkUDP:
		if !t.udpAll || frame.SrcPort == 53 || frame.DstPort == 53 {
			return nil
		}
		t.handleUDP(frame, now)
	case networkTCP:
		if frame.SrcPort != 443 && frame.DstPort != 443 {
			return nil
		}
		return t.handleTCP(frame, now)
	}
	return nil
}

func (t *FlowTable) handleUDP(frame Frame, now time.Time) {
	srcPublic := publicAddress(frame.Src)
	dstPublic := publicAddress(frame.Dst)
	if srcPublic == dstPublic {
		return
	}
	fromClient := dstPublic
	key := flowKey{network: networkUDP}
	if fromClient {
		key.client, key.server = frame.Src, frame.Dst
		key.clientPort, key.serverPort = frame.SrcPort, frame.DstPort
	} else {
		key.client, key.server = frame.Dst, frame.Src
		key.clientPort, key.serverPort = frame.DstPort, frame.SrcPort
	}
	fl := t.flowFor(key, model.NetworkUDP, now)
	if fl == nil {
		return
	}
	if fromClient && isSTUNMessage(frame.Payload) {
		fl.stun = true
	}
	if len(frame.Payload) == 0 {
		return
	}
	if fromClient {
		fl.clientDatagrams++
		fl.lastClientData = now
		fl.clientMAC = frame.SourceMAC
	} else {
		fl.serverResponded = true
		fl.lastServerData = now
	}
}

func (t *FlowTable) handleTCP(frame Frame, now time.Time) *Suspect {
	srcPublic := publicAddress(frame.Src)
	dstPublic := publicAddress(frame.Dst)
	var fromClient bool
	switch {
	case frame.DstPort == 443 && dstPublic && !srcPublic:
		fromClient = true
	case frame.SrcPort == 443 && srcPublic && !dstPublic:
		fromClient = false
	default:
		return nil
	}
	key := flowKey{network: networkTCP, serverPort: 443}
	if fromClient {
		key.client, key.server = frame.Src, frame.Dst
		key.clientPort = frame.SrcPort
	} else {
		key.client, key.server = frame.Dst, frame.Src
		key.clientPort = frame.DstPort
	}
	fl := t.flowFor(key, model.NetworkTCP, now)
	if fl == nil {
		return nil
	}
	if fromClient {
		fl.clientMAC = frame.SourceMAC
		if frame.Flags&tcpSYN != 0 {
			fl.sawSYN = true
			if fl.synAt.IsZero() {
				fl.synAt = now
			}
		}
		newerSequence(&fl.clientEnd, &fl.clientData, frame.Sequence, len(frame.Payload))
		if len(frame.Payload) >= 5 && frame.Payload[0] == 0x16 {
			fl.sawHello = true
			fl.helloAt = now
		}
		if name := tlsServerName(frame.Payload); name != "" {
			fl.setName(name, true)
		}
	} else {
		if frame.Flags&(tcpSYN|tcpACK) == tcpSYN|tcpACK || len(frame.Payload) > 0 || frame.Flags&tcpRST != 0 {
			fl.serverResponded = true
		}
		fresh := newerSequence(&fl.serverEnd, &fl.serverData, frame.Sequence, len(frame.Payload))
		if fresh > 0 {
			fl.serverBytes += fresh
			fl.lastServerData = now
		}
		if frame.Flags&tcpRST != 0 && !fl.submitted {
			return t.submit(fl, "tcp-rst", now)
		}
	}
	if frame.Flags&tcpFIN != 0 {
		fl.finished = true
	}
	return nil
}

func (t *FlowTable) flowFor(key flowKey, network model.Network, now time.Time) *flow {
	fl := t.flows[key]
	if fl == nil {
		if len(t.flows) >= maxFlows {
			t.drops++
			return nil
		}
		target, err := model.NewTarget("", key.server.String(), network, key.serverPort)
		if err != nil {
			t.drops++
			return nil
		}
		fl = &flow{target: target, created: now, last: now, clientIP: key.client}
		t.flows[key] = fl
	}
	fl.last = now
	if !fl.nameFromSNI && fl.target.Domain == "" {
		if name := t.nameForIP(key.client, key.server, now); name != "" {
			fl.setName(name, false)
		}
	}
	return fl
}

func (f *flow) setName(name string, sni bool) {
	target, err := model.NewObservedTarget(name, f.target.IP, f.target.Network, f.target.Port)
	if err != nil {
		return
	}
	f.target = target
	f.nameFromSNI = sni
}

func (t *FlowTable) rememberDNS(frame Frame, now time.Time) {
	if frame.Network != networkUDP || frame.SrcPort != 53 {
		return
	}
	for _, mapping := range parseDNSAnswers(frame.Payload, now) {
		if !publicAddress(mapping.ip) {
			continue
		}
		// A DNS answer names an address only for the client that asked. Other
		// LAN clients may associate a shared address with a different domain.
		key := dnsHintKey{client: frame.Dst, server: mapping.ip}
		hint, ok := t.dns[key]
		if !ok {
			if len(t.dns) >= maxDNSIPs {
				t.drops++
				continue
			}
			hint = dnsHint{names: map[string]time.Time{}}
			t.dns[key] = hint
		}
		if current, ok := hint.names[mapping.name]; !ok || mapping.expires.After(current) {
			hint.names[mapping.name] = mapping.expires
		}
	}
}

func (t *FlowTable) nameForIP(client, ip netip.Addr, now time.Time) string {
	key := dnsHintKey{client: client, server: ip}
	hint, ok := t.dns[key]
	if !ok {
		return ""
	}
	zone := ""
	name := ""
	for candidate, expires := range hint.names {
		if !now.Before(expires) {
			delete(hint.names, candidate)
			continue
		}
		candidateZone := domainpolicy.Zone(candidate)
		if zone != "" && zone != candidateZone {
			return "" // different registered zones never prove one broad rule
		}
		zone = candidateZone
		// The exact host is only probe metadata. Prefer the apex when both it
		// and a subdomain arrived so map iteration cannot affect the result.
		if name == "" || candidate == zone {
			name = candidate
		}
	}
	if len(hint.names) == 0 {
		delete(t.dns, key)
	}
	return name
}

func (t *FlowTable) submit(fl *flow, reason string, now time.Time) *Suspect {
	fl.submitted = true
	protocol := ""
	if fl.sawHello {
		protocol = "tls"
	} else if fl.stun {
		protocol = "stun"
	}
	return &Suspect{
		Target:    fl.target,
		Reason:    reason,
		Protocol:  protocol,
		ClientIP:  fl.clientIP.String(),
		ClientMAC: fl.clientMAC,
		FirstAt:   fl.created,
		LastAt:    now,
	}
}

func (t *FlowTable) Scan(now time.Time) []Suspect {
	t.mu.Lock()
	var suspects []Suspect
	for key, fl := range t.flows {
		if now.Sub(fl.last) > 2*time.Minute || (fl.finished && now.Sub(fl.last) > 5*time.Second) {
			delete(t.flows, key)
			continue
		}
		if fl.submitted || fl.finished {
			continue
		}
		// A late DNS answer may arrive after the flow's final packet. Re-check
		// its client-scoped hint before freezing the target identity.
		if !fl.nameFromSNI && fl.target.Domain == "" {
			if name := t.nameForIP(key.client, key.server, now); name != "" {
				fl.setName(name, false)
			}
		}
		if fl.target.Network == model.NetworkUDP {
			if fl.clientDatagrams == 0 || fl.serverResponded || now.Sub(fl.lastClientData) < suspectDelay {
				continue
			}
			if !fl.stun {
				fl.submitted = true
				continue
			}
			suspect := t.submit(fl, "udp-no-response", now)
			suspects = append(suspects, *suspect)
			continue
		}
		if fl.serverBytes > bodyLimit {
			continue
		}
		reason := ""
		switch {
		case fl.serverBytes > 0 && now.Sub(fl.lastServerData) >= suspectDelay:
			reason = "response-stall"
		case fl.sawHello && fl.serverBytes == 0 && now.Sub(fl.helloAt) >= suspectDelay:
			reason = "tls-no-response"
		case fl.sawSYN && !fl.serverResponded && now.Sub(fl.synAt) >= suspectDelay:
			reason = "connect-no-response"
		}
		if reason != "" {
			suspect := t.submit(fl, reason, now)
			suspects = append(suspects, *suspect)
		}
	}
	for key, hint := range t.dns {
		for name, expires := range hint.names {
			if !now.Before(expires) {
				delete(hint.names, name)
			}
		}
		if len(hint.names) == 0 {
			delete(t.dns, key)
		}
	}
	t.mu.Unlock()
	return suspects
}

func newerSequence(current *uint32, initialized *bool, sequence uint32, payloadLen int) int {
	if payloadLen == 0 {
		return 0
	}
	end := sequence + uint32(payloadLen)
	if !*initialized {
		*current = end
		*initialized = true
		return payloadLen
	}
	fresh := int(int32(end - *current))
	if fresh <= 0 {
		return 0
	}
	*current = end
	return fresh
}

func isSTUNMessage(payload []byte) bool {
	return len(payload) >= 20 && payload[0]&0xc0 == 0 &&
		binary.BigEndian.Uint32(payload[4:8]) == 0x2112a442 &&
		int(binary.BigEndian.Uint16(payload[2:4]))+20 == len(payload)
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),  // protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),  // documentation
	netip.MustParsePrefix("198.18.0.0/15"), // benchmark / sing-box fakeip
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func publicAddress(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
