package nwg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
)

type relayKmodFixture struct {
	m       *RelayKmod
	loaded  bool
	calls   []string
	insmod  error
	version string
	armed   int
	writes  []string
}

func newRelayKmodFixture(t *testing.T) *relayKmodFixture {
	f := &relayKmodFixture{version: ExpectedRelayKmodVersion + "\n"}
	m := NewRelayKmod(nil, func() error { f.armed++; return nil }, func(time.Duration) {})
	m.resolve = func() (string, error) { return "/opt/etc/awg-manager/modules/awgm_relay-mt7621.ko", nil }
	m.isLoadedFn = func() bool { return f.loaded }
	m.modLoadedFn = func(string) bool { return true }
	m.execFn = func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		f.calls = append(f.calls, name+" "+strings.Join(args, " "))
		switch name {
		case "insmod":
			if f.insmod != nil {
				return &exec.Result{ExitCode: 1, Stderr: f.insmod.Error()}, nil
			}
			f.loaded = true
		case "rmmod":
			f.loaded = false
		}
		return &exec.Result{}, nil
	}
	m.procReadFn = func(p string) ([]byte, error) {
		if strings.HasSuffix(p, "version") {
			return []byte(f.version), nil
		}
		return []byte("127.0.0.1:39001 1.2.3.4:5 transform=phobos masking=none rx=0\n"), nil
	}
	m.procWriteFn = func(p string, b []byte) error { f.writes = append(f.writes, p+" "+string(b)); return nil }
	f.m = m
	return f
}

func TestRelayKmod_EnsureArmsBeforeInsmod(t *testing.T) {
	f := newRelayKmodFixture(t)
	if err := f.m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.armed != 1 || !f.loaded {
		t.Fatalf("armed=%d loaded=%v", f.armed, f.loaded)
	}
	if err := f.m.Ensure(context.Background()); err != nil || len(f.calls) != 1 {
		t.Fatalf("повторный Ensure не должен звать insmod: %v %v", err, f.calls)
	}
}

func TestRelayKmod_FailureRemembered(t *testing.T) {
	f := newRelayKmodFixture(t)
	f.insmod = errors.New("Unknown symbol")
	if err := f.m.Ensure(context.Background()); err == nil {
		t.Fatal("ждали ошибку insmod")
	}
	n := len(f.calls)
	if err := f.m.Ensure(context.Background()); err == nil || len(f.calls) != n {
		t.Fatalf("отказ должен запоминаться до рестарта демона: calls=%v", f.calls)
	}
	if f.m.Available() {
		t.Fatal("Available после отказа")
	}
}

func TestRelayKmod_ReconcileVersionReloads(t *testing.T) {
	f := newRelayKmodFixture(t)
	f.loaded, f.version = true, "0.0.9\n"
	f.m.ReconcileVersion(context.Background())
	if f.loaded {
		t.Fatal("устаревший модуль не выгружен")
	}
	if len(f.writes) != 1 || !strings.Contains(f.writes[0], "/proc/awgm_relay/del 127.0.0.1:39001") {
		t.Fatalf("слоты не сняты перед rmmod: %v", f.writes)
	}
}

func TestRelayKmod_ReconcileVersionKeepsCurrent(t *testing.T) {
	f := newRelayKmodFixture(t)
	f.loaded = true
	f.m.ReconcileVersion(context.Background())
	if !f.loaded || len(f.calls) != 0 {
		t.Fatalf("актуальный модуль тронут: %v", f.calls)
	}
}

type relayLogSpy struct{ lines []string }

func (s *relayLogSpy) AppLog(_ logging.Level, _, _, _, _, msg string) { s.lines = append(s.lines, msg) }

// Нет сборки под модель/SoC: Available — false, причина в журнале ОДИН раз,
// повторного resolve нет (отказ запомнен до рестарта демона, §4.5).
func TestRelayKmod_AvailableResolveFailLoggedOnce(t *testing.T) {
	spy := &relayLogSpy{}
	m := NewRelayKmod(spy, nil, nil)
	resolves := 0
	m.resolve = func() (string, error) {
		resolves++
		return "", errors.New("нет сборки awgm_relay.ko под KN-1010/mt7628")
	}
	m.isLoadedFn = func() bool { return false }
	if m.Available() || m.Available() {
		t.Fatal("Available без сборки")
	}
	if resolves != 1 {
		t.Fatalf("resolve вызван %d раз, ждали 1", resolves)
	}
	if len(spy.lines) != 1 || !strings.Contains(spy.lines[0], "нет сборки awgm_relay.ko") ||
		!strings.Contains(spy.lines[0], "Phobos-туннели работают процессом") {
		t.Fatalf("журнал: %q", spy.lines)
	}
}

// relayKoPath: arch-default resolveKoPathFor отдаёт без проверки файла —
// отсутствие ловится здесь, а не insmod-ошибкой.
func TestRelayKoPath(t *testing.T) {
	none := func(string) bool { return false }
	if _, err := relayKoPath("KN-1810", kmod.SoCMT7621, none); err == nil ||
		!strings.Contains(err.Error(), "нет сборки awgm_relay.ko под KN-1810/mt7621") {
		t.Fatalf("arch-default без файла: %v", err)
	}
	all := func(string) bool { return true }
	if p, err := relayKoPath("KN-1810", kmod.SoCMT7621, all); err != nil || p == "" {
		t.Fatalf("файл есть: %q %v", p, err)
	}
}

// F478: модель/SoC ещё не определены (ранний старт) — это не «сборки нет»:
// отказ не запоминается, следующий Start пробует снова.
func TestRelayKmod_AvailableModelUnknownNotRemembered(t *testing.T) {
	m := NewRelayKmod(nil, nil, nil)
	known := false
	m.resolve = func() (string, error) {
		if !known {
			return "", errRelayModelUnknown
		}
		return "/opt/etc/awg-manager/modules/awgm_relay-mt7621.ko", nil
	}
	m.isLoadedFn = func() bool { return false }
	if m.Available() {
		t.Fatal("Available без модели")
	}
	known = true
	if !m.Available() {
		t.Fatal("ранний отказ запомнен: ядро не предложится до рестарта демона")
	}
}

// F477: отказ del при выгрузке виден в ошибке, а не только «rmmod: in use».
func TestRelayKmod_UnloadReportsDelFailure(t *testing.T) {
	f := newRelayKmodFixture(t)
	f.loaded = true
	f.m.procWriteFn = func(string, []byte) error { return errors.New("busy") }
	err := f.m.Unload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "del 127.0.0.1:39001") {
		t.Fatalf("ошибка выгрузки: %v", err)
	}
}
