package wdtt

import (
	"path/filepath"
	"testing"
)

// startedServerWithExpose доводит сервер до живого процесса: интерфейсы «есть»
// по чекеру, NAT выключен (иначе старт полез бы в iptables), процесс подменён
// долгим sleep'ом. Возвращает сервис с работающим сервером.
func startedServerWithExpose(t *testing.T, expose bool) *Service {
	t.Helper()
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "/bin/sh")
	t.Cleanup(svc.Stop)
	svc.SetNDMSInterfaceCommands(&fakeOpkgCommands{})
	svc.SetInterfaceChecker(fakeIfaceChecker{exists: map[string]bool{
		"opkgtun17": true,
		"opkgtun18": true,
	}})
	cfg := ndmsServerConfigWithRaw()
	cfg.NatMode = "none"
	cfg.Password = "mainpass0000000000000000"
	cfg.ConfigDir = t.TempDir()
	cfg.ExposeToPolicies = expose
	if _, err := svc.UpdateServerInstance(DefaultInstanceID, cfg); err != nil {
		t.Fatal(err)
	}
	sleepSeam(svc.serverProcs.get(DefaultInstanceID))
	if err := svc.StartServerInstance(DefaultInstanceID); err != nil {
		t.Fatalf("старт сервера: %v", err)
	}
	return svc
}

// Применённое значение тумблера обязано быть в статусе: применяется он только
// на старте, и без него расхождение с выбранным никто определить не может.
// Проверяем ОБА значения: поле-указатель обязано отличать применённое false от
// «не знаем».
func TestServerStatusReportsAppliedExposeToPolicies(t *testing.T) {
	for _, expose := range []bool{true, false} {
		st := startedServerWithExpose(t, expose).Status()
		if !st.Server.Running {
			t.Fatalf("expose=%v: сервер не запустился", expose)
		}
		got := st.Server.AppliedExposeToPolicies
		if got == nil {
			t.Fatalf("expose=%v: применённое значение не отдано", expose)
		}
		if *got != expose {
			t.Fatalf("применённое значение %v, стартовали с %v", *got, expose)
		}
	}
}

// До старта применённого значения не существует — демон его не знает и гадать
// не должен. Тот же случай, что усыновление процесса от прошлого экземпляра
// демона: значение живёт в памяти и перезапуск не переживает.
func TestServerStatusHasNoAppliedExposeToPoliciesBeforeStart(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "/bin/sh")
	defer svc.Stop()
	cfg := ndmsServerConfigWithRaw()
	cfg.ExposeToPolicies = true
	if _, err := svc.UpdateServerInstance(DefaultInstanceID, cfg); err != nil {
		t.Fatal(err)
	}
	st := svc.Status()
	if st.Server.Running {
		t.Fatal("сервер не запускали, а он работает")
	}
	if st.Server.AppliedExposeToPolicies != nil {
		t.Fatalf("применённое значение выдумано до старта: %v", *st.Server.AppliedExposeToPolicies)
	}
}

// Остановленный сервер применённого значения не имеет: применённым оно было у
// процесса, которого больше нет.
func TestServerStatusDropsAppliedExposeToPoliciesAfterStop(t *testing.T) {
	svc := startedServerWithExpose(t, true)
	if err := svc.StopServerInstance(DefaultInstanceID); err != nil {
		t.Fatalf("стоп сервера: %v", err)
	}
	st := svc.Status()
	if st.Server.Running {
		t.Fatal("сервер остановлен, а статус говорит обратное")
	}
	if st.Server.AppliedExposeToPolicies != nil {
		t.Fatalf("применённое значение пережило стоп: %v", *st.Server.AppliedExposeToPolicies)
	}
}

// Свежий процесс не наследует применённое значение прошлого запуска. Окно
// реально: между успешным Start и записью значения статус уже отдаёт процесс
// живым, и показывать в нём чужое значение — то же враньё, что гадание.
func TestProcessStartClearsAppliedExposeToPolicies(t *testing.T) {
	p := newProcess("server", "/bin/sh", t.TempDir())
	sleepSeam(p)
	if err := p.Start(nil); err != nil {
		t.Fatalf("первый старт: %v", err)
	}
	p.setAppliedExposeToPolicies(true)
	if err := p.Stop(); err != nil {
		t.Fatalf("стоп: %v", err)
	}
	if err := p.Start(nil); err != nil {
		t.Fatalf("второй старт: %v", err)
	}
	defer func() { _ = p.Stop() }()
	st := p.Status()
	if !st.Running {
		t.Fatal("процесс не запустился")
	}
	if st.AppliedExposeToPolicies != nil {
		t.Fatalf("новый процесс унаследовал применённое значение: %v", *st.AppliedExposeToPolicies)
	}
}
