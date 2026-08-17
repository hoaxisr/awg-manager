//go:build linux

package awgmproto

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Сроки записи.
//
// farewellTimeout — секунда, и он общий у двух прощальных сообщений, evicted и
// exit. Обоснование одно на оба (спека §3 про evicted): переполненный буфер
// означает, что менеджер не читает — завис или мёртв, — и ждать его нельзя. У
// exit к этому добавляется своя цена: он стоит на пути выключения процесса, и
// обычный writeTimeout задержал бы выключение на пять секунд ровно тогда, когда
// сообщение всё равно бесполезно. При живом менеджере крошечный кадр уходит в
// пустой буфер мгновенно, так что короткий срок не стоит ничего.
//
// writeTimeout — всё остальное: ответы на команды и обычные push, где адресат
// по определению жив (он же и спросил).
const (
	farewellTimeout  = 1 * time.Second
	writeTimeout     = 5 * time.Second
	probeDialTimeout = 2 * time.Second
)

// Паузы между повторами accept, отказавшего по исчерпании ресурса. Первая
// короткая: дескриптор часто освобождается тут же, и лишняя секунда простоя
// стоила бы менеджеру целого цикла опроса. Удвоение до потолка нужно на случай,
// когда ресурс не вернётся никогда, — иначе цикл сожжёт процессор впустую.
const (
	acceptRetryMin = 5 * time.Millisecond
	acceptRetryMax = 1 * time.Second
)

// Error — отказ с кодом протокола.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Errf собирает отказ с кодом.
func Errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// ErrNotSupported — команда известна, но неприменима к роли.
var ErrNotSupported = &Error{Code: CodeNotSupported, Msg: "команда неприменима к этой роли"}

// Handler — то, что обвязка форка обязана уметь. Реализация роли, у которой
// нет TUN, возвращает из обеих tun-команд ErrNotSupported.
//
// РЕАЛИЗАЦИЯ ОБЯЗАНА БЫТЬ ПОТОКОБЕЗОПАСНОЙ. Библиотека сериализует между собой
// только команды (см. execMu), и этого недостаточно: State() зовётся ещё и на
// приёме соединения — из цикла accept, параллельно уже исполняющейся команде
// другого соединения. Плюс сама обвязка меняет своё состояние из рабочих
// горутин процесса, а Server.Push зовёт из них же. Обвязка без собственного
// мьютекса ловит гонку детектором.
type Handler interface {
	// State — снимок без побочных эффектов. Может быть вызван в любой момент,
	// в том числе одновременно с исполнением команды.
	State() State
	// AttachTun принимает УЖЕ проверенный дескриптор нужного интерфейса.
	// Проверка живёт в библиотеке, а не в обвязках, чтобы четыре бинаря
	// проверяли одинаково. При ошибке дескриптор закрывает вызывающий.
	AttachTun(iface string, f *os.File) error
	DetachTun() error
}

// ServerConfig — что нужно слушателю.
type ServerConfig struct {
	Path     string
	Impl     string
	Role     string
	Instance string
	Handler  Handler
	// OnError — журнал обвязки для того, что не доехало до менеджера. nil — молча.
	OnError func(error)
}

// Server — управляющий сокет процесса.
//
// Слушает процесс, подключается менеджер: так закрытие соединения даёт
// менеджеру сигнал смерти для усыновлённого процесса, где wait(2) недоступен.
type Server struct {
	cfg ServerConfig
	ln  *net.UnixListener

	// acceptHook подменяет accept в тестах. Ставится ДО Serve и больше не
	// трогается: связь горутин — оператор go, отдельной синхронизации нет.
	acceptHook func() (*net.UnixConn, error)

	// execMu сериализует исполнение КОМАНД поверх ВСЕХ соединений: новый
	// владелец не начнёт свою первую команду, пока не завершилась чужая
	// незаконченная. Аборта на середине нет намеренно — решение всё равно
	// принимается по последующему state.
	//
	// Чего он НЕ сериализует, и это часть контракта Handler: State() для hello
	// зовётся из цикла accept мимо execMu, параллельно исполняющейся команде.
	// Собственная синхронизация обвязки обязательна.
	execMu sync.Mutex

	mu     sync.Mutex
	cur    *conn // текущий владелец
	closed bool
	wg     sync.WaitGroup
}

