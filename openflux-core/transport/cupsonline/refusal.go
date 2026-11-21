package cupsonline

import (
	"sync"
	"time"

	"github.com/p1neappleXpress/OpenFlux/utils"
)

// cups.online rations the messages it takes from one address. Past its
// limit it answers every message from that address - any room, any user,
// keepalives too - with code 429 ("Превышено допустимое число отправок
// запроса"), and goes on refusing while messages keep coming: an exit host
// whose sessions kept sending into their rooms stayed refused for good, at
// one message a second as at thirty. So a refusal stops every room of the
// process, not just the one it came on: none sends a message until the pause
// is over. The pause starts at refusalPauseMin, doubles each time the first
// messages after it are refused again, up to refusalPauseMax, and starts
// over after refusalCalm without a refusal.
// Variables only so tests can shorten them.
var (
	refusalPauseMin = 15 * time.Second
	refusalPauseMax = 10 * time.Minute
	refusalCalm     = 15 * time.Minute
)

// refusals is the process's pause: the address is the process's.
var refusals refusalGate

type refusalGate struct {
	mu    sync.Mutex
	until time.Time     // no message before this
	step  time.Duration // the pause the last refusal began
	last  time.Time     // the last refusal
}

// refused notes a 429 at now. A refusal during a pause is of a message sent
// before it began and changes nothing; one after it makes the next pause
// longer. It reports the pause and whether this refusal began it.
func (g *refusalGate) refused(now time.Time) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	calm := g.last.IsZero() || now.Sub(g.last) > refusalCalm
	g.last = now
	if now.Before(g.until) {
		return g.until.Sub(now), false
	}
	if calm || g.step == 0 {
		g.step = refusalPauseMin
	} else {
		g.step = min(2*g.step, refusalPauseMax)
	}
	g.until = now.Add(g.step)
	return g.step, true
}

// wait is how long a message must still wait at now.
func (g *refusalGate) wait(now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Before(g.until) {
		return g.until.Sub(now)
	}
	return 0
}

// isRefusal tells a reply's error of the address's quota: code 429.
func isRefusal(e interface{}) bool {
	m, ok := e.(map[string]interface{})
	if !ok {
		return false
	}
	code, _ := m["code"].(float64)
	return code == 429
}

// noteRefusal is the room's side of a 429: the pause starts for every room.
func (w *cupsWS) noteRefusal() {
	if pause, began := refusals.refused(time.Now()); began {
		utils.Infof("[CUPS] cups.online refuses this address's messages (429): every room waits %v", pause.Round(time.Second))
	}
}

// holdForRefusal waits out a pause in force; false when the room stopped.
func (w *cupsWS) holdForRefusal() bool {
	for {
		d := refusals.wait(time.Now())
		if d <= 0 {
			return true
		}
		if !sleepCtx(w.ctx, min(d, time.Second)) {
			return false
		}
	}
}
