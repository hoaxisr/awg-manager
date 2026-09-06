// Package ndmsres — NDMS-ресурсы прокси-ролей: OpkgTun-интерфейс, адрес,
// admin state, публикация как выход и членство в политиках.
package ndmsres

import "context"

// Commands — мутации NDMS. Сигнатуры повторяют wdtt.NDMSOpkgTunCommands
// (ndms_iface.go:14-27) плюс кандидатура default route: прод-реализация —
// существующий ndmscommand.InterfaceCommands (адаптер плана 5 добавляет
// только EnsureDefaultRouteCandidacy).
type Commands interface {
	CreateOpkgTunWithSecurityLevel(ctx context.Context, name, description, securityLevel string) error
	DeleteOpkgTun(ctx context.Context, name string) error
	SetDescription(ctx context.Context, name, description string) error
	SetSecurityLevel(ctx context.Context, name, level string) error
	SetIPGlobal(ctx context.Context, name string) error
	// ClearIPGlobal снимает `ip global` (обратная команда есть: стенд 2026-09-06).
	ClearIPGlobal(ctx context.Context, name string) error
	SetAddress(ctx context.Context, name, address, mask string) error
	ClearAddress(ctx context.Context, name string) error
	SetMTU(ctx context.Context, name string, mtu int) error
	InterfaceUp(ctx context.Context, name string) error
	InterfaceDown(ctx context.Context, name string) error
	SetPermitAllACL(ctx context.Context, name string) error
	RemovePermitAllACL(ctx context.Context, name string) error
	// EnsureDefaultRouteCandidacy объявляет интерфейс КАНДИДАТОМ в default
	// route политики (запись `ip route default interface X`). Семантика
	// «кандидатура, не захват» — допущение §13 спеки, стендовый гейт волны.
	EnsureDefaultRouteCandidacy(ctx context.Context, name string) error
}

// IfaceFacts — наблюдаемое состояние NDMS-интерфейса. Прод-адаптер (план 5)
// собирает его из ndms.Interface (types.go:7-24): Address, Mask, MTU,
// SecurityLevel, Description; AdminUp — из ConfLayer == "running".
type IfaceFacts struct {
	Description   string
	SecurityLevel string
	Address       string
	Mask          string
	MTU           int
	AdminUp       bool
	// Broken — запись NDMS в состоянии error: устройства за ней нет.
	// Проверено на стенде 5.01.C.3.0-1: после сноса устройства запись живёт
	// дальше со `state: error`, а ConfLayer остаётся `running` — то есть
	// AdminUp говорит «поднят» про интерфейс, которого не существует.
	Broken bool
}

// Query — наблюдения NDMS. ok=false у Iface означает «интерфейса нет»
// (подтверждённое отсутствие); ошибка — «не смогли посмотреть».
type Query interface {
	Iface(ctx context.Context, name string) (IfaceFacts, bool, error)
	HasIPGlobal(ctx context.Context, name string) (bool, error)
	HasPermitAllACL(ctx context.Context, name string) (bool, error)
	HasDefaultRoute(ctx context.Context, name string) (bool, error)
}
