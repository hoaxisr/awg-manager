package singbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// markProxyMgrDur — ВРЕМЕННЫЙ helper для perf-diagnostics. Логирует через
// slog.Default — в этом пакете нет ScopedLogger в ProxyManager. Удалить
// после perf-сессии 2026-05-23.
func markProxyMgrDur(label string, start time.Time) {
	slog.Info("perf-proxy", "label", label, "ms", time.Since(start).Milliseconds())
}

// maxProxySlots caps how many ProxyN slots we will scan when looking for
// a free index. Keenetic does not publish an official ceiling; 128 is
// well above any realistic tunnel count and bounds the loop in NextFreeIndex
// in case NDMS ever returns something unexpected.
const maxProxySlots = 128

// TunnelInboundPortRange returns the inclusive listen-port range reserved for
// sing-box tunnel inbounds (firstPort+slot). Exported so port-conflict
// validation elsewhere reads the same numbers instead of copying them.
func TunnelInboundPortRange() (first, last int) {
	return firstPort, firstPort + maxProxySlots - 1
}

// ErrProxyComponentMissing is returned when the router lacks the NDMS
// "proxy" component. Without it, no ProxyN interface can be created, so
// sing-box cannot route any traffic. Surfaced to the UI as a distinct
// state (separate from generic RCI errors) so we can show the user how
// to fix it instead of a raw NDMS error string.
var ErrProxyComponentMissing = fmt.Errorf("NDMS 'proxy' component is not installed — sing-box integration unavailable")

// ProxyManager orchestrates NDMS Proxy interfaces for sing-box tunnels.
// Reads go through queries.Interfaces (GetProxy helper); writes through
// commands.Proxies.
// ndmsProxies — то, что Operator знает про NDMS-прокси. Интерфейс, а не
// *ProxyManager, ради шва: в проде за ним ProxyManager поверх RCI, в тестах —
// фейк. Без него пути, трогающие прокси (RemoveTunnel, AddTunnels,
// RenameTunnel), из теста не запускались вовсе: ProxyManager с нулевыми
// Queries/Commands разыменовывает nil.
type ndmsProxies interface {
	EnsureProxy(ctx context.Context, index, port int, description, ownedDesc string) error
	RelabelProxy(ctx context.Context, index, port int, description, ownedDesc string) error
	NextFreeIndex(ctx context.Context, reserved map[int]bool) (int, error)
	RemoveProxy(ctx context.Context, index int, desc string) error
	OwnedProxies(ctx context.Context, tunnelProxies, subProxies map[string]string) ([]ProxyMark, error)
	RemoveMarkedProxy(ctx context.Context, m ProxyMark) (MarkedOutcome, error)
	AdoptBareProxy(ctx context.Context, name string, port int, description string) (MarkedOutcome, error)
	ListNativeProxies(ctx context.Context, tunnelTags map[string]bool, subProxyIdx map[int]bool) ([]string, error)
	SyncProxies(ctx context.Context, tunnels []TunnelInfo) error
}

var _ ndmsProxies = (*ProxyManager)(nil)

type ProxyManager struct {
	queries  *query.Queries
	commands *command.Commands
	marks    proxyMarks // nil — меток нет: голая запись всегда чужая
}

// proxyMarks — метки отложенного сноса оператора (F562), которые видит
// ProxyManager (F577): созданное и оставленное на роутере метится здесь же,
// на любом пути создания; голая запись (description "") наша, только пока
// есть метка (name, "") — доказательство, что её создали мы.
type proxyMarks interface {
	deferLeftProxy(name, desc string)
	bareMarked(name string) bool
	clearBareMark(name string)
}

func (pm *ProxyManager) bareMarked(name string) bool {
	return pm.marks != nil && pm.marks.bareMarked(name)
}

// adopted — запись name настроена нами: метка голой записи больше не нужна.
func (pm *ProxyManager) adopted(name string) {
	if pm.marks != nil {
		pm.marks.clearBareMark(name)
	}
}

func NewProxyManager(q *query.Queries, c *command.Commands) *ProxyManager {
	return &ProxyManager{queries: q, commands: c}
}

// ErrProxyForeign — на индексе стоит ProxyN, который не наш: его description
// не совпал с ожидаемым, или NDMS не создал запись — имя уже занято
// (command.ErrNotCreated, тогда обёрнуты оба). Команд по нему нет (F577).
var ErrProxyForeign = errors.New("ProxyN занят чужой записью")

