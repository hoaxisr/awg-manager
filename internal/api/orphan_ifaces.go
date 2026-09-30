package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
)

// OrphanIfaceNDMS — снятие записи интерфейса в NDMS.
type OrphanIfaceNDMS interface {
	DeleteOpkgTun(ctx context.Context, name string) error
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

// linkDelete — шов над `ip link del`: тестам незачем трогать сеть машины.
//
// stderr подмешивается в ошибку намеренно: exec.Run отдаёт «exit status 1» без
// текста, и по одной этой строке «устройства нет» неотличимо от «не смогли
// удалить». Стенд 15.09: снос записи NDMS уносит устройство каскадом, так что к
// нашему `ip link del` его уже нет, и ручка отчитывалась отказом об успешном
// сносе.
var linkDelete = func(ctx context.Context, iface string) error {
	res, err := exec.Run(ctx, "/opt/sbin/ip", "link", "del", "dev", iface)
	if err == nil {
		return nil
	}
	if res != nil {
		if msg := strings.TrimSpace(res.Stderr); msg != "" {
			return fmt.Errorf("%s (%w)", msg, err)
		}
	}
	return err
}

// linkAbsent — «устройства нет», и это ЕДИНСТВЕННОЕ, что мы готовы принять за
// успех несостоявшегося сноса. Отличается от прежней проверки наличия тем, что
// решает по ТЕКСТУ отказа, а не по факту отказа: `ip link del` не запустился
// (нет бинаря, таймаут ctx) — это не «устройства нет», это «мы не проверили».
// Прежняя форма читала любой отказ как отсутствие и отвечала «удалено», не
// удалив.
func linkAbsent(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	// Три формулировки, потому что на роутере их правда три: busybox ip пишет
	// «Device "X" does not exist.», iproute2 — «Cannot find device "X"», ядро
	// через netlink — ENODEV «no such device».
	return strings.Contains(low, "does not exist") ||
		strings.Contains(low, "cannot find device") ||
		strings.Contains(low, "no such device")
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

	// Две ПОЛОВИНЫ, снимаются независимо, потому что существуют независимо:
	// после `ip link del` запись NDMS живёт дальше со state error, а устройство,
	// поднятое мимо NDMS (`ip link add`), записи не имеет вовсе. Обе половины
	// держат номер в занятости, поэтому снимать надо обе и на каждую отвечать
	// отдельно; «нет такой» — успех для обеих (снимаем то, чего и так нет).
	//
	// Порядок — запись, потом устройство: так делает teardown режимов роутера,
	// и так устройство, которое NDMS уносит вместе с записью, не приходится
	// сносить дважды.
	// Имя записи NDMS берётся ИЗ НАЙДЕННОГО, а не собирается из номера: собрать
	// его заново значит завести второй разборщик рядом с тем, которым занятость
	// считал пул, и на записи, которую один принимает, а другой нет, снос ушёл
	// бы мимо — а DeleteOpkgTun к отсутствию толерантен, то есть ручка
	// отчиталась бы успехом. Имя устройства ядра каноническое: клиент мог
	// прислать любое написание (и с ведущими нулями).
	ndmsName := target.NDMSName
	iface := fmt.Sprintf("opkgtun%d", idx)
	if ndmsName != "" {
		if err := h.ndms.DeleteOpkgTun(r.Context(), ndmsName); err != nil {
			response.Error(w, "не удалось снять запись "+ndmsName+": "+err.Error(), "NDMS_DELETE_FAILED")
			return
		}
	}

	// Снос устройства БЕЗУСЛОВНЫЙ — в том числе когда записи NDMS не было вовсе
	// (устройство подняли мимо неё). Прежде он шёл под проверкой наличия, а та
	// была fail-open: незапустившийся `ip` читался как «устройства нет», и
	// ручка отчитывалась успехом, оставив номер занятым. Лишний вызов на
	// несуществующем устройстве стоит одного отказа с понятным текстом.
	if err := linkDelete(r.Context(), iface); err != nil && !linkAbsent(err) {
		// Запись снята, устройство осталось: номер по-прежнему занят, и
		// молчать об этом нельзя — пользователь решит, что убрано всё.
		h.log.Warn("orphan-delete", iface, "запись NDMS снята, устройство осталось: "+err.Error())
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
