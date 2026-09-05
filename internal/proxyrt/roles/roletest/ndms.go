// Package roletest — обвязка для тестов ШВА «роль → желаемое состояние
// ресурса».
//
// Зачем отдельный пакет: ресурсы (`ndmsres`, `netres`, …) проверены сами по
// себе, роли проверены сами по себе, а КАКИЕ значения роль кладёт в ресурс не
// проверял никто — аудит покрытия назвал это сквозной дырой, и три мутации
// (чужая метка владения, подменённый адрес клиента, MASQUERADE на lo) прошли
// по всему дереву незамеченными.
//
// Желаемое состояние ресурсов лежит в неэкспортированных полях, и наружу оно
// видно ТОЛЬКО через вызовы к зависимостям. Поэтому обвязка — не «снимок
// желаемого», а модель роутера: роль объявляет ресурсы, настоящий
// реконсилятор их доводит, а тест смотрит, что в модели осело.
//
// Пакет линкуется только из _test.go обеих ролей.
package roletest

import (
	"context"
	"fmt"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/ndmsres"
)

// ExitFlags — взведённые для интерфейса признаки выхода.
type ExitFlags struct {
	IPGlobal     bool
	PermitAll    bool
	DefaultRoute bool
}

// NDMS — мутирующая модель роутера: реализует и Commands, и Query поверх
// ОДНОГО состояния.
//
// Ассерты по финальному состоянию, а не по журналу вызовов: движок делает
// несколько проходов и вправе повторять шаги, и пин на число вызовов ломался
// бы от любой правки планировщика, ничего не защищая.
type NDMS struct {
	mu    sync.Mutex
	Facts map[string]ndmsres.IfaceFacts
	Flags map[string]ExitFlags
}

func NewNDMS() *NDMS {
	return &NDMS{
		Facts: map[string]ndmsres.IfaceFacts{},
		Flags: map[string]ExitFlags{},
	}
}

// Snapshot — копия фактов интерфейса и признак его существования.
func (n *NDMS) Snapshot(name string) (ndmsres.IfaceFacts, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	f, ok := n.Facts[name]
	return f, ok
}

// ExitOf — копия признаков выхода.
func (n *NDMS) ExitOf(name string) ExitFlags {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.Flags[name]
}

func (n *NDMS) CreateOpkgTunWithSecurityLevel(_ context.Context, name, description, level string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.Facts[name]; ok {
		return fmt.Errorf("создание существующего %s", name)
	}
	n.Facts[name] = ndmsres.IfaceFacts{Description: description, SecurityLevel: level}
	return nil
}

func (n *NDMS) DeleteOpkgTun(_ context.Context, name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.Facts, name)
	delete(n.Flags, name)
	return nil
}

func (n *NDMS) SetDescription(_ context.Context, name, description string) error {
	return n.edit(name, func(f *ndmsres.IfaceFacts) { f.Description = description })
}

func (n *NDMS) SetSecurityLevel(_ context.Context, name, level string) error {
	return n.edit(name, func(f *ndmsres.IfaceFacts) { f.SecurityLevel = level })
}

func (n *NDMS) SetAddress(_ context.Context, name, address, mask string) error {
	return n.edit(name, func(f *ndmsres.IfaceFacts) { f.Address, f.Mask = address, mask })
}

func (n *NDMS) ClearAddress(_ context.Context, name string) error {
	return n.edit(name, func(f *ndmsres.IfaceFacts) { f.Address, f.Mask = "", "" })
}

func (n *NDMS) SetMTU(_ context.Context, name string, mtu int) error {
	return n.edit(name, func(f *ndmsres.IfaceFacts) { f.MTU = mtu })
}

func (n *NDMS) InterfaceUp(_ context.Context, name string) error {
	return n.edit(name, func(f *ndmsres.IfaceFacts) { f.AdminUp = true })
}

func (n *NDMS) InterfaceDown(_ context.Context, name string) error {
	return n.edit(name, func(f *ndmsres.IfaceFacts) { f.AdminUp = false })
}

func (n *NDMS) SetIPGlobal(_ context.Context, name string) error {
	return n.flag(name, func(e *ExitFlags) { e.IPGlobal = true })
}

func (n *NDMS) SetPermitAllACL(_ context.Context, name string) error {
	return n.flag(name, func(e *ExitFlags) { e.PermitAll = true })
}

func (n *NDMS) RemovePermitAllACL(_ context.Context, name string) error {
	return n.flag(name, func(e *ExitFlags) { e.PermitAll = false })
}

func (n *NDMS) EnsureDefaultRouteCandidacy(_ context.Context, name string) error {
	return n.flag(name, func(e *ExitFlags) { e.DefaultRoute = true })
}

func (n *NDMS) Iface(_ context.Context, name string) (ndmsres.IfaceFacts, bool, error) {
	f, ok := n.Snapshot(name)
	return f, ok, nil
}

func (n *NDMS) HasIPGlobal(_ context.Context, name string) (bool, error) {
	return n.ExitOf(name).IPGlobal, nil
}

func (n *NDMS) HasPermitAllACL(_ context.Context, name string) (bool, error) {
	return n.ExitOf(name).PermitAll, nil
}

func (n *NDMS) HasDefaultRoute(_ context.Context, name string) (bool, error) {
	return n.ExitOf(name).DefaultRoute, nil
}

// edit — правка фактов существующего интерфейса. Правка несуществующего это
// ОТКАЗ, а не тихое создание: иначе фикстура прощала бы роли пропущенный
// create, и шов «сначала завести, потом настраивать» перестал бы проверяться.
func (n *NDMS) edit(name string, mut func(*ndmsres.IfaceFacts)) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	f, ok := n.Facts[name]
	if !ok {
		return fmt.Errorf("правка несуществующего интерфейса %s", name)
	}
	mut(&f)
	n.Facts[name] = f
	return nil
}

// flag — в отличие от edit, существования интерфейса НЕ требует.
//
// Асимметрия осознанная, но её надо знать: роль, взводящая признаки выхода
// ДО создания интерфейса, в модели сойдётся зелёной, хотя в проде команда
// ушла бы в никуда. Понадобится пин на этот порядок — сначала добавить
// проверку сюда. Здесь же второе расхождение с продом: создание
// существующего интерфейса модель считает ошибкой, а POST в NDMS
// идемпотентен.
func (n *NDMS) flag(name string, mut func(*ExitFlags)) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	e := n.Flags[name]
	mut(&e)
	n.Flags[name] = e
	return nil
}

var (
	_ ndmsres.Commands = (*NDMS)(nil)
	_ ndmsres.Query    = (*NDMS)(nil)
)
