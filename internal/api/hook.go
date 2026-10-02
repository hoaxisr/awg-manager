package api

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/events"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
)

// HookDispatcher is the subset of events.Dispatcher that HookHandler
// uses. Interface so tests can inject a fake.
type HookDispatcher interface {
	Enqueue(e events.Event)
}

// HookWANModel is the narrow surface HookHandler needs from the WAN
// model. Kept local so api/hook.go doesn't depend on *wan.Model.
type HookWANModel interface {
	SetUp(kernelName string, up bool) (changed bool)
}

// HookSystemNames — карта имён ядра кэша интерфейсов (query.InterfaceStore).
// Эхо id и не похожее на имя ядра она отбрасывает сама.
type HookSystemNames interface {
	OnSystemName(id, name string)
}

// ProxyRuntimeNudge подталкивает прокси-рантайм: пока посев не состоялся,
// повторяет боот, после — будит воркеров. Зовётся по WAN UP: холодный старт
// роутера доходит до посева раньше, чем оживает RCI, и без повторного вызова
// инстансы не поднялись бы вовсе.
type ProxyRuntimeNudge func(reason string)

// HookHandler handles NDM hook events.
type HookHandler struct {
	svc           TunnelService
	orch          *orchestrator.Orchestrator
	dispatcher    HookDispatcher // may be nil until SetDispatcher is called
	wanModel      HookWANModel   // may be nil until SetWANModel is called
	systemNames   HookSystemNames
	proxyNudge    ProxyRuntimeNudge
	endpointNudge func()
	ipv4Running   func(ndmsID string)
	log           *logging.ScopedLogger
	wanLog        *logging.ScopedLogger
	// selfCreateGate counts in-flight awg-manager-initiated NDMS interface
	// creations. While > 0, ifcreated hook events suppress their automatic
	// snapshot rebroadcast — the caller (importer / Create path) is
	// responsible for publishing a fresh snapshot AFTER it has persisted
	// the tunnel to awg-manager's store. Otherwise the hook-triggered
	// snapshot fires before the Save and the new NDMS interface appears
	// briefly in the "system tunnels" list as a ghost duplicate of the
	// managed tunnel.
	selfCreateGate atomic.Int32
}

// EnterSelfCreate marks the start of an awg-manager-initiated NDMS
// interface creation. Pair with ExitSelfCreate via defer.
func (h *HookHandler) EnterSelfCreate() { h.selfCreateGate.Add(1) }

// ExitSelfCreate marks the end of an awg-manager-initiated NDMS
// interface creation. Callers MUST publish a fresh tunnels invalidation
// hint themselves after this (typically via TunnelsHandler.publishTunnelList)
// so UIs see the finalized state.
func (h *HookHandler) ExitSelfCreate() { h.selfCreateGate.Add(-1) }

// NewHookHandler creates a new hook event handler.
func NewHookHandler(svc TunnelService, orch *orchestrator.Orchestrator, appLogger logging.AppLogger) *HookHandler {
	return &HookHandler{
		svc:  svc,
		orch: orch,
		log:  logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubBoot),
		// Переходы WAN — отдельная подгруппа: их ищут при разборе обрывов,
		// не смешивая с потоком NDMS-хуков.
		wanLog: logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubWan),
	}
}

// SetDispatcher wires an events.Dispatcher for hook-driven cache
// invalidation. Call after construction (typically from server.New).
func (h *HookHandler) SetDispatcher(d HookDispatcher) {
	h.dispatcher = d
}

// SetWANModel wires the WAN model so iflayerchanged layer=ipv4 hooks
// can update WAN interface up/down state in-memory before dispatching
// EventWANUp/Down to the orchestrator.
func (h *HookHandler) SetWANModel(m HookWANModel) {
	h.wanModel = m
}

// SetSystemNames подключает карту имён ядра: system_name хука ложится в неё
// синхронно, до WAN-модели (см. Handle).
func (h *HookHandler) SetSystemNames(n HookSystemNames) {
	h.systemNames = n
}

// SetProxyRuntimeNudge wires the proxy-runtime callback fired on WAN up:
// retry of a seed that failed against a not-yet-alive RCI, and a wake-up for
// the workers of an already booted runtime.
func (h *HookHandler) SetProxyRuntimeNudge(fn ProxyRuntimeNudge) {
	h.proxyNudge = fn
}

