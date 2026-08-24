package routing

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

type mockExits struct{ m map[string]ExitEntry }

func (m mockExits) LookupExit(id string) (ExitEntry, bool) {
	e, ok := m.m[id]
	return e, ok
}

func noExits() mockExits { return mockExits{m: map[string]ExitEntry{}} }

func TestExitResolvedFromRegistry(t *testing.T) {
	exits := mockExits{m: map[string]ExitEntry{
		"wdttraw-de": {NDMSName: "OpkgTun18", KernelIface: "opkgtun18", Ready: true},
	}}
	// Стор пуст намеренно: каталог обязан разрешать выход БЕЗ зеркальной записи.
	cat := NewCatalog(&mockTunnelProvider{}, nil, &mockStoreClient{}, exits, nil)

	if got, err := cat.GetKernelIfaceName(context.Background(), "wdttraw-de"); err != nil || got != "opkgtun18" {
		t.Fatalf("GetKernelIfaceName = %q, %v", got, err)
	}
	if got, err := cat.ResolveInterface(context.Background(), "wdttraw-de"); err != nil || got != "OpkgTun18" {
		t.Fatalf("ResolveInterface = %q, %v", got, err)
	}
	if iface, running := cat.GetKernelIface(context.Background(), "wdttraw-de"); iface != "opkgtun18" || !running {
		t.Fatalf("GetKernelIface = %q, %v", iface, running)
	}
	if !cat.Exists(context.Background(), "wdttraw-de") {
		t.Fatal("объявленный выход обязан существовать для правил")
	}
}

func TestDownExitResolvesButIsNotRunning(t *testing.T) {
	// §5: правило на лежачий выход обязано разрешаться в имя — иначе оно
	// становится невидимым вместо «выход недоступен».
	exits := mockExits{m: map[string]ExitEntry{
		"wdttraw-de": {NDMSName: "OpkgTun18", KernelIface: "opkgtun18", Ready: false},
	}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, &mockStoreClient{}, exits, nil)

	if got, err := cat.ResolveInterface(context.Background(), "wdttraw-de"); err != nil || got != "OpkgTun18" {
		t.Fatalf("имя обязано разрешаться и у лежачего выхода: %q, %v", got, err)
	}
	if _, running := cat.GetKernelIface(context.Background(), "wdttraw-de"); running {
		t.Fatal("лежачий выход не может быть running")
	}
	if got := cat.resolveNDMSName(TunnelWithStatus{ID: "wdttraw-de", Backend: backendWdttRaw}); got != "OpkgTun18" {
		t.Fatalf("лежачий выход обязан попадать в список: %q", got)
	}
}

func TestMirrorFallbackWhileRegistryIsSilent(t *testing.T) {
	// РЕЗИДУАЛ ВОЛНЫ (снимает план 5): движок ещё не проведён, записи ведёт
	// старый код. Готовность у такого ответа берётся по-старому — из
	// состояния провайдера.
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"wdttraw-de": {Backend: backendWdttRaw, RawNdmsIface: "OpkgTun18", RawKernelIface: "opkgtun18"},
	}}
	prov := &mockTunnelProvider{states: map[string]tunnel.StateInfo{
		"wdttraw-de": {State: tunnel.StateRunning},
	}}
	cat := NewCatalog(prov, nil, store, noExits(), nil)

	if got, err := cat.ResolveInterface(context.Background(), "wdttraw-de"); err != nil || got != "OpkgTun18" {
		t.Fatalf("фолбэк на зеркальную запись: %q, %v", got, err)
	}
	if iface, running := cat.GetKernelIface(context.Background(), "wdttraw-de"); iface != "opkgtun18" || !running {
		t.Fatalf("готовность на фолбэке — из состояния провайдера: %q, %v", iface, running)
	}
	prov.states["wdttraw-de"] = tunnel.StateInfo{State: tunnel.StateStopped}
	if _, running := cat.GetKernelIface(context.Background(), "wdttraw-de"); running {
		t.Fatal("остановленный выход на фолбэке не может быть running")
	}
}

func TestRegistryWinsOverStaleMirror(t *testing.T) {
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"wdttraw-de": {Backend: backendWdttRaw, RawNdmsIface: "OpkgTun17", RawKernelIface: "opkgtun17"},
	}}
	exits := mockExits{m: map[string]ExitEntry{
		"wdttraw-de": {NDMSName: "OpkgTun19", KernelIface: "opkgtun19", Ready: true},
	}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, store, exits, nil)
	if got, _ := cat.ResolveInterface(context.Background(), "wdttraw-de"); got != "OpkgTun19" {
		t.Fatalf("реестр обязан перебивать протухшее зеркало: %q", got)
	}
}

func TestNilStoreExitLookupDoesNotPanic(t *testing.T) {
	// C1/S3. Гард `c.store == nil` в lookupExit — паритет с сегодняшним
	// поведением resolveNDMSName (catalog.go:419-426): у raw-туннеля при
	// nil-сторе она отдаёт "" и не паникует. Резолвер заменяет собой тот
	// блок и обязан это сохранить.
	//
	// Тест зовёт именно resolveNDMSName. GetKernelIfaceName при nil-сторе
	// паникует на c.store.Get (:331) и ДО правки — «выживание» там было бы
	// не паритетом, а новым поведением чужой функции.
	cat := NewCatalog(&mockTunnelProvider{}, nil, nil, noExits(), nil)
	if got := cat.resolveNDMSName(TunnelWithStatus{ID: "wdttraw-de", Backend: backendWdttRaw}); got != "" {
		t.Fatalf("без стора и без реестра имя не разрешается: %q", got)
	}
}