// Listen занимает управляющий сокет.
//
// Перед bind — проба подключения к существующему файлу. Безусловный unlink
// запрещён: он разводит два процесса на один инстанс.
func Listen(cfg ServerConfig) (*Server, error) {
	if err := reclaimPath(cfg.Path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", cfg.Path)
	if err != nil {
		// В том числе EADDRINUSE при гонке двух стартующих процессов: выйти,
		// не ретраить.
		return nil, fmt.Errorf("слушать %s: %w", cfg.Path, err)
	}
	// Права ставятся ПОСЛЕ bind: между ними файл живёт с правами по umask —
	// короткое окно, в которое сокет теоретически доступен чужому процессу.
	// Закрывает его каталог: /tmp/awgm создаёт менеджер с правами 0700 (план 3),
	// и внутрь чужой процесс не попадает. Сузить окно на стороне процесса
	// нечем: umask — глобальное состояние процесса, менять его под чужими
	// горутинами хуже, чем оставить окно.
	if err := os.Chmod(cfg.Path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return &Server{cfg: cfg, ln: ln.(*net.UnixListener)}, nil
}

// reclaimPath решает судьбу файла, который уже лежит по пути сокета.
func reclaimPath(path string) error {
	c, err := net.DialTimeout("unix", path, probeDialTimeout)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("на инстансе уже работает другой процесс: %s отвечает", path)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, syscall.ECONNREFUSED):
		// Отказ в соединении даёт не только протухший сокет: connect(2)
		// отвечает им и на обычный файл, и на каталог по этому пути. Снимаем
		// только сокет, всё остальное — fail-closed: удалять чужое мы не
		// вправе, а единственной защитой были бы права каталога, то есть
		// внешнее обстоятельство. Lstat, а не Stat: по символической ссылке
		// снос уехал бы за пределы каталога.
		fi, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("по пути %s лежит не сокет (%s)", path, fi.Mode().Type())
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	default:
		// В том числе слишком длинный путь (EINVAL) и отказ доступа:
		// fail-closed, потому что связь с менеджером иначе не состоится.
		return fmt.Errorf("проба %s: %w", path, err)
	}
}

// Serve крутит приём соединений до Close.
//
// Отказ accept по исчерпании ресурса (дескрипторы процесса — EMFILE, дескрипторы
// системы — ENFILE, память под сокет) цикл НЕ убивает: он ретраит с растущей
// паузой и жалуется через OnError. Молчаливый выход из цикла давал бы худший из
// возможных отказов — процесс, который ВЫГЛЯДИТ живым: слушающий сокет остаётся
// в ядре, проба нового процесса дозванивается через backlog и решает, что на
// инстансе уже кто-то работает, а принять соединение некому. Менеджер при этом
// видит «сокет есть, hello нет» и ретраит по кругу.
//
// Настоящие поломки слушателя (в том числе его закрытие в Close) остаются
// концом цикла, как и раньше.
func (s *Server) Serve() {
	var delay time.Duration
	for {
		uc, err := s.accept()
		if err == nil {
			delay = 0
			s.adopt(uc)
			continue
		}
		if !isAcceptRetryable(err) {
			return
		}
		delay = nextAcceptDelay(delay)
		s.report(fmt.Errorf("приём соединения отложен на %v: %w", delay, err))
		time.Sleep(delay)
	}
}

// nextAcceptDelay — шаг паузы между повторами accept. Вынесен из Serve, чтобы
// рост проверялся литеральными значениями, а не измерением сна: без стража
// «всегда acceptRetryMin» выглядит рабочим кодом, а на невозвращаемом ресурсе
// жжёт процессор — ровно то, ради чего рост и заведён.
func nextAcceptDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return acceptRetryMin
	}
	if d *= 2; d > acceptRetryMax {
		return acceptRetryMax
	}
	return d
}

// accept — шов: тестам нужен отказ accept, который на живом сокете
// воспроизводится только просадкой RLIMIT_NOFILE на весь процесс теста.
func (s *Server) accept() (*net.UnixConn, error) {
	if s.acceptHook != nil {
		return s.acceptHook()
	}
	return s.ln.AcceptUnix()
}

// isAcceptRetryable отделяет исчерпание ресурса от поломки слушателя.
//
// Явный список errno, а не net.Error.Temporary(): Temporary объявлен устаревшим
// и на разных ошибках врёт в обе стороны. EINTR, EAGAIN и ECONNABORTED сюда
// приходить не должны — их рантайм Go разбирает внутри accept, — но названы
// намеренно: если однажды придут, это тоже «повторить», а не «умереть».
//
// Всё остальное фатально, и это важно ровно так же: net.ErrClosed после Close
// не errno, поэтому Serve на нём выходит, а не крутится вхолостую.
func isAcceptRetryable(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case unix.EMFILE, unix.ENFILE, unix.ENOBUFS, unix.ENOMEM,
		unix.ECONNABORTED, unix.EINTR, unix.EAGAIN:
		return true
	}
	return false
}

