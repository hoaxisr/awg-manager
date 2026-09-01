// Package manager — жизненный цикл прокси-инстансов: единственная точка, где
// store намерения, реестр выходов, аллокатор, уборщик и воркеры встречаются.
// ЕДИНСТВЕННЫЙ писатель реестра выходов (G1): SetDeclared/MarkSeeded не зовёт
// больше никто — ворота плана держат это грепом.
package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instance"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/wdttclient"
)

// teardownTimeout — потолок ожидания teardown-прогона при удалении (Щ5).
const teardownTimeout = 10 * time.Second

type RegistryPort interface {
	SetDeclared([]exitreg.ExitDecl) error
	MarkSeeded(instances int) error
	// DropMirror снимает ОДНУ зеркальную запись адресно, мимо гейта посева:
	// массовая уборка при незаверенном посеве не зовётся вовсе, а гейт
	// монотонен (см. Delete и смену режима в update).
	DropMirror(id, ownerInstanceID string) error
}

type SweepPort interface {
	Sweep(ctx context.Context, declared map[string]bool) ([]string, error)
	// OwnedNames — имена всего, что сканер нашёл по нашим меткам. Нужен
	// уборке на пути удаления при незаверенном посеве (deleteSweepDeclared).
	OwnedNames(ctx context.Context) ([]string, error)
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
	AllocIndex func(owner string, pinned int, havePin bool) (int, error)
	// AllocListen выдаёт клиенту локальный listen. current — адрес, который уже
	// стоит в записи: годный (в пуле и ничей) возвращается как есть, негодный
	// заменяется свободным. Занятость считается без собственной записи
	// инстанса, поэтому selfKind/selfID.
	AllocListen func(ownerKey string, selfKind instancestore.Kind, selfID, current string) (string, error)
	ReleasePins func(ownerKeys ...string)
	// WaitDisabled — ограниченное ожидание teardown-прогона (Щ5): прод —
	// опрос StateStore до фазы disabled/settled свежее момента вызова.
	WaitDisabled func(key string, timeout time.Duration) bool
	// RecordsChanged — состав записей изменился (создание, удаление). Живёт
	// ЗДЕСЬ, а не в HTTP-обработчике: запись создаёт не только ручка
	// инстансов, но и импорт ссылки через Mutator, и подсказка инвалидации
	// его бы не заметила. nil — никого не уведомляем.
	RecordsChanged func(reason string)
}

// recordsChanged — уведомление о смене состава записей; nil-безопасно.
func (m *Manager) recordsChanged(reason string) {
	if m.deps.RecordsChanged != nil {
		m.deps.RecordsChanged(reason)
	}
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
	// Skipped — старые конфиги, которые посев не разобрал и пропустил.
	// Признак отдельный от Err: причину запертого гейта тот назовёт и так, а
	// вот сказать пользователю, ЧЬИ инстансы не перенеслись, можно только по
	// имени файла.
	Skipped []instancestore.SkippedSource
	// MovedListen — инстансы, которым посев сменил listen-адрес, разводя
	// конфликт за порт (амендмент G3). Признак живёт рядом со Skipped и по той
	// же причине: снаружи мог быть настроен клиент на прежний порт, и узнать о
	// переезде человек обязан.
	MovedListen []instancestore.ListenMove
}

