//go:build !linux

package control

import (
	"bytes"
	"fmt"
	"net"
)

type protoError struct {
	Code string
	Msg  string
}

func (e *protoError) Error() string     { return e.Msg }
func (e *protoError) ProtoCode() string { return e.Code }

func newProtoError(code, msg string) error {
	return &protoError{Code: code, Msg: msg}
}

type frameConn struct {
	uc  *net.UnixConn
	buf []byte
	rd  []byte
}

func newFrameConn(uc *net.UnixConn) *frameConn {
	return &frameConn{
		uc: uc,
		rd: make([]byte, 8*1024),
	}
}

func (c *frameConn) ReadFrame() ([]byte, int, error) {
	for {
		if i := bytes.IndexByte(c.buf, '\n'); i >= 0 {
			line := make([]byte, i)
			copy(line, c.buf[:i])
			c.buf = append(c.buf[:0], c.buf[i+1:]...)
			return line, -1, nil
		}
		if len(c.buf) > 64*1024 {
			return nil, -1, fmt.Errorf("frame exceeds 64 KiB limit")
		}
		n, err := c.uc.Read(c.rd)
		if n > 0 {
			c.buf = append(c.buf, c.rd[:n]...)
		}
		if err != nil {
			return nil, -1, err
		}
	}
}

func encodeRightsOOB(_ int) []byte {
	return nil
}

func closeFD(_ int) {}
