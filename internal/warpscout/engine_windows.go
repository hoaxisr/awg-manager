//go:build windows

package warpscout

import "os"

func terminate(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