type Manager struct {
	deps Deps

	// bootMu сериализует Boot сам с собой. Ретрай посева (задача 16) приводит
	// второго вызывающего: хуки NDMS и wan-up могут выстрелить одновременно, а
	// шаги боота — посев, PostSeed, сертификация, объявление — не рассчитаны на
	// параллельный прогон. Замок, а не однократность: отказ ОБЯЗАН пускать
	// повтор, иначе ретрай бессмыслен.
	bootMu sync.Mutex

	mu        sync.Mutex
	m         map[string]*managed
	booted    bool
	certified bool
	seedErr   string
	skipped   []instancestore.SkippedSource
	moved     []instancestore.ListenMove
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

// mirrorIDOf — id зеркальной записи удаляемой записи. Считается по РОЛИ, а не
// через declsOf: у wdtt-клиента в режиме wg выхода нет, но зеркальная запись,
// оставшаяся от прежнего raw-режима, всё равно его — и удаление инстанса
// последний момент, когда её есть кому снять.
//
// Проверка роли отсеивает и ПУСТУЮ запись (удалять было нечего): у неё Kind
// пуст. Это важнее, чем кажется — RawTunnelID подставляет на пустой id
// "default" (roles/wdttclient/role.go:39-41), то есть указал бы на ЧУЖУЮ
// запись wdttraw-default. Отдельный гард на пустой ID не нужен: запись из
// store без него не проходит валидацию (instancestore/store.go:425).
func mirrorIDOf(rec instancestore.Record) (string, bool) {
	if rec.Kind != instancestore.KindWdttClient {
		return "", false
	}
	return wdttclient.RawTunnelID(rec.ID), true
}

// declaresExit — объявляет ли запись выход ПРЯМО СЕЙЧАС. Тот же источник, что
// у ведомости (RawExiter/RawExit): признак «выход исчез» обязан считаться по
// тому самому правилу, по которому реестр строит ведомость, иначе адресный
// снос разъедется с объявлением.
func declaresExit(rec instancestore.Record) bool {
	ex := rec.RawExiter()
	if ex == nil { // запись мимо store: роли нет, объявлять нечего
		return false
	}
	_, ok := ex.RawExit()
	return ok
}

// recordByKey — запись из состояния store по её адресу; пустая, если такой
// нет. Пустая означает «объявлять нечего»: у неё нет роли.
func recordByKey(recs []instancestore.Record, key string) instancestore.Record {
	for _, r := range recs {
		if r.Key() == key {
			return r
		}
	}
	return instancestore.Record{}
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
	m.bootMu.Lock()
	defer m.bootMu.Unlock()

	res, err := m.deps.Seed(ctx)
	if err != nil {
		m.deps.Journal.Warn("boot", "proxy", "посев отложен: "+err.Error())
		m.mu.Lock()
		// Признак боота сбрасывается вместе с записью ошибки (амендмент D):
		// иначе повторный Boot после успешного отдал бы наружу Booted=true со
		// списком записей ПРОШЛОГО боота и текстом новой ошибки — интерфейс
		// показал бы живой рантайм там, где посев не состоялся. Проводка
		// зовёт Boot повторно только при !Booted, но гейт живёт у неё, а не
		// здесь, и ронять на нём честность SeedInfo нельзя.
		m.booted = false
		m.seedErr = err.Error()
		m.mu.Unlock()
		return err
	}
	list := res.State.Records
	// Занятый listen на бооте — не приговор. Посев разводит претендентов на
	// один порт ТОЛЬКО между записями (resolveListenConflicts), а занятость
	// шире: в неё входят и localhost-endpoint'ы AWG-туннелей. Такой конфликт
	// посев не видит, ресурс listen_port видит и отказывает — инстанс уходил
	// в blocked и сам оттуда не выбирался, потому что ensurePins стоит только
	// на путях Create и Update.
	//
	// Побочно закрывается вторая дыра: после рестарта демона held аллокатора
	// пуст, и до первой мутации порты живых записей не были ни за кем
	// закреплены. Здесь они закрепляются все.
	if moved := m.reconcileBootListen(&list); len(moved) > 0 {
		res.State.MovedListen = append(res.State.MovedListen, moved...)
	}
	declaredNDMS := instance.DeclaredNDMSNames(namedOf(list))

	// Переезд listen-порта — не деталь реализации: снаружи мог быть настроен
	// клиент на прежний адрес. Строка пишется на КАЖДОМ бооте, а не только на
	// свежем посеве: список приходит с диска, и человек, читающий журнал после
	// перезапуска, должен увидеть причину чужого молчания на старом порту.
	for _, mv := range res.State.MovedListen {
		m.deps.Journal.Warn("boot", "proxy", fmt.Sprintf(
			"listen-порт переехал: %s (%s) с %s на %s",
			mv.Instance, mv.Name, mv.From, mv.To))
	}

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
	// Пропущенный старый конфиг занижает len(list): сертификация отперла бы
	// уборку зеркальных записей, и та НЕОБРАТИМО снесла бы записи
	// непереехавших инстансов. Поэтому при непустом списке пропусков
	// MarkSeeded не зовём вовсе. Объявленная цена: гейт монотонный, снять его
	// нельзя — уборка останется запертой для этой установки навсегда. Это
	// плата за решение «пропуск без ретраев», а не дефект.
	if skipped := res.State.SkippedSources; len(skipped) > 0 {
		parts := make([]string, 0, len(skipped))
		for _, s := range skipped {
			parts = append(parts, s.File+": "+s.Reason)
		}
		certified, certErr = false, "пропущен неразобранный старый конфиг — "+strings.Join(parts, "; ")
		m.deps.Journal.Warn("boot", "proxy", "посев не сертифицирован, уборка заперта: "+certErr)
	} else if err := m.deps.Registry.MarkSeeded(len(list)); err != nil {
		certified, certErr = false, err.Error()
		m.deps.Journal.Warn("boot", "proxy", "посев не сертифицирован, уборка заперта: "+err.Error())
	}

	if err := m.deps.Registry.SetDeclared(declsOf(list)); err != nil {
		// Фатально: старт инстансов с невидимыми выходами — правила
		// пользователя, молча указывающие в никуда (M7 плана 4).
		m.deps.Journal.Warn("boot", "proxy", "объявление выходов: "+err.Error())
		m.mu.Lock()
		m.booted = false // тот же класс, что у отказа посева (амендмент D)
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
			m.booted = false // тот же класс, что у отказа посева (амендмент D)
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
	m.skipped = res.State.SkippedSources
	m.moved = res.State.MovedListen
	m.mu.Unlock()

	// Тот же гейт, что у зеркальной уборки (registry.go, гейт посева), и по
	// той же причине: ведомость NDMS-имён собрана из ТОГО ЖЕ списка, который
	// сертификация признала неполным. Незаверенный посев означает, что
	// интерфейсы непереехавших инстансов в ведомость не попали — уборщик снёс
	// бы их вместе с permit'ами политик, а permit живёт ровно столько, сколько
	// интерфейс, и пересозданием не воскрешается.
	//
	// Цена та же, что у амендмента D: осиротевшие интерфейсы доживут до
	// следующего успешного боота, а при пропущенном старом конфиге — навсегда,
	// потому что гейт монотонный.
	if !certified {
		m.deps.Journal.Warn("boot", "proxy", "уборка NDMS пропущена: посев не сертифицирован — "+certErr)
		return nil
	}
	if removedNDMS, serr := m.deps.Sweeper.Sweep(ctx, declaredNDMS); serr != nil {
		m.deps.Journal.Warn("boot", "proxy", "уборка NDMS: "+serr.Error())
	} else {
		m.deps.Journal.Info("boot", "proxy", sweptMessage(removedNDMS))
	}
	return nil
}

// dropMove — переезды без записи об инстансе key.
func dropMove(moves []instancestore.ListenMove, key string) []instancestore.ListenMove {
	out := moves[:0]
	for _, mv := range moves {
		if mv.Instance != key {
			out = append(out, mv)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AckListenMoves — пользователь прочитал уведомления о переезде listen-порта.
// Признание стирает их с диска: без него плашка висела вечно, потому что
// посев не повторяется и переписать свою отметку некому.
func (m *Manager) AckListenMoves() error {
	if _, err := m.mutateStore(func(state *instancestore.State) error {
		state.MovedListen = nil
		return nil
	}); err != nil {
		return err
	}
	m.mu.Lock()
	m.moved = nil
	m.mu.Unlock()
	return nil
}

func (m *Manager) SeedInfo() SeedInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return SeedInfo{Booted: m.booted, Certified: m.booted && m.certified, Err: m.seedErr,
		Skipped: m.skipped, MovedListen: m.moved}
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
// Пины OpkgTun идемпотентны: заполненные имена не трогает. Listen — особый,
// см. ниже: он сверяется каждый раз. Возвращает владельцев СВЕЖИХ
// аллокаций: если операция дальше сорвётся (отказ реестра/диска), вызывающий
// обязан отдать их обратно через ReleasePins — иначе held аллокатора течёт до
// рестарта (Н6 ревью).
//
// Listen спрашивается ВСЕГДА, а не только когда он пуст: годный порт
// аллокатор возвращает как есть, а негодный (вне пула либо занятый чужой
// записью) меняет на свободный. Прежде такой порт был приговором — ресурс
// listen_port отказывал, инстанс уходил в blocked и сам оттуда не выбирался.
// Момент подходящий: ensurePins стоит на пути и Create, и Update, а порт
// значит что-то только у запускаемого инстанса — старт идёт через Update.
// Endpoint связанного туннеля за переездом следует сам (linkres.LinkedEndpoint
// правит его ДО подъёма).
//
// Listen идёт под СВОИМ ключом владельца — key+"/listen" (I-3 ревью, круг 2),
// ровно как обе половины сервера (key+"/wg", key+"/raw"). Голый key брать
// нельзя: Allocator.Release (alloc.go:78-86) освобождает ВСЕ номера владельца,
// и возврат после listen-only-аллокации отобрал бы у ЖИВОЙ записи её индекс
// OpkgTun (путь Update: пин на диске есть, пуст только listen). Отдельный ключ
// сохраняет резерв — без него два параллельных Create (ручки задачи 7 — HTTP)
// получили бы от сканирующего аллокатора ОДИН порт: ensurePins и запись на
// диск не атомарны, а validateState уникальность Listen не проверяет.
//
// НАЗВАННАЯ ЦЕНА (F4 ревью задачи 14): зовётся ПОД m.mu (Create и update), а
// прод-AllocIndex внутри считает занятость пула OpkgTun — то есть ходит в RCI
// за списком интерфейсов NDMS. На время этого запроса вся поверхность
// /api/proxyrt/* и Shutdown стоят. Вынести вызов из-под замка нельзя дёшево:
// выделение и запись записи обязаны быть одной сериализованной операцией,
// иначе два параллельных Create получат один номер. Класс существовал у
// Create и до волны; ретраи боота (задача 16) лишь повышают частоту.
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
		l, err := m.deps.AllocListen(key+"/listen", rec.Kind, rec.ID, c.Listen)
		if err != nil {
			return allocated, fmt.Errorf("нет свободного listen-порта: %w", err)
		}
		allocated = append(allocated, key+"/listen")
		c.Listen = l
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
		l, err := m.deps.AllocListen(key+"/listen", rec.Kind, rec.ID, c.Listen)
		if err != nil {
			return allocated, fmt.Errorf("нет свободного listen-порта: %w", err)
		}
		allocated = append(allocated, key+"/listen")
		c.Listen = l
	}
	return allocated, nil
}

// reconcileBootListen сверяет локальные порты клиентов с занятостью и
// переселяет тех, чей порт негоден. Возвращает переезды; список записей
// правится по месту, чтобы воркеры стартовали уже с новыми портами.
//
// Отказ аллокатора (пул исчерпан) переездом не считается: запись остаётся с
// прежним портом и уйдёт в blocked ровно как раньше — это не хуже, чем было,
// а ронять боот из-за одного инстанса нельзя.
//
// Endpoint связанного AWG-туннеля за переездом идёт сам: LinkedEndpoint видит
// расхождение с listen и правит его ДО подъёма туннеля.
func (m *Manager) reconcileBootListen(list *[]instancestore.Record) []instancestore.ListenMove {
	recs := *list
	want := map[string]string{}
	var moves []instancestore.ListenMove
	for i := range recs {
		cur := instancestore.ClientListen(&recs[i])
		if cur == nil {
			continue // серверные роли: их listen задаёт пользователь
		}
		key := recs[i].Key()
		next, err := m.deps.AllocListen(key+"/listen", recs[i].Kind, recs[i].ID, *cur)
		if err != nil {
			m.deps.Journal.Warn("boot", "proxy", fmt.Sprintf(
				"инстанс %s: порт %s оставлен как есть — %v", key, *cur, err))
			continue
		}
		if next == *cur {
			continue
		}
		moves = append(moves, instancestore.ListenMove{
			Instance: key, Name: recs[i].Name, From: *cur, To: next})
		want[key] = next
	}
	if len(want) == 0 {
		return nil
	}
	next, err := m.deps.Store.Replace(func(st *instancestore.State) error {
		for i := range st.Records {
			if l, ok := want[st.Records[i].Key()]; ok {
				if p := instancestore.ClientListen(&st.Records[i]); p != nil {
					*p = l
				}
			}
		}
		// Молча сменить порт нельзя — тем же каналом, что и переезды посева.
		st.MovedListen = append(st.MovedListen, moves...)
		return nil
	})
	if err != nil {
		// Диск не принял — воркеры пойдут со СТАРЫМИ портами: правка в памяти
		// без записи означала бы, что после рестарта порт снова другой.
		m.deps.Journal.Warn("boot", "proxy", "переезд listen-портов не записан: "+err.Error())
		return nil
	}
	*list = next.Records
	return moves
}

// mutateStore — общий каркас мутаций: кандидат → объявление → запись.
// Порядок «объявление до записи» — требование 15: отказ реестра отклоняет
// операцию, пока диск не тронут. КОМПЕНСАЦИИ при отказе ДИСКА после
// успешного объявления НЕТ (снята по ревью как излишество): остаточное
// расхождение «реестр впереди диска» живёт до следующей мутации или боота —
// любой следующий SetDeclared строится от диска и выправляет реестр и
// зеркало. Это принято и названо здесь, чтобы ревью исполнения не «дочинило».
// errNotBooted — отказ мутации до состоявшегося посева: ведомость была бы
// неполной, а объявление неполной ведомости сносит зеркальные записи.
func errNotBooted(seedErr string) error {
	return fmt.Errorf("прокси-подсистема не загружена (посев не прошёл: %s) — мутации отклоняются: ведомость была бы неполной", seedErr)
}

func (m *Manager) mutateStore(mutate func(*instancestore.State) error) (instancestore.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mutateStoreLocked(mutate)
}

func (m *Manager) mutateStoreLocked(mutate func(*instancestore.State) error) (instancestore.State, error) {
	if !m.booted {
		return instancestore.State{}, errNotBooted(m.seedErr)
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
	// Идентификатор проверяется ДО записи. Раньше он проверялся только при
	// сборке пути управляющего сокета, то есть уже после того, как запись
	// легла на диск: пользователь получал отказ, а инстанс оставался — и
	// удалить его через API было нечем, потому что ключ с пробелом ручка не
	// находит ни в одной форме кодирования (стенд 2026-08-28). Заодно это
	// корень коллизии имён файлов: «ft x» и «ft_x» дают один путь данных.
	if err := control.ValidateInstance(rec.ID); err != nil {
		return err
	}
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
	// Уведомление ПОСЛЕ записи на диск и до сборки воркера: счётчик инстансов
	// читает диск, и отказ фабрики (запись есть, воркера нет) его не отменяет.
	m.recordsChanged("created")

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
//
// Возврат пинов идёт ВНЕ m.mu: он доходит до ведомости INPUT-портов, а та
// ходит в iptables — держать на этом лок менеджера значило бы вешать всю
// поверхность API на секунды.
func (m *Manager) Update(ctx context.Context, key string, mutate func(*instancestore.Record) error) error {
	allocated, err := m.update(key, mutate)
	if err != nil {
		m.deps.ReleasePins(allocated...) // Н6
		return err
	}
	return nil
}

// update — тело правки под m.mu. Порядок здесь несущий (Х1): мутатор и
// выделение пинов идут ДО транзакции хранилища, а транзакция кладёт готовую
// запись на место.
//
// Причина — дедлок, а не вкус: ensurePins ходит в аллокаторы, а прод-аллокаторы
// читают ТОТ ЖЕ store (им нужны пины и порты соседних записей). Замок store не
// реентрантен, поэтому выделение внутри Store.ReplaceChecked вешает Update
// НАВСЕГДА, а с ним — m.mu, то есть всю поверхность API и Shutdown. Так уже
// сделаны Create и посев; Update был последним, кто выделял внутри транзакции.
//
// Мутатор при этом прогоняется РОВНО ОДИН раз: холостой прогон по копии ради
// «какие пины понадобятся» был бы вторым исполнением чужого замыкания.
// Потери параллельной правки нет — все мутации менеджера сериализует m.mu, а
// других писателей у store после посева не бывает.
func (m *Manager) update(key string, mutate func(*instancestore.Record) error) (allocated []string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.booted {
		return nil, errNotBooted(m.seedErr)
	}
	cur, err := m.deps.Store.Load()
	if err != nil {
		return nil, err
	}
	cand, found := instancestore.Record{}, false
	for _, rec := range cur.Records {
		if rec.Key() == key {
			cand, found = rec, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("инстанс %s не найден", key)
	}
	// Объявление ДО правки. Снимается ЗДЕСЬ и отдельным значением, а не копией
	// записи: конфиг лежит за указателем, копия Record им не владеет, и после
	// mutate она показала бы уже новый режим. Роль проверять не нужно — выход
	// объявляет только raw-клиент.
	prevExitOwner := "" // id инстанса, объявлявшего выход до правки
	if declaresExit(cand) {
		prevExitOwner = cand.ID
	}
	// Прежний listen — ДО мутатора и ДО ensurePins: аллокатор молча меняет
	// негодный порт (занятый чужой записью либо вне пула), и без снимка
	// сказать о переезде было бы нечем.
	prevListen := ""
	if p := instancestore.ClientListen(&cand); p != nil {
		prevListen = *p
	}
	if err := mutate(&cand); err != nil {
		return nil, err
	}
	if allocated, err = m.ensurePins(&cand); err != nil { // Щ1: wg→raw и пустой listen
		return allocated, err
	}
	// PF16: канал уведомления о переезде ОДИН на все пути. Боот пишет свои
	// переезды в MovedListen (reconcileBootListen), правка молчала — при том
	// что снаружи мог быть настроен клиент на прежний адрес, и молчание здесь
	// стоит ровно столько же, сколько молчание там.
	//
	// Пустой prevListen пропускается намеренно: у создания прежнего порта нет,
	// «переезд с ничего» — не переезд. Роли без своего listen (серверные)
	// отсеивает сам ClientListen, возвращая nil.
	var moved []instancestore.ListenMove
	if p := instancestore.ClientListen(&cand); p != nil && prevListen != "" && *p != prevListen {
		moved = append(moved, instancestore.ListenMove{
			Instance: key, Name: cand.Name, From: prevListen, To: *p})
	}

	st, err := m.mutateStoreLocked(func(state *instancestore.State) error {
		for i := range state.Records {
			if state.Records[i].Key() == key {
				state.Records[i] = cand
				state.MovedListen = append(state.MovedListen, moved...)
				return nil
			}
		}
		// Запись исчезла между чтением и транзакцией: другого писателя у
		// store нет, поэтому сюда попадают только дефекты вызывающего.
		return fmt.Errorf("инстанс %s не найден", key)
	})
	if err != nil {
		return allocated, err
	}
	if len(moved) > 0 {
		m.moved = st.MovedListen
	}

	// Выход, которого больше нет (смена режима raw→wg): ведомость его уже не
	// содержит, а снять зеркальную запись могла бы только массовая уборка —
	// она заперта гейтом посева, и гейт монотонен, так что карточка туннеля
	// без выхода висела бы до перезапуска процесса. Снос адресный, ровно той
	// записи, что перестала быть выходом; сам инстанс живёт дальше в новом
	// режиме. Отказ — предупреждение, а не отказ правки: конфиг уже записан.
	//
	// Состояние ПОСЛЕ берётся из ЗАПИСАННОГО состояния: ровно из него собрана
	// ведомость, которую увидел реестр, — значит и «в ведомости этого выхода
	// больше нет» считается по нему же. По cand ответ сегодня тот же, но
	// только по случайности: конфиг лежит за указателем, общим с записью в
	// state, и режим ему приводит normalizeRecord уже внутри транзакции.
	// Опираться на это нельзя — PATCH кладёт connMode из тела запроса как
	// есть (api/proxy_instances.go:735), и "RAW" по cand читался бы как
	// исчезнувший выход.
	if prevExitOwner != "" && !declaresExit(recordByKey(st.Records, key)) {
		if derr := m.deps.Registry.DropMirror(wdttclient.RawTunnelID(prevExitOwner), prevExitOwner); derr != nil {
			m.deps.Journal.Warn("update", key, "зеркальная запись не убрана: "+derr.Error())
		}
	}

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
	return nil, nil
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

	// removed — запись, которую удаляем, как её видит store. Берётся здесь, а
	// не из lastRec: живого инстанса могло не быть вовсе (запись на диске без
	// воркера), а имена интерфейсов нужны уборке ниже в обоих случаях.
	var removed instancestore.Record
	st, err := m.mutateStore(func(state *instancestore.State) error {
		out := state.Records[:0]
		found := false
		for _, r := range state.Records {
			if r.Key() == key {
				found = true
				removed = r
				continue
			}
			out = append(out, r)
		}
		if !found && !ok {
			return fmt.Errorf("инстанс %s не найден", key)
		}
		state.Records = out
		// Уведомление о переезде listen-порта переживало своего инстанса:
		// плашка рассказывала про адрес того, кого уже нет, и убрать её было
		// нечем. Снимается той же транзакцией, что и запись.
		state.MovedListen = dropMove(state.MovedListen, key)
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

	// Кэш переездов идёт следом за диском: SeedInfo читает его, и без этой
	// строки плашка про удалённый инстанс дожила бы до перезапуска демона.
	m.mu.Lock()
	m.moved = st.MovedListen
	m.mu.Unlock()

	// Зеркальная запись — адресно, а не ведомостью: Sweep реестра заперт
	// гейтом посева, гейт монотонен, и запись пережила бы удаление инстанса
	// навсегда — пользователь остался бы с карточкой туннеля, за которой
	// ничего нет. Отказ не отменяет удаления: инстанса уже нет, откатывать
	// нечего.
	if id, ok := mirrorIDOf(removed); ok {
		if derr := m.deps.Registry.DropMirror(id, removed.ID); derr != nil {
			m.deps.Journal.Warn("delete", key, "зеркальная запись не убрана: "+derr.Error())
		}
	}

	// Пины отдаются ДО уборки: Sweep приговаривает ресурс, только если его
	// номер не закреплён в аллокаторе (sweep.go), а закреплён он как раз за
	// удаляемым инстансом — уборщик пропускал ровно те записи, ради которых
	// вызван, и OpkgTun удалённого инстанса доживал до следующего боота, съедая
	// индекс (стенд 2026-08-28). Обратный порядок безопасен: решение о сносе
	// принимается под локом аллокатора, и номер, уже перехваченный параллельным
	// Create, уборщик пропустит.
	m.deps.ReleasePins(key, key+"/wg", key+"/raw", key+"/listen")

	if declaredNDMS, lerr := m.deleteSweepDeclared(ctx, removed, st.Records); lerr != nil {
		// Ведомость не собрана — не сносим ничего: «не знаем» не равно «наш и
		// лишний» (тот же довод, что у Sweep на упавшем скане).
		m.deps.Journal.Warn("delete", key, "уборка NDMS пропущена: "+lerr.Error())
	} else if removedNDMS, serr := m.deps.Sweeper.Sweep(ctx, declaredNDMS); serr != nil {
		m.deps.Journal.Warn("delete", key, "уборка NDMS: "+serr.Error())
	} else {
		m.deps.Journal.Info("delete", key, sweptMessage(removedNDMS))
	}
	m.deleteDataDir(key, removed, st.Records)
	m.recordsChanged("deleted")
	return nil
}

// sweptMessage — итог уборки для журнала. Молчание уборщика стоило дефекта:
// «снёс две записи» и «пропустил обе» выглядели в журнале одинаково — никак,
// и пропуск по закреплённому номеру пережил стендовую приёмку (2026-08-28).
func sweptMessage(removed []string) string {
	if len(removed) == 0 {
		return "уборка NDMS: сносить нечего"
	}
	return "уборка NDMS: снято " + strings.Join(removed, ", ")
}

// deleteDataDir убирает данные удалённого инстанса — см. Record.DataTargets:
// что именно и почему их два у freeturn-сервера, знает запись, а не менеджер.
//
// Сносится только то, что лежит внутри своего поддерева каталога данных и
// только строго внутри: сверка с каталогом данных целиком пропускала бы и сам
// dataDir (os.RemoveAll снёс бы данные всего приложения), и каталоги соседей.
//
// И только то, на что не ссылается никто из ОСТАВШИХСЯ записей. Поддерево
// общее на всю подсистему, а путь может совпасть с чужим двумя способами:
// clientsFile правится через API и его можно навести на файл соседа, а имя
// файла по умолчанию строится из ID, где недопустимые символы заменяются
// подчёркиванием — «a b» и «a_b» дают один и тот же путь.
//
// Отказ не отменяет удаления: инстанса уже нет, откатывать нечего.
func (m *Manager) deleteDataDir(key string, removed instancestore.Record, left []instancestore.Record) {
	dir := m.deps.Store.Dir()
	claimed := map[string]bool{}
	for _, rec := range left {
		for _, t := range rec.DataTargets(dir) {
			claimed[filepath.Clean(t.Path)] = true
		}
	}
	for _, t := range removed.DataTargets(dir) {
		if claimed[filepath.Clean(t.Path)] {
			m.deps.Journal.Info("delete", key, "данные оставлены — ими владеет другой инстанс: "+t.Path)
			continue
		}
		if !strictlyUnder(filepath.Join(dir, t.Root), t.Path) {
			m.deps.Journal.Warn("delete", key,
				"данные не убраны: путь вне "+t.Root+" в каталоге данных: "+t.Path)
			continue
		}
		// Путь по умолчанию есть у каждого freeturn-сервера, а список мог не
		// включаться ни разу: RemoveAll на отсутствующем пути молчит, и запись
		// «данные удалены» в журнале была бы неправдой.
		if _, err := os.Lstat(t.Path); os.IsNotExist(err) {
			continue
		}
		if err := os.RemoveAll(t.Path); err != nil {
			m.deps.Journal.Warn("delete", key, "данные не убраны: "+err.Error())
			continue
		}
		m.deps.Journal.Info("delete", key, "данные удалены: "+t.Path)
	}
}

// strictlyUnder — лежит ли path СТРОГО внутри dir. Оба приводятся к чистому
// виду: «..» в пути пользователя иначе увели бы снос наружу.
//
// Равенство путей — не «внутри»: иначе снос уносил бы само поддерево вместе с
// данными всех остальных инстансов. Тот же запрет стоит у соседнего сторожа
// снаружи прокси-рантайма (singbox/router.isManagedLocalRuleSet).
func strictlyUnder(dir, path string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// deleteSweepDeclared — ведомость уборщика на пути удаления инстанса.
//
// Заверенный посев: обычная ведомость из ОСТАВШИХСЯ записей — список полон, и
// уборка заодно подбирает сирот.
//
// Незаверенный: список записей неполон (амендмент F), интерфейсов
// непереехавших инстансов в нём нет, и обычная ведомость приговорила бы их
// вместе с permit'ами политик — необратимо. Запереть уборку здесь нельзя:
// сертификация монотонна, и интерфейс ТОЛЬКО ЧТО удалённого инстанса остался
// бы сиротой навсегда. Поэтому ведомость строится наоборот — всё, что нашёл
// сканер, МИНУС имена удаляемого: снесётся ровно то, что удаляем, а всё
// незнакомое уцелеет.
func (m *Manager) deleteSweepDeclared(ctx context.Context, removed instancestore.Record,
	left []instancestore.Record) (map[string]bool, error) {
	if m.SeedInfo().Certified {
		return instance.DeclaredNDMSNames(namedOf(left)), nil
	}
	found, err := m.deps.Sweeper.OwnedNames(ctx)
	if err != nil {
		return nil, err
	}
	doomed := instance.DeclaredNDMSNames(namedOf([]instancestore.Record{removed}))
	declared := make(map[string]bool, len(found))
	for _, name := range found {
		if !doomed[name] {
			declared[name] = true
		}
	}
	return declared, nil
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
