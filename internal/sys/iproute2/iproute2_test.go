package iproute2

import (
	"errors"
	"strings"
	"testing"
)

// want — подстрока ошибки, по которой видно, какая ветка сработала; "" — ошибки нет.
func TestClassify(t *testing.T) {
	for _, c := range []struct {
		name string
		out  string
		err  error
		want string
	}{
		{"ip-full 4.4", "ip utility, iproute2-ss160111\n", nil, ""},
		{"ip-full 6.x", "ip utility, iproute2-6.11.0, libbpf 1.4.5\n", nil, ""},
		{"busybox", "BusyBox v1.37.0 (2026-02-13 14:22:43 UTC) multi-call binary.\n\nUsage: ip", errors.New("exit status 1"), "busybox"},
		{"нет файла", "", errors.New("no such file or directory"), "не запускается"},
		{"чужой вывод", "something else", nil, "не похож"},
	} {
		got := classify(c.out, c.err)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s: classify = %v, want nil", c.name, got)
		case c.want != "" && (got == nil || !strings.Contains(got.Error(), c.want)):
			t.Errorf("%s: classify = %v, want ошибку с %q", c.name, got, c.want)
		}
	}
}
