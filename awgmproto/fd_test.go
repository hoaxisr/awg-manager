//go:build linux

package awgmproto

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCheckTunIfreq(t *testing.T) {
	const want = "opkgtun18"
	cases := []struct {
		name  string
		got   string
		flags uint16
		bad   string // подстрока, обязанная быть в объяснении; "" = ошибки нет
	}{
		{"свой tun", want, unix.IFF_TUN | unix.IFF_NO_PI, ""},
		{"чужой интерфейс", "opkgtun17", unix.IFF_TUN | unix.IFF_NO_PI, "opkgtun17"},
		{"tap вместо tun", want, unix.IFF_TAP | unix.IFF_NO_PI, "tap"},
		{"без IFF_NO_PI", want, unix.IFF_TUN, "IFF_NO_PI"},
		{"без IFF_TUN", want, unix.IFF_NO_PI, "IFF_TUN"},
		{"с IFF_VNET_HDR", want, unix.IFF_TUN | unix.IFF_NO_PI | unix.IFF_VNET_HDR, "IFF_VNET_HDR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkTunIfreq(c.got, c.flags, want)
			if c.bad == "" {
				if err != nil {
					t.Fatalf("законный дескриптор отвергнут: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("расхождение принято на веру")
			}
			if !strings.Contains(err.Error(), c.bad) {
				t.Fatalf("в объяснении нет %q: %v", c.bad, err)
			}
			// Ожидаемое имя обязано быть в тексте: иначе отказ не
			// диагностируется по журналу.
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("в объяснении нет ожидаемого интерфейса: %v", err)
			}
		})
	}
}

// tunNSChildEnv — маркер дочернего прогона: тот же тестовый бинарь, запущенный
// в своих user+net namespace, где TUNSETIFF разрешён без прав на машине.
const tunNSChildEnv = "AWGM_TEST_TUN_NS_CHILD"

func TestVerifyTunFDOnRealTun(t *testing.T) {
	// Положительный путь VerifyTunFD (сам ioctl, а не разбор ответа) иначе не
	// исполняется ничем: checkTunIfreq зовётся напрямую, а единственный тест с
	// настоящим дескриптором — отрицательный. Здесь же ловится IFF_VNET_HDR:
	// сам по себе ядро его не выставляет (живой tun отдаёт 0x1001), но выдаёт
	// без единого возражения, если флаг попросили в TUNSETIFF, — а контракт
	// §5.3 такой дескриптор запрещает.
	if os.Getenv(tunNSChildEnv) == "" && !tunCreatable() {
		rerunInUserNS(t)
		return
	}

	fd, err := openTestTun("awgmtun0", unix.IFF_TUN|unix.IFF_NO_PI)
	if err != nil {
		t.Skipf("tun создать не удалось (%v): положительный путь не проверен", err)
	}
	defer unix.Close(fd)

	if err := VerifyTunFD(fd, "awgmtun0"); err != nil {
		t.Fatalf("законный tun отвергнут: %v", err)
	}
	if err := VerifyTunFD(fd, "awgmtun1"); err == nil {
		t.Fatal("имя интерфейса взято из сообщения, а не у ядра: чужое имя принято")
	}

	vfd, err := openTestTun("awgmtun2", unix.IFF_TUN|unix.IFF_NO_PI|unix.IFF_VNET_HDR)
	if err != nil {
		t.Skipf("tun с IFF_VNET_HDR создать не удалось: %v", err)
	}
	defer unix.Close(vfd)
	err = VerifyTunFD(vfd, "awgmtun2")
	if err == nil {
		t.Fatal("дескриптор с virtio-заголовком принят: перед каждым пакетом поедет virtio_net_hdr")
	}
	if !strings.Contains(err.Error(), "IFF_VNET_HDR") {
		t.Fatalf("в объяснении нет IFF_VNET_HDR: %v", err)
	}
}

