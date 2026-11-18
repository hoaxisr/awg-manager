//go:build linux

package control

import (
	"net"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"golang.org/x/sys/unix"
)

type frameConn = awgmproto.FrameConn

func newFrameConn(uc *net.UnixConn) *frameConn {
	return awgmproto.NewFrameConn(uc)
}

func newProtoError(code, msg string) error {
	return &awgmproto.Error{Code: code, Msg: msg}
}

func encodeRightsOOB(fd int) []byte {
	if fd < 0 {
		return nil
	}
	return unix.UnixRights(fd)
}

func closeFD(fd int) {
	if fd >= 0 {
		_ = unix.Close(fd)
	}
}
