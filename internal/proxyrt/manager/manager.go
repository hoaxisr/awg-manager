// Package manager — жизненный цикл прокси-инстансов: единственная точка, где
// store намерения, реестр выходов, аллокатор, уборщик и воркеры встречаются.
// ЕДИНСТВЕННЫЙ писатель реестра выходов (G1): SetDeclared/MarkSeeded не зовёт
// больше никто — ворота плана держат это грепом.
package manager

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instance"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// teardownTimeout — потолок ожидания teardown-прогона при удалении (Щ5).
const teardownTimeout = 10 * time.Second

type RegistryPort interface {
	SetDeclared([]exitreg.ExitDecl) error
	MarkSeeded(instances int) error
}

type SweepPort interface {
	Sweep(ctx context.Context, declared map[string]bool) ([]string, error)
}

type RunningInstance interface {
	Start(ctx context.Context)
	Post(k proxyrt.EventKind) bool
	// ResetStartBackoff снимает у процесса инстанса паузу повторного старта
	// (proxyrt.BackoffResetter).
	ResetStartBackoff()
	Stop()
}

type Journal interface {
	Info(action, target, message string)
	Warn(action, target, message string)
}

// Live — бес-локовый снимок записи для замыканий Cfg/Intent инстанса (Щ6):
// воркер читает их из своей горутины на каждом прогоне, а Delete/Shutdown
// держат m.mu и ждут Worker.Stop — замыкание, берущее m.mu, взаимоблокировало
// бы остановку так, что ни один исполнитель этого не увидел бы в своём коде.
// Указатель обновляют только мутаторы менеджера.
type Live struct {
	rec atomic.Pointer[instancestore.Record]
}

func newLive(rec instancestore.Record) *Live {
	l := &Live{}
	l.rec.Store(&rec)
	return l
}

func (l *Live) Config() instancestore.Record { return *l.rec.Load() }

func (l *Live) Intent() proxyrt.Intent {
	if l.rec.Load().Enabled {
		return proxyrt.IntentEnabled
	}
	return proxyrt.IntentDisabled
}

// Factory собирает инстанс под запись: роль + Link + instance.New. Живёт в
// cmd (задача 14) — только там есть прод-адаптеры. Замыкания Cfg/Intent
// инстанса обязаны читать live, не менеджер.
type Factory func(rec instancestore.Record, live *Live) (RunningInstance, error)

// Deps — все зависимости, конструктором (G4).
type Deps struct {
	Store    *instancestore.Store
	Registry RegistryPort
	Sweeper  SweepPort
	Factory  Factory
	Journal  Journal
	// Seed — замыкание instancestore.Seed с прод-SeedDeps (задача 14).
	Seed func(ctx context.Context) (instancestore.SeedResult, error)
	// PostSeed — уборочные шаги боота (задача 6): обнуление адресов зеркальных
	// записей — на КАЖДОМ бооте; добивание старого поколения и уборка
	// наследия — только при res.SeededNow.
	PostSeed func(ctx context.Context, res instancestore.SeedResult, declaredNDMS map[string]bool) error
	// Выделение пинов — обязанность писателя конфига (план 3), то есть НАША
	// (Щ1): без этого создание raw-клиента и сервера через API невозможно.
	AllocIndex  func(owner string, pinned int, havePin bool) (int, error)
	AllocListen func(ownerKey string) (string, error)
	ReleasePins func(ownerKeys ...string)
	// WaitDisabled — ограниченное ожидание teardown-прогона (Щ5): прод —
	// опрос StateStore до фазы disabled/settled свежее момента вызова.
	WaitDisabled func(key string, timeout time.Duration) bool
}

type managed struct {
	live *Live
	inst RunningInstance
}

// SeedInfo — состояние посева наружу (Щ8, требование 17): запертый гейт
// (Certified=false) отличим от несостоявшегося посева (Booted=false).
type SeedInfo struct {
	Booted    bool
	Certified bool
	Err       string
}