// EnsureProxy — ProxyN индекса из хранилища, указывающий на 127.0.0.1:port,
// с description. По свежему списку (F577): записи нет — создание
// (CreateProxy); запись есть — настраивается, только если она наша.
// Наша — description записи равен ownedDesc (то, что мы ставили: при
// переименовании — СТАРЫЙ тег/Label) или уже равен description (повтор
// после применённой настройки). Иначе ErrProxyForeign без команд: индекс в
// хранилище переживает выключение режима и сбои, и его мог занять
// пользовательский KeenOS-прокси (F562). Принятый остаток: пользовательский
// прокси с ТОЧНО таким же description, как наш тег/Label, считается нашим.
// Голая запись (description "") с меткой (name, "") — наша сирота:
// настраивается и метка снимается (F577).
// Компонента proxy нет — ErrProxyComponentMissing до NDMS.
func (pm *ProxyManager) EnsureProxy(ctx context.Context, index, port int, description, ownedDesc string) error {
	defer markProxyMgrDur(fmt.Sprintf("EnsureProxy(%d)", index), time.Now())
	return pm.ensureProxy(ctx, index, port, description, ownedDesc, true)
}

// RelabelProxy — EnsureProxy без создания: записи нет — ничего (nil).
// Возврат description при откате переименования.
func (pm *ProxyManager) RelabelProxy(ctx context.Context, index, port int, description, ownedDesc string) error {
	return pm.ensureProxy(ctx, index, port, description, ownedDesc, false)
}

func (pm *ProxyManager) ensureProxy(ctx context.Context, index, port int, description, ownedDesc string, create bool) error {
	if !ndmsinfo.HasProxyComponent() {
		return ErrProxyComponentMissing
	}
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	c, iface, ok, err := pm.queries.Interfaces.Confirm(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		if !create {
			return nil
		}
		_, err := pm.CreateProxy(ctx, index, port, description)
		if errors.Is(err, command.ErrNotCreated) {
			return fmt.Errorf("%w: %w", ErrProxyForeign, err)
		}
		return err
	}
	if iface == nil || iface.Description != ownedDesc && iface.Description != description && !pm.bareOurs(iface) {
		return foreignProxy(name, iface, ownedDesc)
	}
	if err := pm.commands.Proxies.ConfigureProxy(ctx, c, description, "127.0.0.1", port, true); err != nil {
		return err
	}
	pm.adopted(name)
	return nil
}

// bareOurs — голая запись с меткой (name, ""): наша сирота (F577). Без метки
// пустой description чужой.
func (pm *ProxyManager) bareOurs(iface *ndms.Interface) bool {
	return iface.Description == "" && pm.bareMarked(iface.ID)
}

// CreateProxy — создание ProxyN на только что выбранном свободным индексе
// (command.ProxyCommands.CreateProxy: голое создание → подтверждение →
// настройки, F577). Индекс, занятый чужим ещё не видимым ProxyN, — ошибка
// command.ErrNotCreated без настроек и сноса.
// ours — откат вправе снести ProxyN (command.CreateReply.Proven, F574).
func (pm *ProxyManager) CreateProxy(ctx context.Context, index, port int, description string) (ours bool, err error) {
	if !ndmsinfo.HasProxyComponent() {
		return false, ErrProxyComponentMissing
	}
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	_, reply, err := pm.commands.Proxies.CreateProxy(ctx, name, description, "127.0.0.1", port, true)
	// Созданное и оставленное на роутере — в метку отложенного сноса с
	// description, который у записи там; на любом пути создания (F577 N1).
	var left *command.LeftCreatedError
	if errors.As(err, &left) && pm.marks != nil {
		pm.marks.deferLeftProxy(left.Name, left.Desc)
	}
	if err == nil {
		// Создано и настроено заново — прежняя метка голой записи с этим
		// именем (её уже нет в списке) больше ничего не доказывает.
		pm.adopted(name)
	}
	return reply.Proven(), err
}