// adopt делает нового владельца.
//
// Точка вытеснения — момент accept: старое соединение помечается вытесненным
// атомарно, ДО отправки hello новому. Новое обслуживается, не дожидаясь ни
// отправки evicted, ни закрытия старого.
//
// hello — первый кадр соединения ПО КОНСТРУКЦИИ (§5.4), а не по удаче: пока он
// не записан, соединение не лежит в s.cur, и Push его не находит. Прежний
// порядок (публикация владельца в одной горутине, отправка hello — в другой)
// давал менеджеру первым кадром обычный push подавляющим большинством
// прогонов, а по спеке такое соединение менеджер обязан отвергнуть без ретрая.
//
// Цена решения названа прямо: пока hello собирается и пишется, владельца нет —
// Push в это окно теряется. Это законно (push — будильник, менеджер всё равно
// спрашивает state сразу после hello) и предпочтительнее, чем кадр не в том
// порядке. Второй расход — цикл accept на это время занят: Handler.State() и
// запись hello происходят в нём. Звать State() под s.mu было бы хуже:
// небыстрый снимок заблокировал бы Push и Close всему серверу.
func (s *Server) adopt(uc *net.UnixConn) {
	nc := &conn{uc: uc, fc: NewFrameConn(uc)}

	// State() — вне всех замков сервера.
	st := s.cfg.Handler.State()
	hello := Event{
		V: Version, Event: EventHello, Impl: s.cfg.Impl, Role: s.cfg.Role, Instance: s.cfg.Instance,
		PID: st.PID, ConfigHash: st.ConfigHash, BinarySHA256: st.BinarySHA256,
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = uc.Close()
		return
	}
	old := s.cur
	// Владельца снимаем сразу: до публикации нового Push адресата не находит.
	s.cur = nil
	s.mu.Unlock()

	if old != nil {
		old.evict()
	}

	if err := nc.send(hello, writeTimeout); err != nil {
		s.report(err)
		_ = uc.Close()
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = uc.Close()
		return
	}
	s.cur = nc
	// Add — под тем же замком, что и проверка closed: иначе Close успевал бы
	// уйти в Wait между проверкой и Add, а это паника «WaitGroup misuse».
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		s.serveConn(nc)
	}()
}

// serveConn читает команды соединения, которому hello уже отправлен.
func (s *Server) serveConn(c *conn) {
	defer func() {
		_ = c.uc.Close()
		c.fc.CloseUnclaimedFDs()
		s.mu.Lock()
		if s.cur == c {
			s.cur = nil
		}
		s.mu.Unlock()
	}()

	for {
		line, fd, err := c.fc.ReadFrame()
		if err != nil {
			closeFD(fd)
			return
		}
		s.dispatch(c, line, fd)
	}
}

// dispatch — граница «выполнение началось». Кадр прочитан целиком, но если
// соединение уже вытеснено, команда НЕ исполняется и ответ не пишется.
func (s *Server) dispatch(c *conn, line []byte, fd int) {
	kind, msg, err := DecodeLine(line)
	if err != nil {
		closeFD(fd)
		s.report(err)
		return
	}
	req, ok := msg.(Request)
	if kind != KindRequest || !ok {
		closeFD(fd)
		s.report(fmt.Errorf("на управляющем сокете ожидается запрос, пришло другое сообщение"))
		return
	}

	s.execMu.Lock()
	defer s.execMu.Unlock()
	if c.evicted.Load() {
		closeFD(fd)
		return
	}
	resp := s.exec(req, fd)
	// Исполнение доведено до конца, эффект остаётся; ответ вытесненному
	// соединению не пишется.
	if c.evicted.Load() {
		return
	}
	if err := c.send(resp, writeTimeout); err != nil {
		s.report(err)
	}
}

func (s *Server) exec(req Request, fd int) Response {
	switch req.Cmd {
	case CmdState:
		// Дескриптор без attach-tun — закрыть и продолжить.
		closeFD(fd)
		st := s.cfg.Handler.State()
		return Response{V: Version, ID: req.ID, OK: true, State: &st}

	case CmdAttachTun:
		return s.execAttach(req, fd)

	case CmdDetachTun:
		closeFD(fd)
		if err := s.cfg.Handler.DetachTun(); err != nil {
			return errResponse(req.ID, err)
		}
		// Идемпотентна: отсутствие дескриптора — не ошибка.
		return Response{V: Version, ID: req.ID, OK: true}

	default:
		closeFD(fd)
		return Response{V: Version, ID: req.ID, OK: false, Code: CodeUnknownCommand,
			Error: fmt.Sprintf("команда %q не поддерживается версией %d", req.Cmd, Version)}
	}
}

