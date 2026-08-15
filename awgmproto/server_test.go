//go:build linux

package awgmproto

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fakeHandler — роль в тестах: считает вызовы, отдаёт заданное состояние.
type fakeHandler struct {
	mu        sync.Mutex
	st        State
	attaches  int
	detaches  int
	attachErr error
	detachErr error
	// hook зовётся синхронно внутри DetachTun: тестам барьера вытеснения нужно
	// попасть ровно в момент «исполнение началось».
	hook func()
}

func (h *fakeHandler) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.st
}

func (h *fakeHandler) AttachTun(iface string, f *os.File) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attaches++
	if h.attachErr != nil {
		return h.attachErr
	}
	h.st.Tun = &TunState{Iface: iface, Attached: true}
	_ = f.Close()
	return nil
}

func (h *fakeHandler) DetachTun() error {
	if h.hook != nil {
		h.hook()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.detaches++
	if h.detachErr != nil {
		return h.detachErr
	}
	h.st.Tun = &TunState{Attached: false}
	return nil
}

func startServer(t *testing.T, h Handler) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.sock")
	srv, err := Listen(ServerConfig{
		Path: path, Impl: "wt-client", Role: "client", Instance: "default", Handler: h,
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })
	return srv, path
}

// testClient — менеджер в тестах.
type testClient struct {
	t  *testing.T
	uc *net.UnixConn
	fc *FrameConn
}

func dialTest(t *testing.T, path string) *testClient {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	uc := c.(*net.UnixConn)
	t.Cleanup(func() { _ = uc.Close() })
	return &testClient{t: t, uc: uc, fc: NewFrameConn(uc)}
}

func (c *testClient) next() (Kind, any, error) {
	c.t.Helper()
	_ = c.uc.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, fd, err := c.fc.ReadFrame()
	closeFD(fd)
	if err != nil {
		return 0, nil, err
	}
	return DecodeLine(line)
}

func (c *testClient) mustNext() (Kind, any) {
	c.t.Helper()
	k, m, err := c.next()
	if err != nil {
		c.t.Fatalf("чтение кадра: %v", err)
	}
	return k, m
}

func (c *testClient) send(req Request, fd int) {
	c.t.Helper()
	req.V = Version
	line, err := EncodeLine(req)
	if err != nil {
		c.t.Fatal(err)
	}
	var oob []byte
	if fd >= 0 {
		oob = unix.UnixRights(fd)
	}
	if _, _, err := c.uc.WriteMsgUnix(line, oob, nil); err != nil {
		c.t.Fatal(err)
	}
}

func (c *testClient) hello() Event {
	c.t.Helper()
	k, m := c.mustNext()
	if k != KindEvent {
		c.t.Fatalf("первым сообщением ожидали push, пришло %v", k)
	}
	ev := m.(Event)
	if ev.Event != EventHello {
		c.t.Fatalf("первым сообщением ожидали hello, пришло %q", ev.Event)
	}
	return ev
}

func TestHelloIsFirstMessage(t *testing.T) {
	h := &fakeHandler{st: State{Role: "client", Instance: "default", PID: 4821,
		ConfigHash: "9f2a", BinarySHA256: "ce92"}}
	_, path := startServer(t, h)

	ev := dialTest(t, path).hello()
	if ev.V != Version || ev.Impl != "wt-client" || ev.Role != "client" || ev.Instance != "default" {
		t.Fatalf("hello без опознавательных полей: %+v", ev)
	}
	if ev.PID != 4821 || ev.ConfigHash != "9f2a" || ev.BinarySHA256 != "ce92" {
		t.Fatalf("hello не несёт pid/отпечатки: %+v", ev)
	}
}