type Manager struct {
	deps Deps

	mu        sync.Mutex
	m         map[string]*managed
	booted    bool
	certified bool
	seedErr   string
	ctx       context.Context // контекст боота — для стартов из мутаторов
}

func New(d Deps) *Manager {
	if d.Store == nil || d.Registry == nil || d.Sweeper == nil || d.Factory == nil ||
		d.Journal == nil || d.Seed == nil || d.PostSeed == nil ||
		d.AllocIndex == nil || d.AllocListen == nil || d.ReleasePins == nil ||
		d.WaitDisabled == nil {
		panic("manager.New: неполные зависимости (G4)")
	}
	return &Manager{deps: d, m: make(map[string]*managed)}
}

// declsOf — ведомость выходов из записей store: типизированные конфиги
// ЗНАЧЕНИЯМИ (требования 16 и 19). После валидации store (Load и Replace)
// RawExiter не бывает nil; nil-гард остаётся в DeclaredExits (план 4).
func declsOf(recs []instancestore.Record) []exitreg.ExitDecl {
	ics := make([]exitreg.InstanceConfig, 0, len(recs))
	for _, r := range recs {
		ics = append(ics, exitreg.InstanceConfig{ID: r.ID, Cfg: r.RawExiter(), Enabled: r.Enabled})
	}
	return exitreg.DeclaredExits(ics)
}

// namedOf — та же дисциплина для ведомости NDMS-имён уборщика; сама ведомость
// собирается instance.DeclaredNDMSNames (план 1 написал её под этот вызов).
func namedOf(recs []instancestore.Record) []instance.NDMSNamed {
	out := make([]instance.NDMSNamed, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.NDMSNamed())
	}
	return out
}

