package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/proxyapp/captcha"
	"github.com/hoaxisr/awg-manager/internal/proxyapp/ftlink"
	"github.com/hoaxisr/awg-manager/internal/proxyapp/install"
	proxysub "github.com/hoaxisr/awg-manager/internal/proxyapp/subscription"
	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttusers"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instance"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/manager"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/freeturn"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/procres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/wdttclient"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/wdttserver"
	"github.com/hoaxisr/awg-manager/internal/server"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/traffic"
)

// Проводка прокси-рантайма: аллокаторы номеров и портов, посев, менеджер,
// фабрика инстансов и продуктовые ручки под /api/proxyrt/*.

// proxySubgroup — подгруппа app-журнала прокси-рантайма (у старого мира была
// своя, "wdtt": рантайм один на обе подсистемы, поэтому подгруппа общая).
const proxySubgroup = "proxy"

// proxyBackendWdttRaw — бэкенд зеркальной записи raw-клиента. Локальная
// константа, а не импорт internal/wdtt: тот пакет умирает вместе со старым
// движком (тот же приём, что у DefaultWdttIface).
const proxyBackendWdttRaw = "wdtt-raw"

// proxyLogTailBytes — сколько байт хвоста журнала процесса читается для
// ручки инстансов и решателя капчи. Журнал форка пишется в tmpfs и растёт.
const proxyLogTailBytes = 64 << 10

// proxyLogTailLines — сколько строк из этого хвоста уходит наружу.
const proxyLogTailLines = 200

// ── связи с процессами ───────────────────────────────────────────

// proxyLinkBook — связи инстансов по ключу записи. Владелец каждой связи —
// инстанс (её закрывает instance.Stop), здесь только ССЫЛКИ: снимок процесса
// нужен ручке инстансов, решателю капчи и доставке SIGHUP абонентам, а тем
// добраться до инстанса нечем.
//
// Записи не удаляются: карту читают только по ключам живых записей менеджера,
// а пересоздание инстанса перезаписывает ссылку.
type proxyLinkBook struct {
	mu sync.Mutex
	m  map[string]*control.Link
}

func newProxyLinkBook() *proxyLinkBook {
	return &proxyLinkBook{m: map[string]*control.Link{}}
}

func (b *proxyLinkBook) put(key string, l *control.Link) {
	b.mu.Lock()
	b.m[key] = l
	b.mu.Unlock()
}

func (b *proxyLinkBook) get(key string) (*control.Link, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.m[key]
	return l, ok
}

// snapshot — последнее, что процесс инстанса о себе рассказал.
func (b *proxyLinkBook) snapshot(key string) (awgmproto.State, bool) {
	l, ok := b.get(key)
	if !ok {
		return awgmproto.State{}, false
	}
	snap, ok := l.Snapshot()
	if !ok {
		return awgmproto.State{}, false
	}
	return snap.State, true
}

// ── занятость номеров OpkgTun ────────────────────────────────────

// proxyOpkgOccupancy — занятость пула OpkgTun: живое (только /sys) плюс пины
// ЧЕТЫРЁХ владельцев — записи AWG-туннелей, удерживающая запись настроек,
// записи NDMS и записи прокси-инстансов.
//
// Состав собран здесь, а не в месте вызова, потому что он и есть контракт:
// выпавший поставщик не ломает ни сборку, ни один прогон — он просто отдаёт
// чужой номер как свободный, и коллизия всплывает интерфейсом, который увели
// у соседней подсистемы.
//
// Записи NDMS приходят ОТДЕЛЬНЫМ поставщиком, а не половиной живого: после
// `ip link del opkgtunN` запись живёт дальше со state error, устройства нет.
// Номер занят, интерфейс мёртв, и одна карта на оба вопроса врёт.
func proxyOpkgOccupancy(live storage.OpkgTunIndexLister, ndmsPins storage.OpkgTunPins,
	awg *storage.AWGTunnelStore, settings *storage.SettingsStore, store *instancestore.Store,
) storage.OpkgTunPins {
	return storage.OpkgTunOccupancy(live,
		awg.OpkgTunPinsOf,
		settings.OpkgTunPinsOf,
		ndmsPins,
		proxyRecordPins(store),
	)
}

// proxyRecordPins — четвёртый поставщик пинов пула OpkgTun: записи
// прокси-инстансов. Три остальных (записи туннелей, удерживающая запись
// настроек, записи NDMS) приходят готовыми из internal/storage и адаптера
// NDMS — здесь только своё.
func proxyRecordPins(store *instancestore.Store) storage.OpkgTunPins {
	return func(context.Context) (map[int]bool, error) {
		st, err := store.Load()
		if err != nil {
			return nil, err
		}
		pins := map[int]bool{}
		for _, rec := range st.Records {
			for _, name := range proxyRecordIfaces(rec) {
				if idx, ok := opkgTunIndex(name); ok {
					pins[idx] = true
				}
			}
		}
		return pins, nil
	}
}

// proxyRecordIfaces — NDMS-имена, которые держит запись. У сервера их два:
// WG-половина и raw-половина.
func proxyRecordIfaces(rec instancestore.Record) []string {
	switch {
	case rec.WdttClient != nil:
		return []string{rec.WdttClient.NdmsIface}
	case rec.WdttServer != nil:
		return []string{rec.WdttServer.NdmsIface, rec.WdttServer.RawNdmsIface}
	}
	return nil
}

// proxyOwnPin — пин, принадлежащий ИМЕННО этому владельцу, и признак того,
// есть ли у владельца запись вообще. Владелец бывает трёх форм: key
// (raw-клиент), key+"/wg" и key+"/raw" (половины сервера) — суффикс выбирает
// ПОЛЕ записи. Второй пин той же записи собственным НЕ считается: это другой
// интерфейс, и коллизия с ним запрещена.
func proxyOwnPin(recs []instancestore.Record, owner string) (idx int, ok, haveRecord bool) {
	key, field := owner, ""
	if i := strings.LastIndex(owner, "/"); i >= 0 {
		key, field = owner[:i], owner[i+1:]
	}
	for _, rec := range recs {
		if rec.Key() != key {
			continue
		}
		switch {
		case field == "" && rec.WdttClient != nil:
			idx, ok = opkgTunIndex(rec.WdttClient.NdmsIface)
		case field == "wg" && rec.WdttServer != nil:
			idx, ok = opkgTunIndex(rec.WdttServer.NdmsIface)
		case field == "raw" && rec.WdttServer != nil:
			idx, ok = opkgTunIndex(rec.WdttServer.RawNdmsIface)
		}
		return idx, ok, true
	}
	return 0, false, false
}