func TestStateRoundTrip(t *testing.T) {
	h := &fakeHandler{st: State{Role: "client", Mode: "raw", Instance: "default",
		PID: 7, ConfigHash: "aa", BinarySHA256: "bb", Address: "10.70.0.5", MTU: 1300}}
	_, path := startServer(t, h)
	c := dialTest(t, path)
	c.hello()

	c.send(Request{ID: 1, Cmd: CmdState}, -1)
	k, m := c.mustNext()
	if k != KindResponse {
		t.Fatalf("ожидали ответ, пришло %v", k)
	}
	resp := m.(Response)
	if !resp.OK || resp.ID != 1 || resp.State == nil {
		t.Fatalf("ответ на state: %+v", resp)
	}
	if resp.State.Address != "10.70.0.5" || resp.State.MTU != 1300 {
		t.Fatalf("состояние не доехало: %+v", resp.State)
	}
}

func TestUnknownCommand(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	c := dialTest(t, path)
	c.hello()

	c.send(Request{ID: 2, Cmd: "stats"}, -1)
	_, m := c.mustNext()
	resp := m.(Response)
	if resp.OK || resp.Code != CodeUnknownCommand {
		t.Fatalf("ожидали unknown-command, получили %+v", resp)
	}
}

func TestDetachTunIsIdempotent(t *testing.T) {
	h := &fakeHandler{}
	_, path := startServer(t, h)
	c := dialTest(t, path)
	c.hello()

	for i := uint64(1); i <= 2; i++ {
		c.send(Request{ID: i, Cmd: CmdDetachTun}, -1)
		_, m := c.mustNext()
		if resp := m.(Response); !resp.OK {
			t.Fatalf("detach-tun обязан быть идемпотентным: %+v", resp)
		}
	}
}

func TestNotSupportedRoleAnswersWithCode(t *testing.T) {
	h := &fakeHandler{detachErr: ErrNotSupported}
	_, path := startServer(t, h)
	c := dialTest(t, path)
	c.hello()

	c.send(Request{ID: 1, Cmd: CmdDetachTun}, -1)
	_, m := c.mustNext()
	resp := m.(Response)
	if resp.OK || resp.Code != CodeNotSupported {
		t.Fatalf("ожидали not-supported, получили %+v", resp)
	}
}

func TestAttachTunWithoutFD(t *testing.T) {
	h := &fakeHandler{}
	_, path := startServer(t, h)
	c := dialTest(t, path)
	c.hello()

	c.send(Request{ID: 1, Cmd: CmdAttachTun, Iface: "opkgtun18"}, -1)
	_, m := c.mustNext()
	resp := m.(Response)
	if resp.OK || resp.Code != CodeBadRequest {
		t.Fatalf("attach-tun без дескриптора обязан давать bad-request: %+v", resp)
	}
	if h.attaches != 0 {
		t.Fatal("обвязку позвали при отсутствии дескриптора")
	}
}

// TestAttachTunRejectsForeignFD — главный тест контракта: дескриптор, который
// не является tun нужного интерфейса, отвергается, а не принимается на веру.
func TestAttachTunRejectsForeignFD(t *testing.T) {
	h := &fakeHandler{}
	_, path := startServer(t, h)
	c := dialTest(t, path)
	c.hello()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	c.send(Request{ID: 1, Cmd: CmdAttachTun, Iface: "opkgtun18"}, int(w.Fd()))
	_ = w.Close()

	_, m := c.mustNext()
	resp := m.(Response)
	if resp.OK || resp.Code != CodeBadRequest {
		t.Fatalf("чужой дескриптор обязан давать bad-request: %+v", resp)
	}
	if h.attaches != 0 {
		t.Fatal("непроверенный дескриптор доехал до обвязки")
	}
	// При ok:false дескриптор обязан быть закрыт: иначе он утечёт, а менеджер
	// решит, что интерфейс кем-то занят. Закрытие видно по EOF на своём конце.
	assertClosed(t, r)
}

// TestFDWithoutAttachIsClosed — дескриптор, пришедший не с attach-tun,
// закрывается и не копится.
func TestFDWithoutAttachIsClosed(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	c := dialTest(t, path)
	c.hello()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	c.send(Request{ID: 1, Cmd: CmdState}, int(w.Fd()))
	_ = w.Close()
	if _, m := c.mustNext(); !m.(Response).OK {
		t.Fatal("state обязан ответить ok")
	}
	assertClosed(t, r)
}