// Boot — порядок §9 и требования 2 (блокирующего) в ОДНОЙ функции над ОДНИМ
// списком: посев → список (и ведомость NDMS-имён от него же) → PostSeed →
// MarkSeeded(len) → SetDeclared(тот же список) → старт → уборка. Расщеплять
// шаги по вызывающим нельзя. Почему PostSeed стоит ДО сертификации и
// объявления — в комментарии на месте вызова.
//
// Замечание 10 ревью зафиксировано: MarkSeeded идёт ДО построения ведомости.
// Это безопасно — сборка ведомости из провалидированного store не падает — и
// НЕ подлежит «починке» перестановкой: порядок «сертификация → объявление»
// предписан требованием 2 (один список на оба вызова).
func (m *Manager) Boot(ctx context.Context) error {
	res, err := m.deps.Seed(ctx)
	if err != nil {
		m.deps.Journal.Warn("boot", "proxy", "посев отложен: "+err.Error())
		m.mu.Lock()
		m.seedErr = err.Error()
		m.mu.Unlock()
		return err
	}
	list := res.State.Records
	declaredNDMS := instance.DeclaredNDMSNames(namedOf(list))

	// PostSeed — СРАЗУ после посева (F2 ревью, принят вариант «перенести»):
	// уборочные шаги считаются от ТОГО ЖЕ списка и в реестр не ходят, поэтому
	// зависимости от MarkSeeded/SetDeclared у них нет. Отказ любого шага ниже
	// оставлял бы их невыполненными до следующего боота: посев уже лёг на
	// диск, и SeededNow там будет false — повтор держится на отметке
	// cleanupPending в store (амендмент A3), а не на признаке свежего посева.
	// Побочно закрывается окно драки за порты на УСПЕШНОМ пути — старое
	// поколение добивается ДО старта новых воркеров.
	//
	// Обнуление адресов зеркал ДО объявления безвредно — но НЕ потому, что
	// «SetDeclared перепишет»: StoreMirror.Ensure адрес принципиально не
	// трогает (mirror.go:47-50, объявленный резидуал В2, страж
	// mirror_test.go:111) — он его сохраняет через read-modify-write. Причина
	// другая: между обнулением и Ensure адрес не пишет никто, а новые
	// зеркальные записи создаются вообще без адреса, поэтому Ensure донесёт
	// ноль как есть. Порядок здесь НЕ свободный: довод держится на том, что
	// между этими двумя шагами нет писателя адреса.
	//
	// Две принятые цены: фатальный отказ запуска ниже оставит убитыми оба
	// поколения; отказ самого PostSeed — это Warn и продолжение боота, то есть
	// старт нового поколения ПОВЕРХ недобитого старого (отметка уборки при
	// этом остаётся висеть, и следующий боот повторит шаги).
	if perr := m.deps.PostSeed(ctx, res, declaredNDMS); perr != nil {
		m.deps.Journal.Warn("boot", "proxy", "уборочные шаги посева: "+perr.Error())
	}

	certified := true
	certErr := ""
	if err := m.deps.Registry.MarkSeeded(len(list)); err != nil {
		certified, certErr = false, err.Error()
		m.deps.Journal.Warn("boot", "proxy", "посев не сертифицирован, уборка заперта: "+err.Error())
	}

	if err := m.deps.Registry.SetDeclared(declsOf(list)); err != nil {
		// Фатально: старт инстансов с невидимыми выходами — правила
		// пользователя, молча указывающие в никуда (M7 плана 4).
		m.deps.Journal.Warn("boot", "proxy", "объявление выходов: "+err.Error())
		m.mu.Lock()
		m.seedErr = err.Error()
		m.mu.Unlock()
		return err
	}

	m.mu.Lock()
	m.ctx = ctx
	for _, rec := range list {
		if _, exists := m.m[rec.Key()]; exists {
			continue // повторный Boot (ретрай посева): живые не пересоздаём
		}
		live := newLive(rec)
		inst, ferr := m.deps.Factory(rec, live)
		if ferr != nil {
			// Наблюдаемость (F1 + I-4 ревью): боот оборван, часть воркеров уже
			// бежит, booted остаётся false — причина обязана быть видна в
			// SeedInfo, иначе наружу уедет «посев не состоялся» без причины.
			m.seedErr = fmt.Sprintf("собрать инстанс %s: %v", rec.Key(), ferr)
			m.mu.Unlock()
			return fmt.Errorf("собрать инстанс %s: %w", rec.Key(), ferr)
		}
		m.m[rec.Key()] = &managed{live: live, inst: inst}
		inst.Start(ctx)
		inst.Post(proxyrt.EventBoot)
	}
	m.booted = true
	m.certified = certified
	m.seedErr = certErr
	m.mu.Unlock()

	if _, serr := m.deps.Sweeper.Sweep(ctx, declaredNDMS); serr != nil {
		m.deps.Journal.Warn("boot", "proxy", "уборка NDMS: "+serr.Error())
	}
	return nil
}

func (m *Manager) SeedInfo() SeedInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return SeedInfo{Booted: m.booted, Certified: m.booted && m.certified, Err: m.seedErr}
}

func (m *Manager) Enabled(key string) (on, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mg, found := m.m[key]; found {
		return mg.live.Config().Enabled, true
	}
	return false, false
}