// proxyAllocIndex — формула taken для manager.Deps.AllocIndex и SeedDeps:
// общая занятость МИНУС собственные пины владельца.
//
// Собственный пин берётся из ЗАПИСИ владельца, а при её отсутствии — из
// заявленного pinned. Развилка не косметическая: пока записи нет (посев),
// живой интерфейс с этим номером принадлежит тому же инстансу, и без
// вычитания усыновление превратилось бы в перепин с повисшими permit'ами
// пользователя. Как только запись есть, своим считается ровно пин ЕЁ поля:
// заявка на чужой номер (в том числе на вторую половину собственного
// сервера) собственной не становится.
//
// Fail-closed: отказ любого поставщика занятости — отказ аллокации. Неполная
// картина читается как «номер свободен», а это единственное направление
// ошибки, дающее коллизию интерфейсов.
func proxyAllocIndex(ctx context.Context, alloc *proxyrt.Allocator, min int,
	occupancy storage.OpkgTunPins, store *instancestore.Store,
) func(owner string, pinned int, havePin bool) (int, error) {
	return func(owner string, pinned int, havePin bool) (int, error) {
		taken, err := occupancy(ctx)
		if err != nil {
			return 0, err
		}
		st, err := store.Load()
		if err != nil {
			return 0, err
		}
		switch idx, ok, haveRecord := proxyOwnPin(st.Records, owner); {
		case ok:
			delete(taken, idx)
		case !haveRecord && havePin:
			// Записи ещё нет — это посев: заявленный пин прочитан из СТАРОГО
			// конфига того же инстанса, и живой интерфейс с этим номером —
			// его собственный.
			delete(taken, pinned)
		}
		// Сентинел «пина нет» — min-1, а не ноль: на mips диапазон начинается
		// с нуля, и ноль там законный пин (alloc.go сверяет pinned с
		// диапазоном, всё вне него игнорируя).
		p := min - 1
		if havePin {
			p = pinned
		}
		return alloc.AllocIndex(owner, p, taken)
	}
}

// proxyAllocListen — выдача локального listen-порта клиенту. РЕЗЕРВИРУЮЩАЯ:
// свой аллокатор с собственным ключом владельца (key+"/listen"), а не скан
// занятых портов. Скан отдал бы двум параллельным Create ОДИН порт — запись
// на диск и выделение не атомарны, а уникальность Listen хранилище не
// проверяет.
func proxyAllocListen(ctx context.Context, alloc *proxyrt.Allocator,
	store *instancestore.Store, tunnels *storage.AWGTunnelStore,
) func(ownerKey string) (string, error) {
	return func(ownerKey string) (string, error) {
		// Занятость — порты ВСЕХ записей store и localhost-endpoint'ы
		// связанных туннелей (паритет OccupiedLocalListenPorts старого мира).
		// Своя запись не исключается: AllocListen зовётся только при пустом
		// Listen, собственного порта у записи в этот момент нет.
		occ := newProxyOccupancy(store, tunnels, "", "")
		taken, err := occ.OccupiedLocalListenPorts(ctx)
		if err != nil {
			return "", fmt.Errorf("занятость портов: %w", err)
		}
		p, err := alloc.AllocIndex(ownerKey, roles.ListenPortMin-1, taken)
		if err != nil {
			return "", fmt.Errorf("нет свободного порта в %d..%d: %w",
				roles.ListenPortMin, roles.ListenPortMax, err)
		}
		return fmt.Sprintf("127.0.0.1:%d", p), nil
	}
}

// proxyReleasePins — возврат свежих аллокаций и снятие вклада инстанса из
// ведомости INPUT-портов.
//
// Без аргументов — no-op: Update зовёт с nil на любом отказе. Неизвестные
// владельцы терпятся молча: Delete зовёт четыре ключа вслепую.
func proxyReleasePins(ctx context.Context, opkg, port *proxyrt.Allocator,
	book *proxyFWBook, journal instance.Journal,
) func(ownerKeys ...string) {
	return func(ownerKeys ...string) {
		for _, k := range ownerKeys {
			opkg.Release(k)
			port.Release(k)
		}
		if len(ownerKeys) == 0 {
			return
		}
		// Точка названа по пинам, а делает шире: явный хук снятия вклада в
		// менеджере был бы чище, но требует правки закрытой задачи ради
		// одного вызова. Безопасно потому, что ресурс input_port есть только
		// у серверных ролей — клиентского ключа в ведомости не бывает, и
		// forget по нему no-op.
		if err := book.forget(ctx, ownerKeys[0]); err != nil {
			journal.Warn("release-pins", ownerKeys[0], "ведомость портов: "+err.Error())
		}
	}
}

// ── журнал процесса ──────────────────────────────────────────────

// proxyImplRole — значения impl и role протокола для роли записи. Из них
// строятся пути сокета и журнала процесса.
func proxyImplRole(kind instancestore.Kind) (impl, role string, ok bool) {
	switch kind {
	case instancestore.KindWdttClient:
		return roles.ImplWtClient, roles.RoleClient, true
	case instancestore.KindWdttServer:
		return roles.ImplWdttServer, roles.RoleServer, true
	case instancestore.KindFreeTurnClient:
		return roles.ImplFtClient, roles.RoleClient, true
	case instancestore.KindFreeTurnServer:
		return roles.ImplFtServer, roles.RoleServer, true
	}
	return "", "", false
}

// proxyLogTail — хвост журнала процесса инстанса. ОДИН источник на ручку
// инстансов и на решатель капчи: разойдись они, старый баннер капчи вернул бы
// «ждёт капчу» там, где ручка показывает свежий журнал.
func proxyLogTail(recs func() []instancestore.Record) func(key string) string {
	return func(key string) string {
		for _, rec := range recs() {
			if rec.Key() != key {
				continue
			}
			impl, role, ok := proxyImplRole(rec.Kind)
			if !ok {
				return ""
			}
			path, err := control.LogPath(roles.RuntimeDir, impl, role, rec.ID)
			if err != nil {
				return ""
			}
			return readTail(path, proxyLogTailBytes, proxyLogTailLines)
		}
		return ""
	}
}

// readTail — последние lines строк из последних maxBytes байт файла.
func readTail(path string, maxBytes int64, lines int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := st.Size() - maxBytes
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, 0); err != nil {
		return ""
	}
	buf := make([]byte, st.Size()-off)
	n, _ := f.Read(buf)
	text := string(buf[:n])
	if off > 0 {
		// Первая строка обрезана серединой — выбрасываем её целиком.
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	rows := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	return strings.Join(rows, "\n")
}