func (s *Server) execAttach(req Request, fd int) Response {
	if fd < 0 {
		return Response{V: Version, ID: req.ID, OK: false, Code: CodeBadRequest,
			Error: "attach-tun без дескриптора"}
	}
	if req.Iface == "" {
		closeFD(fd)
		return Response{V: Version, ID: req.ID, OK: false, Code: CodeBadRequest,
			Error: "attach-tun без имени интерфейса"}
	}
	// Проверка до передачи в обвязку: ошибка менеджера не должна доехать до
	// трафика. Менеджер трактует такой отказ как Failed ресурса tun_handoff и
	// вслепую не ретраит.
	if err := VerifyTunFD(fd, req.Iface); err != nil {
		closeFD(fd)
		return Response{V: Version, ID: req.ID, OK: false, Code: CodeBadRequest, Error: err.Error()}
	}
	f := os.NewFile(uintptr(fd), req.Iface)
	if err := s.cfg.Handler.AttachTun(req.Iface, f); err != nil {
		// При любом ok:false дескриптор закрывается. В том числе busy: процесс
		// не подменяет уже прикреплённый дескриптор молча.
		_ = f.Close()
		return errResponse(req.ID, err)
	}
	return Response{V: Version, ID: req.ID, OK: true}
}

func errResponse(id uint64, err error) Response {
	code := CodeInternal
	var pe *Error
	if errors.As(err, &pe) {
		code = pe.Code
	}
	return Response{V: Version, ID: id, OK: false, Code: code, Error: err.Error()}
}

// Push шлёт событие текущему владельцу. Best effort: владельца нет или запись
// не удалась — событие теряется. Для менеджера push — будильник, решение он
// принимает по последующему state.
func (s *Server) Push(ev Event) { s.push(ev, writeTimeout) }

// PushExit — прощальное exit с коротким сроком записи.
//
// Отдельный метод, а не Push с другим полем: exit единственный из push стоит на
// пути выключения процесса, и обычный срок задержал бы выключение на пять
// секунд в том самом случае, когда сообщение бесполезно (буфер полон — значит
// менеджер не читает). Обвязкам не приходится помнить об этом по одной: правило
// живёт здесь, в единственном месте, общем для всех четырёх бинарей.
func (s *Server) PushExit(code int) {
	s.push(Event{Event: EventExit, Code: code}, farewellTimeout)
}

func (s *Server) push(ev Event, timeout time.Duration) {
	ev.V = Version
	s.mu.Lock()
	c := s.cur
	s.mu.Unlock()
	if c == nil || c.evicted.Load() {
		return
	}
	if err := c.send(ev, timeout); err != nil {
		s.report(err)
	}
}

// Close закрывает слушателя и текущее соединение и ждёт обработчиков.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	c := s.cur
	s.mu.Unlock()

	err := s.ln.Close()
	if c != nil {
		_ = c.uc.Close()
	}
	s.wg.Wait()
	return err
}

func (s *Server) report(err error) {
	if err != nil && s.cfg.OnError != nil {
		s.cfg.OnError(err)
	}
}

// conn — одно управляющее соединение.
type conn struct {
	uc  *net.UnixConn
	fc  *FrameConn
	wmu sync.Mutex
	// evicted — защёлка вытеснения. Читается и до, и после исполнения команды.
	evicted atomic.Bool
}

func (c *conn) send(msg any, timeout time.Duration) error {
	line, err := EncodeLine(msg)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.uc.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	_, err = c.uc.Write(line)
	return err
}

// evict помечает соединение вытесненным и уходит.
//
// Пометка синхронна, запись и close — асинхронны с дедлайном: блокирующая
// запись отдала бы нового владельца заложником буфера сокета старого, то есть
// скорости самого медленного из двух менеджеров. Потеря evicted протокол не
// ломает: старый увидит «разорвано без evicted» и пойдёт по своей строке §7.
func (c *conn) evict() {
	c.evicted.Store(true)
	go func() {
		_ = c.send(Event{V: Version, Event: EventEvicted}, farewellTimeout)
		_ = c.uc.Close()
	}()
}

func closeFD(fd int) {
	if fd >= 0 {
		_ = unix.Close(fd)
	}
}