// ensurePins — выделение пинов и listen-порта писателем конфига (Щ1, план 3).
// Идемпотентен: заполненные поля не трогает. Возвращает владельцев СВЕЖИХ
// аллокаций: если операция дальше сорвётся (отказ реестра/диска), вызывающий
// обязан отдать их обратно через ReleasePins — иначе held аллокатора течёт до
// рестарта (Н6 ревью).
//
// Listen идёт под СВОИМ ключом владельца — key+"/listen" (I-3 ревью, круг 2),
// ровно как обе половины сервера (key+"/wg", key+"/raw"). Голый key брать
// нельзя: Allocator.Release (alloc.go:78-86) освобождает ВСЕ номера владельца,
// и возврат после listen-only-аллокации отобрал бы у ЖИВОЙ записи её индекс
// OpkgTun (путь Update: пин на диске есть, пуст только listen). Отдельный ключ
// сохраняет резерв — без него два параллельных Create (ручки задачи 7 — HTTP)
// получили бы от сканирующего аллокатора ОДИН порт: ensurePins и запись на
// диск не атомарны, а validateState уникальность Listen не проверяет.
func (m *Manager) ensurePins(rec *instancestore.Record) (allocated []string, err error) {
	key := rec.Key()
	switch rec.Kind {
	case instancestore.KindWdttClient:
		c := rec.WdttClient
		if c == nil {
			return nil, fmt.Errorf("инстанс %s: нет конфига", key)
		}
		if c.Mode == "raw" && (c.NdmsIface == "" || c.RawIface == "") {
			idx, err := m.deps.AllocIndex(key, 0, false)
			if err != nil {
				return allocated, fmt.Errorf("нет свободного OpkgTun: %w", err)
			}
			allocated = append(allocated, key)
			c.NdmsIface = fmt.Sprintf("OpkgTun%d", idx)
			c.RawIface = fmt.Sprintf("opkgtun%d", idx)
		}
		if c.Listen == "" {
			l, err := m.deps.AllocListen(key + "/listen")
			if err != nil {
				return allocated, fmt.Errorf("нет свободного listen-порта: %w", err)
			}
			allocated = append(allocated, key+"/listen")
			c.Listen = l
		}
	case instancestore.KindWdttServer:
		c := rec.WdttServer
		if c == nil {
			return nil, fmt.Errorf("инстанс %s: нет конфига", key)
		}
		if c.NdmsIface == "" || c.WgIface == "" {
			idx, err := m.deps.AllocIndex(key+"/wg", 0, false)
			if err != nil {
				return allocated, fmt.Errorf("нет свободного OpkgTun (wg): %w", err)
			}
			allocated = append(allocated, key+"/wg")
			c.NdmsIface = fmt.Sprintf("OpkgTun%d", idx)
			c.WgIface = fmt.Sprintf("opkgtun%d", idx)
		}
		if c.RawNdmsIface == "" || c.RawIface == "" {
			idx, err := m.deps.AllocIndex(key+"/raw", 0, false)
			if err != nil {
				return allocated, fmt.Errorf("нет свободного OpkgTun (raw): %w", err)
			}
			allocated = append(allocated, key+"/raw")
			c.RawNdmsIface = fmt.Sprintf("OpkgTun%d", idx)
			c.RawIface = fmt.Sprintf("opkgtun%d", idx)
		}
	case instancestore.KindFreeTurnClient:
		c := rec.FreeTurnClient
		if c == nil {
			return nil, fmt.Errorf("инстанс %s: нет конфига", key)
		}
		if c.Listen == "" {
			l, err := m.deps.AllocListen(key + "/listen")
			if err != nil {
				return allocated, fmt.Errorf("нет свободного listen-порта: %w", err)
			}
			allocated = append(allocated, key+"/listen")
			c.Listen = l
		}
	}
	return allocated, nil
}

// mutateStore — общий каркас мутаций: кандидат → объявление → запись.
// Порядок «объявление до записи» — требование 15: отказ реестра отклоняет
// операцию, пока диск не тронут. КОМПЕНСАЦИИ при отказе ДИСКА после
// успешного объявления НЕТ (снята по ревью как излишество): остаточное
// расхождение «реестр впереди диска» живёт до следующей мутации или боота —
// любой следующий SetDeclared строится от диска и выправляет реестр и
// зеркало. Это принято и названо здесь, чтобы ревью исполнения не «дочинило».
func (m *Manager) mutateStore(mutate func(*instancestore.State) error) (instancestore.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mutateStoreLocked(mutate)
}

func (m *Manager) mutateStoreLocked(mutate func(*instancestore.State) error) (instancestore.State, error) {
	if !m.booted {
		return instancestore.State{}, fmt.Errorf("прокси-подсистема не загружена (посев не прошёл: %s) — мутации отклоняются: ведомость была бы неполной", m.seedErr)
	}
	next, err := m.deps.Store.ReplaceChecked(mutate,
		// З1 (финальный круг): объявление — хуком beforeWrite, то есть ПОСЛЕ
		// нормализации и валидации записи: реестр и зеркало видят обрезанный
		// peer, а отказ валидации не оставляет реестр впереди диска.
		// Отказ реестра отменяет ЗАПИСЬ store (требование 15). Точность
		// формулировки (контрольный круг): сам SetDeclared не транзакционен —
		// при ЧАСТИЧНОМ отказе (registry.go:171-231 копит ошибки errors.Join)
		// память реестра и зеркальные записи уже обновлены, отменена только
		// наша запись. Самолечение: следующая мутация и боот строят ведомость
		// от диска, Sweep сносит осиротевшее.
		func(state instancestore.State) error {
			return m.deps.Registry.SetDeclared(declsOf(state.Records))
		})
	if err != nil {
		return instancestore.State{}, err
	}
	return next, nil
}

