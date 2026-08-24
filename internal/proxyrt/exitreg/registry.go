// Package exitreg — реестр выходов прокси-рантайма (§5 спеки) и поддержка
// зеркальных записей tunnel-store, от которых на время волны зависят список
// туннелей, pingcheck и testing.
//
// Два писателя и одно правило между ними: ОБЪЯВЛЕНИЕ (SetDeclared, полный
// список конфигов инстансов) даёт идентичность выхода и живёт независимо от
// роутера; НАБЛЮДЕНИЕ (Ensure из ресурса routable_exit) даёт только
// готовность.
//
// Почему идентичность НЕ приходит через ресурс (G2 плана): ресурс не умеет
// выразить собственное отсутствие — у RoutableExit есть шаг publish и нет
// unpublish (roles/linkres/exit.go:70), снятие выхода выражается только
// ведомостью; из ExitInfo (там же, :15-20) нельзя собрать зеркальную запись
// — в нём нет ни имени инстанса, ни имени карточки, ни peer'а; и уборке
// нужен полный объявленный список, которого у отдельного ресурса нет.
package exitreg

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
)

// ExitDecl — объявленный выход: всё, что известно о нём из конфига инстанса,
// и ничего сверх. Наблюдаемого здесь нет: готовность приходит через Ensure,
// адрес не приходит вовсе (объявленный резидуал, решение владельца В2).
type ExitDecl struct {
	ID          string // wdttraw-<инстанс>; он же id зеркальной записи
	InstanceID  string // WdttClientID зеркальной записи
	Name        string // человеческое имя инстанса, как его дал пользователь
	NDMSName    string // пин: OpkgTun17..49
	KernelIface string // пин: opkgtun17..49
	Peer        string // адрес сервера — в зеркальную запись, для карточки
	Enabled     bool   // НАМЕРЕНИЕ, а не факт: бежит ли процесс, знает ядро
}

func (d ExitDecl) validate() error {
	switch {
	case strings.TrimSpace(d.ID) == "":
		return fmt.Errorf("объявление выхода без id")
	case strings.TrimSpace(d.InstanceID) == "":
		return fmt.Errorf("выход %s: не назван инстанс", d.ID)
	case strings.TrimSpace(d.NDMSName) == "":
		return fmt.Errorf("выход %s: не выделен NDMS-интерфейс", d.ID)
	case strings.TrimSpace(d.KernelIface) == "":
		return fmt.Errorf("выход %s: не выделен kernel-интерфейс", d.ID)
	}
	return nil
}

// Journal — журнал приложения глазами реестра: узкий интерфейс по
// потребителю, та же форма, что у instance.Journal (instance/instance.go:14-17).
// В проде ему удовлетворяет *logging.ScopedLogger как есть.
type Journal interface {
	Info(action, target, message string)
	Warn(action, target, message string)
}

// Mirror — зеркальные записи tunnel-store. Реализация — StoreMirror
// (mirror.go); отдельным интерфейсом, чтобы реестр тестировался без диска.
type Mirror interface {
	Ensure(d ExitDecl) error
	// Sweep удаляет зеркальные записи выходов, которых нет в ведомости, и
	// возвращает id удалённых. Список возвращается, а не пишется в журнал на
	// месте: журналом владеет реестр — он знает ПРИЧИНУ («снят с
	// объявления»), а зеркало знает только файлы.
	//
	// Ведомость ВСЕГДА полная: неполная сносит живое.
	Sweep(declared map[string]bool) ([]string, error)
	// Owned — id зеркальных записей, которые уборка вправе удалить. Нужен
	// гейту посева: пустой посев при пустом списке безопасен, при непустом —
	// подозрителен (см. MarkSeeded).
	Owned() ([]string, error)
}

type record struct {
	decl  ExitDecl
	ready bool
}