// ── связанные AWG-туннели ────────────────────────────────────────

// proxyLinkedCleaner — wdttlink.LinkedCleaner для ОДНОЙ роли: поле связи у
// подсистем разное, и один уборщик на обе выбрать его не может.
type proxyLinkedCleaner struct {
	store   *storage.AWGTunnelStore
	svc     api.TunnelService
	field   api.LinkedField
	traffic *traffic.History
	pub     proxyrt.Publisher
}

func (c proxyLinkedCleaner) DeleteLinked(ctx context.Context, clientID string) (deleted []string, errs []string) {
	if strings.TrimSpace(clientID) == "" {
		return nil, nil
	}
	// ГРОМКО: старый deleteLinkedAwgTunnels на неподключённом хранилище
	// отвечал «удалено ноль», и очистка выглядела успешной, ничего не сделав.
	if c.store == nil || c.svc == nil {
		return nil, []string{"хранилище туннелей не подключено — связанные туннели не удалены"}
	}
	tunnels, err := c.store.List()
	if err != nil {
		return nil, []string{err.Error()}
	}
	for _, tun := range tunnels {
		if !proxyTunnelLinkedTo(tun, c.field, clientID) {
			continue
		}
		// Зеркальная запись raw-клиента — проекция ЖИВОГО инстанса, чьи связи
		// сейчас снимают (уборщика зовёт только ручка clear по существующей
		// записи). Снести её здесь значило бы соврать: ближайшее объявление
		// создаст запись заново, но с дефолтами, и настройки карточки пропадут
		// молча (амендмент F2). Уносит запись удаление инстанса — через
		// зеркало.
		//
		// Не кандидат, а не отказ: «связанный AWG-туннель» в пользовательском
		// смысле — то, что пользователь вправе снять, а проекцию инстанса он
		// снять не может в принципе. Ошибка здесь была бы у КАЖДОГО
		// raw-клиента и читалась бы как сбой штатной операции. Прямое
		// удаление записи отказ по-прежнему получает (api/tunnels_crud.go).
		if tun.Backend == proxyBackendWdttRaw {
			continue
		}
		if err := c.svc.Delete(ctx, tun.ID); err != nil {
			errs = append(errs, fmt.Sprintf("%s (%s): %v", tun.Name, tun.ID, err))
			continue
		}
		if c.traffic != nil {
			c.traffic.Clear(tun.ID)
		}
		deleted = append(deleted, tun.ID)
	}
	if len(deleted) > 0 {
		proxyPublishTunnels(c.pub, "proxy-linked-tunnel-delete")
	}
	return deleted, errs
}

// proxyLinkedField — поле связи AWG-туннеля для роли клиента (амендмент B).
// Одно место на всех потребителей: перепутанное поле не даёт ни ошибки, ни
// отказа — только ПУСТОЙ список связанных туннелей и вечное молчание, а с
// четырьмя литералами по проводке спутать их вопрос времени.
func proxyLinkedField(kind instancestore.Kind) api.LinkedField {
	if kind == instancestore.KindFreeTurnClient {
		return api.LinkedFreeTurn
	}
	return api.LinkedWdtt
}

func proxyTunnelLinkedTo(tun storage.AWGTunnel, field api.LinkedField, clientID string) bool {
	if field == api.LinkedFreeTurn {
		return strings.TrimSpace(tun.FreeTurnClientID) == clientID
	}
	return strings.TrimSpace(tun.WdttClientID) == clientID
}

// proxyLinkedCleaners — уборщики связанных туннелей ПО РОЛИ. Карта собирается
// здесь, а не литералом в месте вызова: поле связи у ролей разное, а ошибка в
// нём не даёт ни отказа, ни жалобы — просто чужие туннели остаются, а свои не
// удаляются. Один источник поля (proxyLinkedField) и одно место сборки — чтобы
// перепутать было негде.
func proxyLinkedCleaners(store *storage.AWGTunnelStore, svc api.TunnelService,
	traffic *traffic.History, pub proxyrt.Publisher,
) map[instancestore.Kind]wdttlink.LinkedCleaner {
	out := map[instancestore.Kind]wdttlink.LinkedCleaner{}
	for _, kind := range []instancestore.Kind{
		instancestore.KindWdttClient, instancestore.KindFreeTurnClient,
	} {
		out[kind] = proxyLinkedCleaner{store: store, svc: svc,
			field: proxyLinkedField(kind), traffic: traffic, pub: pub}
	}
	return out
}

// proxyPublishTunnels — фронт обязан узнать об изменении списка туннелей:
// пути прокси-рантайма живут вне HTTP-хендлеров, где публикацию делал бы
// TunnelsHandler.
func proxyPublishTunnels(pub proxyrt.Publisher, reason string) {
	if pub == nil {
		return
	}
	for _, res := range []string{api.ResourceTunnels, api.ResourceRoutingTunnels} {
		pub.Publish("resource:invalidated", events.ResourceInvalidatedEvent{
			Resource: res, Reason: reason,
		})
	}
}

// proxyTunnelImporter — wdttlink.TunnelImporter поверх хранилища и службы
// туннелей. Снятие истории трафика и публикация списка — ОТДЕЛЬНЫЕ методы
// интерфейса: спрятанные в чужом Delete, они терялись молча.
type proxyTunnelImporter struct {
	store   *storage.AWGTunnelStore
	svc     api.TunnelService
	traffic *traffic.History
	pub     proxyrt.Publisher
}

func (t proxyTunnelImporter) List() ([]storage.AWGTunnel, error) { return t.store.List() }

func (t proxyTunnelImporter) Get(tunnelID string) (*storage.AWGTunnel, error) {
	return t.store.Get(tunnelID)
}

func (t proxyTunnelImporter) Save(tun *storage.AWGTunnel) error { return t.store.Save(tun) }

func (t proxyTunnelImporter) Delete(ctx context.Context, tunnelID string) error {
	return t.svc.Delete(ctx, tunnelID)
}

func (t proxyTunnelImporter) Import(ctx context.Context, conf, name string) (string, string, error) {
	// Бэкенд пустой — тот же аргумент, что у старой ручки: его выбирает сама
	// служба по прошивке.
	res, err := t.svc.Import(ctx, conf, name, "")
	if err != nil {
		return "", "", err
	}
	return res.ID, res.Name, nil
}

func (t proxyTunnelImporter) Start(ctx context.Context, tunnelID string) error {
	return t.svc.Start(ctx, tunnelID)
}

