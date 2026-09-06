package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/sys/routerclock"
)

// Subsystem — подсистема прокси. У каждой свой пин, свои бинари и свой
// version-файл; общий у них только механизм установки.
type Subsystem string

const (
	SubsystemWdtt     Subsystem = "wdtt"
	SubsystemFreeTurn Subsystem = "freeturn"
)

// Downloader — узкий контракт загрузки (форма childproc.Downloader, по
// фактическим вызовам старых wdttDownloaderAdapter/freeturnDownloaderAdapter).
// Прод-обёртку над общим downloader.Service строит проводка.
type Downloader interface {
	// DownloadFile кладёт url в destPath (режим 0644, не атомарно — активацию
	// делает childproc.Install через chmod+rename). maxBytes — жёсткий потолок
	// передачи.
	DownloadFile(ctx context.Context, url, destPath string, maxBytes int64) error
}

// InstallStatus — install-блок ответа. Семь полей вербатим старого
// wdtt.Status: фронт живёт всеми — полоса состояния бинарей, гейт мастера на
// арке без сервера и отметка времени в журнале.
type InstallStatus struct {
	// ServerSupported — собирается ли сервер под эту архитектуру (или уже
	// лежит на диске). На mips/mipsel wdtt-сервера нет, UI прячет вкладку.
	ServerSupported bool `json:"serverSupported"`
	// InstallAvailable — для этой арки есть пин и подключён загрузчик:
	// установка в один клик возможна.
	InstallAvailable bool `json:"installAvailable"`
	// InstallVersion — версия, которую поставит установка.
	InstallVersion string `json:"installVersion,omitempty"`
	// InstalledVersion — версия, установленная сейчас.
	InstalledVersion string `json:"installedVersion,omitempty"`
	// UpdateAvailable — бинарей нет либо они не те, что в пине.
	UpdateAvailable bool `json:"updateAvailable"`
	// Installing — установка идёт прямо сейчас (кнопка блокируется).
	Installing bool `json:"installing"`
	// RouterClock — часы роутера для сверки с метками в журнале форка.
	RouterClock string `json:"routerClock,omitempty"`
	// Instances — сколько инстансов подсистемы существует сейчас, включая
	// выключенные. Фронт гасит по нему «Удалить» и объясняет почему, не
	// дожидаясь 409 от ручки.
	Instances int `json:"instances"`
	// BinariesPresent — нужные бинари лежат на диске. Признак принадлежит
	// ПОДСИСТЕМЕ, а не инстансу: полоса установки судит по нему, и без него она
	// брала `binaryPresent` первого клиентского инстанса — у подсистемы без
	// клиентов продукт объявлялся неустановленным, а «Установить» работала
	// вхолостую (бинари-то на месте).
	BinariesPresent bool `json:"binariesPresent"`
}

// defaultBinDir — прод-каталог бинарей: /opt/bin/wdtt-client,
// /opt/bin/wdtt-server, /opt/bin/freeturn-client, /opt/bin/freeturn-server.
const defaultBinDir = "/opt/bin"

// Deps — зависимости пакета.
type Deps struct {
	// DataDir — каталог данных awg-manager: рядом с конфигами лежат
	// wdtt-version.json и freeturn-version.json. Пусто = версии не пишутся и
	// не читаются (в текущий каталог демона писать нельзя).
	DataDir string
	// Arch — ключ архитектуры сборки (detectArch()). Нет секции в пине =
	// установка недоступна, UI оставляет подсказку про ручную установку.
	Arch string
	// Downloader — загрузчик; nil = установка недоступна.
	Downloader Downloader
	// Warn/Info — app-журнал (необязательны). Сюда уходят исходы, которые не
	// роняют операцию, но терять их молча нельзя.
	Warn func(msg string)
	Info func(msg string)
	// Clock — часы роутера; nil = routerclock.Get().
	Clock func() (now time.Time, zone string)
	// InstanceCount — сколько инстансов подсистемы записано на ДИСКЕ, включая
	// выключенные. Гейт удаления бинарей; nil = удаление недоступно.
	//
	// Именно с диска, а не из памяти менеджера: боот прокси-рантайма идёт
	// горутиной уже ПОСЛЕ старта HTTP (wiring_proxyrt.go), и на холодном
	// старте роутера, пока RCI мёртв и посев ретраится, инстансы в памяти
	// ещё не собраны. Счётчик по памяти отдавал бы в этом окне ноль — и
	// удаление сносило бы бинари из-под живых записей.
	//
	// Отказ чтения — не ноль: без ответа гейт закрывается.
	InstanceCount func(Subsystem) (int, error)
	// Installed — успешная РУЧНАЯ установка (ServeInstall). Проводка нуджит
	// бут прокси-рантайма, чтобы ожидание бинарей (F98) снялось без таймера.
	// Из Install не зовётся: Install работает внутри Boot под bootMu.
	Installed func(Subsystem)
}

