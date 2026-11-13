package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// STUN message types and the magic cookie (RFC 5389).
const (
	stunBindingRequest = 0x0001
	stunBindingSuccess = 0x0101
	stunBindingError   = 0x0111
	stunMagicCookie    = 0x2112a442
	stunHeaderSize     = 20
)

// isSTUNMessage reports that payload looks like a STUN message: the top two
// bits are zero, the cookie matches and the length field fits the datagram.
func isSTUNMessage(payload []byte) bool {
	return len(payload) >= stunHeaderSize && payload[0]&0xc0 == 0 &&
		binary.BigEndian.Uint32(payload[4:8]) == stunMagicCookie &&
		int(binary.BigEndian.Uint16(payload[2:4]))+stunHeaderSize <= len(payload)
}

// stunCheck sends one STUN Binding Request to the observed IP and port and
// waits for the matching response. A Binding Error response still proves
// reachability: the endpoint answered STUN, which is all a candidate needs.
func stunCheck(parent context.Context, target model.Target, path Path) Result {
	started := time.Now()
	fail := func(verdict model.Verdict, detail string) Result {
		return Result{Verdict: verdict, Duration: time.Since(started), Detail: detail}
	}
	ip := net.ParseIP(target.IP)
	if ip == nil {
		return fail(model.VerdictError, "UDP target requires an observed IP")
	}
	if target.Port == 0 {
		return fail(model.VerdictError, "UDP target requires a port")
	}

	network := "udp4"
	local := &net.UDPAddr{IP: net.IPv4zero}
	if ip.To4() == nil {
		network = "udp6"
		local = &net.UDPAddr{IP: net.IPv6unspecified}
	}
	conn, err := net.ListenUDP(network, local)
	if err != nil {
		return fail(model.VerdictError, err.Error())
	}
	defer conn.Close()
	if path.Interface != "" {
		if err := bindUDPInterface(conn, path.Interface); err != nil {
			return fail(model.VerdictError, err.Error())
		}
	}

	request := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(request[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(request[2:4], 0) // no attributes
	binary.BigEndian.PutUint32(request[4:8], stunMagicCookie)
	if _, err := rand.Read(request[8:20]); err != nil {
		return fail(model.VerdictError, err.Error())
	}

	remote := &net.UDPAddr{IP: ip, Port: int(target.Port)}
	if _, err := conn.WriteToUDP(request, remote); err != nil {
		return fail(model.VerdictError, err.Error())
	}

	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	// Closing the socket from the watchdog unblocks a parked ReadFromUDP.
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()

	buffer := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if parent.Err() != nil {
				return fail(model.VerdictError, "STUN probe cancelled")
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fail(model.VerdictBlocked, fmt.Sprintf("STUN response timeout after %s", probeTimeout))
			}
			return fail(model.VerdictError, err.Error())
		}
		if n < stunHeaderSize || !isSTUNMessage(buffer[:n]) {
			continue // not STUN: ignore stray datagrams
		}
		if from.IP.String() != ip.String() || from.Port != int(target.Port) {
			continue // wrong source endpoint: ignore spoofed or relayed datagrams
		}
		if string(buffer[8:20]) != string(request[8:20]) {
			continue // wrong transaction: belongs to another request
		}
		messageType := binary.BigEndian.Uint16(buffer[0:2])
		if messageType != stunBindingSuccess && messageType != stunBindingError {
			continue
		}
		return Result{
			Verdict:  model.VerdictReachable,
			Bytes:    n,
			Duration: time.Since(started),
			Detail:   fmt.Sprintf("STUN response 0x%04x from %s", messageType, from),
		}
	}
}
