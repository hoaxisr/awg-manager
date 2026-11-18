//go:build !linux

package installer

func killPID(pid int) error {
	return nil
}