// assertClosed ждёт EOF на читающем конце пайпа: он наступает, только когда
// закрыты ВСЕ копии пишущего конца.
func assertClosed(t *testing.T, r *os.File) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := r.Read(buf)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("ожидали EOF после закрытия дескриптора, получили %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("дескриптор не закрыт: EOF не пришёл")
	}
}

func TestEvictionAnnouncedToOldOwner(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	old := dialTest(t, path)
	old.hello()

	newOwner := dialTest(t, path)
	newOwner.hello()

	k, m := old.mustNext()
	if k != KindEvent || m.(Event).Event != EventEvicted {
		t.Fatalf("старому владельцу обязан прийти evicted, пришло %v %+v", k, m)
	}
	if _, _, err := old.next(); err == nil {
		t.Fatal("старое соединение обязано быть закрыто после evicted")
	}

	// Новый владелец работает.
	newOwner.send(Request{ID: 1, Cmd: CmdState}, -1)
	if _, m := newOwner.mustNext(); !m.(Response).OK {
		t.Fatal("новый владелец не обслуживается")
	}
}

// Барьер вытеснения: негативная сторона правил §3.
//
// В живом сценарии вытесненное соединение почти сразу закрывается, и его
// молчание объясняется закрытием, а не барьером — поэтому мутация, снимающая
// обе проверки c.evicted.Load() из dispatch, проходит сквозь тесты «сверху».
// Оба теста ниже зовут dispatch руками на живом сокете: закрытия нет, и
// проверяется ровно барьер.

// evictionFixture — соединение без слушателя: dispatch зовётся напрямую.
func evictionFixture(t *testing.T, h Handler) (*Server, *conn, *net.UnixConn) {
	t.Helper()
	left, right, err := unixPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = left.Close() })
	t.Cleanup(func() { _ = right.Close() })
	return &Server{cfg: ServerConfig{Handler: h}}, &conn{uc: left, fc: NewFrameConn(left)}, right
}

// assertSilent убеждается, что в соединение ничего не написано.
func assertSilent(t *testing.T, peer *net.UnixConn) {
	t.Helper()
	_ = peer.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 256)
	n, err := peer.Read(buf)
	if err == nil {
		t.Fatalf("вытесненному соединению написан ответ: %q", buf[:n])
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("ожидали тишину, получили %v", err)
	}
}

// TestEvictedConnectionCommandNotExecuted — кадр прочитан целиком, но
// исполнение не начато: команда НЕ исполняется и ответ не пишется.
func TestEvictedConnectionCommandNotExecuted(t *testing.T) {
	h := &fakeHandler{}
	s, c, peer := evictionFixture(t, h)
	c.evicted.Store(true)

	s.dispatch(c, []byte(`{"v":1,"id":1,"cmd":"detach-tun"}`), -1)

	if h.detaches != 0 {
		t.Fatal("команда вытесненного соединения исполнена")
	}
	assertSilent(t, peer)
}

// TestEvictedDuringExecKeepsEffect — исполнение началось: доводится до конца,
// эффект остаётся, но ответ в вытесненное соединение не пишется.
func TestEvictedDuringExecKeepsEffect(t *testing.T) {
	h := &fakeHandler{}
	s, c, peer := evictionFixture(t, h)
	h.hook = func() { c.evicted.Store(true) }

	s.dispatch(c, []byte(`{"v":1,"id":1,"cmd":"detach-tun"}`), -1)

	if h.detaches != 1 {
		t.Fatalf("начатая команда не доведена до конца: detaches=%d", h.detaches)
	}
	assertSilent(t, peer)
}

func TestPushGoesToCurrentOwner(t *testing.T) {
	srv, path := startServer(t, &fakeHandler{})
	c := dialTest(t, path)
	c.hello()

	srv.Push(Event{Event: EventAddress, Address: "10.70.0.5", MTU: 1300})
	k, m := c.mustNext()
	if k != KindEvent {
		t.Fatalf("ожидали push, пришло %v", k)
	}
	ev := m.(Event)
	if ev.V != Version || ev.Event != EventAddress || ev.Address != "10.70.0.5" {
		t.Fatalf("push пришёл искажённым: %+v", ev)
	}
}