func (t proxyTunnelImporter) ForgetTraffic(tunnelID string) {
	if t.traffic != nil {
		t.traffic.Clear(tunnelID)
	}
}

func (t proxyTunnelImporter) PublishList(context.Context) {
	proxyPublishTunnels(t.pub, "proxy-linked-tunnel-import")
}

// ── обёртки менеджера для продуктовых пакетов ────────────────────

// proxyManagerRef — менеджер, которого ещё нет в момент, когда его требуют
// зависимости: фабрика инстансов нужна КОНСТРУКТОРУ менеджера, а сама зовёт
// его Post, и продуктовые пакеты держат обёртки над ним. Развязать иначе
// нечем; ссылка проставляется сразу после manager.New и до первого Boot.
type proxyManagerRef struct{ mgr *manager.Manager }

// proxySubsystemOf — подсистема роли. Нужна гейту удаления бинарей: снимать
// их можно, только если инстансов ЭТОЙ подсистемы не осталось.
func proxySubsystemOf(kind instancestore.Kind) install.Subsystem {
	switch kind {
	case instancestore.KindWdttClient, instancestore.KindWdttServer:
		return install.SubsystemWdtt
	case instancestore.KindFreeTurnClient, instancestore.KindFreeTurnServer:
		return install.SubsystemFreeTurn
	}
	return ""
}

// proxyRecords — wdttlink.RecordSource поверх менеджера.
type proxyRecords struct{ ref *proxyManagerRef }

func (r proxyRecords) Get(key string) (instancestore.Record, bool) {
	for _, rec := range r.ref.mgr.Records() {
		if rec.Key() == key {
			return rec, true
		}
	}
	return instancestore.Record{}, false
}

// proxyMutator — wdttlink.Mutator поверх менеджера: правка записей идёт
// ЕДИНСТВЕННЫМ путём, через Update/Create.
type proxyMutator struct{ ref *proxyManagerRef }

func (m proxyMutator) Update(ctx context.Context, key string, mutate func(*instancestore.Record) error) error {
	return m.ref.mgr.Update(ctx, key, mutate)
}

func (m proxyMutator) Create(ctx context.Context, rec instancestore.Record) error {
	return m.ref.mgr.Create(ctx, rec)
}

// proxyInstanceLister — captcha.RecordLister поверх менеджера.
type proxyInstanceLister struct{ ref *proxyManagerRef }

func (l proxyInstanceLister) Records() []instancestore.Record { return l.ref.mgr.Records() }

// proxyBinaryDownloader — install.Downloader поверх общего загрузчика.
// Свой, а не заимствованный у умирающих подсистем: пакет install ставит
// бинари ОБЕИХ, и чужая метка назначения врала бы в телеметрии половине
// загрузок.
type proxyBinaryDownloader struct{ svc *downloader.Service }

func (d proxyBinaryDownloader) DownloadFile(ctx context.Context, url, destPath string, maxBytes int64) error {
	if d.svc == nil {
		return fmt.Errorf("загрузчик не подключён")
	}
	_, err := d.svc.DownloadFile(ctx, downloader.FileRequest{
		Request: downloader.Request{
			Purpose: "proxy-binary", URL: url, Timeout: 5 * time.Minute,
		},
		DestPath: destPath, TempPath: destPath,
		MaxFileBytes: maxBytes, Mode: 0o644, Atomic: false,
	})
	return err
}

// ── системные мелочи ─────────────────────────────────────────────

// proxyIfaceExists — жив ли kernel-интерфейс. Адрес NDMS ставится только
// после появления netdev от процесса.
func proxyIfaceExists(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	_, err := os.Stat(filepath.Join("/sys/class/net", name))
	return err == nil
}

// proxyEnableForward — включение маршрутизации вместе с правилами сервера
// (паритет старого entware-пути).
func proxyEnableForward() error {
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644)
}

// proxyRunHook — прогон netfilter.d-хука по одной таблице сразу после записи:
// правила встают, не дожидаясь перезаписи таблиц движком ndm.
func proxyRunHook(ctx context.Context, path, table string) error {
	_, err := exec.Run(ctx, "sh", "-c", "table="+table+" type=iptables sh "+path)
	return err
}

