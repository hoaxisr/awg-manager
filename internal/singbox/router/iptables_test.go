package router

import (
	"context"
	"strings"
	"testing"
)

type fakeExec struct {
	calls []fakeCall
	err   error
}

type fakeCall struct {
	kind  string
	args  []string
	stdin string
}

func (f *fakeExec) restoreNoflush(_ context.Context, input string) error {
	f.calls = append(f.calls, fakeCall{kind: "restore", stdin: input})
	return f.err
}

func (f *fakeExec) runIPTables(_ context.Context, args ...string) error {
	f.calls = append(f.calls, fakeCall{kind: "iptables", args: args})
	return f.err
}

func (f *fakeExec) runIP(_ context.Context, args ...string) error {
	f.calls = append(f.calls, fakeCall{kind: "ip", args: args})
	return f.err
}

func newFakeIPTables(fe *fakeExec) *IPTables {
	return &IPTables{
		restoreNoflush: fe.restoreNoflush,
		runIPTables:    fe.runIPTables,
		runIP:          fe.runIP,
	}
}

func TestBuildTProxyModulePath(t *testing.T) {
	got := buildTProxyModulePath("5.15.0-mips")
	want := "/lib/modules/5.15.0-mips/xt_TPROXY.ko"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestKernelModuleName(t *testing.T) {
	if kernelModuleName() != "xt_TPROXY" {
		t.Errorf("got %q", kernelModuleName())
	}
}

func TestBuildRestoreInput_PolicyMark_EmitsConnmarkRule(t *testing.T) {
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa"}
	out := buildRestoreInput(spec)
	want := "-I PREROUTING 1 -m connmark --mark 0xffffaaa -j " + ChainName
	if !strings.Contains(out, want) {
		t.Errorf("output missing PREROUTING rule\nwant substring: %s\ngot:\n%s", want, out)
	}
}

func TestBuildRestoreInput_EmptyMark_NoPrerouting(t *testing.T) {
	spec := RestoreInputSpec{PolicyMark: ""}
	out := buildRestoreInput(spec)
	if strings.Contains(out, "-I PREROUTING") {
		t.Errorf("expected no PREROUTING jump for empty mark, got:\n%s", out)
	}
}

func TestBuildRestoreInput_BaseRules_AlwaysPresent(t *testing.T) {
	input := buildRestoreInput(RestoreInputSpec{PolicyMark: "0xffffaaa"})

	expected := []string{
		"*mangle",
		":AWGM-TPROXY - [0:0]",
		"-A AWGM-TPROXY -d 127.0.0.0/8 -j RETURN",
		"-A AWGM-TPROXY -d 192.168.0.0/16 -j RETURN",
		"-A AWGM-TPROXY -p tcp --dport 79 -j RETURN",
		"-A AWGM-TPROXY -m mark --mark 0xff -j RETURN",
		"-A AWGM-TPROXY -p tcp -j TPROXY --on-port 51271 --on-ip 127.0.0.1 --tproxy-mark 0x1",
		"-A AWGM-TPROXY -p udp -j TPROXY --on-port 51271 --on-ip 127.0.0.1 --tproxy-mark 0x1",
		"COMMIT",
	}
	for _, line := range expected {
		if !strings.Contains(input, line) {
			t.Errorf("missing line: %q\nin:\n%s", line, input)
		}
	}
}

func TestIPTablesInstallSequence(t *testing.T) {
	fe := &fakeExec{}
	it := newFakeIPTables(fe)
	if err := it.Install(context.Background(), "0xffffaaa"); err != nil {
		t.Fatal(err)
	}
	if len(fe.calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %+v", len(fe.calls), fe.calls)
	}
	if fe.calls[0].kind != "restore" || !strings.Contains(fe.calls[0].stdin, "AWGM-TPROXY") {
		t.Errorf("call 0: %+v", fe.calls[0])
	}
	if fe.calls[1].kind != "ip" || !strings.Contains(strings.Join(fe.calls[1].args, " "), "rule add fwmark") {
		t.Errorf("call 1: %+v", fe.calls[1])
	}
	if fe.calls[2].kind != "ip" || !strings.Contains(strings.Join(fe.calls[2].args, " "), "route add local") {
		t.Errorf("call 2: %+v", fe.calls[2])
	}
}

func TestIPTablesUninstallSequence(t *testing.T) {
	fe := &fakeExec{err: nil}
	it := newFakeIPTables(fe)
	if err := it.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fe.calls) < 3 {
		t.Errorf("expected >=3 calls, got %d", len(fe.calls))
	}
}

