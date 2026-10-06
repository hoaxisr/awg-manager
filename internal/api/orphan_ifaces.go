package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
)

// OrphanIfaceNDMS — снятие записи интерфейса в NDMS и его устройства ядра.
// Устройство снимается только бэкендом (backend.KernelBackend) — под барьером
// списков (netdev.SwapGate, D-N1) и с гардом чужого держателя (F500).
type OrphanIfaceNDMS interface {
	// InterfaceDownIfUp — `interface down` записи в State "up"; записи нет
	// или она опущена — nil, команды нет.
	InterfaceDownIfUp(ctx context.Context, name string) error
	DeleteOpkgTun(ctx context.Context, name string) error
	// ReplaceWithTun — plain tun вместо устройства одной подменой под
	// барьером; устройства нет — только tun.
	ReplaceWithTun(ctx context.Context, iface string) error
	// StopIfPresent — снос устройства, если оно есть; нет — nil.
	StopIfPresent(ctx context.Context, iface string) error
}

// DeleteOrphanIfaceRequest — тело POST /tunnels/orphans/delete.
type DeleteOrphanIfaceRequest struct {
	Iface string `json:"iface" example:"opkgtun10"`
}

// OrphanIfaceHandler удаляет интерфейс OpkgTun, за которым не стоит ни одной
// записи владельца.
type OrphanIfaceHandler struct {
	list              func(ctx context.Context) ([]external.OrphanIface, error)
	ndms              OrphanIfaceNDMS
	log               *logging.ScopedLogger
	publishTunnelList func(ctx context.Context)
}

// SetTunnelListPublisher подключает публикацию списка туннелей в шину событий.
// Снос интерфейса меняет тот же список, что и любая правка туннеля, и молчать
// о нём значит оставить остальные открытые вкладки с удалённой строкой до
// следующего опроса.
func (h *OrphanIfaceHandler) SetTunnelListPublisher(fn func(ctx context.Context)) {
	h.publishTunnelList = fn
}

// NewOrphanIfaceHandler. Любая nil-зависимость выключает ручку: половинчатое
// удаление интерфейса хуже, чем его отсутствие.
func NewOrphanIfaceHandler(list func(ctx context.Context) ([]external.OrphanIface, error), ndms OrphanIfaceNDMS, appLog logging.AppLogger) *OrphanIfaceHandler {
	return &OrphanIfaceHandler{
		list: list,
		ndms: ndms,
		log:  logging.NewScopedLogger(appLog, logging.GroupSystem, logging.SubCleanup),
	}
}

