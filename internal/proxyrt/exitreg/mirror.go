package exitreg

import (
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// backendWdttRaw — значение storage.AWGTunnel.Backend у зеркальной записи.
// Копия wdtt.BackendWdttRaw (raw_tunnel_meta.go:11), а не импорт: пакет
// internal/wdtt умирает в плане 5, и тянуть его в рантайм ради константы
// значило бы отложить его смерть.
const backendWdttRaw = "wdtt-raw"

// Ключи инвалидации — копия закрытого набора api (publish.go:8, :22).
// Держать в синхроне с frontend/src/lib/stores/storeRegistry.ts.
const (
	resourceTunnels        = "tunnels"
	resourceRoutingTunnels = "routing.tunnels"
)

// TunnelStore — то, что зеркалу нужно от хранилища туннелей.
// *storage.AWGTunnelStore удовлетворяет как есть: ни один метод под этот
// интерфейс не дописывался.
//
// Exists здесь не роскошь: Get отдаёт неразличимую ошибку и на «файла нет», и
// на «файл битый» (awg_store.go:88-114), а решения у этих случаев
// противоположные — создать запись или отказаться её трогать.
type TunnelStore interface {
	Get(id string) (*storage.AWGTunnel, error)
	Exists(id string) bool
	Save(t *storage.AWGTunnel) error
	Delete(id string) error
	ListStrict() ([]storage.AWGTunnel, error)
}

// StoreMirror ведёт зеркальные записи tunnel-store — те, от которых на время
// волны зависят список туннелей, карточка, pingcheck и testing (§5).
//
// Запись — производная ОБЪЯВЛЕНИЯ. Наблюдаемого в ней не осталось: имена
// интерфейсов стали пинами конфига, «бежит или нет» считается из ядра по
// имени (tunnel/service/wdtt_raw_state.go:17-41). Единственное исключение —
// Interface.Address: он приходит от VPS в снимке процесса, в объявление не
// попадает и потому здесь НЕ трогается (объявленный резидуал, В2).
type StoreMirror struct {
	st  TunnelStore
	pub proxyrt.Publisher
}

func NewStoreMirror(st TunnelStore, pub proxyrt.Publisher) *StoreMirror {
	if st == nil {
		panic("exitreg.NewStoreMirror: хранилище обязательно")
	}
	return &StoreMirror{st: st, pub: pub}
}

// Ensure приводит зеркальную запись к объявлению. Read-modify-write:
// пользовательские поля записи (PingCheck, ISPInterface*, Interface.Address и
// прочее) переживают обновление — иначе каждый пин перетирал бы настройки
// карточки.
func (m *StoreMirror) Ensure(d ExitDecl) error {
	prev, err := m.st.Get(d.ID)

	var rec storage.AWGTunnel
	created := false
	switch {
	case err == nil && prev != nil:
		rec = *prev
	case err != nil && m.st.Exists(d.ID):
		// Запись ЕСТЬ, но не читается: битый JSON, ошибка чтения, права.
		// Собрать её заново с дефолтами значит стереть настройки
		// пользователя, а различить эти случаи нечем — Get отдаёт их
		// одинаковой ошибкой без sentinel'а. Отказ: ни Save, ни удаления.
		// Строгий контур exitreg записи НЕ карантинит; битую запись лечит
		// только сторонний List() (UI списка туннелей) либо руки — до тех
		// пор Ensure отказывает, и это правильная сторона: пересоздание с
		// дефолтами стёрло бы настройки пользователя.
		//
		// err != nil в условии не украшение: без него ветка достижима при
		// (nil, nil), и тогда %w свернулся бы в %!w(<nil>) — причина отказа
		// исчезла бы ровно там, где её читают.
		return fmt.Errorf("зеркальная запись %s есть, но не читается: %w", d.ID, err)
	default:
		created = true
		rec = storage.AWGTunnel{
			Type:      "awg",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			Interface: storage.AWGInterface{MTU: 1300},
			Peer:      storage.AWGPeer{AllowedIPs: []string{"0.0.0.0/0"}},
			// Дефолт карточки — тот же, что у старого билдера
			// (wdtt.DefaultRawConnectivityCheck, raw_tunnel_meta.go:13-15).
			ConnectivityCheck: &storage.ConnectivityCheckConfig{Method: "http"},
		}
	}

	// Поля владения зеркала — и ровно они (таблица в шапке задачи).
	rec.ID = d.ID
	rec.Type = "awg"
	rec.Backend = backendWdttRaw
	rec.WdttClientID = d.InstanceID
	rec.Name = MirrorName(d.Name)
	rec.RawNdmsIface = d.NDMSName
	rec.RawKernelIface = d.KernelIface
	rec.Peer.Endpoint = d.Peer
	rec.Enabled = d.Enabled

	if !created && reflect.DeepEqual(rec, *prev) {
		return nil // ни файла, ни события: SetDeclared зовётся на любую правку
	}
	if err := m.st.Save(&rec); err != nil {
		return fmt.Errorf("сохранить зеркальную запись %s: %w", d.ID, err)
	}
	m.invalidate()
	return nil
}

// ownedByMirror — условие «наша зеркальная запись». Единственная копия на оба
// пути сноса: перечисление (Owned, а через него Sweep) и адресный Remove.
func ownedByMirror(t storage.AWGTunnel) bool { return t.Backend == backendWdttRaw }

// Owned — id зеркальных записей, которые уборка вправе удалить. Sweep ходит
// через него же, чтобы второй копии условия владения в пакете не появилось.
//
// Отдельный метод нужен гейту посева (registry.go, MarkSeeded): пустой посев
// при пустом списке — чистая установка и безопасен, при непустом — след
// инстансов, которых посев не выразил.
//
// Строгое перечисление (требование 20 плана 4): пофайловая беда каталога —
// ошибка всего вызова, без карантина и прощения. «Не смогли перечислить» и
// «записей нет» здесь имеют противоположные последствия: первое закрывает
// гейт посева ошибкой (и не запирает его — отметка не ставится), второе
// открывает по ветке чистой установки. Цена: уборка блокируется целиком,
// пока каталог не читается полностью, — снести можно только то, что видел.
func (m *StoreMirror) Owned() ([]string, error) {
	all, err := m.st.ListStrict()
	if err != nil {
		return nil, fmt.Errorf("перечислить записи туннелей: %w", err)
	}
	var out []string
	for _, t := range all {
		if ownedByMirror(t) {
			out = append(out, t.ID)
		}
	}
	return out, nil
}

// Remove сносит ОДНУ зеркальную запись по id — ту, чей выход исчез (инстанс
// удалён либо сменил режим на wg) — и говорит, была ли она наша.
//
// Мимо гейта посева, и это не дыра в нём: гейт бережёт от МАССОВОГО сноса по
// неполной ведомости, а здесь ведомости нет вовсе — снимается ровно названная
// запись. Без адресного сноса она пережила бы исчезновение выхода навсегда:
// отметка посева монотонна, и до перезапуска процесса Sweep не позовётся ни
// разу — пользователь остался бы с карточкой туннеля, за которой ничего нет.
//
// Читает точечно (Get/Exists), а не через Owned: строгое перечисление падает
// целиком от одного битого файла в каталоге, и запись удаляемого инстанса
// осталась бы сиротой из-за чужой беды.
func (m *StoreMirror) Remove(id, ownerInstanceID string) (bool, error) {
	rec, err := m.st.Get(id)
	if err != nil {
		if !m.st.Exists(id) {
			return false, nil // сносить нечего: Sweep успел раньше либо записи не было
		}
		// Тот же отказ, что в Ensure, и по той же причине: запись есть, но
		// владение по ней не проверить, а снос вслепую — риск для чужого
		// туннеля пользователя.
		return false, fmt.Errorf("зеркальная запись %s есть, но не читается: %w", id, err)
	}
	if rec == nil || !ownedByMirror(*rec) {
		return false, nil
	}
	// Сверка владельца, а не только «наша ли запись». RawTunnelID усекает имя
	// до 20 символов, и два клиента с длинными именами дают ОДИН id: без этой
	// проверки удаление одного снесло бы живое зеркало другого.
	if rec.WdttClientID != ownerInstanceID {
		return false, nil
	}
	if err := m.st.Delete(id); err != nil {
		return false, fmt.Errorf("удалить зеркальную запись %s: %w", id, err)
	}
	m.invalidate()
	return true, nil
}

// Sweep удаляет зеркальные записи выходов, которых нет в ведомости, и
// возвращает id удалённых — строку в журнал пишет реестр, он знает причину.
//
// Рассматриваются ТОЛЬКО наши записи: чужая не может быть тронута ни при
// какой ведомости, даже пустой. ПРАВО на сам вызов даёт гейт посева
// (registry.go): пока посев не подтверждён, Sweep не зовётся вовсе.
func (m *StoreMirror) Sweep(declared map[string]bool) ([]string, error) {
	owned, err := m.Owned()
	if err != nil {
		return nil, err
	}
	var removed []string
	var errs []error
	for _, id := range owned {
		if declared[id] {
			continue
		}
		if err := m.st.Delete(id); err != nil {
			errs = append(errs, fmt.Errorf("удалить зеркальную запись %s: %w", id, err))
			continue
		}
		removed = append(removed, id)
	}
	if len(removed) > 0 {
		m.invalidate()
	}
	return removed, errors.Join(errs...)
}

// ZeroStaleAddresses обнуляет Interface.Address у зеркальных записей.
// Требование 13 плана 4: после смерти наложения-на-чтении старое значение
// показывалось бы как текущий адрес. Живёт в зеркале — единственном
// владельце записей wdtt-raw: фильтр по Backend здесь дисциплина, а не
// вежливость (требование 22). Идемпотентен и зовётся на КАЖДОМ бооте:
// одноразовый вызов с проглоченной ошибкой делал бы потерю вечной.
func (m *StoreMirror) ZeroStaleAddresses() (int, error) {
	all, err := m.st.ListStrict()
	if err != nil {
		return 0, fmt.Errorf("перечислить записи туннелей: %w", err)
	}
	n := 0
	var errs []error
	for i := range all {
		rec := all[i]
		if rec.Backend != backendWdttRaw || rec.Interface.Address == "" {
			continue
		}
		rec.Interface.Address = ""
		if err := m.st.Save(&rec); err != nil {
			errs = append(errs, fmt.Errorf("обнулить адрес %s: %w", rec.ID, err))
			continue
		}
		n++
	}
	if n > 0 {
		m.invalidate()
	}
	return n, errors.Join(errs...)
}

// invalidate — копия api.publishInvalidated (publish.go:33-41). Импортировать
// internal/api нельзя: в плане 5 он начнёт импортировать proxyrt. Ровно так
// же поступили internal/orchestrator (orchestrator.go:737) и
// internal/pingcheck (events.go:13) — обе копии называются
// publishInvalidatedBus; общий TODO о консолидации — там же.
func (m *StoreMirror) invalidate() {
	if m.pub == nil {
		return
	}
	for _, res := range []string{resourceTunnels, resourceRoutingTunnels} {
		m.pub.Publish("resource:invalidated", events.ResourceInvalidatedEvent{
			Resource: res, Reason: "proxy-exit-mirror",
		})
	}
}
