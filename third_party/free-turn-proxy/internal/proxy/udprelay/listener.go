package udprelay

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
)

const inboundQueueCap = 2000

// Packet - пулированная UDP-датаграмма, передаваемая из listener'а в per-stream
// DTLS-воркер. N - заполненный префикс Data.
type Packet struct {
	Data []byte
	N    int
}

// packetPool переиспользует Packet-буферы на горячем inbound пути. Размер буфера
// соответствует 2048 байт, которые ожидает цикл listener'а.
var packetPool = sync.Pool{
	New: func() any { return &Packet{Data: make([]byte, 2048)} },
}

// runListener читает пакеты из listenConn, обновляет кэш active-peer
// и публикует каждый пакет в inboundChan. При переполнении канала пакет
// отбрасывается - цикл чтения остаётся wait-free.
func runListener(ctx context.Context, listenConn net.PacketConn, activeLocalPeer *atomic.Value, inboundChan chan<- *Packet) {
	// Pointer-кэш последнего виденного адреса local peer. Позволяет избежать
	// per-packet аллокации addr.String() на горячем WG ingest пути:
	// большинство пакетов приходит от одного UDPAddr, поэтому проверка
	// по указателю покрывает fast path. Медленный путь (новый экземпляр
	// от ReadFrom для того же ip:port) делает одно String-сравнение и обновляет кэш.
	var lastAddr net.Addr
	var lastAddrStr string
	for {
		if ctx.Err() != nil {
			return
		}
		pktIface := packetPool.Get()
		pkt := pktIface.(*Packet) //nolint:errcheck // pool New always returns *Packet
		nRead, addr, err := listenConn.ReadFrom(pkt.Data)
		if err != nil {
			return
		}

		if addr != lastAddr {
			s := addr.String()
			if s != lastAddrStr {
				activeLocalPeer.Store(addr)
				lastAddrStr = s
			}
			lastAddr = addr
		}

		pkt.N = nRead

		select {
		case inboundChan <- pkt:
		default:
			packetPool.Put(pkt)
		}
	}
}