// Registry — реестр выходов. Реализует linkres.ExitRegistry.
type Registry struct {
	// writeMu сериализует ОБЪЯВЛЕНИЯ целиком — и расчёт дельты, и работу с
	// зеркалом. Мало держать mu на время расчёта: без writeMu пара
	// SetDeclared({de})/SetDeclared({}) укладывается так, что память видит
	// один состав, а зеркало — другой (см. TestSetDeclaredIsSerialized).
	// Читатели его НЕ берут: резолв имён не должен ждать диска.
	writeMu sync.Mutex

	mu     sync.RWMutex
	m      map[string]record
	mirror Mirror
	log    Journal

	// seeded — гейт посева (В9). Пока не поднят, уборка не зовётся вовсе.
	// Отдельным атомиком, а не полем под mu: его читает SetDeclared и пишет
	// MarkSeeded, и связывать их с локом памяти незачем. Гонка «посев ровно
	// во время объявления» безвредна по построению: худшее — одна пропущенная
	// уборка, которую доделает следующее объявление.
	seeded atomic.Bool
}

func New(m Mirror, j Journal) *Registry {
	// Дефект проводки, не рантайма (G4): реестр без зеркала молча перестал бы
	// вести записи, от которых зависит список туннелей, а без журнала —
	// молча сносил бы их.
	if m == nil {
		panic("exitreg.New: зеркало обязательно")
	}
	if j == nil {
		panic("exitreg.New: журнал обязателен")
	}
	return &Registry{m: make(map[string]record), mirror: m, log: j}
}

// MarkSeeded подтверждает, что посев инстансов (§9) прошёл и восстановил
// instances инстансов. До этого подтверждения уборка зеркальных записей не
// удаляет НИЧЕГО: объявления принимаются, записи создаются и обновляются,
// заперто ровно удаление (В9).
//
// instances — число ИНСТАНСОВ, а не выходов: посев, поднявший три wg-клиента
// и ни одного raw, отработал успешно, и следующая ведомость без выходов —
// законная.
//
// НОЛЬ САМ ПО СЕБЕ ГЕЙТ НЕ ОТКРЫВАЕТ. «Инстансов нет» и «конфиги не
// прочитались» по результату неразличимы, а цена ошибки несимметрична: во
// втором случае уборка снесла бы все зеркальные записи разом. Различает их
// хранилище: нет наших записей — терять нечего, гейт открывается (чистая
// установка); есть — это следы инстансов, которых посев не выразил, и гейт
// остаётся закрытым, а причина возвращается вызывающему.
//
// Отметка монотонна: снять её нельзя, повторный вызов ничего не меняет. Живёт
// столько же, сколько процесс: после перезапуска гейт снова закрыт, и это
// верно — посев обязан пройти заново, прежде чем что-то удалять.
func (r *Registry) MarkSeeded(instances int) error {
	if instances <= 0 {
		if r.seeded.Load() {
			return nil // гейт уже открыт; снять отметку нельзя
		}
		owned, err := r.mirror.Owned()
		if err != nil {
			return fmt.Errorf("посев не восстановил инстансов, а зеркальные записи не перечислить: %w", err)
		}
		if len(owned) > 0 {
			// Id, а не только число (18а, закрыто здесь): выход из запертого
			// гейта — удалить карточку-призрак руками, и без id её не найти.
			r.log.Warn("seed", "exitreg", fmt.Sprintf(
				"посев не восстановил ни одного инстанса, а зеркальных записей %d (%s): уборка заблокирована",
				len(owned), strings.Join(owned, ", ")))
			return fmt.Errorf("посев не восстановил ни одного инстанса при %d зеркальных записях (%s): уборка заблокирована",
				len(owned), strings.Join(owned, ", "))
		}
	}
	if r.seeded.CompareAndSwap(false, true) {
		r.log.Info("seed", "exitreg", fmt.Sprintf("посев подтверждён (%d инстансов): уборка зеркальных записей разблокирована", instances))
	}
	return nil
}