// SetEndpointGuardNudge подключает внеочередной проход endpoint-стража.
// Повод — ifipchanged: адрес WAN сменился, и адрес сервера за DDNS-именем
// мог смениться заодно (у провайдера это одно событие). Ждать тика стража
// незачем — лишний проход дёшев, адрес он меняет только на смену резолва.
func (h *HookHandler) SetEndpointGuardNudge(fn func()) {
	h.endpointNudge = fn
}

// SetIPv4RunningHook — колбэк на iflayerchanged layer=ipv4 level=running для
// ЛЮБОГО интерфейса: переприменение клиентских маршрутов system:-выхода
// (F497; ядро снимает `default dev` на down/up интерфейса).
func (h *HookHandler) SetIPv4RunningHook(fn func(ndmsID string)) {
	h.ipv4Running = fn
}

// HookSink — приёмник событий spool (events.SpoolReader) с первых секунд
// жизни демона. Читатель обязан стартовать ДО установки хук-скриптов и до
// первого чтения списка интерфейсов, а готовый HookHandler появляется только
// в registerRoutes (srv.Start), заметно позже.
//
// Поэтому «Handle — единственная точка входа» нарушено только на окне
// старта: пока сервер не опубликовал готовый обработчик, событие идёт лишь
// в диспетчер (тот же enqueueHook, с которого начинается Handle) — кэш его
// получает. Реакции оркестратора, WAN-модели и UI на хуки этого окна
// теряются — ровно как до F571 терялся отказанный HTTP POST, пока листенера
// не было.
type HookSink struct {
	dispatcher HookDispatcher
	ready      atomic.Pointer[HookHandler]
}

// NewHookSink создаёт приёмник; до Publish события идут только в d.
func NewHookSink(d HookDispatcher) *HookSink {
	return &HookSink{dispatcher: d}
}

// Publish отдаёт приёмнику ПОЛНОСТЬЮ настроенный обработчик. Звать после
// всех Set*: читатель spool зовёт Handle из своей горутины.
func (s *HookSink) Publish(h *HookHandler) { s.ready.Store(h) }

// Handle — sink для SpoolReader.
func (s *HookSink) Handle(event events.Event) {
	if h := s.ready.Load(); h != nil {
		h.Handle(event)
		return
	}
	enqueueHook(s.dispatcher, event)
}

// enqueueHook ставит событие в диспетчер (инвалидация кэшей, неблокирующе).
func enqueueHook(d HookDispatcher, event events.Event) {
	if d != nil {
		d.Enqueue(event)
	}
}