func TestPushExitDelivers(t *testing.T) {
	// PushExit — отдельный путь (короткий срок записи), и он обязан доезжать до
	// владельца так же, как обычный push. Сам срок тестом не покрыт намеренно:
	// чтобы запись заблокировалась, надо забить буфер сокета сотнями килобайт, а
	// ловит такой тест разницу «1 с против 5 с» на таймингах — цена высокая,
	// защита хрупкая. Держит срок докстрока farewellTimeout и единственная точка
	// вызова.
	srv, path := startServer(t, &fakeHandler{})
	c := dialTest(t, path)
	c.hello()

	srv.PushExit(3)
	k, m := c.mustNext()
	if k != KindEvent {
		t.Fatalf("ожидали push, пришло %v", k)
	}
	ev := m.(Event)
	if ev.V != Version || ev.Event != EventExit || ev.Code != 3 {
		t.Fatalf("exit пришёл искажённым: %+v", ev)
	}
}

func TestListenRefusesLiveInstance(t *testing.T) {
	h := &fakeHandler{}
	_, path := startServer(t, h)

	// Безусловный unlink развёл бы два процесса на один инстанс.
	if _, err := Listen(ServerConfig{Path: path, Handler: h}); err == nil {
		t.Fatal("второй процесс занял живой сокет инстанса")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("живой сокет удалён пробой: %v", err)
	}
}

func TestListenReclaimsStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	// Оставляем файл на месте — так выглядит сокет после SIGKILL процесса.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()

	srv, err := Listen(ServerConfig{Path: path, Handler: &fakeHandler{}})
	if err != nil {
		t.Fatalf("протухший сокет не переиспользован: %v", err)
	}
	_ = srv.Close()
}

func TestFrameConnAssemblesLineFromChunks(t *testing.T) {
	// Граница сообщения — перевод строки, а не граница пакета.
	left, right, err := unixPair()
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	defer right.Close()

	go func() {
		_, _ = right.Write([]byte(`{"v":1,"id":1,"c`))
		time.Sleep(20 * time.Millisecond)
		_, _ = right.Write([]byte("md\":\"state\"}\n"))
	}()

	fc := NewFrameConn(left)
	line, fd, err := fc.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	closeFD(fd)
	kind, msg, err := DecodeLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindRequest || msg.(Request).Cmd != "state" {
		t.Fatalf("кадр не собран из двух чтений: %q", line)
	}
}

// unixPair — пара соединённых unix-сокетов без файла в ФС.
func unixPair() (*net.UnixConn, *net.UnixConn, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}
	conns := make([]*net.UnixConn, 2)
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "pair")
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			return nil, nil, err
		}
		conns[i] = c.(*net.UnixConn)
	}
	return conns[0], conns[1], nil
}

// Дальше — свойства слушателя, которых нет в тестах выше: права сокета, коды
// ошибок обвязки, судьба нечитаемого кадра, сериализация исполнения поверх
// соединений и снятие владельца.

// startServerWithErrors — как startServer, но копит то, что слушатель не смог
// доставить менеджеру: без OnError эти пути не видны ни одному тесту.
func startServerWithErrors(t *testing.T, h Handler) (string, func() []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.sock")
	var mu sync.Mutex
	var errs []string
	srv, err := Listen(ServerConfig{
		Path: path, Impl: "wt-client", Role: "client", Instance: "default", Handler: h,
		OnError: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, err.Error())
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })
	return path, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), errs...)
	}
}

// sendRaw пишет кадр как есть: тестам нечитаемых кадров нужна строка, которую
// EncodeLine не собрал бы.
func (c *testClient) sendRaw(line string, fd int) {
	c.t.Helper()
	var oob []byte
	if fd >= 0 {
		oob = unix.UnixRights(fd)
	}
	if _, _, err := c.uc.WriteMsgUnix([]byte(line), oob, nil); err != nil {
		c.t.Fatal(err)
	}
}

