//go:build linux

package fpclient

import (
	"os/exec"
	"syscall"
)

// awgm-fpc без демона бесполезен: TLS-мост живёт в процессе awg-manager.
// Без Pdeathsig сирота держала бы socks-порт, и рестарт демона поднимал бы
// дубль на другом порту, расходящемся с конфигом sing-box.
func setParentDeath(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
