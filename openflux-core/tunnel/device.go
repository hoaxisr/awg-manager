package tunnel

import (
	"errors"
	"sync/atomic"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/tunnel/l3"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

// deviceCut counts the packets SendFromDevice had to cut, for the log.
var deviceCut atomic.Uint64

// SendFromDevice sends an IPv4 packet that a device's own network stack wrote to its TUN interface.
//
// The interface has an MTU of its own (the Android app asks for 1400) and the device's TCP fills it,
// while a carrier can move less: the MAX script declares 1200, and the session then refuses a TCP
// packet above that with *transport.PacketTooLargeError. A stack inside this process is told with an
// ICMP "fragmentation needed" (TCPTunnel.refused) and retransmits smaller, but nobody tells a device's
// kernel: the refused packet is gone, the kernel sends it again after its timeout in the same size,
// and the first segment of a connection (a TLS ClientHello over 1200 bytes, the first flight of
// MTProto) never arrives. So the segment is cut here, into packets that fit, as the exit does for the
// way back (l3.SplitTCP). SYNs are clamped first, so that new connections do not need the cut.
// Packets of other protocols go as the session takes them.
func SendFromDevice(trans transport.Transport, p []byte) error {
	ClampDeviceSyn(trans, p)
	err := trans.Send(p)
	var big *transport.PacketTooLargeError
	if err == nil || !errors.As(err, &big) {
		return err
	}
	parts := l3.SplitTCP(p, big.Limit)
	if parts == nil {
		return err
	}
	if n := deviceCut.Add(1); n == 1 || n%5000 == 0 {
		utils.Infof("[TUNNEL] the device's TCP packets of %d bytes are cut to %d, the most the carrier moves (%d cut so far)", len(p), big.Limit, n)
	}
	for _, part := range parts {
		if err := trans.Send(part); err != nil {
			return err
		}
	}
	return nil
}

// ClampDeviceSyn holds the MSS option of a SYN or SYN-ACK to what the carriers move, in place: for a
// SYN the device writes, the segments the peer sends come within it; for a SYN-ACK the peer answers
// with (hand it here before it reaches the device), the segments the device sends do. The same limit
// as the MTU of the stack inside this process (tunnelMTU), so both kinds of client agree.
func ClampDeviceSyn(trans transport.Transport, p []byte) {
	l3.ClampSynMSS(p, func() int { return tunnelMTU(trans) - 40 })
}
