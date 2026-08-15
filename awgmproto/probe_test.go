package awgmproto

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSplitArgsStripsWrapperFlags(t *testing.T) {
	rest, opts := SplitArgs([]string{
		"-peer", "1.2.3.4:56000",
		"--awgm-control-socket", "/tmp/awgm/wt-client-client-default.sock",
		"--awgm-log-file=/tmp/awgm/wt-client-client-default.log",
		"-mode", "rawtun",
		"--awgm-protocol",
	})
	want := []string{"-peer", "1.2.3.4:56000", "-mode", "rawtun"}
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("остаток для форка %v, ожидали %v", rest, want)
	}
	if !opts.Protocol {
		t.Fatal("проба не распознана")
	}
	if opts.Socket != "/tmp/awgm/wt-client-client-default.sock" {
		t.Fatalf("сокет: %q", opts.Socket)
	}
	if opts.LogFile != "/tmp/awgm/wt-client-client-default.log" {
		t.Fatalf("журнал: %q", opts.LogFile)
	}
}

func TestSplitArgsKeepsConfigHashStable(t *testing.T) {
	// Отпечаток считается по ПОЛНОМУ argv, поэтому наличие awgm-флагов на
	// отпечаток не влияет — обе стороны считают одно и то же.
	full := []string{"-peer", "x", "--awgm-control-socket", "/tmp/a.sock"}
	rest, _ := SplitArgs(full)
	if ConfigHash(full) != ConfigHash(rest) {
		t.Fatal("отпечаток зависит от того, срезаны awgm-флаги или нет")
	}
}

func TestWrapperFlagsRequireLeadingDash(t *testing.T) {
	// splitFlag срезает любое число дефисов и принимает запись вовсе без них —
	// §5.5 п.2 требует этого от ConfigHash. В настоящем argv так нельзя:
	// позиционное значение, совпавшее с именем флага, стало бы флагом, и форк
	// получил бы argv без своего значения.
	rest, opts := SplitArgs([]string{"-mode", "awgm-protocol"})
	if opts.Protocol {
		t.Fatal("значение без дефиса принято за флаг обвязки")
	}
	if !reflect.DeepEqual(rest, []string{"-mode", "awgm-protocol"}) {
		t.Fatalf("остаток для форка %v: значение флага съедено", rest)
	}
	if got := FlagValue([]string{"-peer", "x", "listen", ":56002"}, "listen"); got != "" {
		t.Fatalf("позиционное значение разобрано как флаг: %q", got)
	}
}

func TestFlagValueReadsBothForms(t *testing.T) {
	args := []string{"-listen", ":56002", "--config-dir=/opt/etc/wdtt", "-no-nat"}
	if got := FlagValue(args, "listen"); got != ":56002" {
		t.Fatalf("-listen: %q", got)
	}
	if got := FlagValue(args, "config-dir"); got != "/opt/etc/wdtt" {
		t.Fatalf("--config-dir=: %q", got)
	}
	if got := FlagValue(args, "no-nat"); got != "" {
		t.Fatalf("переключатель без значения: %q", got)
	}
	if got := FlagValue(args, "нет-такого"); got != "" {
		t.Fatalf("отсутствующий флаг: %q", got)
	}
	// Следующий аргумент — сам флаг, а не значение: иначе переключатель на
	// последнем месте «съел» бы соседа, и обвязка прочла бы имя флага как порт.
	if got := FlagValue([]string{"-no-nat", "-listen", ":56002"}, "no-nat"); got != "" {
		t.Fatalf("соседний флаг принят за значение: %q", got)
	}
}

func TestInstanceFromPath(t *testing.T) {
	got := InstanceFromPath("/tmp/awgm/wt-client-client-mytunnel.sock", "wt-client", "client")
	if got != "mytunnel" {
		t.Fatalf("идентификатор инстанса %q", got)
	}
	// Чужое имя — пустая строка: менеджер увидит расхождение и откажет.
	if got := InstanceFromPath("/tmp/awgm/чужой.sock", "wt-client", "client"); got != "" {
		t.Fatalf("чужое имя разобрано как %q", got)
	}
}

func TestPrintProtocolIsOneLine(t *testing.T) {
	var buf bytes.Buffer
	err := PrintProtocol(&buf, ProtocolInfo{
		Impl: "wt-client", Role: "client", Modes: []string{"raw", "wg"},
		Commands: []string{CmdState, CmdAttachTun, CmdDetachTun},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("проба обязана печатать ровно одну строку: %q", out)
	}
	var info ProtocolInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &info); err != nil {
		t.Fatal(err)
	}
	if info.V != Version || info.Impl != "wt-client" || len(info.Commands) != 3 {
		t.Fatalf("проба напечатала не то: %+v", info)
	}
}