// Delete handles POST /api/tunnels/orphans/delete.
//
//	@Summary		Delete an orphaned OpkgTun interface
//	@Description	Removes an OpkgTun interface that belongs to no awg-manager record. The orphan status is re-checked server-side immediately before removal.
//	@Tags			tunnels
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		DeleteOrphanIfaceRequest	true	"Interface name, e.g. opkgtun10"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Router			/tunnels/orphans/delete [post]
func (h *OrphanIfaceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[DeleteOrphanIfaceRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if h.list == nil || h.ndms == nil {
		response.Error(w, "учёт номеров OpkgTun недоступен", "ORPHAN_WIRING_MISSING")
		return
	}
	idx, ok := opkgtun.IndexOf(req.Iface)
	if !ok {
		response.Error(w, "имя не похоже на интерфейс OpkgTun: "+req.Iface, "BAD_IFACE")
		return
	}

	// Сиротство перепроверяется ЗДЕСЬ, а не принимается от клиента. Список, по
	// которому нажали кнопку, мог устареть на любую величину: за это время
	// номер мог достаться новому туннелю. Удалить по названному имени значит
	// снести чужой живой интерфейс по устаревшему экрану. Перепроверка идёт под
	// семафором выбора (OrphansExclusive) — иначе между ней и сносом осталось
	// бы то же окно.
	orphans, err := h.list(r.Context())
	if err != nil {
		response.Error(w, "не удалось собрать занятость OpkgTun: "+err.Error(), "OCCUPANCY_FAILED")
		return
	}
	target, isOrphan := orphanByIndex(orphans, idx)
	if !isOrphan {
		response.ErrorWithStatus(w, http.StatusConflict,
			req.Iface+": у номера появился владелец, удалять нечего. Обновите список туннелей.", "NOT_ORPHAN")
		return
	}

	// Две ПОЛОВИНЫ, существуют независимо: после `ip link del` запись NDMS
	// живёт дальше со state error, а устройство, поднятое мимо NDMS
	// (`ip link add`), записи не имеет вовсе. Обе держат номер в занятости,
	// поэтому снимать надо обе; «нет такой» — успех для обеих.
	//
	// Запись есть — снятие C3a, как ops.Delete (стенд Task 59: 20/20 без C
	// всех классов): запись up — `interface down`; устройство под записью
	// подменяется на plain tun под барьером списков; `no interface` — NDMS
	// снимает tun сам. Прежний порядок «устройство, затем запись» оставлял
	// запись без устройства до `no interface` — 0767 у читателей списка
	// (X5b), под up-записью — 0ba1; запись при живом amneziawg — 003b.
	// Любой шаг отказал — дальше не идём: запись при живом устройстве — C.
	// Ожиданий хуков от ручки нет: у сироты нет туннеля, оркестратор этот
	// OpkgTunN не ведёт (ожидание disabled регистрирует сам InterfaceDown).
	//
	// Имя записи NDMS берётся ИЗ НАЙДЕННОГО, а не собирается из номера: собрать
	// его заново значит завести второй разборщик рядом с тем, которым занятость
	// считал пул, и на записи, которую один принимает, а другой нет, снос ушёл
	// бы мимо — а DeleteOpkgTun к отсутствию толерантен, то есть ручка
	// отчиталась бы успехом. Имя устройства ядра каноническое: клиент мог
	// прислать любое написание (и с ведущими нулями).
	ndmsName := target.NDMSName
	iface := fmt.Sprintf("opkgtun%d", idx)

	// Устройства нет (stat доказал; `ip link del`/rmmod — запись в state
	// error при conf running) — ни down, ни подмены, сразу `no interface`, как
	// ops.removeOpkgTun (M1): подмена здесь — голый `tuntap add`, NEWLINK под
	// running-записью, а снос записи без устройства — 0 C ×12 (стенд Task 59).
	// stat не ответил — «не знаем»: идём C3a, как с живым устройством.
	_, absentErr := netdev.Absent(iface)
	if ndmsName != "" && absentErr != nil {
		if err := h.ndms.InterfaceDownIfUp(r.Context(), ndmsName); err != nil {
			h.log.Warn("orphan-delete", iface, "запись NDMS не опущена, ничего не тронуто: "+err.Error())
			response.Error(w, "не удалось опустить запись "+ndmsName+": "+err.Error(), "NDMS_DOWN_FAILED")
			return
		}
		// Устройство не подменено и живо (в т.ч. чужой держатель tun) —
		// запись не трогаем: снос записи при живом устройстве и есть C.
		// del прошёл, а tun не встал — устройства нет: запись снимается
		// ниже, оставленная без устройства она давала бы 0767 на каждом
		// нашем списке (L2).
		if err := h.ndms.ReplaceWithTun(r.Context(), iface); err != nil {
			if _, gone := netdev.Absent(iface); gone != nil {
				h.log.Warn("orphan-delete", iface, "устройство не заменено, запись NDMS не тронута: "+err.Error())
				response.Error(w, "устройство "+iface+" удалить не удалось: "+err.Error(), "LINK_DELETE_FAILED")
				return
			}
		}
	}
	if ndmsName != "" {
		if err := h.ndms.DeleteOpkgTun(r.Context(), ndmsName); err != nil {
			// Запись осталась с plain tun под ней (как после ребута): номер
			// по-прежнему занят, и молчать об этом нельзя. Повторный снос
			// доберёт запись.
			h.log.Warn("orphan-delete", iface, "запись NDMS осталась: "+err.Error())
			response.Error(w, "не удалось снять запись "+ndmsName+": "+err.Error(), "NDMS_DELETE_FAILED")
			return
		}
	}

	// Устройство без записи — поднятое мимо NDMS, либо tun, который NDMS не
	// снял вместе с записью. Наличие решает stat /sys/class/net в бэкенде, а
	// не текст отказа `ip`: незапустившийся `ip` — ошибка, а не «устройства
	// нет». Записи уже нет — C невозможен.
	if err := h.ndms.StopIfPresent(r.Context(), iface); err != nil {
		h.log.Warn("orphan-delete", iface, "устройство не удалено: "+err.Error())
		response.Error(w, "устройство "+iface+" удалить не удалось: "+err.Error(), "LINK_DELETE_FAILED")
		return
	}

	h.log.Info("orphan-delete", iface, "осиротевший интерфейс удалён")
	response.Success(w, map[string]bool{"ok": true})
	if h.publishTunnelList != nil {
		h.publishTunnelList(r.Context())
	}
}

// orphanByIndex — поиск по НОМЕРУ, а не по строке имени. IndexOf принимает оба
// написания («OpkgTun10» и «opkgtun10») и глотает ведущие нули, а список сирот
// строится в одном каноническом; сравнение строк отвечало бы на «OpkgTun10»
// отказом «у номера есть владелец», что неправда. Отдаёт саму находку: снос
// ходит по ЕЁ имени записи NDMS, а не по собранному заново.
func orphanByIndex(orphans []external.OrphanIface, idx int) (external.OrphanIface, bool) {
	for _, o := range orphans {
		if n, ok := opkgtun.IndexOf(o.Iface); ok && n == idx {
			return o, true
		}
	}
	return external.OrphanIface{}, false
}