// NextFreeIndex returns the lowest ProxyN index not occupied on the
// router. The NDMS namespace is shared with whatever the user created
// manually through the router UI, so we must scan /show/interface/
// before picking a slot — otherwise CreateProxy would silently mutate
// the user's existing Proxy0. Скан — свой свежий список, не память
// (InterfaceStore.FreeIndex, F574). reserved lets a batch allocator skip
// indices it has already handed out earlier in the same batch, before
// those ProxyN interfaces have been committed to NDMS.
func (pm *ProxyManager) NextFreeIndex(ctx context.Context, reserved map[int]bool) (int, error) {
	defer markProxyMgrDur("NextFreeIndex", time.Now())
	idx, ok, err := pm.queries.Interfaces.FreeIndex(ctx, proxyIfacePrefix, maxProxySlots, reserved)
	if err != nil {
		return 0, err
	}
	if ok {
		return idx, nil
	}
	return 0, fmt.Errorf("no free Proxy slot (scanned %d)", maxProxySlots)
}

// RemoveProxy снимает ProxyN, только если он наш: description в свежем списке
// равен desc (тег туннеля / Label подписки), как у RemoveMarkedProxy. Иначе
// ErrProxyForeign без команд: индекс мог занять пользовательский прокси
// (F577).
func (pm *ProxyManager) RemoveProxy(ctx context.Context, index int, desc string) error {
	defer markProxyMgrDur(fmt.Sprintf("RemoveProxy(%d)", index), time.Now())
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	// Прокси нет в NDMS — снимать нечего, и слать ничего нельзя: `interface
	// ProxyN down` по отсутствующему имени NDMS СОЗДАЁТ запись, `no` тут же
	// её сносит, а запоздалый хук ifcreated читает уже снятую — E «unable to
	// find» в журнале NDMS (стенд 5.01.C.6, F546). Список не прочитан — «не
	// знаем»: ошибка без команд, запись владельца остаётся для повтора.
	c, iface, ok, err := pm.queries.Interfaces.Confirm(ctx, name)
	if err != nil || !ok {
		return err
	}
	if iface == nil || iface.Description != desc {
		return foreignProxy(name, iface, desc)
	}
	_ = pm.commands.Proxies.ProxyDown(ctx, c) // ignore error — may be already down
	return pm.commands.Proxies.DeleteProxy(ctx, c)
}

func foreignProxy(name string, iface *ndms.Interface, want string) error {
	got := ""
	if iface != nil {
		got = iface.Description
	}
	return fmt.Errorf("%w: %s (description %q, ждали %q)", ErrProxyForeign, name, got, want)
}

// ProxyMark — ProxyN, который мы создали и не смогли снять: точная пара
// (имя, description) на момент метки (F562). Снос — только записи, у которой
// в свежем списке совпадает и то и другое.
type ProxyMark struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// errProxyListUnread — RemoveMarkedProxy не прочитал список: следующие метки
// того же тика упрутся в тот же отказ (ре-ревью N2).
var errProxyListUnread = errors.New("interface list unread")

// MarkedOutcome — чем кончился RemoveMarkedProxy без ошибки.
type MarkedOutcome int

const (
	MarkedRemoved MarkedOutcome = iota // снят
	MarkedAbsent                       // записи нет — снимать нечего
	MarkedForeign                      // description другой — запись уже не наша
	MarkedAdopted                      // голая запись настроена владельцем
)

// OwnedProxies — наши ProxyN по свежему списку (Confirm, не память, F562).
// tunnelProxies — имя ProxyN туннеля → его тег: наш, если description равен
// тегу или запись голая с меткой (name, "") (F577: без метки пустой
// description чужой); subProxies — имя ProxyN
// подписки → её Label: наш, только если description равен Label. Одного
// имени мало: индекс подписки в store переживает MigrateOff, и
// пользовательский прокси, занявший освободившееся имя, был бы снесён
// (ре-ревью N1). Одного description тоже мало: пользовательский KeenOS-прокси
// с description, равным тегу, был бы снесён (ревью F1). Для
// уборки режима NDMS Proxy off: найденное уходит в метки отложенного сноса.
// Список не прочитан — ошибка.
func (pm *ProxyManager) OwnedProxies(ctx context.Context, tunnelProxies, subProxies map[string]string) ([]ProxyMark, error) {
	// Кандидаты — только имена туннелей и подписок, по Confirm, а не по своему
	// Snapshot: в ctx тика уборки (WithActionList) это тот же список, что у
	// сноса меток следом (F597).
	names := make([]string, 0, len(tunnelProxies)+len(subProxies))
	for n := range tunnelProxies {
		names = append(names, n)
	}
	for n := range subProxies {
		if _, dup := tunnelProxies[n]; !dup {
			names = append(names, n)
		}
	}
	var out []ProxyMark
	for _, name := range names {
		if !strings.HasPrefix(name, proxyIfacePrefix) {
			continue
		}
		_, iface, ok, err := pm.queries.Interfaces.Confirm(ctx, name)
		if err != nil {
			return nil, err
		}
		if !ok || iface == nil {
			continue
		}
		tag, tunnel := tunnelProxies[name]
		label, sub := subProxies[name]
		if tunnel && (iface.Description == tag || pm.bareOurs(iface)) || sub && iface.Description == label {
			out = append(out, ProxyMark{Name: name, Desc: iface.Description})
		}
	}
	return out, nil
}

