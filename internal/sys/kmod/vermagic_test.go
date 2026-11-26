package kmod

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakeKo(t *testing.T, vermagic string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.ko")
	data := []byte("\x7fELF....version=3.1\x00srcversion=ABC\x00")
	if vermagic != "" {
		data = append(data, []byte("vermagic="+vermagic+"\x00tail")...)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func withKernel(t *testing.T, rel string) {
	t.Helper()
	prev := kernelRelease
	kernelRelease = func() string { return rel }
	t.Cleanup(func() { kernelRelease = prev })
}

func TestCheckKernelMatch(t *testing.T) {
	withKernel(t, "4.9-ndm-6")

	p := writeFakeKo(t, "4.9-ndm-5 SMP mod_unload MIPS32_R2 32BIT ")
	err := CheckKernelMatch(p)
	var mm *KernelMismatchError
	if !errors.As(err, &mm) || !errors.Is(err, ErrKernelMismatch) {
		t.Fatalf("want KernelMismatchError, got %v", err)
	}
	if mm.Module != "4.9-ndm-5" || mm.Kernel != "4.9-ndm-6" {
		t.Fatalf("mismatch = %+v", mm)
	}

	if err := CheckKernelMatch(writeFakeKo(t, "4.9-ndm-6 SMP mod_unload aarch64")); err != nil {
		t.Fatalf("matching module refused: %v", err)
	}
	if err := CheckKernelMatch(writeFakeKo(t, "")); err != nil {
		t.Fatalf("module without vermagic must be left to insmod: %v", err)
	}
	if err := CheckKernelMatch(filepath.Join(t.TempDir(), "absent.ko")); err != nil {
		t.Fatalf("absent file must be left to the loader: %v", err)
	}
}

func TestCheckKernelMatch_UnknownKernel(t *testing.T) {
	withKernel(t, "")
	if err := CheckKernelMatch(writeFakeKo(t, "4.9-ndm-5 SMP")); err != nil {
		t.Fatalf("unknown kernel must not block: %v", err)
	}
}

// Модуль под другое ядро (5.2 Beta 5: 4.9-ndm-6 при модулях ndm-5) не
// доходит до insmod, причина уходит в панель, Usable — ложь.
func TestEnsureModule_SkipsModuleOfAnotherKernel(t *testing.T) {
	withKernel(t, "4.9-ndm-6")
	dir := t.TempDir()
	setModulesDir(t, dir)
	calls := stubRunCmd(t)
	ko := filepath.Join(dir, "amneziawg.ko")
	if err := os.WriteFile(ko, []byte("x\x00vermagic=4.9-ndm-5 SMP mod_unload\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &Loader{model: "KN-1810", modulePath: ko}
	err := l.EnsureModule(context.Background())
	if !errors.Is(err, ErrKernelMismatch) {
		t.Fatalf("want ErrKernelMismatch, got %v", err)
	}
	for _, c := range *calls {
		if c == "insmod" {
			t.Fatal("insmod called for a module of another kernel")
		}
	}
	if !strings.Contains(l.LastEnsureError(), "4.9-ndm-6") {
		t.Fatalf("panel reason = %q", l.LastEnsureError())
	}
	if l.Usable() {
		t.Fatal("Usable() = true for a module of another kernel")
	}
}
