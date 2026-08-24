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
	List() ([]storage.AWGTunnel, error)
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
		// Гарантия локальна: битый JSON карантинит сам AWGTunnelStore.List()
		// (<id>.json.corrupt), после чего Exists даст false и следующий
		// Ensure пересоздаст запись — см. «Граница гарантии» в плане.
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

// Owned — id зеркальных записей, которые уборка вправе удалить. Единственное
// место, где живёт условие «наша запись»; Sweep ходит через него же, чтобы
// второй копии условия в пакете не появилось.
//
// Отдельный метод нужен гейту посева (registry.go, MarkSeeded): пустой посев
// при пустом списке — чистая установка и безопасен, при непустом — след
// инстансов, которых посев не выразил.
func (m *StoreMirror) Owned() ([]string, error) {
	all, err := m.st.List()
	if err != nil {
		return nil, fmt.Errorf("перечислить записи туннелей: %w", err)
	}
	var out []string
	for _, t := range all {
		if t.Backend == backendWdttRaw {
			out = append(out, t.ID)
		}
	}
	return out, nil
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
