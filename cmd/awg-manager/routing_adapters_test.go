package main

import (
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestExitRegistryAdapter(t *testing.T) {
	dir := t.TempDir()
	// S2: ТОЛЬКО WithLockDir. Конструктор без lockDir подставляет lock.LockDir
	// = /opt/var/lock/awg-manager (sys/lock/lock.go:16), то есть тест ушёл бы
	// в настоящий системный каталог локов. Имя того конструктора здесь не
	// пишется намеренно: ворота плана грепают его по тестам подстрокой.
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	// Журнал с nil-логгером — устоявшаяся в проекте форма для тестов
	// (internal/api/hook_test.go:40): методы ScopedLogger сами проверяют
	// appLogger == nil (logging/applogger.go:23).
	reg := exitreg.New(
		exitreg.NewStoreMirror(store, nil),
		logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubLifecycle),
	)

	// Гейт посева не подтверждён — и это не мешает объявлению: заперто
	// удаление, а не регистрация.
	if err := reg.SetDeclared([]exitreg.ExitDecl{{
		ID: "wdttraw-de", InstanceID: "de", Name: "Германия",
		NDMSName: "OpkgTun18", KernelIface: "opkgtun18",
	}}); err != nil {
		t.Fatal(err)
	}

	var a routing.ExitRegistry = exitRegistryAdapter{reg: reg}
	got, ok := a.LookupExit("wdttraw-de")
	if !ok || got.NDMSName != "OpkgTun18" || got.KernelIface != "opkgtun18" || got.Ready {
		t.Fatalf("адаптер потерял поля: %+v, %v", got, ok)
	}
	if _, ok := a.LookupExit("wdttraw-нет"); ok {
		t.Fatal("неизвестный выход обязан не находиться")
	}

	// Сверх брифа: объявление даёт Ready=false, поэтому потерянное поле
	// готовности выше неотличимо от сохранённого. А потеря эта fail-open
	// наоборот: каталог считал бы КАЖДЫЙ выход лежачим, и правила клиентов
	// вместе с соединениями молча уходили бы мимо туннеля.
	if err := reg.Ensure(linkres.ExitInfo{
		ID: "wdttraw-de", NDMSName: "OpkgTun18", KernelIface: "opkgtun18", Ready: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := a.LookupExit("wdttraw-de"); !ok || !got.Ready {
		t.Fatalf("адаптер потерял готовность: %+v, %v", got, ok)
	}
}
