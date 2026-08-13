//go:build !linux

package wdtt

import "errors"

func signalProcessHUP(int) error {
	return errors.New("SIGHUP unsupported on this platform")
}
