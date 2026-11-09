package judge

import (
	"net/netip"
	"time"
)

// Capacities and timing are the autolist constants. NameMax includes the
// C terminator; consequently a DNS hint may contain at most NameMax-1 bytes.
const (
	FlowCapacity   = 1024
	HostCapacity   = 512
	DomainCapacity = 512
	HintCapacity   = 4096

	FlowTTL = 300 * time.Second
	HintTTL = 600 * time.Second
	NameMax = 254

	DefaultFailThreshold    uint32 = 3
	DefaultFailWindow              = 60 * time.Second
	DefaultRetransThreshold uint32 = 3
	SYNRetransDelay                = 2 * time.Second

	StallMinBytes       uint32 = 8192
	StallMaxBytes       uint32 = 32768
	StallMinAge                = 10 * time.Second
	StallNotesThreshold        = 2
	StallNoteWindow            = 120 * time.Second

	AddressCapacity       = 64 // capacity of each of the failed and bypass quotas
	AddressClientCapacity = 8
	ProtocolTCP           = 6
	AddressTTL            = 24 * time.Hour
	DayHours              = 24
	GroupMax              = 8
)

const (
	badAddressCapacity = 8
	domainHostCapacity = 8
	addressRows        = 2 * AddressCapacity
)

// Flow identifies the client and server TCP endpoints. Protocol is implicit:
// Hello, Answered, Reset, Close and SYN operate on TCP flows.
type Flow struct {
	Client     netip.Addr
	Server     netip.Addr
	ClientPort uint16
	ServerPort uint16
}

// Params controls the configurable autolist thresholds. Zero values select the
// corresponding defaults. Durations have one-second resolution, as in autolist.
type Params struct {
	FailThreshold       uint32
	FailWindow          time.Duration
	RetransThreshold    uint32
	StallNotesThreshold uint32
}

// DefaultParams returns the autolist default configuration.
func DefaultParams() Params {
	return Params{
		FailThreshold:       DefaultFailThreshold,
		FailWindow:          DefaultFailWindow,
		RetransThreshold:    DefaultRetransThreshold,
		StallNotesThreshold: StallNotesThreshold,
	}
}

func normalizeParams(p Params) Params {
	d := DefaultParams()
	if p.FailThreshold == 0 {
		p.FailThreshold = d.FailThreshold
	}
	if p.FailWindow <= 0 {
		p.FailWindow = d.FailWindow
	}
	if p.RetransThreshold == 0 {
		p.RetransThreshold = d.RetransThreshold
	}
	if p.StallNotesThreshold == 0 {
		p.StallNotesThreshold = d.StallNotesThreshold
	}
	return p
}

// Why is the transport observation responsible for a failure.
type Why uint8

const (
	WhyNone Why = iota
	WhyRetrans
	WhyReset
	WhyNoConnect
)

// Outcome identifies the quota and interpretation of an address row.
type Outcome uint8

const (
	OutcomeFailed Outcome = iota
	OutcomeBypass
)

// How records how a bypassed connection acquired its name.
type How uint8

const (
	HowNone How = iota
	HowAnswer
	HowSNI
	HowPTR
)

// NominationKind identifies an aggregate ready for an active probe.
type NominationKind uint8

const (
	NominationNone NominationKind = iota
	NominationStall
	NominationNoConnect
)

// Failure is emitted once for a judged HELLO retransmission or TCP reset.
// Listed means the host or domain reached FailThreshold. Host is the exact
// host, the representative host of an aggregate, or the original SNI for a
// bypass. Domain is nonempty only when the aggregate reached the threshold.
type Failure struct {
	Host   string
	Domain string
	Client netip.Addr
	Server netip.Addr
	Fails  uint32
	Why    Why
	Listed bool
	Bypass bool
}

// Nomination is emitted when stall notes or unanswered SYNs reach a host or
// domain threshold. Received is the wrapped TCP byte count for a stall.
type Nomination struct {
	Host     string
	Domain   string
	Client   netip.Addr
	Server   netip.Addr
	Received uint32
	Kind     NominationKind
	Bypass   bool
}

// Address is a snapshot of one failed or bypass address observation.
type Address struct {
	Server  netip.Addr
	Proto   uint8
	Port    uint16
	Name    string
	Why     Why
	Outcome Outcome
	How     How
	Group   string
	Fails   uint32
	FirstAt time.Time
	LastAt  time.Time
	Clients []netip.Addr
}

type addressRow struct {
	server  netip.Addr
	proto   uint8
	port    uint16
	name    string
	why     Why
	outcome Outcome
	how     How
	group   string
	fails   uint32
	firstAt int64
	lastAt  int64
	clients [AddressClientCapacity]netip.Addr
	nClient uint8
}

func (r *addressRow) snapshot() Address {
	out := Address{
		Server:  r.server,
		Proto:   r.proto,
		Port:    r.port,
		Name:    r.name,
		Why:     r.why,
		Outcome: r.outcome,
		How:     r.how,
		Group:   r.group,
		Fails:   r.fails,
		FirstAt: time.Unix(r.firstAt, 0),
		LastAt:  time.Unix(r.lastAt, 0),
		Clients: make([]netip.Addr, int(r.nClient)),
	}
	copy(out.Clients, r.clients[:r.nClient])
	return out
}