// waitErr ждёт, пока OnError позовут хотя бы раз: доставка ошибки в обвязку
// асинхронна относительно теста.
func waitErr(t *testing.T, errs func() []string) []string {
	t.Helper()
	for range 500 {
		if got := errs(); len(got) > 0 {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ошибка не доехала до OnError")
	return nil
}

// TestSocketIsPrivate — права сокета 0600 (§3). Без chmod файл рождается по
// umask (обычно 0755), и каталог 0700 остаётся единственной защитой.
func TestSocketIsPrivate(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("права сокета %04o, ожидали 0600", perm)
	}
}

// TestAttachTunWithoutIfaceIsBadRequest — имя интерфейса обязательно: без него
// проверять дескриптор не с чем.
func TestAttachTunWithoutIfaceIsBadRequest(t *testing.T) {
	h := &fakeHandler{}
	_, path := startServer(t, h)
	c := dialTest(t, path)
	c.hello()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	c.send(Request{ID: 1, Cmd: CmdAttachTun}, int(w.Fd()))
	_ = w.Close()

	_, m := c.mustNext()
	resp := m.(Response)
	if resp.OK || resp.Code != CodeBadRequest {
		t.Fatalf("attach-tun без имени интерфейса обязан давать bad-request: %+v", resp)
	}
	if h.attaches != 0 {
		t.Fatal("обвязку позвали без имени интерфейса")
	}
	// Тест держится за формулировку отказа, и это осознанно: другой
	// наблюдаемой разницы нет. Без этой ветки дескриптор всё равно отвергнет
	// VerifyTunFD с тем же кодом bad-request, и пропажу проверки видно только
	// по тексту — менеджер получит жалобу на TUNGETIFF вместо «в команде нет
	// имени интерфейса». Переформулировали сообщение — правьте и тест.
	if !strings.Contains(resp.Error, "без имени интерфейса") {
		t.Fatalf("отказ не называет причину: %q", resp.Error)
	}
	assertClosed(t, r)
}

// TestAttachTunWithoutFDNamesTheReason — то же про отсутствующий дескриптор:
// код bad-request дала бы и проверка VerifyTunFD над fd=-1, поэтому саму ветку
// держит только пояснение, и тест держится за его формулировку.
func TestAttachTunWithoutFDNamesTheReason(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	c := dialTest(t, path)
	c.hello()

	c.send(Request{ID: 1, Cmd: CmdAttachTun, Iface: "opkgtun18"}, -1)
	_, m := c.mustNext()
	if resp := m.(Response); !strings.Contains(resp.Error, "без дескриптора") {
		t.Fatalf("отказ не называет причину: %q", resp.Error)
	}
}

// TestErrfCodeReachesResponse — код отказа обвязки доезжает до менеджера как
// есть. Проверяется на busy, которого нет ни в одной ветке слушателя: код
// берётся из ошибки, а не из места её возникновения.
func TestErrfCodeReachesResponse(t *testing.T) {
	h := &fakeHandler{detachErr: Errf(CodeBusy, "дескриптор занят интерфейсом %s", "opkgtun18")}
	_, path := startServer(t, h)
	c := dialTest(t, path)
	c.hello()

	c.send(Request{ID: 1, Cmd: CmdDetachTun}, -1)
	_, m := c.mustNext()
	resp := m.(Response)
	if resp.OK || resp.Code != CodeBusy {
		t.Fatalf("ожидали busy, получили %+v", resp)
	}
	if resp.Error != "дескриптор занят интерфейсом opkgtun18" {
		t.Fatalf("пояснение отказа искажено: %q", resp.Error)
	}
}

// TestPlainErrorBecomesInternal — ошибка обвязки без кода протокола едет как
// internal, а не как пустой код: менеджер различает отказы по code.
func TestPlainErrorBecomesInternal(t *testing.T) {
	h := &fakeHandler{detachErr: errors.New("не смогли отцепить")}
	_, path := startServer(t, h)
	c := dialTest(t, path)
	c.hello()

	c.send(Request{ID: 1, Cmd: CmdDetachTun}, -1)
	_, m := c.mustNext()
	resp := m.(Response)
	if resp.OK || resp.Code != CodeInternal {
		t.Fatalf("ожидали internal, получили %+v", resp)
	}
	if resp.Error != "не смогли отцепить" {
		t.Fatalf("пояснение отказа искажено: %q", resp.Error)
	}
}

// TestUnreadableFrameIsReportedNotAnswered — на кадр, который не разобрался,
// ответа нет вовсе: id у него неизвестен, отвечать некому. Ошибка уходит в
// журнал обвязки, дескриптор закрывается.
func TestUnreadableFrameIsReportedNotAnswered(t *testing.T) {
	path, errs := startServerWithErrors(t, &fakeHandler{})
	c := dialTest(t, path)
	c.hello()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	c.sendRaw("не json\n", int(w.Fd()))
	_ = w.Close()

	waitErr(t, errs)
	assertClosed(t, r)
	assertSilent(t, c.uc)
}

// TestNonRequestFrameIsReportedNotAnswered — ответ или push от менеджера на
// управляющем сокете не команда: исполнять нечего, отвечать нечем.
func TestNonRequestFrameIsReportedNotAnswered(t *testing.T) {
	path, errs := startServerWithErrors(t, &fakeHandler{})
	c := dialTest(t, path)
	c.hello()

	c.sendRaw(`{"v":1,"id":1,"ok":true}`+"\n", -1)

	got := waitErr(t, errs)
	if !strings.Contains(got[0], "ожидается запрос") {
		t.Fatalf("непохожая жалоба на не-запрос: %q", got[0])
	}
	assertSilent(t, c.uc)
}

// TestExecIsSerializedAcrossConnections — команды сериализуются поверх ВСЕХ
// соединений (§3): новый владелец не начнёт свою первую команду, пока не
// завершилась чужая незаконченная. Соединения без слушателя, dispatch зовётся
// руками: живой accept вытеснил бы первое, и барьер спрятался бы за вытеснение.
func TestExecIsSerializedAcrossConnections(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	h := &fakeHandler{hook: func() {
		entered <- struct{}{}
		<-release
	}}

	s := &Server{cfg: ServerConfig{Handler: h}}
	c1, c2 := dispatchConn(t), dispatchConn(t)

	done := make(chan struct{}, 2)
	line := []byte(`{"v":1,"id":1,"cmd":"detach-tun"}`)
	go func() { s.dispatch(c1, line, -1); done <- struct{}{} }()
	<-entered

	go func() { s.dispatch(c2, line, -1); done <- struct{}{} }()
	select {
	case <-entered:
		t.Fatal("вторая команда началась, не дождавшись первой")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("вторая команда не началась после освобождения первой")
	}
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("dispatch не завершился")
		}
	}
}

