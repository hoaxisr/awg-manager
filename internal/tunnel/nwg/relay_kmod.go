package nwg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
)

// ExpectedRelayKmodVersion — PKG_VERSION в kmod/awgm-relay/package/Makefile.
const ExpectedRelayKmodVersion = "0.2.1"

const relayKmodDisarmAfter = 5 * time.Minute

// RelayKmod — загрузка awgm_relay.ko (kernel-релей обфускатора, спека §4.6).
// Выбор файла под модель/SoC — тот же resolveKoPathFor, что у awg_proxy.
type RelayKmod struct {
	mu     sync.Mutex
	failed error // отказ загрузки запоминается до рестарта демона (§4.5)
	appLog *logging.ScopedLogger

	arm         func() error        // метка сторожа ДО insmod (§4.9)
	disarmAfter func(time.Duration) // снятие метки после окна стабильности

	resolve     func() (string, error)
	execFn      func(ctx context.Context, name string, args ...string) (*exec.Result, error)
	isLoadedFn  func() bool
	modLoadedFn func(name string) bool
	procReadFn  func(path string) ([]byte, error)
	procWriteFn func(path string, data []byte) error
}

func NewRelayKmod(appLogger logging.AppLogger, arm func() error, disarmAfter func(time.Duration)) *RelayKmod {
	return &RelayKmod{
		appLog:      logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubKmod),
		arm:         arm,
		disarmAfter: disarmAfter,
		resolve: func() (string, error) {
			return relayKoPath(kmod.DetectModel(), kmod.DetectSoC(), func(p string) bool {
				_, err := os.Stat(p)
				return err == nil
			})
		},
		execFn:      exec.Run,
		isLoadedFn:  func() bool { _, err := os.Stat("/proc/awgm_relay/version"); return err == nil },
		modLoadedFn: isModuleLoadedProc,
		procReadFn:  kmod.ReadProc,
		procWriteFn: func(p string, b []byte) error { return os.WriteFile(p, b, 0) },
	}
}

// relayKoPath — файл awgm_relay.ko под модель/SoC. resolveKoPathFor отдаёт
// arch-default, не проверяя файл (awg_proxy так и нужно), а сборки релея под
// SoC может не быть — без проверки отказ всплыл бы только ошибкой insmod.
func relayKoPath(model string, soc kmod.SoC, exists func(string) bool) (string, error) {
	if model == "" || soc == kmod.SoCUnknown {
		return "", errRelayModelUnknown
	}
	p, _, err := resolveKoPathFor("awgm_relay", model, soc, exists)
	if err != nil {
		return "", err
	}
	if !exists(p) {
		return "", fmt.Errorf("нет сборки awgm_relay.ko под %s/%s", model, soc)
	}
	return p, nil
}

// errRelayModelUnknown — модель/SoC роутера ещё не определены (ранний старт):
// не «сборки нет», поэтому не запоминается (F478).
var errRelayModelUnknown = errors.New("модель роутера ещё не определена")

// Available — ядро можно предлагать диспетчеру: модуль загружен или его файл
// под этот SoC есть и загрузка ещё не отказывала. Отказ resolve (кроме «модель
// ещё не определена») запоминается и пишется в журнал один раз: Available
// зовётся на каждый Start.
func (m *RelayKmod) Available() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failed != nil {
		return false
	}
	if m.isLoadedFn() {
		return true
	}
	if _, err := m.resolve(); err != nil {
		if errors.Is(err, errRelayModelUnknown) {
			return false
		}
		m.failed = err
		m.appLog.Info("awgm-relay", "", err.Error()+" — Phobos-туннели работают процессом")
		return false
	}
	return true
}

// Ensure грузит модуль, если он не загружен. Отказ запоминается: иначе insmod
// и Warn на каждом WAN-up (Start прилетает на каждый).
func (m *RelayKmod) Ensure(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failed != nil {
		return m.failed
	}
	if m.isLoadedFn() {
		return nil
	}
	path, err := m.resolve()
	if err != nil {
		if !errors.Is(err, errRelayModelUnknown) {
			m.failed = err
		}
		return err
	}
	if !m.modLoadedFn("udp_tunnel") {
		_, _ = m.execFn(ctx, "modprobe", "udp_tunnel")
	}
	if m.arm != nil {
		if err := m.arm(); err != nil {
			m.appLog.Warn("awgm-relay", "", "метка сторожа не записана: "+err.Error())
		}
	}
	res, err := m.execFn(ctx, "insmod", path)
	if err == nil && res != nil && res.ExitCode == 0 {
		m.appLog.Info("awgm-relay", "", "awgm_relay.ko загружен ("+path+")")
		if m.disarmAfter != nil {
			m.disarmAfter(relayKmodDisarmAfter)
		}
		return nil
	}
	if err == nil {
		err = fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	m.failed = fmt.Errorf("insmod %s: %w", path, err)
	if m.disarmAfter != nil {
		m.disarmAfter(0) // отказ insmod — метку снять сразу (§4.9)
	}
	m.appLog.Warn("awgm-relay", "", m.failed.Error()+" — Phobos-туннели работают процессом")
	return m.failed
}

// ReconcileVersion — на старте демона до первого Start (§4.2): загружена не
// та версия → снять все слоты и выгрузить; туннели поднимутся на новом
// модуле своими Start. Отказ rmmod → ядро недоступно до рестарта демона.
func (m *RelayKmod) ReconcileVersion(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.isLoadedFn() {
		return
	}
	v, _ := m.procReadFn("/proc/awgm_relay/version")
	if strings.TrimSpace(string(v)) == ExpectedRelayKmodVersion {
		return
	}
	if err := m.unloadLocked(ctx); err != nil {
		m.failed = err
		m.appLog.Warn("awgm-relay", "", "устаревший модуль не выгружен: "+err.Error()+" — Phobos-туннели работают процессом")
		return
	}
	m.appLog.Info("awgm-relay", "", "выгружен awgm_relay "+strings.TrimSpace(string(v))+", нужен "+ExpectedRelayKmodVersion)
}

// Unload — opkg remove: снять слоты и выгрузить (§4.6).
func (m *RelayKmod) Unload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.isLoadedFn() {
		return nil
	}
	return m.unloadLocked(ctx)
}

// unloadLocked: отказ del уходит в ошибку вместе с rmmod — иначе виден был бы
// только «in use» без причины (F477).
func (m *RelayKmod) unloadLocked(ctx context.Context) error {
	var delErrs []error
	if list, err := m.procReadFn("/proc/awgm_relay/list"); err == nil {
		for _, line := range strings.Split(string(list), "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				if err := m.procWriteFn("/proc/awgm_relay/del", []byte(f[0])); err != nil {
					delErrs = append(delErrs, fmt.Errorf("del %s: %w", f[0], err))
				}
			}
		}
	}
	res, err := m.execFn(ctx, "rmmod", "awgm_relay")
	if err == nil && res != nil && res.ExitCode != 0 {
		err = fmt.Errorf("rmmod: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return errors.Join(append(delErrs, err)...)
}