func (m *Manager) Create(ctx context.Context, rec instancestore.Record) error {
	m.mu.Lock()
	allocated, err := m.ensurePins(&rec)
	m.mu.Unlock()
	if err != nil {
		m.deps.ReleasePins(allocated...)
		return err
	}
	st, err := m.mutateStore(func(state *instancestore.State) error {
		state.Records = append(state.Records, rec)
		return nil
	})
	if err != nil {
		// Н6: запись не легла — свежие пины отдаются, иначе held течёт до
		// рестарта и индексы «заняты» несуществующим инстансом.
		m.deps.ReleasePins(allocated...)
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range st.Records {
		if r.Key() != rec.Key() {
			continue
		}
		live := newLive(r)
		inst, ferr := m.deps.Factory(r, live)
		if ferr != nil {
			return ferr
		}
		m.m[r.Key()] = &managed{live: live, inst: inst}
		inst.Start(m.ctx)
		inst.Post(proxyrt.EventIntentChanged)
	}
	return nil
}

// Update — правка записи. mutate получает УКАЗАТЕЛЬ и правит поля ПО МЕСТУ:
// пересборка записи литералом молча теряет CreatedAt/Sub/Users (задача 7
// предупреждает хендлеры о том же — замечание 11 ревью).
func (m *Manager) Update(ctx context.Context, key string, mutate func(*instancestore.Record) error) error {
	var allocated []string
	st, err := m.mutateStore(func(state *instancestore.State) error {
		for i := range state.Records {
			if state.Records[i].Key() == key {
				if err := mutate(&state.Records[i]); err != nil {
					return err
				}
				var perr error
				allocated, perr = m.ensurePins(&state.Records[i]) // Щ1: wg→raw и пустой listen
				return perr
			}
		}
		return fmt.Errorf("инстанс %s не найден", key)
	})
	if err != nil {
		m.deps.ReleasePins(allocated...) // Н6
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if mg, ok := m.m[key]; ok {
		for i := range st.Records {
			if st.Records[i].Key() == key {
				rec := st.Records[i]
				mg.live.rec.Store(&rec)
			}
		}
		// Update — ЕДИНСТВЕННАЯ точка правки записи: сюда сходятся PATCH
		// конфига всех четырёх ролей, импорт ссылки, обновление подписки,
		// правка абонентов сервера, смена намерения и запоминание адреса
		// последней ссылки (wdttlink.persistPeer). Первые пять — ручные
		// действия, которые могли устранить причину отказа старта, поэтому
		// пауза анти-флаппинга снимается здесь, а не швами по продуктовым
		// пакетам: их пришлось бы заводить шестью копиями и одну всё равно
		// забыть.
		//
		// Цена принята и она шире прежнего мира: тот не сбрасывал ни на смене
		// намерения, ни на запоминании адреса ссылки. Шестой путь безвреден:
		// persistPeer — побочная запись под условием «адрес изменился», а не
		// периодическая, так что сброса на ровном месте от неё не будет.
		//
		// Строго ДО побудки. Период перепроверки считается ПОСЛЕ цикла
		// (reconcile.go), и сброс, легший между отказом применения и этим
		// подсчётом, даёт Recheck=0: воркер получает пустой таймер, выбор по
		// нему не срабатывает никогда, и инстанс стоит до следующего внешнего
		// события — которого у отказавшего инстанса не будет.
		mg.inst.ResetStartBackoff()
		mg.inst.Post(proxyrt.EventIntentChanged)
	}
	return nil
}

func (m *Manager) SetEnabled(ctx context.Context, key string, on bool) error {
	return m.Update(ctx, key, func(r *instancestore.Record) error {
		r.Enabled = on
		return nil
	})
}

// Delete — порядок G3 (требование 3 + Щ5 + замечание 6): teardown-прогон с
// выключенным намерением → ограниченное ожидание → Stop (вне лока — Щ6) →
// удаление записи (отказ → воскрешение) → уборка → освобождение пинов.
//
// ОСТАТОЧНЫЙ РИСК НАЗВАН: teardown, не уложившийся в таймаут, оставляет
// ресурсы вне OpkgTun (правила netfilter сервера, INPUT-порты, client-routes)
// до ручной чистки — уборщик ходит только по интерфейсам. Warn в журнал.
func (m *Manager) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	_, hasInst := m.m[key]
	m.mu.Unlock()

	if hasInst {
		if err := m.SetEnabled(ctx, key, false); err != nil {
			return err
		}
		if !m.deps.WaitDisabled(key, teardownTimeout) {
			m.deps.Journal.Warn("delete", key,
				"teardown не завершился за отведённое время: ресурсы вне OpkgTun могли остаться (см. журнал инстанса)")
		}
	}

	m.mu.Lock()
	mg, ok := m.m[key]
	delete(m.m, key)
	m.mu.Unlock()
	var lastRec instancestore.Record
	if ok {
		lastRec = mg.live.Config()
		mg.inst.Stop() // вне m.mu (Щ6); Link закрывает инстанс (план 2)
	}

	st, err := m.mutateStore(func(state *instancestore.State) error {
		out := state.Records[:0]
		found := false
		for _, r := range state.Records {
			if r.Key() == key {
				found = true
				continue
			}
			out = append(out, r)
		}
		if !found && !ok {
			return fmt.Errorf("инстанс %s не найден", key)
		}
		state.Records = out
		return nil
	})
	if err != nil {
		if ok { // воскрешение (замечание 6): запись осталась — инстанс обязан жить
			m.mu.Lock()
			live := newLive(lastRec)
			if inst, ferr := m.deps.Factory(lastRec, live); ferr == nil {
				m.m[key] = &managed{live: live, inst: inst}
				inst.Start(m.ctx)
			} else {
				m.deps.Journal.Warn("delete", key, "воскрешение после отказа записи не удалось: "+ferr.Error())
			}
			m.mu.Unlock()
		}
		return err
	}

	declaredNDMS := instance.DeclaredNDMSNames(namedOf(st.Records))
	if _, serr := m.deps.Sweeper.Sweep(ctx, declaredNDMS); serr != nil {
		m.deps.Journal.Warn("delete", key, "уборка NDMS: "+serr.Error())
	}
	m.deps.ReleasePins(key, key+"/wg", key+"/raw", key+"/listen")
	return nil
}

func (m *Manager) Post(key string, k proxyrt.EventKind) bool {
	m.mu.Lock()
	mg, ok := m.m[key]
	m.mu.Unlock()
	if ok {
		return mg.inst.Post(k)
	}
	return false
}

func (m *Manager) PostAll(k proxyrt.EventKind) {
	m.mu.Lock()
	insts := make([]RunningInstance, 0, len(m.m))
	for _, mg := range m.m {
		insts = append(insts, mg.inst)
	}
	m.mu.Unlock()
	for _, inst := range insts {
		inst.Post(k)
	}
}

func (m *Manager) Records() []instancestore.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]instancestore.Record, 0, len(m.m))
	for _, mg := range m.m {
		out = append(out, mg.live.Config())
	}
	return out
}

// Shutdown гасит все инстансы; Stop — вне лока (Щ6).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	insts := make([]RunningInstance, 0, len(m.m))
	for _, mg := range m.m {
		insts = append(insts, mg.inst)
	}
	m.m = make(map[string]*managed)
	m.mu.Unlock()
	for _, inst := range insts {
		inst.Stop()
	}
}
