//go:build !linux

package procres

import (
	"errors"
	"os"

	"github.com/hoaxisr/awg-manager/awgmproto"
)

func OpenTunFD(_ string) (*os.File, error) {
	return nil, errors.New("TUN FD handoff requires linux")
}

func isProtoBusy(err error) bool {
	var pe interface{ ProtoCode() string }
	return errors.As(err, &pe) && pe.ProtoCode() == awgmproto.CodeBusy
}
