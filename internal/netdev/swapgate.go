package netdev

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// SwapHoldMax — потолок удержания барьера одной подменой устройства: ctx
// внутри Hold истекает через него, и зависший `ip` убивается
// (exec.CommandContext), а барьер отпускается.
const SwapHoldMax = 2 * time.Second

// SwapGate — барьер между нашими чтениями списка интерфейсов NDMS
// (GET /show/interface/) и подменой kernel-устройства под записью OpkgTunN
// (D-N1).
//
// Зачем. Запись без устройства, прочитанная списком, — `C 0xcffd0767` в
// журнале ndm (стенд K-0767 3/3). Атомарной подмены средствами ядра нет:
// между `ip link del` и `ip link add` запись живёт без устройства. Читатели,
// которые нас касаются, — наши (демон и наша панель), поэтому зазор
// закрывается барьером на них: пока идёт подмена, наши списки ждут (П3,
// стенд 05.10: без барьера 1/20 `0767`, под барьером 0/20). Дерево rc и
// running-config в том же зазоре C не дают (N5) — они вне барьера.
//
// Читатель — Read (RLock); единственный в прод-коде —
// query.InterfaceStore.fetchListMap. Писатель — Hold (Lock). Примитивы
// устройств (del/add/tuntap) существуют только как методы Swapper, а Swapper
// выдаёт только Hold: снять или создать устройство мимо барьера компилятор не
// даёт (N1). Нулевое значение готово к работе; экземпляр на процесс один
// (wiring_core.go), уборка при удалении пакета — свой.
//
// Дедлоки:
//   - fn не читает список: вложенный Read внутри Hold ждал бы сам себя.
//     Hold зовут только internal/tunnel/backend и этот пакет, оба не
//     зависят от internal/ndms/query (TestHoldCallers_CannotReadList);
//   - вложенный Hold невозможен: у fn есть только Swapper, гейта у неё нет;
//   - Read не держит замков стора (HTTP вне s.mu) и не ждёт ничего, кроме
//     RCI; порядок замков один: замок туннеля → гейт.
//
// Ожидание Hold ограничено списком в полёте (≤ таймаут RCI); ждущий Lock
// задерживает новые Read на то же время — Фаза 2 старта может подождать
// один список, это принято.
type SwapGate struct {
	mu sync.RWMutex
}

// Read входит читателем списка; release обязателен (defer). Не прерывается
// по ctx: ждёт не дольше одной подмены (SwapHoldMax).
func (g *SwapGate) Read() (release func()) {
	g.mu.RLock()
	return g.mu.RUnlock
}

// Hold — подмена устройства под барьером: ждёт читателей в полёте, держит
// новых, пока идёт fn.
//
// ctx fn — context.WithoutCancel(ctx) с потолком SwapHoldMax (N4): обрыв
// HTTP-запроса вызывающего не рвёт подмену посередине, зависший `ip`
// убивается по потолку.
//
// Исход таймаута (N4): `del` прошёл, `add` убит — до возврата Hold запись
// живёт без устройства, и ждавшие читатели после отпускания могут дать
// `0767` (аварийный остаток: только при зависании `ip` дольше SwapHoldMax).
// Вызывающий получает ошибку; KernelBackend.Start → ошибка → откат старта
// (существующая запись — ReplaceWithTun: устройства нет → только `tuntap
// add`; созданная попыткой — сразу `no interface`), следующий Start
// подменяет штатно — fail-closed и восстановимо. `del` убит — устройство
// осталось, Start вернёт ошибку и повторится следующим.
//
// Требование к fn (N11): только ctx-aware exec `ip` через методы Swapper.
// Обход /proc (держатель tun), stat, RCI — ДО Hold: Read не прерываем, и
// всё, что fn делает сверх `ip`, читатели списка ждут. Swapper вне fn
// недействителен: сохранённый и вызванный после возврата Hold, он отвечает
// ErrSwapperDone и `ip` не запускает (снос/создание мимо барьера).
func (g *SwapGate) Hold(ctx context.Context, fn func(sw Swapper) error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SwapHoldMax)
	defer cancel()
	sw := &swapper{ctx: ctx}
	defer sw.done.Store(true) // раньше Unlock: defer — в обратном порядке
	return fn(sw)
}

// ErrSwapperDone — примитив Swapper вызван после возврата Hold.
var ErrSwapperDone = errors.New("netdev: Swapper вызван после Hold")

// DeleteLink — `ip link del` под барьером для тех, у кого нет бэкенда
// (fakeip-tun в sing-box роутере).
func (g *SwapGate) DeleteLink(ctx context.Context, name string) error {
	return g.Hold(ctx, func(sw Swapper) error { return sw.LinkDel(name) })
}

// Swapper — примитивы kernel-устройств; живёт только внутри Hold.
type Swapper interface {
	// LinkDel — `ip link del dev name`.
	LinkDel(name string) error
	// LinkAdd — `ip link add dev name type kind`.
	LinkAdd(name, kind string) error
	// TuntapAdd — `ip tuntap add dev name mode tun` (persistent plain tun,
	// как у NDMS по записи OpkgTunN).
	TuntapAdd(name string) error
	// Context — ctx удержания (WithoutCancel + SwapHoldMax).
	Context() context.Context
}

// runIP — шов над exec.Run; тесты подменяют через StubRunIP.
var runIP = exec.Run

// StubRunIP подменяет запуск `ip` у Swapper и возвращает восстановление.
// Только для тестов: прод-вызов ловит TestNoBareLinkCommands.
func StubRunIP(fn func(ctx context.Context, name string, args ...string) (*exec.Result, error)) (restore func()) {
	old := runIP
	runIP = fn
	return func() { runIP = old }
}

const ipBin = "/opt/sbin/ip"

type swapper struct {
	ctx  context.Context
	done atomic.Bool // fn вернулась — примитивы больше не работают
}

func (s *swapper) Context() context.Context {
	if s.ctx == nil {
		panic("netdev: Swapper вне Hold")
	}
	return s.ctx
}

func (s *swapper) run(args ...string) error {
	if s.done.Load() {
		return ErrSwapperDone
	}
	res, err := runIP(s.Context(), ipBin, args...)
	if err != nil {
		return exec.FormatError(res, err)
	}
	return nil
}

func (s *swapper) LinkDel(name string) error {
	if err := s.run("link", "del", "dev", name); err != nil {
		return fmt.Errorf("ip link del %s: %w", name, err)
	}
	return nil
}

func (s *swapper) LinkAdd(name, kind string) error {
	if err := s.run("link", "add", "dev", name, "type", kind); err != nil {
		return fmt.Errorf("ip link add %s type %s: %w", name, kind, err)
	}
	return nil
}

func (s *swapper) TuntapAdd(name string) error {
	if err := s.run("tuntap", "add", "dev", name, "mode", "tun"); err != nil {
		return fmt.Errorf("ip tuntap add %s: %w", name, err)
	}
	return nil
}
