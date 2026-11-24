//go:build linux

package fpclient

import (
	"os/exec"
	"syscall"
)

// awgm-fpc без демона бесполезен: TLS-мост живёт в процессе awg-manager.
// Без Pdeathsig сирота держала бы socks-порт, и рестарт демона поднимал бы
// дубль на другом порту, расходящемся с конфигом sing-box.
//
// Флаг дописывается в уже выставленный SysProcAttr, а не заменяет его:
// childproc.SetProcessGroup (Setsid) зовётся раньше и иначе бы потерялся.
func setParentDeath(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