// RemoveMarkedProxy снимает m.Name, только если он есть в свежем списке и
// его description равен m.Desc: по одному description (свободный текст
// пользователя) чужой KeenOS-прокси не отличить от нашего. Нет записи или
// description другой — без команд. Список не прочитан (errProxyListUnread)
// или NDMS отказал — ошибка.
func (pm *ProxyManager) RemoveMarkedProxy(ctx context.Context, m ProxyMark) (MarkedOutcome, error) {
	c, iface, ok, err := pm.queries.Interfaces.Confirm(ctx, m.Name)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errProxyListUnread, err)
	}
	if !ok {
		return MarkedAbsent, nil
	}
	if iface == nil || iface.Description != m.Desc {
		return MarkedForeign, nil
	}
	_ = pm.commands.Proxies.ProxyDown(ctx, c) // ignore error — may be already down
	if err := pm.commands.Proxies.DeleteProxy(ctx, c); err != nil {
		return 0, err
	}
	return MarkedRemoved, nil
}

// AdoptBareProxy — тик: метка (name, "") на имени живого владельца. Только
// запись с этим именем и пустым description настраивается с description и
// port владельца (как Sync, F577 R1) → MarkedAdopted. Записи нет —
// MarkedAbsent, description не пуст — MarkedForeign: без команд. Список не
// прочитан (errProxyListUnread) или NDMS отказал — ошибка.
func (pm *ProxyManager) AdoptBareProxy(ctx context.Context, name string, port int, description string) (MarkedOutcome, error) {
	c, iface, ok, err := pm.queries.Interfaces.Confirm(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errProxyListUnread, err)
	}
	if !ok {
		return MarkedAbsent, nil
	}
	if iface == nil || iface.Description != "" {
		return MarkedForeign, nil
	}
	if err := pm.commands.Proxies.ConfigureProxy(ctx, c, description, "127.0.0.1", port, true); err != nil {
		return 0, err
	}
	return MarkedAdopted, nil
}

// ListNativeProxies returns kernel names (e.g. "t2s0") of NDMS Proxy
// interfaces NOT created by us — KeenOS-native SOCKS proxies the user may
// bind a router direct outbound to (#323). Владение — proxyIsOurs (description
// ∈ тегов при любом индексе, голая запись только с меткой — bareOurs, R40,
// индекс подписки): тут оно только скрывает кандидатов, ничего не сносит.
func (pm *ProxyManager) ListNativeProxies(ctx context.Context, tunnelTags map[string]bool, subProxyIdx map[int]bool) ([]string, error) {
	ifaces, err := pm.queries.Interfaces.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	var entries []proxyEntry
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.ID, proxyIfacePrefix) {
			continue
		}
		var idx int
		if n, e := fmt.Sscanf(iface.ID, proxyIfacePrefix+"%d", &idx); e != nil || n != 1 {
			continue
		}
		kernel := pm.queries.Interfaces.ResolveSystemName(ctx, iface.ID)
		if kernel == "" {
			continue
		}
		entries = append(entries, proxyEntry{idx: idx, desc: iface.Description, bare: pm.bareOurs(&iface), kernel: kernel})
	}
	return nativeProxyKernelNames(entries, tunnelTags, subProxyIdx), nil
}

// SubscriptionProxy describes an NDMS ProxyN created for a subscription
// composite (urltest/selector). These live in a separate managed set from
// tunnel proxies (Tunnels()): their port and proxy index are allocated by the
// subscription system, not derived from listen_port-firstPort.
type SubscriptionProxy struct {
	Index int
	Port  int
	Label string
}

// SubscriptionProxySet enumerates active subscription composite proxies.
// Implemented by the wiring layer over the subscription store.
type SubscriptionProxySet interface {
	SubscriptionProxies() []SubscriptionProxy
}