// proxyWaitDisabled — ограниченное ожидание teardown-прогона: фаза
// disabled/settled со временем ОБНОВЛЕНИЯ свежее момента вызова.
func proxyWaitDisabled(states *proxyrt.StateStore) func(key string, timeout time.Duration) bool {
	return func(key string, timeout time.Duration) bool {
		since := time.Now()
		deadline := since.Add(timeout)
		for {
			st, ok := states.Get(key)
			if ok && !st.UpdatedAt.Before(since) &&
				(st.Phase == proxyrt.PhaseDisabled || st.Phase == proxyrt.PhaseSettled) {
				return true
			}
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// proxySignalReload — просьба ЖИВОМУ серверу перечитать passwords.json.
// Единственный производитель поля reload у ручек абонентов.
func proxySignalReload(links *proxyLinkBook) func(key string) (bool, error) {
	return func(key string) (bool, error) {
		st, ok := links.snapshot(key)
		if !ok || st.PID <= 0 {
			return false, nil // сервер не запущен: файл вступит в силу при старте
		}
		if err := killPID(st.PID, syscall.SIGHUP); err != nil {
			return false, err
		}
		return true, nil
	}
}

// proxyTunnels — служба туннелей для адаптеров прокси-рантайма. Метод, а не
// прямое чтение поля: конкретный указатель, положенный в интерфейс, делает его
// НЕпустым даже будучи nil, и все проверки вида `if svc != nil` вниз по стеку
// (api.ListLinkedProxyTunnels, proxyLinkedCleaner) на нём не срабатывают —
// вместо честного «служба не подключена» получается разыменование nil.
func (a *app) proxyTunnels() api.TunnelService {
	if a.tunnelService == nil {
		return nil
	}
	return a.tunnelService
}

// proxyExternalIP — внешний адрес роутера для сборщиков ссылок (перенос
// resolveExternalIP старых хендлеров).
func (a *app) proxyExternalIP(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var fallback testing.WANIPFallback
	var wanKernel string
	if a.ndmsQueries != nil {
		fallback = a.ndmsQueries.WANInterfaceAddress
		if a.ndmsQueries.Routes != nil && a.ndmsQueries.Interfaces != nil {
			if name, err := a.ndmsQueries.Routes.GetDefaultGatewayInterface(ctx); err == nil && name != "" {
				wanKernel = a.ndmsQueries.Interfaces.ResolveSystemName(ctx, name)
			}
		}
	}
	return testing.GetWANIPBound(ctx, wanKernel, fallback)
}

// ── усыновление абонентов сервера (fail-closed) ──────────────────

// proxyUsersResource — идентификатор ресурса-гейта абонентов. Заведён здесь, а
// не в roles/ids.go: сам гейт — принадлежность проводки, роль о нём не знает.
const proxyUsersResource proxyrt.ResourceID = "server_users"

// proxyUsersRetry — период повтора усыновления. Внешнего события у этой беды
// нет (каталог конфига появился, диск отпустило), поэтому без подстраховочной
// сверки сервер стоял бы до перезапуска демона.
const proxyUsersRetry = 30 * time.Second

// proxyBlocked — ресурс-приговор: объявляет причину, по которой инстансу
// нельзя работать, и валит прогон в фазу failed. Нужен там, где приговор
// выносит НЕ конфиг роли: у ролей вердикт приходит из Validate(), а причина
// «абоненты не усыновлены» роли неизвестна.
type proxyBlocked struct {
	id     proxyrt.ResourceID
	reason error
}

func (b proxyBlocked) ID() proxyrt.ResourceID { return b.id }

func (b proxyBlocked) Observe(context.Context) (proxyrt.Observation, error) {
	return proxyrt.Observation{Known: true, Exists: false, Detail: b.reason.Error()}, nil
}

func (b proxyBlocked) Plan(proxyrt.Observation) []proxyrt.Step {
	return []proxyrt.Step{{Resource: b.id, Op: "fail", Reason: b.reason.Error()}}
}

func (b proxyBlocked) Apply(context.Context, proxyrt.Step) error { return b.reason }

func (b proxyBlocked) RecheckAfter() time.Duration { return proxyUsersRetry }

var _ proxyrt.Resource = proxyBlocked{}

// proxyUsersGate — цикл абонентов на пути старта, доведённый до успеха.
// Повторяется, пока не пройдёт; после первого успеха молчит — переписывать
// passwords.json на каждом прогоне значило бы точить флеш роутера.
//
// Гонок нет: ensure зовётся только из Resources, а тот — из воркерной горутины
// инстанса, которая одна.
type proxyUsersGate struct {
	sync func() error
	done bool
}

func (g *proxyUsersGate) ensure() error {
	if g.done {
		return nil
	}
	if err := g.sync(); err != nil {
		return err
	}
	g.done = true
	return nil
}

// proxyAdoptedRole — роль wdtt-сервера с ОБЯЗАТЕЛЬНЫМ усыновлением абонентов
// перед работой (рулинг Н3, fail-closed).
//
// Цена выбрана осознанно: материализация passwords.json без усыновления
// НЕОБРАТИМО отбирает доступ у абонентов, заведённых телеграм-ботом или
// admin-API форка (их нет в записи — значит не будет и в файле), а
// невзлетевший сервер обратим и виден. Поэтому пока усыновление не прошло,
// ведомость роли не объявляется вовсе: процесс не стартует, фаза — failed с
// причиной.
//
// Выключенный инстанс гейта не знает: там желаемое — снятие, и доводить его
// надо в любом состоянии абонентов.
type proxyAdoptedRole struct {
	inner proxyrt.Role
	gate  *proxyUsersGate
}

func (r *proxyAdoptedRole) Resources(intent proxyrt.Intent, cfg any, obs proxyrt.Observations) []proxyrt.Resource {
	if intent == proxyrt.IntentEnabled {
		if err := r.gate.ensure(); err != nil {
			return []proxyrt.Resource{proxyBlocked{id: proxyUsersResource,
				reason: fmt.Errorf("абоненты сервера не усыновлены: %w", err)}}
		}
	}
	return r.inner.Resources(intent, cfg, obs)
}

// ResetStartBackoff — обязателен и обязан ДОХОДИТЬ до внутренней роли
// (амендмент C): обёртка без него компилируется молча, а пауза перезапуска
// перестаёт сниматься правкой записи.
func (r *proxyAdoptedRole) ResetStartBackoff() {
	if b, ok := r.inner.(proxyrt.BackoffResetter); ok {
		b.ResetStartBackoff()
	}
}

var _ proxyrt.BackoffResetter = (*proxyAdoptedRole)(nil)

// ── сборка ───────────────────────────────────────────────────────

// wireProxyrt собирает узел прокси-рантайма и регистрирует его поверхность.
//
// Зовётся ПОСЛЕ конструкции router-сервиса: proxyIngressEnsurer держит на нём
// reconcile, а до присвоения a.routerSvc там nil.
func (a *app) wireProxyrt() {
	journal := logging.NewScopedLogger(a.loggingService, logging.GroupRouting, proxySubgroup)
	// Хранилище — то же, что читают потребители вне рантайма (setupCore):
	// писатель у proxy-instances.json один, и второй экземпляр развёл бы
	// сериализацию записи по разным замкам.
	store := a.proxyStore

	// (1) Аллокаторы: номера OpkgTun и локальные listen-порты клиентов.
	opkgMin, opkgMax, _ := roles.OpkgIndexRange(runtime.GOARCH)
	opkgAlloc := proxyrt.NewAllocator(proxyrt.IndexRange{Min: opkgMin, Max: opkgMax})
	portAlloc := proxyrt.NewAllocator(proxyrt.IndexRange{
		Min: roles.ListenPortMin, Max: roles.ListenPortMax})

	// (2) Занятость пула OpkgTun (состав и его цена — proxyOpkgOccupancy).
	ndmsIfaces := &routerOpkgTunIndexAdapter{store: a.ndmsQueries.Interfaces}
	occupancy := proxyOpkgOccupancy(ndmsIfaces, ndmsIfaces.NDMSOpkgTunPins,
		a.awgStore, a.settingsStore, store)
	allocIndex := proxyAllocIndex(a.shutdownCtx, opkgAlloc, opkgMin, occupancy, store)
	allocListen := proxyAllocListen(a.shutdownCtx, portAlloc, store, a.awgStore)

	// (3) Посев из конфигов старого мира.
	seed := func(ctx context.Context) (instancestore.SeedResult, error) {
		return instancestore.Seed(ctx, store, instancestore.SeedDeps{
			WdttPath:     filepath.Join(a.dataDir, "wdtt.json"),
			FreeturnPath: filepath.Join(a.dataDir, "freeturn.json"),
			RuntimeDir:   filepath.Join(a.dataDir, "run"),
			LivePermits:  livePermitsFor(a.ndmsQueries.Policies),
			AllocIndex:   allocIndex,
			GOARCH:       runtime.GOARCH,
		})
	}

	// (4) Уборщик NDMS-интерфейсов без живой декларации.
	cmds := proxyNDMSCommands{
		InterfaceCommands: a.ndmsCommands.Interfaces,
		routes:            a.ndmsCommands.Routes,
	}
	sweeper := proxyrt.NewSweeper(
		proxySweepScanner{ifaces: a.ndmsQueries.Interfaces},
		proxySweepRemover{cmds: cmds},
		opkgAlloc, instance.SweepLabels(), opkgTunIndex)

	// (5) Состояние реконсиляции — его читает ручка списка инстансов.
	states := proxyrt.NewStateStore(a.eventBus, nil)

	// (6) ОДНА ведомость INPUT-портов на процесс: второй экземпляр вернул бы
	// исходный дефект — два сервера закрывают порты друг друга. Список
	// серверных ключей нужен ДО конструктора: окно ожидания отчётов
	// отсчитывается от него.
	// ref заводится ДО ведомости: её гейт прохода окна спрашивает менеджера,
	// состоялся ли посев, а сам менеджер строится ниже. Окно ведомость заводит
	// не здесь, а armGrace ПОСЛЕ записи ref.mgr — иначе будильник читал бы
	// ссылку из своей горутины раньше, чем её проставят.
	ref := &proxyManagerRef{}
	book := newProxyFWBook(proxyServerKeys(store), func() bool {
		return ref.mgr != nil && ref.mgr.SeedInfo().Booted
	})

	links := newProxyLinkBook()
	installSvc := install.New(install.Deps{
		DataDir:    a.dataDir,
		Arch:       detectArch(),
		Downloader: proxyBinaryDownloader{svc: a.downloadSvc},
		Warn:       func(msg string) { journal.Warn("install", "proxy", msg) },
		Info:       func(msg string) { journal.Info("install", "proxy", msg) },
		// Гейт удаления бинарей: считаем по ДИСКУ, а не по памяти менеджера.
		// Боот прокси-рантайма идёт горутиной после старта HTTP, и до его
		// конца Records() пуст — гейт был бы открыт всё окно посева, а на
		// холодном старте роутера оно длится минутами. Со стора же читаются и
		// записи без воркера (отказ фабрики в Create).
		InstanceCount: func(name install.Subsystem) (int, error) {
			st, err := store.Load()
			if err != nil {
				return 0, err
			}
			n := 0
			for _, rec := range st.Records {
				if proxySubsystemOf(rec.Kind) == name {
					n++
				}
			}
			return n, nil
		},
	})

	records := proxyRecords{ref: ref}
	mutator := proxyMutator{ref: ref}
	users := wdttusers.New(wdttusers.Deps{
		Records:      records,
		Mutator:      mutator,
		SignalReload: proxySignalReload(links),
		Warn:         func(msg string) { journal.Warn("users", "proxy", msg) },
	})

	// (7) Фабрика инстансов и (8) сам менеджер.
	factory := a.proxyFactory(ref, journal, links, book, states, installSvc, users, store)

	mgr := manager.New(manager.Deps{
		Store:    store,
		Registry: a.exitRegistry,
		Sweeper:  sweeper,
		Factory:  factory,
		Journal:  journal,
		Seed:     seed,
		PostSeed: proxyPostSeed(a.exitMirror, proxyIPT{}, cmds, a.ndmsQueries.Interfaces,
			proxyKillBinaries(installSvc),
			func() error { return instancestore.ClearCleanupPending(store) }),
		AllocIndex:   allocIndex,
		AllocListen:  allocListen,
		ReleasePins:  proxyReleasePins(a.shutdownCtx, opkgAlloc, portAlloc, book, journal),
		WaitDisabled: proxyWaitDisabled(states),
	})
	ref.mgr = mgr
	a.proxyMgr = mgr
	book.armGrace() // всё, что читает гейт окна, уже записано

	logTail := proxyLogTail(mgr.Records)
	snapshots := wdttlink.Snapshots(links.snapshot)

	linkHandler := wdttlink.NewHandler(wdttlink.Deps{
		Records:   records,
		Mutator:   mutator,
		Snapshots: snapshots,
		Tunnels: proxyTunnelImporter{store: a.awgStore, svc: a.proxyTunnels(),
			traffic: a.trafficHistory, pub: a.eventBus},
		Cleaners: proxyLinkedCleaners(a.awgStore, a.proxyTunnels(), a.trafficHistory, a.eventBus),
		Builders: map[instancestore.Kind]wdttlink.LinkBuilder{
			instancestore.KindWdttServer: wdttlink.NewBuilder(wdttlink.BuilderDeps{
				Vetting:    wdttusers.Vetting{},
				Mutator:    mutator,
				ExternalIP: a.proxyExternalIP,
			}),
			instancestore.KindFreeTurnServer: ftlink.NewBuilder(ftlink.BuilderDeps{
				ExternalIP: a.proxyExternalIP,
			}),
		},
	})
	allowlist := ftlink.New(ftlink.Deps{Records: records, Mutator: mutator, DataDir: a.dataDir})
	subs := proxysub.New(proxysub.Deps{Records: records, Mutator: mutator,
		Fetch: wdttlink.DecodeLink})
	captchaSvc := captcha.New(captcha.Deps{
		Records:   records,
		Instances: proxyInstanceLister{ref: ref},
		Snapshots: snapshots,
		Log:       logTail,
	})
	instances := api.NewProxyInstancesHandler(api.ProxyInstancesDeps{
		Manager:          mgr,
		States:           states,
		Snapshot:         links.snapshot,
		Log:              logTail,
		BinaryInfo:       installSvc.Binary,
		OpkgTunSupported: opkgTunSupported,
	})

	a.srv.SetProxyRtSurface(server.ProxyRtSurface{
		Instances: proxyrtDispatch{
			instances: instances.Handle,
			users:     users.Serve,
			captcha:   captchaSvc.Serve,
			allowlist: allowlist.Serve,
			link:      linkHandler.Link,
			ensureWG:  linkHandler.EnsureWGTunnel,
			clear:     linkHandler.ClearLinkedTunnels,
			refresh:   subs.Serve,
		}.handler(),
		ListenMoves:        instances.AckListenMoves,
		WdttLinkDecode:     linkHandler.Decode,
		WdttLinkImport:     linkHandler.Import,
		FreeTurnLinkDecode: allowlist.Decode,
		CaptchaStatus:      captchaSvc.ServeStatus,
		InstallStatus:      installSvc.ServeStatus,
		Install:            installSvc.ServeInstall,
		Uninstall:          installSvc.ServeUninstall,
	})

	// Тумблер намерения инстанса (карточка зеркальной записи wdtt-raw) и
	// глушение/подъём на время бэкапа: маршруты строятся в srv.Start, то есть
	// после этой строки.
	a.srv.SetProxyRuntime(mgr)
	a.srv.SetProxyRuntimeNudge(func(reason string) {
		a.proxyRuntimeNudge(reason, proxyrt.EventWANUp)
	})

	// (9) Боот — горутиной ПОСЛЕ старта HTTP: на бооте роутера RCI ещё
	// недоступен, а блокировать здесь значит не поднять веб-морду вовсе.
	// Ретрай зовут фазы боота и хуки wan-up через proxyRuntimeNudge; Boot
	// идемпотентен — живые инстансы не пересоздаются — и сериализован сам с
	// собой (manager.bootMu).
	go func() {
		if err := mgr.Boot(a.shutdownCtx); err != nil {
			journal.Warn("boot", "proxy", "прокси-рантайм не поднялся: "+err.Error())
		}
	}()
}

// proxyRuntime — срез менеджера, нужный ретраю боота. Шов ради теста: иначе
// ретрай наблюдаем только настоящим менеджером с одиннадцатью зависимостями.
type proxyRuntime interface {
	SeedInfo() manager.SeedInfo
	Boot(ctx context.Context) error
	PostAll(k proxyrt.EventKind)
}

// proxyNudge — один шаг ретрая посева. Пока посев не состоялся, зовём Boot:
// на ХОЛОДНОМ старте роутера RCI ещё мёртв, посев падает fail-closed, и без
// повторной попытки инстансы не поднимаются вовсе, а ведомость INPUT-портов
// через две минуты сводит объединение к пустому и закрывает порты
// переживших процессов. После успешного посева повторять боот незачем —
// достаточно разбудить воркеров.
//
// Возвращает признак поднятого рантайма, чтобы вызывающий отличил успех
// повтора от «и так уже работало».
func proxyNudge(ctx context.Context, mgr proxyRuntime, kind proxyrt.EventKind) (bootedNow bool, err error) {
	if mgr.SeedInfo().Booted {
		mgr.PostAll(kind)
		return false, nil
	}
	if err := mgr.Boot(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// proxyRuntimeNudge — proxyNudge с журналом, точка вызова из фаз боота и
// WAN-хука.
func (a *app) proxyRuntimeNudge(reason string, kind proxyrt.EventKind) {
	if a.proxyMgr == nil {
		return
	}
	bootedNow, err := proxyNudge(a.shutdownCtx, a.proxyMgr, kind)
	if a.bootLog == nil {
		return
	}
	if err != nil {
		a.bootLog.Warn("proxy-boot", reason, "прокси-рантайм не поднялся: "+err.Error())
		return
	}
	if bootedNow {
		a.bootLog.Info("proxy-boot", reason, "прокси-рантайм поднят повторной попыткой")
	}
}

// proxyServerKeys — ключи ВСЕХ серверных записей на момент боота, включая
// выключенные: выключенный сервер тоже держит input_port, и его порты в окне
// ожидания надо щадить. Клиентских ключей здесь НЕТ — лишний ключ продержал
// бы окно все две минуты.
//
// Отказ чтения store не фатален: посев его повторит и отчитается сам, а пустой
// список лишь укорачивает щадящее окно.
func proxyServerKeys(store *instancestore.Store) []string {
	st, err := store.Load()
	if err != nil {
		return nil
	}
	var keys []string
	for _, rec := range st.Records {
		if rec.Kind == instancestore.KindWdttServer || rec.Kind == instancestore.KindFreeTurnServer {
			keys = append(keys, rec.Key())
		}
	}
	return keys
}

// proxyKillBinaries — пути бинарей обеих подсистем для добивания старого
// поколения: сверка процесса по имени бинаря.
func proxyKillBinaries(svc *install.Service) []string {
	var out []string
	for _, kind := range []instancestore.Kind{
		instancestore.KindWdttClient, instancestore.KindWdttServer,
		instancestore.KindFreeTurnClient, instancestore.KindFreeTurnServer,
	} {
		if path, _ := svc.Binary(kind); path != "" {
			out = append(out, path)
		}
	}
	return out
}

// proxyFactory — сборка инстанса под запись: роль из адаптеров, связь с
// процессом, инстанс движка.
//
// Менеджер приходит ССЫЛКОЙ (proxyManagerRef): фабрика нужна его конструктору,
// а её замыкание Post — самому менеджеру.
func (a *app) proxyFactory(ref *proxyManagerRef, journal *logging.ScopedLogger,
	links *proxyLinkBook, book *proxyFWBook, states *proxyrt.StateStore,
	installSvc *install.Service, users *wdttusers.Service, store *instancestore.Store,
) manager.Factory {
	gate := procres.NewGate()
	return func(rec instancestore.Record, live *manager.Live) (manager.RunningInstance, error) {
		key := rec.Key()
		impl, roleName, ok := proxyImplRole(rec.Kind)
		if !ok {
			return nil, fmt.Errorf("инстанс %s: неизвестная роль %s", key, rec.Kind)
		}
		sock, err := control.SocketPath(roles.RuntimeDir, impl, roleName, rec.ID)
		if err != nil {
			return nil, err
		}
		binary, _ := installSvc.Binary(rec.Kind)
		link := control.NewLink(control.LinkOpts{
			Path: sock, Impl: impl, Role: roleName, Instance: rec.ID, Binary: binary,
			// Владение связью — инстанс (её закрывает Stop); здесь только
			// будильник воркеру и пояснение к нему в журнал.
			Post:  func(k proxyrt.EventKind) bool { return ref.mgr.Post(key, k) },
			Log:   func(msg string) { journal.Info("link", key, msg) },
			Alive: childproc.MatchesBinary,
		})
		links.put(key, link)
		runner := procres.NewRunner(binary, strings.TrimSuffix(sock, ".sock")+".pid", nil)

		var role proxyrt.Role
		var cfg func() any
		switch rec.Kind {
		case instancestore.KindWdttClient:
			r, err := wdttclient.New(wdttclient.Deps{
				Instance: rec.ID, Binary: binary,
				PinnedSHA256: installSvc.PinnedSHA256(rec.Kind),
				Link:         link, Runner: runner, Gate: gate,
				Cmds:  proxyNDMSCommands{InterfaceCommands: a.ndmsCommands.Interfaces, routes: a.ndmsCommands.Routes},
				Query: proxyNDMSQuery{ifaces: a.ndmsQueries.Interfaces, rc: a.ndmsQueries.RunningConfig},
				// Policies/Permit — членство raw-клиента в политиках.
				Policies: a.ndmsQueries.Policies,
				Permit:   a.ndmsCommands.Policies,
				Hooks:    proxyRouteHooks{svc: a.clientRouteService},
				Registry: a.exitRegistry,
				Sync: newProxyEndpointSync(a.awgStore, a.proxyTunnels(),
					proxyLinkedField(rec.Kind), a.eventBus),
				Occ: newProxyOccupancy(store, a.awgStore, rec.Kind, rec.ID),
			})
			if err != nil {
				return nil, err
			}
			role = r
			cfg = func() any { c, _ := live.Config().WdttClientConfig(); return c }
		case instancestore.KindWdttServer:
			r, err := wdttserver.New(wdttserver.Deps{
				Instance: rec.ID, Binary: binary,
				PinnedSHA256: installSvc.PinnedSHA256(rec.Kind),
				Link:         link, Runner: runner, Gate: gate,
				Cmds:          proxyNDMSCommands{InterfaceCommands: a.ndmsCommands.Interfaces, routes: a.ndmsCommands.Routes},
				Query:         proxyNDMSQuery{ifaces: a.ndmsQueries.Interfaces, rc: a.ndmsQueries.RunningConfig},
				IPT:           proxyIPT{},
				FW:            book.forInstance(key),
				RunHook:       proxyRunHook,
				EnableForward: proxyEnableForward,
				IfaceExists:   proxyIfaceExists,
				KernelWAN:     proxyKernelWAN(a.ndmsQueries.Interfaces),
				PolicyMark:    proxyPolicyMark(ndmsquery.NewPolicyMarkStore(a.ndmsTransportClient, nil)),
				Access: proxyAccessApplier{svc: a.managedService,
					ifaces: a.ndmsCommands.Interfaces},
				Ingress: proxyIngressEnsurer{settings: a.settingsStore, router: a.routerSvc},
			})
			if err != nil {
				return nil, err
			}
			// Усыновление абонентов — гейт, а не побочный шаг сборки:
			// см. proxyAdoptedRole.
			role = &proxyAdoptedRole{inner: r, gate: &proxyUsersGate{
				sync: func() error { return users.SyncOnStart(a.shutdownCtx, key) },
			}}
			cfg = func() any { c, _ := live.Config().WdttServerConfig(); return c }
		case instancestore.KindFreeTurnClient:
			r, err := freeturn.NewClient(freeturn.ClientDeps{
				Instance: rec.ID, Binary: binary,
				PinnedSHA256: installSvc.PinnedSHA256(rec.Kind),
				Link:         link, Runner: runner, Gate: gate,
				Sync: newProxyEndpointSync(a.awgStore, a.proxyTunnels(),
					proxyLinkedField(rec.Kind), a.eventBus),
				Occ: newProxyOccupancy(store, a.awgStore, rec.Kind, rec.ID),
			})
			if err != nil {
				return nil, err
			}
			role = r
			cfg = func() any { c, _ := live.Config().FreeTurnClientConfig(); return c }
		case instancestore.KindFreeTurnServer:
			r, err := freeturn.NewServer(freeturn.ServerDeps{
				Instance: rec.ID, Binary: binary,
				PinnedSHA256: installSvc.PinnedSHA256(rec.Kind),
				Link:         link, Runner: runner, Gate: gate,
				FW: book.forInstance(key),
			})
			if err != nil {
				return nil, err
			}
			role = r
			cfg = func() any { c, _ := live.Config().FreeTurnServerConfig(); return c }
		default:
			return nil, fmt.Errorf("инстанс %s: неизвестная роль %s", key, rec.Kind)
		}

		// Возвращается сам instance.Instance, БЕЗ обёрток: сброс паузы
		// перезапуска обязан доходить до роли, а обёртка, потерявшая
		// ResetStartBackoff, собралась бы молча (RunningInstance ловит только
		// отсутствие метода, не её подмену заглушкой).
		return instance.New(instance.Config{
			ID: key, Role: role, Cfg: cfg, Intent: live.Intent,
			Link: link, States: states, Journal: journal,
		}), nil
	}
}

// proxyrtDispatch — разбор подпути /api/proxyrt/instances[/...]. Ручной: в
// дереве нет wildcard-паттернов, а ключ инстанса содержит двоеточие
// (wdtt-client:default), сегменту пути законное.
//
// Ручки продуктовых пакетов стоят ДО хендлера инстансов: тот терминальный
// владелец поддерева и отвечает 404 на неизвестном хвосте, то есть проглотил
// бы users, link, captcha и allowlist целиком.
type proxyrtDispatch struct {
	instances http.HandlerFunc
	users     func(w http.ResponseWriter, r *http.Request, key string, sub []string)
	captcha   func(w http.ResponseWriter, r *http.Request, key string, sub []string)
	allowlist func(w http.ResponseWriter, r *http.Request, key string, sub []string)
	link      func(w http.ResponseWriter, r *http.Request, key string)
	ensureWG  func(w http.ResponseWriter, r *http.Request, key string)
	clear     func(w http.ResponseWriter, r *http.Request, key string)
	refresh   func(w http.ResponseWriter, r *http.Request, key string)
}

func (d proxyrtDispatch) handler() http.HandlerFunc {
	const base = "/api/proxyrt/instances"
	return func(w http.ResponseWriter, r *http.Request) {
		tail := strings.Trim(strings.TrimPrefix(r.URL.Path, base), "/")
		key, rest, _ := strings.Cut(tail, "/")
		if key == "" || rest == "" {
			d.instances(w, r)
			return
		}
		section, sub, _ := strings.Cut(rest, "/")
		var parts []string
		if sub != "" {
			parts = strings.Split(sub, "/")
		}
		switch section {
		case "users":
			d.users(w, r, key, parts)
		case "captcha":
			d.captcha(w, r, key, parts)
		case "allowlist":
			d.allowlist(w, r, key, parts)
		case "link":
			d.link(w, r, key)
		case "ensure-wg-tunnel":
			d.ensureWG(w, r, key)
		case "linked-tunnels":
			if sub == "clear" {
				d.clear(w, r, key)
				return
			}
			d.instances(w, r)
		case "subscription":
			if sub == "refresh" {
				d.refresh(w, r, key)
				return
			}
			d.instances(w, r)
		default:
			// apply и неизвестный хвост — хозяину поддерева.
			d.instances(w, r)
		}
	}
}
