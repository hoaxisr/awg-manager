package avito

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/p1neappleXpress/OpenFlux/utils"
)

// dialGate paces every request this host makes to code.avito.ru: the WebSocket handshakes of all of a transport's
// rooms and the creation of rooms.
//
// Avito rate-limits an address to 10 handshakes in any 60 seconds (measured from a server at 1, 2 and 3 seconds a
// handshake: always the first 10 accepted, the rest answered 429, and 10 more accepted exactly a minute later); a
// process that keeps retrying keeps the limit up for good. So the gate does two things:
//   - it budgets the dials ahead of time (GCRA: a burst of avitoBurst, then one per avitoRefill, which keeps any minute
//     under the limit), so that a node with 16 rooms and several nodes on one address come up in minutes instead of
//     never;
//   - after a 429 anyway (the budget is only an estimate) it holds every room back for a pause that doubles,
//     30 s up to 30 minutes, until a handshake goes through and Avito stays quiet for 2 minutes.
//
// The budget and the pause are kept in a small file shared by all the Avito processes of the host.
type dialGate struct {
	mu      sync.Mutex
	next    time.Time // the earliest the next dial may start
	hold    time.Time // no dial before this, after a 429
	backoff time.Duration
	last429 time.Time // when Avito last refused
	tat     time.Time // GCRA's theoretical arrival time: how far ahead the budget is spent

	// noBudget keeps only the pause after a 429 (room creation has its own, unmeasured, limit); suffix names its file.
	noBudget bool
	suffix   string

	// refill, burst and spacing override the defaults (tests); zero means the default.
	refill  time.Duration
	burst   int
	spacing time.Duration

	// file is where the budget and the pause are shared with the other Avito processes on this host (a server runs
	// a node per channel, all behind one address, all under one limit); "" keeps them to this process.
	file      string
	useEnv    bool // the file is OPENFLUX_AVITO_GATE_FILE or the default one, looked up at each use
	lastAdopt time.Time
}

func (g *dialGate) path() string {
	if g.file != "" {
		return g.file
	}
	if g.useEnv {
		if f := gateFile(); f != "" {
			return f + g.suffix
		}
	}
	return ""
}

// processGate is every Avito request of this process; the file shares it with the others of the host.
var processGate = &dialGate{useEnv: true}

// roomGate is the creation of rooms: not in the handshake budget (sixteen creations in seven seconds have always
// gone through), but a refusal holds it back.
var roomGate = &dialGate{useEnv: true, noBudget: true, suffix: "-rooms"}

// gateFile is the shared file: OPENFLUX_AVITO_GATE_FILE, "off" for none, else a file in the temp directory.
func gateFile() string {
	switch v := os.Getenv("OPENFLUX_AVITO_GATE_FILE"); v {
	case "off":
		return ""
	case "":
		return filepath.Join(os.TempDir(), "openflux-avito-gate")
	default:
		return v
	}
}

const (
	dialSpacing    = 250 * time.Millisecond
	holdFirst      = 30 * time.Second
	holdMax        = 30 * time.Minute
	holdJitterFrac = 0.2
	retryAfterMax  = 10 * time.Minute
	// A good handshake resets the pause only when Avito has not refused for this long: a limit that lets one in now
	// and then is still a limit.
	calmAfter = 2 * time.Minute
	// How often a process looks at the shared file.
	adoptEvery = 500 * time.Millisecond

	// avitoBurst dials at once, then one per avitoRefill: at most 4 + 60/12 = 9 in any minute, one under Avito's 10.
	// OPENFLUX_AVITO_REFILL_MS and OPENFLUX_AVITO_BURST override them; a refill of 0 turns the budget off (the pause
	// after a 429 stays).
	avitoBurst  = 4
	avitoRefill = 12 * time.Second
)

// budget is the refill interval and the burst in force.
func (g *dialGate) budget() (time.Duration, int) {
	if g.noBudget {
		return 0, 0
	}
	refill, burst := avitoRefill, avitoBurst
	if v := os.Getenv("OPENFLUX_AVITO_REFILL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			refill = time.Duration(n) * time.Millisecond
		}
	}
	if v := os.Getenv("OPENFLUX_AVITO_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			burst = n
		}
	}
	if g.refill != 0 {
		refill = g.refill
	}
	if g.burst != 0 {
		burst = g.burst
	}
	return refill, burst
}

func (g *dialGate) dialSpacing() time.Duration {
	if g.spacing != 0 {
		return g.spacing
	}
	return dialSpacing
}