// proxyIsOurs reports whether ProxyN (index idx, interface description desc) was
// created by awg-manager for sing-box. Tunnel proxies are matched by their tag
// description; subscription composites carry the user label as description
// (not a tunnel tag), so they are recognised by their explicitly tracked proxy
// index instead. Пустой description — наш, только если bareOurs (метка
// (name, ""), R40): слот порта владения не доказывает.
func proxyIsOurs(idx int, desc string, bare bool, tunnelTags map[string]bool, subProxyIdx map[int]bool) bool {
	if subProxyIdx[idx] {
		return true
	}
	if desc != "" {
		return tunnelTags[desc]
	}
	return bare
}

// proxyEntry is an NDMS Proxy interface candidate: NDMS slot index, NDMS
// description, and resolved kernel name (e.g. "t2s0").
type proxyEntry struct {
	idx    int
	desc   string
	bare   bool // bareOurs: голая запись с нашей меткой
	kernel string
}

// nativeProxyKernelNames returns kernel names of Proxy interfaces NOT created
// by us — KeenOS-native SOCKS proxies the user may bind a router direct
// outbound to (#323). Pure filter over proxyIsOurs; I/O lives in the caller.
func nativeProxyKernelNames(proxies []proxyEntry, tunnelTags map[string]bool, subProxyIdx map[int]bool) []string {
	var out []string
	for _, p := range proxies {
		if proxyIsOurs(p.idx, p.desc, p.bare, tunnelTags, subProxyIdx) {
			continue
		}
		out = append(out, p.kernel)
	}
	return out
}

// SyncProxies reconciles NDMS Proxy interfaces with current config.json tunnels.
// Creates missing Proxy for each tunnel and brings existing Proxy up if Down.
// Removal of proxies for absent tunnels is the Operator's responsibility.
// Запись на слоте не наша (description ≠ тегу) или NDMS не создал её (имя
// занято) — Warn и следующий туннель, команд по ней нет (F577). Созданное и
// оставленное на роутере — *command.LeftCreatedError: вызывающий ставит метку.
func (pm *ProxyManager) SyncProxies(ctx context.Context, tunnels []TunnelInfo) error {
	if len(tunnels) == 0 {
		return nil
	}
	names := make([]string, len(tunnels))
	idxs := make([]int, len(tunnels))
	for i, t := range tunnels {
		if _, err := fmt.Sscanf(t.ProxyInterface, proxyIfacePrefix+"%d", &idxs[i]); err != nil {
			return fmt.Errorf("bad proxy iface name %q: %w", t.ProxyInterface, err)
		}
		names[i] = t.ProxyInterface
	}
	// Один свежий список на всех (F546); не прочитан — ошибка без команд.
	confirmed, err := pm.queries.Interfaces.ConfirmEach(ctx, names)
	if err != nil {
		return err
	}
	for i, t := range tunnels {
		c, ok := confirmed[t.ProxyInterface]
		if !ok {
			// Записи нет в только что прочитанном списке — создание.
			_, err := pm.CreateProxy(ctx, idxs[i], t.ListenPort, t.Tag)
			var left *command.LeftCreatedError
			switch {
			case errors.Is(err, command.ErrNotCreated):
				slog.Warn("sync proxy: slot taken by foreign record", "tag", t.Tag, "err", err)
				continue
			case errors.As(err, &left):
				// Метка уже поставлена; следующий туннель обслуживается (F577 R3).
				slog.Warn("sync proxy: created proxy left on router, deferred", "tag", t.Tag, "err", err)
				continue
			case err != nil:
				return err
			}
			continue
		}
		// Состояние — из карты, которую только что положил ConfirmEach.
		info, err := pm.queries.Interfaces.GetProxy(ctx, t.ProxyInterface)
		if err != nil {
			return err
		}
		if info.Description == "" && pm.bareMarked(t.ProxyInterface) {
			// Наша голая сирота (метка (name, "")) — настраивается и
			// становится обычной записью туннеля (F577 R1).
			if err := pm.commands.Proxies.ConfigureProxy(ctx, c, t.Tag, "127.0.0.1", t.ListenPort, true); err != nil {
				return err
			}
			pm.adopted(t.ProxyInterface)
			continue
		}
		if info.Description != t.Tag {
			slog.Warn("sync proxy: slot taken by foreign record", "tag", t.Tag, "iface", t.ProxyInterface, "description", info.Description)
			continue
		}
		if !info.Up {
			if err := pm.commands.Proxies.ProxyUp(ctx, c); err != nil {
				return err
			}
		}
	}
	return nil
}
