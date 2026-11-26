package kmod

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// kernelRelease — точка подмены для тестов.
var kernelRelease = osdetect.KernelRelease

// ErrKernelMismatch — модуль собран под другое ядро (vermagic не совпал с
// uname -r). insmod такой модуль отвергнет; на ядрах Keenetic без
// CONFIG_MODVERSIONS совпадение vermagic — единственная защита, поэтому
// грузить не пытаемся вовсе: прошивка с новым ядром (5.2 Beta 5 —
// 4.9-ndm-6) получает понятную причину вместо «exit status 8».
var ErrKernelMismatch = errors.New("модуль собран под другое ядро")

// KernelMismatchError — подробности ErrKernelMismatch.
type KernelMismatchError struct {
	Path   string
	Module string // ядро из vermagic модуля
	Kernel string // uname -r роутера
}

func (e *KernelMismatchError) Error() string {
	return fmt.Sprintf("%s собран для ядра %s, а на роутере ядро %s — загрузка пропущена", e.Path, e.Module, e.Kernel)
}

func (e *KernelMismatchError) Unwrap() error { return ErrKernelMismatch }

// moduleKernelRelease — ядро из строки vermagic в .modinfo модуля
// ("vermagic=4.9-ndm-5 SMP mod_unload ..."): первое поле. "" — строки нет.
func moduleKernelRelease(data []byte) string {
	const key = "vermagic="
	i := bytes.Index(data, []byte(key))
	if i < 0 {
		return ""
	}
	rest := data[i+len(key):]
	if end := bytes.IndexByte(rest, 0); end >= 0 {
		rest = rest[:end]
	}
	fields := bytes.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	return string(fields[0])
}

// CheckKernelMatch — nil, если модуль подходит ядру роутера или сверить
// нечем (ядро не определилось, vermagic в файле нет: решает insmod).
// *KernelMismatchError — модуль собран под другое ядро.
func CheckKernelMatch(path string) error {
	kernel := kernelRelease()
	if kernel == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // файла нет или не читается — отказ назовёт загрузчик
	}
	mod := moduleKernelRelease(data)
	if mod == "" || mod == kernel {
		return nil
	}
	return &KernelMismatchError{Path: path, Module: mod, Kernel: kernel}
}
