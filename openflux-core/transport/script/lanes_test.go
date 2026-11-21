package script

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

// lanesScript: N lanes, each a UDP socket to a hub that hands a lane's
// datagrams to the same lane of the other side. Every lane ticks every 5 ms,
// packs what the stripe gives it into one datagram (a 2-byte length per
// frame) and drops one datagram in drop - the stripe must resend.
const lanesScript = `
var socks = [], timer = null, n = 0, side = "", drop = 0, sent = 0;
function pack(frames) {
  var len = 0, i;
  for (i = 0; i < frames.length; i++) len += 2 + frames[i].byteLength;
  var out = new Uint8Array(1 + len), off = 1;
  out[0] = side === "a" ? 0 : 1;
  for (i = 0; i < frames.length; i++) {
    var f = new Uint8Array(frames[i]);
    out[off] = f.length >> 8; out[off + 1] = f.length & 255;
    out.set(f, off + 2); off += 2 + f.length;
  }
  return out.buffer;
}
function unpack(lane, buf) {
  var b = new Uint8Array(buf), off = 1;
  while (off + 2 <= b.length) {
    var l = (b[off] << 8) | b[off + 1];
    lanes.deliver(lane, b.slice(off + 2, off + 2 + l).buffer);
    off += 2 + l;
  }
}
function send(i, frames) {
  if (!frames.length) return;
  sent++;
  if (drop && sent % drop === 0) return;
  socks[i].send(pack(frames));
}
var Transport = {
  info: function () { return { name: "lanes-test", version: "1.0.0" }; },
  open: async function (cfg) {
    n = Number(cfg.params.lanes); side = cfg.params.side; drop = Number(cfg.params.drop || 0);
    lanes.open(n, { pieceBytes: 1000, noDups: true, laneWindow: true, ackIntervalMs: 20 });
    for (var i = 0; i < n; i++) {
      var s = await udp.open(cfg.url);
      (function (i, s) { s.onmessage = function (d) { unpack(i, d); }; })(i, s);
      socks.push(s);
      // say hello so the hub knows this lane's address
      s.send(new Uint8Array([side === "a" ? 0 : 1]).buffer);
      lanes.state(i, true);
    }
    timer = setInterval(function () {
      for (var i = 0; i < n; i++) send(i, lanes.pull(i, 32));
    }, 5);
    setState("connected");
  },
  laneSend: function (i, data) { send(i, [data]); },
  write: function () { throw new Error("write must not be called once lanes are open"); },
  close: function () { if (timer) clearInterval(timer); }
};`

// laneHub relays datagrams between the two sides' sockets of one lane: it
// learns a lane's addresses from the first byte (side) and the order of the
// sockets (lane = order of arrival per side).
type laneHub struct {
	conn  net.PacketConn
	mu    sync.Mutex
	addrs [2][]string
	byKey map[string][2]int // addr -> side, lane
}

func newLaneHub(t *testing.T) *laneHub {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &laneHub{conn: c, byKey: map[string][2]int{}}
	go h.run()
	t.Cleanup(func() { c.Close() })
	return h
}

func (h *laneHub) run() {
	buf := make([]byte, 65536)
	for {
		n, addr, err := h.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if n < 1 {
			continue
		}
		side := int(buf[0])
		h.mu.Lock()
		k, ok := h.byKey[addr.String()]
		if !ok {
			k = [2]int{side, len(h.addrs[side])}
			h.byKey[addr.String()] = k
			h.addrs[side] = append(h.addrs[side], addr.String())
		}
		var peer string
		if other := h.addrs[1-k[0]]; k[1] < len(other) {
			peer = other[k[1]]
		}
		h.mu.Unlock()
		if peer == "" || n == 1 {
			continue
		}
		pa, _ := net.ResolveUDPAddr("udp", peer)
		_, _ = h.conn.WriteTo(buf[:n], pa)
	}
}

func startLanesSide(t *testing.T, path string, pub []byte, hub string, side string, nLanes, drop int) (*ScriptTransport, chan []byte) {
	tr, err := New("lanes-"+side, path, pub, hub, map[string]interface{}{"lanes": fmt.Sprint(nLanes), "side": side, "drop": fmt.Sprint(drop)}, transport.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte, 4096)
	tr.Receive(func(b []byte) { got <- b })
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Stop() })
	return tr, got
}

func TestScriptLanesStripeInOrder(t *testing.T) {
	for _, drop := range []int{0, 7} {
		t.Run(fmt.Sprintf("drop%d", drop), func(t *testing.T) {
			path, pub := mustSignedScript(t, lanesScript)
			hub := newLaneHub(t)
			a, _ := startLanesSide(t, path, pub, hub.conn.LocalAddr().String(), "a", 3, drop)
			_, got := startLanesSide(t, path, pub, hub.conn.LocalAddr().String(), "b", 3, drop)
			deadline := time.Now().Add(10 * time.Second)
			for !a.IsConnected() && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			time.Sleep(200 * time.Millisecond) // both sides' sockets known to the hub

			const packets = 400
			want := make([][]byte, packets)
			for i := range want {
				want[i] = make([]byte, 200+i%1300)
				_, _ = rand.Read(want[i])
			}
			go func() {
				for _, p := range want {
					for a.Send(p) != nil {
						time.Sleep(2 * time.Millisecond)
					}
				}
			}()
			for i := 0; i < packets; i++ {
				select {
				case p := <-got:
					if !bytes.Equal(p, want[i]) {
						t.Fatalf("packet %d differs (len %d, want %d)", i, len(p), len(want[i]))
					}
				case <-time.After(20 * time.Second):
					t.Fatalf("got %d of %d packets", i, packets)
				}
			}
		})
	}
}