// ParseRetryAfter reads a Retry-After header (seconds, or an HTTP date), clamped to [0, retryAfterMax]; 0 if absent or bad.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.Atoi(v); err == nil {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), retryAfterMax)
}

func newDialGate() *dialGate { return &dialGate{} }

// newSharedGate is a gate that shares its state through file.
func newSharedGate(file string) *dialGate { return &dialGate{file: file} }

// adopt takes the later budget and pause that another process wrote to the shared file. Called with g.mu held.
func (g *dialGate) adopt() {
	path := g.path()
	if path == "" || time.Since(g.lastAdopt) < adoptEvery {
		return
	}
	g.lastAdopt = time.Now()
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var holdMs, backoffMs, lastMs, tatMs int64
	n, _ := fmt.Sscanf(string(raw), "%d %d %d %d", &holdMs, &backoffMs, &lastMs, &tatMs)
	if n < 3 {
		return
	}
	if h := time.UnixMilli(holdMs); h.After(g.hold) {
		g.hold = h
	}
	if b := time.Duration(backoffMs) * time.Millisecond; b > g.backoff {
		g.backoff = b
	}
	if l := time.UnixMilli(lastMs); l.After(g.last429) {
		g.last429 = l
	}
	if n == 4 {
		if t := time.UnixMilli(tatMs); t.After(g.tat) {
			g.tat = t
		}
	}
}

// persist writes the state for the other processes. Called with g.mu held; a failure only costs the sharing.
func (g *dialGate) persist() {
	path := g.path()
	if path == "" {
		return
	}
	tmp := path + ".tmp"
	data := fmt.Sprintf("%d %d %d %d\n", g.hold.UnixMilli(), g.backoff.Milliseconds(), g.last429.UnixMilli(), g.tat.UnixMilli())
	if os.WriteFile(tmp, []byte(data), 0o666) == nil {
		_ = os.Rename(tmp, path)
	}
}

// wait blocks until this dial may start; false when ctx ended first. A pause that begins while a room waits for its
// turn holds that room back too.
func (g *dialGate) wait(ctx context.Context) bool {
	reserved := false
	for {
		g.mu.Lock()
		g.adopt()
		now := time.Now()
		at := now
		if g.next.After(at) {
			at = g.next
		}
		if g.hold.After(at) {
			at = g.hold
		}
		if refill, burst := g.budget(); refill > 0 && !reserved {
			// GCRA: the budget is spent when the dial's turn is reserved, so waiting rooms spread out.
			tat := g.tat
			if tat.Before(now) {
				tat = now
			}
			tau := time.Duration(burst-1) * refill
			if early := tat.Sub(now) - tau; early > 0 && now.Add(early).After(at) {
				at = now.Add(early)
			}
			g.tat = tat.Add(refill)
			g.persist()
			reserved = true
		}
		d := at.Sub(now)
		if d <= 0 {
			g.next = now.Add(g.dialSpacing())
			g.mu.Unlock()
			return ctx.Err() == nil
		}
		// Wait for the earliest turn, then look again: a 429 may have moved the pause in the meantime.
		g.next = at.Add(g.dialSpacing())
		g.mu.Unlock()
		if !sleepCtx(ctx, d) {
			return false
		}
		g.mu.Lock()
		held := g.hold.After(time.Now())
		g.mu.Unlock()
		if !held {
			return ctx.Err() == nil
		}
	}
}

// limited records a 429 (retry is Avito's Retry-After, 0 when it gave none).
func (g *dialGate) limited(retry time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if now.Before(g.hold) {
		return // another room already set this pause
	}
	if g.backoff == 0 {
		g.backoff = holdFirst
	} else {
		g.backoff = min(g.backoff*2, holdMax)
	}
	d := max(g.backoff, retry)
	d += time.Duration(float64(d) * holdJitterFrac * rand.Float64())
	g.hold = now.Add(d)
	g.last429 = now
	// A refusal means the bucket is empty: after the pause the budget starts from nothing.
	if refill, burst := g.budget(); refill > 0 {
		if empty := g.hold.Add(time.Duration(burst-1) * refill); empty.After(g.tat) {
			g.tat = empty
		}
	}
	g.persist()
	utils.Infof("[AVITO] Avito ограничивает подключения (429): пауза %v", d.Round(time.Second))
}

// ok records a handshake that went through: the next 429 starts from the short pause again.
func (g *dialGate) ok() {
	g.mu.Lock()
	if time.Since(g.last429) >= calmAfter && g.backoff != 0 {
		g.backoff = 0
		g.persist()
	}
	g.mu.Unlock()
}
