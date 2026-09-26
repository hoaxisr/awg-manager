package obfuscator

import (
	"context"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type Relay interface {
	Start(ctx context.Context, tunnelID string, o *storage.Obfuscator, ip string) error
	Stop(tunnelID string) error
	Alive(tunnelID string) bool
	Backend(tunnelID string) string
}

var _ Relay = (*Runner)(nil)
var _ Relay = (*KernelRunner)(nil)

// Dispatcher выбирает бэкенд релея на каждый Start (спека §4.2): ядро — если
// useKernel разрешает (Phobos, выключатель, модуль, IPv4), иначе процесс.
// Перед стартом на одном бэкенде туннель гасится на другом.
//
// Инвариант: Start/Stop ОДНОГО туннеля вызывающий обязан сериализовать
// (per-tunnel лок оркестратора/стража). mu бережёт только карту chosen, но не
// пару «погасить на одном — поднять на другом»: на этой сериализации держатся
// «двух релеев на одном loopback-порту нет» и соответствие chosen живому бэкенду.
type Dispatcher struct {
	process, kernel Relay
	useKernel       func(o *storage.Obfuscator, ip string) bool
	log             *logging.ScopedLogger
	mu              sync.Mutex
	chosen          map[string]Relay
}

func NewDispatcher(process, kernel Relay, useKernel func(o *storage.Obfuscator, ip string) bool, log *logging.ScopedLogger) *Dispatcher {
	return &Dispatcher{process: process, kernel: kernel, useKernel: useKernel, log: log, chosen: map[string]Relay{}}
}

func (d *Dispatcher) Start(ctx context.Context, tunnelID string, o *storage.Obfuscator, ip string) error {
	want, other := d.process, d.kernel
	if d.kernel != nil && d.useKernel(o, ip) {
		want, other = d.kernel, d.process
	}
	if other != nil {
		_ = other.Stop(tunnelID)
	}
	err := want.Start(ctx, tunnelID, o, ip)
	if err != nil && want == d.kernel {
		if d.log != nil {
			d.log.Warn("obfuscator", tunnelID, "kernel-релей не поднялся: "+err.Error()+" — работаем процессом")
		}
		_ = d.kernel.Stop(tunnelID)
		want = d.process
		err = want.Start(ctx, tunnelID, o, ip)
	}
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.chosen[tunnelID] = want
	d.mu.Unlock()
	return nil
}

func (d *Dispatcher) Stop(tunnelID string) error {
	d.mu.Lock()
	r := d.chosen[tunnelID]
	delete(d.chosen, tunnelID)
	d.mu.Unlock()
	if r != nil {
		return r.Stop(tunnelID)
	}
	err := d.process.Stop(tunnelID)
	if d.kernel != nil {
		_ = d.kernel.Stop(tunnelID)
	}
	return err
}

func (d *Dispatcher) Alive(tunnelID string) bool {
	if r := d.current(tunnelID); r != nil {
		return r.Alive(tunnelID)
	}
	return d.process.Alive(tunnelID) || (d.kernel != nil && d.kernel.Alive(tunnelID))
}

func (d *Dispatcher) Backend(tunnelID string) string {
	if r := d.current(tunnelID); r != nil {
		return r.Backend(tunnelID)
	}
	if d.kernel != nil {
		if b := d.kernel.Backend(tunnelID); b != "" {
			return b
		}
	}
	return d.process.Backend(tunnelID)
}

func (d *Dispatcher) current(tunnelID string) Relay {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.chosen[tunnelID]
}