// dispatchConn — соединение для прямого вызова dispatch: писать ответ есть
// куда, читать его никто не обязан.
func dispatchConn(t *testing.T) *conn {
	t.Helper()
	left, right, err := unixPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = left.Close() })
	t.Cleanup(func() { _ = right.Close() })
	return &conn{uc: left, fc: NewFrameConn(left)}
}

// TestCloseDropsOwnerAndSocket — Close рвёт соединение с менеджером, снимает
// сокет и повторного вызова не боится (его делает и t.Cleanup).
func TestCloseDropsOwnerAndSocket(t *testing.T) {
	srv, path := startServer(t, &fakeHandler{})
	c := dialTest(t, path)
	c.hello()

	if err := srv.Close(); err != nil {
		t.Fatalf("закрытие слушателя: %v", err)
	}
	if _, _, err := c.next(); err == nil {
		t.Fatal("соединение с менеджером пережило Close")
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("повторный Close обязан быть тихим: %v", err)
	}
	if _, err := net.Dial("unix", path); err == nil {
		t.Fatal("после Close на сокет всё ещё пускают")
	}
}

// TestOwnerClearedOnDisconnect — ушедший менеджер перестаёт быть владельцем:
// иначе push писался бы в мёртвое соединение до самого конца процесса.
func TestOwnerClearedOnDisconnect(t *testing.T) {
	srv, path := startServer(t, &fakeHandler{})
	c := dialTest(t, path)
	c.hello()
	_ = c.uc.Close()

	for range 500 {
		srv.mu.Lock()
		cur := srv.cur
		srv.mu.Unlock()
		if cur == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("владелец не снят после разрыва соединения")
}

// TestEvictedConnectionIsClosed — вытесненное соединение именно ЗАКРЫВАЕТСЯ, а
// не просто замолкает. Проверка на EOF, а не на «любую ошибку чтения»: с
// дедлайном чтения молчание неотличимо от закрытия, и мутант, снявший
// c.uc.Close() из evict, проходит сквозь проверку «err != nil».
func TestEvictedConnectionIsClosed(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	old := dialTest(t, path)
	old.hello()
	dialTest(t, path).hello()

	if k, m := old.mustNext(); k != KindEvent || m.(Event).Event != EventEvicted {
		t.Fatalf("старому владельцу обязан прийти evicted, пришло %v %+v", k, m)
	}
	if _, _, err := old.next(); !errors.Is(err, io.EOF) {
		t.Fatalf("после evicted ожидали закрытие соединения (EOF), получили %v", err)
	}
}

// TestOversizeFrameIsNotExecuted — потолок кадра держит разбор, а не читатель.
// ReadFrame отдаёт строку длиннее потолка, если перевод строки приехал в том же
// чтении, что и перебор: опираться на «читатель длинного не отдаст» нельзя, и
// dispatch на это не опирается — команда не исполняется, ответа нет.
func TestOversizeFrameIsNotExecuted(t *testing.T) {
	h := &fakeHandler{}
	s, c, peer := evictionFixture(t, h)

	line := []byte(`{"v":1,"id":1,"cmd":"detach-tun","iface":"` + strings.Repeat("x", maxLine) + `"}`)
	s.dispatch(c, line, -1)

	if h.detaches != 0 {
		t.Fatal("команда из кадра сверх потолка исполнена")
	}
	assertSilent(t, peer)
}

// TestOversizeStateIsReportedNotAnswered — ответ, который не влезает в кадр,
// уходит в журнал обвязки, а менеджеру не приходит ничего. Это осознанная
// цена: пропущенный ответ менеджер разберёт по таймауту (§5.1), а порезанный
// кадр разъехался бы с потоком. Достижим случай только через wg-конфиг в
// state — единственное поле без потолка длины.
func TestOversizeStateIsReportedNotAnswered(t *testing.T) {
	h := &fakeHandler{st: State{WG: &WGState{Config: strings.Repeat("x", maxLine)}}}
	path, errs := startServerWithErrors(t, h)
	c := dialTest(t, path)
	c.hello()

	c.send(Request{ID: 1, Cmd: CmdState}, -1)

	got := waitErr(t, errs)
	if !strings.Contains(got[0], "превышает потолок") {
		t.Fatalf("непохожая жалоба на длинный ответ: %q", got[0])
	}
	assertSilent(t, c.uc)
}

// TestListenRefusesRegularFileAtPath — по пути сокета лежит обычный файл.
// connect(2) отвечает на него ECONNREFUSED — тем же, чем на протухший сокет, —
// поэтому без проверки типа он был бы снесён. Чужое не удаляем: отказ.
func TestListenRefusesRegularFileAtPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	if err := os.WriteFile(path, []byte("не наш файл"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Listen(ServerConfig{Path: path, Handler: &fakeHandler{}}); err == nil {
		t.Fatal("слушатель занял путь, на котором лежит обычный файл")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("посторонний файл снесён пробой: %v", err)
	}
	if string(body) != "не наш файл" {
		t.Fatalf("посторонний файл изменён: %q", body)
	}
}

// TestListenRefusesDirectoryAtPath — то же про каталог: пустой каталог
// os.Remove снимает молча, а connect(2) на него тоже отвечает ECONNREFUSED.
func TestListenRefusesDirectoryAtPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := Listen(ServerConfig{Path: path, Handler: &fakeHandler{}}); err == nil {
		t.Fatal("слушатель занял путь, на котором лежит каталог")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("каталог снесён пробой: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("по пути больше не каталог: %s", fi.Mode().Type())
	}
}