// openTestTun создаёт tun с заданными флагами и возвращает его дескриптор.
func openTestTun(name string, flags uint16) (int, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return -1, err
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	ifr.SetUint16(flags)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func tunCreatable() bool {
	fd, err := openTestTun("awgmprobe0", unix.IFF_TUN|unix.IFF_NO_PI)
	if err != nil {
		return false
	}
	_ = unix.Close(fd)
	return true
}

// rerunInUserNS перезапускает этот же тест в своих user+net namespace: там мы
// root, TUNSETIFF разрешён, а созданный интерфейс умирает вместе с namespace и
// машину не пачкает. Нет прав на namespace или самого unshare — тест честно
// пропускается, а не притворяется пройденным.
func rerunInUserNS(t *testing.T) {
	t.Helper()
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("TUNSETIFF запрещён и unshare не найден: положительный путь VerifyTunFD не проверен")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(unshare, "-Urn", self, "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), tunNSChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	switch {
	case !bytes.Contains(out, []byte("=== RUN")):
		t.Skipf("подпроцесс в user namespace не стартовал (%v): %s", err, out)
	case bytes.Contains(out, []byte("--- SKIP")):
		t.Skipf("подпроцесс пропустил проверку: %s", out)
	case err != nil:
		t.Fatalf("прогон в user namespace провалился (%v):\n%s", err, out)
	}
	t.Logf("проверено в user namespace:\n%s", out)
}

func TestReadFrameCapIsExact(t *testing.T) {
	// Потолок сборки кадра — ровно maxLine: строка такой длины законна,
	// а буфер, переросший потолок без перевода строки, обязан дать ошибку,
	// а не расти дальше. Проверяется точное значение: со сравнением >=
	// законная строка отвергалась бы, со сравнением по readChunk граница
	// уехала бы незаметно.
	t.Run("ровно потолок", func(t *testing.T) {
		left, right := fdTestPair(t)
		go func() {
			_, _ = right.Write(bytes.Repeat([]byte("x"), maxLine))
			time.Sleep(20 * time.Millisecond)
			_, _ = right.Write([]byte("\n"))
		}()
		line, fd, err := NewFrameConn(left).ReadFrame()
		if fd != -1 {
			t.Fatalf("дескриптора не было, а вернулся %d", fd)
		}
		if err != nil {
			t.Fatalf("кадр длиной ровно %d байт отвергнут: %v", maxLine, err)
		}
		if len(line) != maxLine {
			t.Fatalf("кадр собран длиной %d, ожидали %d", len(line), maxLine)
		}
	})

	t.Run("потолок плюс байт без перевода строки", func(t *testing.T) {
		left, right := fdTestPair(t)
		go func() {
			_, _ = right.Write(bytes.Repeat([]byte("x"), maxLine+1))
		}()
		_, _, err := NewFrameConn(left).ReadFrame()
		if err == nil {
			t.Fatal("буфер сверх потолка принят без ошибки")
		}
		if !strings.Contains(err.Error(), "потолка") {
			t.Fatalf("ошибка не про потолок: %v", err)
		}
	})
}

func TestReadFrameKeepsPreviousLine(t *testing.T) {
	// Кадр отдаётся вызывающему копией. Верни ReadFrame срез внутреннего
	// буфера — сдвиг остатка затрёт уже отданную строку прямо под руками
	// разборщика, и увидим мы это как «битый JSON» в чужом кадре. Двух кадров
	// в одном чтении достаточно: остаток переезжает в начало того же массива.
	const (
		frame1 = `{"v":1,"id":1,"cmd":"aaaaa"}`
		frame2 = `{"v":1,"id":2,"cmd":"bbbbb"}`
	)
	left, right := fdTestPair(t)
	go func() {
		_, _ = right.Write([]byte(frame1 + "\n" + frame2 + "\n"))
	}()

	fc := NewFrameConn(left)
	first, _, err := fc.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	// Сверка с ЛИТЕРАЛОМ, а не со снимком: сдвиг остатка происходит внутри той
	// же ReadFrame, до возврата, поэтому снимок «до и после» затирания не
	// увидел бы — он взялся бы уже с испорченной строки.
	if string(first) != frame1 {
		t.Fatalf("первый кадр = %q, ожидали %q: отдан срез внутреннего буфера", first, frame1)
	}
	second, _, err := fc.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != frame2 {
		t.Fatalf("второй кадр = %q, ожидали %q", second, frame2)
	}
	if string(first) != frame1 {
		t.Fatalf("первый кадр затёрт вторым: %q", first)
	}
}

func TestFrameConnFDQueue(t *testing.T) {
	// Дескрипторы раздаются кадрам в порядке прихода, помечаются close-on-exec
	// и не утекают, если кадра им не досталось. Ни одно из трёх свойств не
	// имеет симптома при поломке: дескриптор просто уедет не туда, утечёт в
	// хелпера или в счётчик открытых файлов.
	var fds [3][2]int
	for i := range fds {
		var p [2]int
		if err := unix.Pipe(p[:]); err != nil {
			t.Fatal(err)
		}
		fds[i] = p
		// Читающий конец — неблокирующий: открытый пишущий конец даст EAGAIN,
		// закрытый — конец файла. Иначе непроверенное закрытие вешало бы тест
		// вместо того, чтобы его провалить.
		if err := unix.SetNonblock(p[0], true); err != nil {
			t.Fatal(err)
		}
		defer unix.Close(p[0]) //nolint:revive // читающий конец держим до конца теста
	}

	c := NewFrameConn(nil)
	c.collectFDs(unix.UnixRights(fds[0][1], fds[1][1], fds[2][1]))

	for _, p := range fds {
		flags, err := unix.FcntlInt(uintptr(p[1]), unix.F_GETFD, 0)
		if err != nil {
			t.Fatal(err)
		}
		if flags&unix.FD_CLOEXEC == 0 {
			t.Fatalf("на дескрипторе %d нет FD_CLOEXEC: он утечёт в каждого хелпера", p[1])
		}
	}

	if got := c.popFD(); got != fds[0][1] {
		t.Fatalf("первым кадру достался дескриптор %d, ожидали %d", got, fds[0][1])
	}

	c.CloseUnclaimedFDs()
	for _, p := range fds[1:] {
		var b [1]byte
		n, err := unix.Read(p[0], b[:])
		if n != 0 || err != nil {
			t.Fatalf("недоставшийся дескриптор не закрыт: n=%d err=%v", n, err)
		}
	}
	if got := c.popFD(); got != -1 {
		t.Fatalf("пустая очередь вернула %d вместо -1", got)
	}
}

// fdTestPair — пара соединённых unix-сокетов для тестов кадрирования.
func fdTestPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]*net.UnixConn, 2)
	for i, fd := range pair {
		f := os.NewFile(uintptr(fd), "pair")
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = c.(*net.UnixConn)
		t.Cleanup(func() { _ = conns[i].Close() })
	}
	return conns[0], conns[1]
}

func TestVerifyTunFDRejectsNonTun(t *testing.T) {
	// Дескриптор, который вообще не tun: ioctl обязан провалиться, а не быть
	// пропущен как «ну, наверное, tun».
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])

	if err := VerifyTunFD(fds[0], "opkgtun18"); err == nil {
		t.Fatal("не-tun дескриптор принят")
	}
}