func TestNonExitLookupCostsNoExtraStoreRead(t *testing.T) {
	// I1: резолвер выхода не имеет права добавить чтение файла на пути
	// ОБЫЧНОГО туннеля — эти вызовы идут в цикле по всем туннелям (ListAll),
	// по всем активным соединениям (connections/service.go:247) и по каждому
	// правилу clientroute (impl.go:381). Фолбэк на зеркало заперт проверкой
	// префикса ExitID, поэтому до диска он не доходит вовсе.
	store := &mockStoreClient{entries: map[string]StoreEntry{"awg10": {Backend: "kernel"}}}
	// Туннель БЕЖИТ намеренно: иначе GetKernelIface возвращается по состоянию
	// провайдера (catalog.go:296-299) и до хранилища не доходит вовсе — счётчик
	// показывал бы ноль на обеих сторонах правки и не стерёг бы ничего.
	prov := &mockTunnelProvider{states: map[string]tunnel.StateInfo{
		"awg10": {State: tunnel.StateRunning},
	}}
	cat := NewCatalog(prov, nil, store, noExits(), nil)

	store.gets = 0
	if _, err := cat.GetKernelIfaceName(context.Background(), "awg10"); err != nil {
		t.Fatal(err)
	}
	if store.gets != 1 {
		t.Fatalf("GetKernelIfaceName: чтений хранилища %d, было и обязано остаться 1", store.gets)
	}

	store.gets = 0
	if _, err := cat.ResolveInterface(context.Background(), "awg10"); err != nil {
		t.Fatal(err)
	}
	if store.gets != 1 {
		t.Fatalf("ResolveInterface: чтений хранилища %d, ожидалось 1", store.gets)
	}

	// M1 редакции 4: САМАЯ горячая из трёх точек. GetKernelIface зовут на каждое
	// активное соединение (connections/service.go:247, :296) и на каждое правило
	// clientroute (impl.go:381) — без неё страж цены не стерёг главное.
	// Сегодня здесь ДВА store.Get (catalog.go:301 и :304); после правки ветка
	// wdtt-raw уходит в lookupExit, который на не-выходе до диска не доходит.
	store.gets = 0
	if iface, running := cat.GetKernelIface(context.Background(), "awg10"); iface == "" || !running {
		t.Fatalf("GetKernelIface: %q, %v", iface, running)
	}
	if store.gets != 1 {
		t.Fatalf("GetKernelIface: чтений хранилища %d, ожидалось 1 (было 2 до правки)", store.gets)
	}
}

func TestUnknownExitIsErrorNotGarbageName(t *testing.T) {
	// M2: РЕГРЕССИОННЫЙ страж, а не падающий тест. Он ЗЕЛЁНЫЙ и до правки:
	// GetKernelIfaceName уже отвергает неизвестный id через c.store.Get
	// (catalog.go:331-334). Смысл — запереть эту гарантию на новом пути:
	// после правки lookupExit стоит ПЕРЕД store.Get, и уход в tunnel.NewNames
	// при !ok записал бы мусорное имя интерфейса в domain.conf HydraRoute.
	// Живой мутационный страж на это — пункт (5) в Step 5.
	cat := NewCatalog(&mockTunnelProvider{}, nil, &mockStoreClient{}, noExits(), nil)
	got, err := cat.GetKernelIfaceName(context.Background(), "wdttraw-ghost")
	if err == nil {
		t.Fatalf("неизвестный выход обязан быть ошибкой, получено %q", got)
	}
}

func TestExitWithoutAllocatedNamesFailsClosed(t *testing.T) {
	// Сверх брифа: три ветки «имя не выделено» иначе не покрыты ничем, а
	// именно они держат fail-closed. Их снятие отдаёт пустое имя интерфейса
	// как валидное — правило уходит в никуда, а HydraRoute пишет пустоту в
	// domain.conf. До правки то же самое стерегли проверки RawNdmsIface/
	// RawKernelIface != "" (catalog.go:255, :305, :340).
	exits := mockExits{m: map[string]ExitEntry{
		"wdttraw-de": {NDMSName: "", KernelIface: "", Ready: true},
	}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, &mockStoreClient{}, exits, nil)

	if got, err := cat.ResolveInterface(context.Background(), "wdttraw-de"); err == nil {
		t.Fatalf("ResolveInterface обязан отказать без NDMS-имени, получено %q", got)
	}
	if got, running := cat.GetKernelIface(context.Background(), "wdttraw-de"); got != "" || running {
		t.Fatalf("GetKernelIface = %q, %v; ожидалось \"\", false", got, running)
	}
	if got, err := cat.GetKernelIfaceName(context.Background(), "wdttraw-de"); err == nil {
		t.Fatalf("GetKernelIfaceName обязан отказать без kernel-имени, получено %q", got)
	}
}

func TestMirrorFallbackIgnoresForeignBackend(t *testing.T) {
	// Сверх брифа: проверка бэкенда внутри фолбэка иначе не покрыта. До
	// правки каждая из четырёх точек сверяла entry.Backend сама, и запись с
	// чужим бэкендом шла обычным путём независимо от того, как её назвали.
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"wdttraw-de": {Backend: "kernel", RawKernelIface: "opkgtun18"},
	}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, store, noExits(), nil)

	got, err := cat.GetKernelIfaceName(context.Background(), "wdttraw-de")
	if err != nil {
		t.Fatal(err)
	}
	if want := tunnel.NewNames("wdttraw-de").IfaceName; got != want {
		t.Fatalf("запись с чужим бэкендом обязана идти обычным путём: %q, ожидалось %q", got, want)
	}
}