// subsys — состояние одной подсистемы: где её бинари, какой у неё пин и не
// идёт ли прямо сейчас установка.
type subsys struct {
	name        Subsystem
	clientBin   string
	serverBin   string
	versionPath string
	specs       *ArchSpecs

	// Кеш сверки бинарей с пином (см. binariesMatchSpecs).
	matchMu  sync.Mutex
	matchKey string
	matchVal bool

	installMu  sync.Mutex
	installing bool
}

// Service — установка бинарей прокси и статус установки.
type Service struct {
	deps Deps
	subs map[Subsystem]*subsys
}

func New(d Deps) *Service {
	return &Service{
		deps: d,
		subs: map[Subsystem]*subsys{
			SubsystemWdtt: {
				name:        SubsystemWdtt,
				clientBin:   filepath.Join(defaultBinDir, "wdtt-client"),
				serverBin:   filepath.Join(defaultBinDir, "wdtt-server"),
				versionPath: versionPath(d.DataDir, "wdtt-version.json"),
				specs:       archSpecs(WdttEmbeddedBinaries, d.Arch),
			},
			SubsystemFreeTurn: {
				name:        SubsystemFreeTurn,
				clientBin:   filepath.Join(defaultBinDir, "freeturn-client"),
				serverBin:   filepath.Join(defaultBinDir, "freeturn-server"),
				versionPath: versionPath(d.DataDir, "freeturn-version.json"),
				specs:       archSpecs(FreeTurnEmbeddedBinaries, d.Arch),
			},
		},
	}
}

// versionPath — путь version-файла. Без каталога данных пути нет: относительный
// путь уехал бы в текущий каталог демона.
func versionPath(dataDir, name string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(dataDir, name)
}

func archSpecs(table map[string]ArchSpecs, arch string) *ArchSpecs {
	specs, ok := table[arch]
	if !ok {
		return nil
	}
	return &specs
}

