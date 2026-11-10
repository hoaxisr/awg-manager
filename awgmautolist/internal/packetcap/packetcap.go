// Package packetcap provides passive LAN-only AF_PACKET capture. It never
// writes firewall rules, routes, marks, or packet contents.
package packetcap

import "errors"

const (
	DefaultReadBuffer   = 64 * 1024
	DefaultSocketBuffer = 4 * 1024 * 1024
)

var (
	ErrUnsupported = errors.New("packetcap: AF_PACKET is supported only on Linux")
	ErrMalformed   = errors.New("packetcap: malformed Ethernet frame")
	ErrTruncated   = errors.New("packetcap: truncated frame")
	ErrNotIP       = errors.New("packetcap: frame is not IPv4 or IPv6")
)

// Config controls one interface-bound reader.
type Config struct {
	Interface    string
	ReadBuffer   int
	SocketBuffer int
}