// Handle обрабатывает разобранное событие хука: диспетчер (инвалидация
// кэшей и публикация списка туннелей), WAN-модель и оркестратор.
// Синхронна только WAN-модель: на незнакомом интерфейсе SetUp
// перечитывает список WAN (RCI); остальное уходит в горутины.
func (h *HookHandler) Handle(event events.Event) {
	// 0) Имя ядра из хука — в кэш синхронно (I3, F570): SetUp WAN-модели ниже
	// на незнакомом имени перечитывает ListWAN, а тот читает только память.
	// Через одну лишь очередь диспетчера имя горячо подключённого модема
	// доходило бы позже, и первый WAN up терялся. Диспетчер повторит то же
	// (идемпотентно). OnSystemName до очереди — чтобы ListWAN в этом же Handle
	// знал имя; порядок с воркером неважен: хук карту не трогает, имя снятого
	// id снимет список.
	if event.SystemName != "" && h.systemNames != nil {
		h.systemNames.OnSystemName(event.ID, event.SystemName)
	}

	// 1) Enqueue into Dispatcher for cache invalidation (async, non-blocking).
	// Своё создание (EnterSelfCreate) помечается: диспетчер сверит его списком,
	// но публиковать tunnels/servers не станет — до записи туннеля в наш стор
	// новый интерфейс показался бы в «системных» призраком; создатель публикует
	// сам после Save. Остальные ifcreated/ifdestroyed диспетчер публикует после
	// списка своей пачки.
	event.SelfCreated = event.Type == events.EventIfCreated && h.selfCreateGate.Load() > 0
	enqueueHook(h.dispatcher, event)

	// 1a) Смена адреса интерфейса — повод перепроверить DDNS-имена: страж
	// пройдётся вне очереди. Вызов неблокирующий (будит чужую горутину), так
	// что ответ на хук он не задерживает.
	if event.Type == events.EventIfIPChanged && h.endpointNudge != nil {
		h.endpointNudge()
	}

	// 2) For iflayerchanged, route to the orchestrator:
	//    - layer=conf → NDMS hook path (tunnel lifecycle)
	//    - layer=ipv4 → WAN model update + EventWANUp/Down
	if event.Type == events.EventIfLayerChanged {
		if event.Layer == "ipv4" && event.Level == "running" && h.ipv4Running != nil {
			go h.ipv4Running(event.ID)
		}
		if event.Layer == "ipv4" {
			h.handleWANLayerEvent(event)
		} else if h.orch != nil {
			go func(e events.Event) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := h.orch.HandleEvent(ctx, orchestrator.Event{
					Type:     orchestrator.EventNDMSHook,
					NDMSName: e.ID,
					Layer:    e.Layer,
					Level:    e.Level,
				}); err != nil {
					h.log.Warn("hook", e.ID, "orchestrator HandleEvent failed: "+err.Error())
				}
			}(event)
		}
	}

	// 3) ifdestroyed — в оркестратор явным событием: реакция на снятие нашей
	// записи OpkgTun не зависит от layer-хуков (#328, F569). Свой снос
	// оркестратор поглощает по ожиданию "destroyed".
	if event.Type == events.EventIfDestroyed && h.orch != nil {
		go func(e events.Event) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := h.orch.HandleEvent(ctx, orchestrator.Event{
				Type:     orchestrator.EventNDMSIfDestroyed,
				NDMSName: e.ID,
			}); err != nil {
				h.log.Warn("hook", e.ID, "orchestrator HandleEvent failed: "+err.Error())
			}
		}(event)
	}

	h.log.Info("hook", event.ID, fmt.Sprintf("ndms: type=%s layer=%s level=%s", event.Type, event.Layer, event.Level))
}

// handleWANLayerEvent processes an iflayerchanged hook with layer=ipv4.
// Updates the WAN model synchronously so the orchestrator's WAN-up
// decision sees the fresh state, then dispatches EventWANUp/Down in a
// goroutine (same 60s timeout the legacy /api/wan/event handler used).
//
// Skips VPN/tunnel kernel names (nwg*, opkgtun*, awg*, wg*, wireguard*,
// ipsec*, sstp*, openvpn*, proxy*) — they fire ipv4-layer too but aren't
// WAN. If we didn't skip, wanModel.SetUp would trigger a repopulate
// storm and the orchestrator would treat tunnel events as WAN events.
func (h *HookHandler) handleWANLayerEvent(e events.Event) {
	kernelName := e.SystemName
	if kernelName == "" {
		// Can't update WAN model without a kernel name.
		return
	}
	if ndmsquery.IsNonISPInterface(kernelName) {
		return
	}
	if h.wanModel == nil {
		return
	}
	up := e.Level == "running"

	// Sync WAN model update — must happen before the orch decides
	// whether any WAN is up. SetUp handles hot-plug via repopulateFn.
	changed := h.wanModel.SetUp(kernelName, up)

	// Логируем только реальные переходы: NDMS повторяет hook-события с
	// неизменным уровнем, и без этого фильтра каждый повтор писал бы строку.
	if h.wanLog != nil && changed {
		if up {
			h.wanLog.Info("wan-state", kernelName, "WAN interface up")
		} else {
			h.wanLog.Warn("wan-state", kernelName, "WAN interface down")
		}
	}

	// Прокси-рантайм не зависит от оркестратора — будим до его гарда, иначе в
	// конфигурации без orch коллбэк не сработает. Горутиной: ретрай боота
	// ходит в RCI и держал бы ответ на хук.
	if up && changed && h.proxyNudge != nil {
		go h.proxyNudge("wan-up")
	}

	if h.orch == nil {
		return
	}
	action := "up"
	evType := orchestrator.EventWANUp
	if !up {
		action = "down"
		evType = orchestrator.EventWANDown
	}
	go func(iface, act string, et orchestrator.EventType) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := h.orch.HandleEvent(ctx, orchestrator.Event{
			Type:     et,
			WANIface: iface,
		}); err != nil {
			h.log.Warn("hook", iface, "orchestrator WAN "+act+" failed: "+err.Error())
		}
	}(kernelName, action, evType)
}