// pick возвращает подсистему по имени. Неизвестное имя — отказ, а не молчаливый
// нулевой статус: фронт по нему нарисовал бы «ничего не установлено».
func (s *Service) pick(name string) (*subsys, error) {
	sub, ok := s.subs[Subsystem(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("неизвестная подсистема %q: ожидается wdtt или freeturn", name)
	}
	return sub, nil
}

// Binary — путь бинаря роли и его наличие. Потребители: вид процесса в ручке
// инстансов, фабрика процессов и уборка старого поколения.
func (s *Service) Binary(kind instancestore.Kind) (string, bool) {
	var path string
	switch kind {
	case instancestore.KindWdttClient:
		path = s.subs[SubsystemWdtt].clientBin
	case instancestore.KindWdttServer:
		path = s.subs[SubsystemWdtt].serverBin
	case instancestore.KindFreeTurnClient:
		path = s.subs[SubsystemFreeTurn].clientBin
	case instancestore.KindFreeTurnServer:
		path = s.subs[SubsystemFreeTurn].serverBin
	default:
		return "", false
	}
	return path, binaryPresent(path)
}

// PinnedSHA256 — вшитая в эту сборку сумма бинаря роли. Пусто означает «пина
// нет» (арка без закреплённой сборки, роль без сервера) — ресурс process
// читает это как «сверять нечем», а не как «не совпало».
//
// Экспорт нужен фабрике инстансов: роли требуют пин полем Deps.PinnedSHA256, а
// читать таблицы pins.go из проводки нельзя — знание архитектур продублировалось
// бы вторым местом.
func (s *Service) PinnedSHA256(kind instancestore.Kind) string {
	var sub *subsys
	server := false
	switch kind {
	case instancestore.KindWdttClient:
		sub = s.subs[SubsystemWdtt]
	case instancestore.KindWdttServer:
		sub, server = s.subs[SubsystemWdtt], true
	case instancestore.KindFreeTurnClient:
		sub = s.subs[SubsystemFreeTurn]
	case instancestore.KindFreeTurnServer:
		sub, server = s.subs[SubsystemFreeTurn], true
	default:
		return ""
	}
	if sub.specs == nil {
		return ""
	}
	if server {
		return sub.specs.Server.SHA256
	}
	return sub.specs.Client.SHA256
}

// SubsystemOf — подсистема, чьи бинари держит роль. Пусто для неизвестного
// kind: вызывающие обязаны считать это отказом классификации, а не «ничьё».
func SubsystemOf(kind instancestore.Kind) Subsystem {
	switch kind {
	case instancestore.KindWdttClient, instancestore.KindWdttServer:
		return SubsystemWdtt
	case instancestore.KindFreeTurnClient, instancestore.KindFreeTurnServer:
		return SubsystemFreeTurn
	}
	return ""
}

// Stale — подсистемы, которым на буте нужна загрузка (F98): есть включённая
// запись, для архитектуры есть пин и бинари на диске не совпали с SHA пина.
// Арка без пина не stale — скачать нечего, гейт назовёт причину сам.
// Выключенные записи бут не держат: пока никто не хочет работать, ждать
// нечего. Порядок детерминирован.
func (s *Service) Stale(records []instancestore.Record) []Subsystem {
	want := map[Subsystem]bool{}
	for _, rec := range records {
		if rec.Enabled {
			want[SubsystemOf(rec.Kind)] = true
		}
	}
	var out []Subsystem
	for _, name := range []Subsystem{SubsystemWdtt, SubsystemFreeTurn} {
		sub := s.subs[name]
		if want[name] && sub.specs != nil && !sub.binariesMatchSpecs() {
			out = append(out, name)
		}
	}
	return out
}

// Status — install-статус подсистемы.
func (s *Service) Status(subsystem string) (InstallStatus, error) {
	sub, err := s.pick(subsystem)
	if err != nil {
		return InstallStatus{}, err
	}
	version, available := s.installInfo(sub)
	installedVersion, updateAvailable := sub.statusFields(version)
	now, zone := s.clock()
	return InstallStatus{
		ServerSupported:  sub.specs.serverSupported() || binaryPresent(sub.serverBin),
		InstallAvailable: available,
		InstallVersion:   version,
		InstalledVersion: installedVersion,
		UpdateAvailable:  updateAvailable,
		Installing:       sub.isInstalling(),
		RouterClock:      now.Format("2006-01-02 15:04:05") + " " + zone,
		Instances:        s.instanceCount(sub.name),
		BinariesPresent:  sub.binariesInstalled(),
	}, nil
}

// instanceCount для статуса: отказ чтения показывается нулём — гейт всё равно
// на ручке, а статус опрашивается постоянно и ронять его из-за одного чтения
// незачем.
func (s *Service) instanceCount(name Subsystem) int {
	if s.deps.InstanceCount == nil {
		return 0
	}
	n, err := s.deps.InstanceCount(name)
	if err != nil {
		return 0
	}
	return n
}

// Install скачивает, сверяет и активирует бинари подсистемы. Проверка — по
// вшитому в сборку SHA256; при любой беде для конкретного бинаря ничего не
// активируется (tmp удаляется), а уже активированный ранее в этом же вызове
// остаётся: бинари независимы. Сериализовано: второй параллельный вызов
// получает отказ.
func (s *Service) Install(ctx context.Context, subsystem string) error {
	sub, err := s.pick(subsystem)
	if err != nil {
		return err
	}
	if sub.specs == nil || s.deps.Downloader == nil {
		return fmt.Errorf("установка недоступна: для этой архитектуры нет закреплённой сборки %s", sub.name)
	}
	sub.installMu.Lock()
	if sub.installing {
		sub.installMu.Unlock()
		return errors.New("установка уже выполняется")
	}
	sub.installing = true
	sub.installMu.Unlock()
	defer func() {
		sub.installMu.Lock()
		sub.installing = false
		sub.installMu.Unlock()
	}()

	specs := *sub.specs
	if err := s.installOne(ctx, sub.clientBin, specs.Client); err != nil {
		return fmt.Errorf("клиент: %w", err)
	}
	if specs.serverSupported() {
		if err := s.installOne(ctx, sub.serverBin, specs.Server); err != nil {
			return fmt.Errorf("сервер: %w", err)
		}
	}
	label := sub.versionLabel()
	if err := sub.writeInstalledVersion(label); err != nil {
		s.warn(fmt.Sprintf("%s: version-file: %s", sub.name, err.Error()))
	}
	s.info(fmt.Sprintf("%s v%s установлен: %s, %s", sub.name, label, sub.clientBin, sub.serverBin))
	return nil
}

// ErrInstancesExist — удаление бинарей отклонено: подсистемой ещё пользуются.
var ErrInstancesExist = errors.New("сначала удалите инстансы подсистемы")

// Uninstall удаляет бинари подсистемы (обе половины разом) и её version-файл.
// Гранулярность подсистемой, а не половиной: версия и version-файл у них
// общие, и раздельный снос сделал бы статус неоднозначным.
//
// Гейт: ни одного инстанса подсистемы, включая ВЫКЛЮЧЕННЫЕ — снятый из-под
// живой записи бинарь оставил бы инстанс, который нечем запустить. Fail-closed:
// без подключённого счётчика удаление не пускается вовсе.
//
// Каталоги данных не трогает: их уносит удаление самого инстанса
// (manager.Delete), у каждой сущности свой мусор.
//
// Идемпотентно: отсутствующий файл не ошибка.
func (s *Service) Uninstall(subsystem string) error {
	sub, err := s.pick(subsystem)
	if err != nil {
		return err
	}
	if s.deps.InstanceCount == nil {
		return fmt.Errorf("удаление недоступно: счётчик инстансов не подключён")
	}
	// Флаг занятости взводится ДО проверок и держится до конца: иначе между
	// проверкой и os.Remove успевал стартовать Install, и снос уносил только
	// что активированный бинарь или version-файл (образец — singbox.Operator,
	// он держит свой installBusy на весь снос).
	sub.installMu.Lock()
	if sub.installing {
		sub.installMu.Unlock()
		return errors.New("установка уже выполняется")
	}
	sub.installing = true
	sub.installMu.Unlock()
	defer func() {
		sub.installMu.Lock()
		sub.installing = false
		sub.installMu.Unlock()
	}()

	n, err := s.deps.InstanceCount(sub.name)
	if err != nil {
		return fmt.Errorf("не прочитать записи инстансов: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("%w: их %d", ErrInstancesExist, n)
	}

	var errs []error
	for _, path := range []string{sub.clientBin, sub.serverBin, sub.versionPath} {
		if path == "" {
			continue
		}
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			errs = append(errs, rmErr)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	// Кеш сверки с пином ключуется по mtime+size бинарей и инвалидируется сам.
	s.info(fmt.Sprintf("%s удалён: %s, %s", sub.name, sub.clientBin, sub.serverBin))
	return nil
}

func (s *Service) installOne(ctx context.Context, binPath string, spec BinarySpec) error {
	err := childproc.Install(ctx, s.deps.Downloader, binPath, spec.URL, spec.SHA256, spec.Size)
	var ce *childproc.ChecksumError
	if errors.As(err, &ce) {
		s.warn(fmt.Sprintf("%s: sha256 mismatch: got %s, want %s", spec.URL, ce.Got, ce.Want))
	}
	return err
}

// installInfo — версия, которую поставит установка, и доступна ли она.
// Установка всегда ставит закреплённую в сборку версию; отдельного удалённого
// канала обновлений нет — новый бинарь приходит с новой сборкой awg-manager.
func (s *Service) installInfo(sub *subsys) (version string, available bool) {
	if sub.specs == nil || s.deps.Downloader == nil {
		return "", false
	}
	return sub.versionLabel(), true
}

func (s *Service) clock() (time.Time, string) {
	if s.deps.Clock != nil {
		return s.deps.Clock()
	}
	c := routerclock.Get()
	return c.Now, c.ZoneName
}

func (s *Service) warn(msg string) {
	if s.deps.Warn != nil {
		s.deps.Warn(msg)
	}
}

func (s *Service) info(msg string) {
	if s.deps.Info != nil {
		s.deps.Info(msg)
	}
}

func (sub *subsys) isInstalling() bool {
	sub.installMu.Lock()
	defer sub.installMu.Unlock()
	return sub.installing
}

// versionLabel — метка версии пина. У wdtt клиент и сервер выпускаются
// раздельно, поэтому метка составная; у freeturn один релиз на оба бинаря.
func (sub *subsys) versionLabel() string {
	if sub.specs == nil {
		return ""
	}
	if sub.name == SubsystemWdtt && sub.specs.serverSupported() {
		return sub.specs.Client.Version + "+server-" + sub.specs.Server.Version
	}
	return sub.specs.Client.Version
}

// statusFields — установленная версия и признак «есть что ставить». Правила у
// подсистем РАЗНЫЕ и переносятся как есть: у wdtt сервер бывает не собран под
// арку, у freeturn версии сравниваются по номеру пересборки.
func (sub *subsys) statusFields(installVersion string) (installedVersion string, updateAvailable bool) {
	if sub.name == SubsystemFreeTurn {
		return sub.freeturnStatusFields(installVersion)
	}
	return sub.wdttStatusFields(installVersion)
}

func (sub *subsys) wdttStatusFields(installVersion string) (installedVersion string, updateAvailable bool) {
	if installVersion == "" {
		return sub.readInstalledVersion(), false
	}
	installedVersion = sub.readInstalledVersion()
	if !binaryPresent(sub.clientBin) {
		return installedVersion, true
	}
	if sub.specs.serverSupported() && !binaryPresent(sub.serverBin) {
		return installedVersion, true
	}
	// Бинарь на диске не тот, что в пине (протухший бандл, ручная подмена):
	// версия из wdtt-version.json ему не принадлежит, поэтому не выдаём её за
	// установленную и зовём поставить пин.
	if !sub.binariesMatchSpecs() {
		return "", true
	}
	if installedVersion == "" {
		return installedVersion, true
	}
	if installedVersion != installVersion {
		return installedVersion, true
	}
	return installedVersion, false
}

func (sub *subsys) freeturnStatusFields(installVersion string) (installedVersion string, updateAvailable bool) {
	installedVersion = sub.effectiveInstalledVersion()
	if !sub.binariesOperational() {
		if installVersion == "" {
			return installedVersion, false
		}
		return installedVersion, true
	}
	if installVersion == "" {
		return installedVersion, false
	}
	if installedVersion == "" {
		return installVersion, false
	}
	if compareFreeturnVersion(installedVersion, installVersion) < 0 {
		return installedVersion, true
	}
	return installedVersion, false
}

// binariesInstalled — нужные бинари подсистемы лежат на диске. Серверный
// спрашивается, только когда сервер под эту арку вообще существует: на арке
// без него ждать файл значило бы вечно звать «Установить».
func (sub *subsys) binariesInstalled() bool {
	if !binaryPresent(sub.clientBin) {
		return false
	}
	return !sub.specs.serverSupported() || binaryPresent(sub.serverBin)
}

func (sub *subsys) binariesOperational() bool {
	ensureExecutable(sub.clientBin)
	ensureExecutable(sub.serverBin)
	if binaryPresent(sub.clientBin) && binaryPresent(sub.serverBin) {
		return true
	}
	return sub.binariesMatchSpecs()
}

// effectiveInstalledVersion предпочитает пин сборки, когда бинари на диске
// совпали с его SHA256: version-файл мог протухнуть после обновления IPK.
func (sub *subsys) effectiveInstalledVersion() string {
	if sub.binariesMatchSpecs() {
		return sub.specs.Client.Version
	}
	return sub.readInstalledVersion()
}