// SetDeclared принимает ПОЛНЫЙ список объявленных выходов. Отсутствие в
// списке — единственный способ снять выход (G1).
//
// ВЕДОМОСТЬ ОБЯЗАНА БЫТЬ ПОЛНОЙ. Список строится по всем конфигам инстансов,
// включая ВЫКЛЮЧЕННЫЕ: disabled — живое объявление (§4.2), и выход
// выключенного инстанса обязан разрешаться в имя. Вызов с неполным списком
// сносит чужие зеркальные записи — та же цена, что у ведомости уборщика.
func (r *Registry) SetDeclared(list []ExitDecl) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	// Валидация ПЕРЕД любым касанием памяти и зеркала: один битый конфиг не
	// имеет права оставить остальные выходы в половинчатом состоянии.
	// Дубликат ID — тоже дефект ведомости: RawTunnelID усекает безопасную
	// часть id до 20 символов (roles/wdttclient/role.go:41-43), и два разных
	// инстанса могут схлопнуться в один ExitID; молча принять — в память
	// ляжет последний, а зеркало получит два Ensure одной записи.
	seen := make(map[string]bool, len(list))
	for _, d := range list {
		if err := d.validate(); err != nil {
			return err
		}
		if seen[d.ID] {
			return fmt.Errorf("выход %s объявлен дважды: ведомость собрана с дубликатами", d.ID)
		}
		seen[d.ID] = true
	}

	next := make(map[string]record, len(list))
	declared := make(map[string]bool, len(list))
	r.mu.Lock()
	for _, d := range list {
		prev, had := r.m[d.ID]
		// Готовность переживает объявление, но только пока речь об одном и
		// том же интерфейсе: перепиновка индекса делает прежнее наблюдение
		// наблюдением другого интерфейса.
		ready := had && prev.ready &&
			prev.decl.NDMSName == d.NDMSName && prev.decl.KernelIface == d.KernelIface
		next[d.ID] = record{decl: d, ready: ready}
		declared[d.ID] = true
	}
	r.m = next
	r.mu.Unlock()

	// Зеркало — вне r.mu (но внутри writeMu): Save берёт межпроцессный
	// файловый лок (5 с), и держать поверх него лок памяти значит вешать
	// резолв имён каталогу.
	var errs []error
	for _, d := range list {
		if err := r.mirror.Ensure(d); err != nil {
			errs = append(errs, fmt.Errorf("зеркальная запись %s: %w", d.ID, err))
		}
	}

	// Гейт посева (G7/В9). Заперто ровно удаление: всё выше уже случилось.
	if !r.seeded.Load() {
		r.log.Warn("sweep-blocked", "exitreg",
			"уборка зеркальных записей пропущена: посев инстансов не подтверждён (MarkSeeded не звали или он вернул отказ)")
		return errors.Join(errs...)
	}

	removed, err := r.mirror.Sweep(declared)
	if err != nil {
		errs = append(errs, fmt.Errorf("уборка зеркальных записей: %w", err))
	}
	for _, id := range removed {
		r.log.Info("exit-mirror-removed", id, "зеркальная запись удалена: выход снят с объявления")
	}
	return errors.Join(errs...)
}

// Ensure — наблюдение от ресурса routable_exit. Меняет ТОЛЬКО готовность:
// идентичность выхода приходит объявлением (G3), и создать её здесь нечем —
// в ExitInfo нет ни имени инстанса, ни peer'а, из которых собирается
// зеркальная запись.
func (r *Registry) Ensure(info linkres.ExitInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.m[info.ID]
	if !ok {
		return fmt.Errorf("выход %q не объявлен: конфиг инстанса не доехал до реестра (проводка)", info.ID)
	}
	if info.NDMSName != rec.decl.NDMSName || info.KernelIface != rec.decl.KernelIface {
		return fmt.Errorf("выход %q: наблюдение зовёт %q/%q, объявлено %q/%q",
			info.ID, info.NDMSName, info.KernelIface, rec.decl.NDMSName, rec.decl.KernelIface)
	}
	rec.ready = info.Ready
	r.m[info.ID] = rec
	return nil
}

// Lookup — то, ради чего реестр существует: каталог маршрутизации разрешает
// имя выхода, не читая зеркальную запись.
func (r *Registry) Lookup(id string) (linkres.ExitInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.m[id]
	if !ok {
		return linkres.ExitInfo{}, false
	}
	return linkres.ExitInfo{ID: rec.decl.ID, NDMSName: rec.decl.NDMSName,
		KernelIface: rec.decl.KernelIface, Ready: rec.ready}, true
}
