//go:build !linux

package packetcap

// Reader is the non-Linux stub.
type Reader struct{}

func Probe() error                      { return ErrUnsupported }
func Open(Config) (*Reader, error)      { return nil, ErrUnsupported }
func (*Reader) FD() int                 { return -1 }
func (*Reader) Read(func([]byte)) error { return ErrUnsupported }
func (*Reader) Close() error            { return nil }
