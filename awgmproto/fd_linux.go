//go:build linux

package awgmproto

import (
	"bytes"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// readChunk — размер одного чтения. Кадры протокола короткие; исключение —
// wg-конфиг в ответе state, он собирается из нескольких чтений.
const readChunk = 8 * 1024

// FrameConn — управляющее соединение со стороны процесса.
//
// Читать его через bufio.Scanner или обычный Read НЕЛЬЗЯ: ядро молча выбросит
// и закроет пришедший с сообщением дескриптор. Поэтому единственный путь
// чтения — recvmsg с буфером под ancillary-данные, и он же собирает строку из
// нескольких чтений: граница сообщения — перевод строки, а не граница пакета.
type FrameConn struct {
	uc  *net.UnixConn
	buf []byte
	rd  []byte
	oob []byte
	// fds — дескрипторы, пришедшие раньше, чем завершился их кадр, в порядке
	// прихода. Менеджер обязан слать одну строку за один sendmsg (§5.3), и
	// очередь FIFO даёт ровно нужную привязку. Если он всё же склеит две
	// строки с одним дескриптором, тот достанется первой — она ответит
	// bad-request, дескриптор закроется, и ошибка будет видимой, а не тихой.
	fds []int
}

func NewFrameConn(uc *net.UnixConn) *FrameConn {
	return &FrameConn{
		uc:  uc,
		rd:  make([]byte, readChunk),
		oob: make([]byte, unix.CmsgSpace(4)*4),
	}
}

// ReadFrame возвращает очередной кадр без завершающего перевода строки и
// дескриптор, пришедший вместе с ним; -1, если дескриптора не было.
//
// Дескриптор возвращается вызывающему в ЛЮБОМ случае, в том числе при ошибке
// разбора: закрыть его — обязанность вызывающего, иначе он утечёт.
func (c *FrameConn) ReadFrame() ([]byte, int, error) {
	for {
		if i := bytes.IndexByte(c.buf, '\n'); i >= 0 {
			line := make([]byte, i)
			copy(line, c.buf[:i])
			c.buf = append(c.buf[:0], c.buf[i+1:]...)
			return line, c.popFD(), nil
		}
		if len(c.buf) > maxLine {
			return nil, c.popFD(), fmt.Errorf("кадр длиннее потолка %d байт без перевода строки", maxLine)
		}
		n, oobn, _, _, err := c.uc.ReadMsgUnix(c.rd, c.oob)
		if oobn > 0 {
			c.collectFDs(c.oob[:oobn])
		}
		if n > 0 {
			c.buf = append(c.buf, c.rd[:n]...)
		}
		if err != nil {
			return nil, c.popFD(), err
		}
	}
}

// collectFDs вынимает дескрипторы из ancillary-данных одного чтения.
func (c *FrameConn) collectFDs(oob []byte) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, m := range msgs {
		if m.Header.Level != unix.SOL_SOCKET || m.Header.Type != unix.SCM_RIGHTS {
			continue
		}
		fds, err := unix.ParseUnixRights(&m)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			// Полученный по SCM_RIGHTS дескриптор close-on-exec не наследует:
			// без этого он утёк бы в каждого хелпера, которого породит процесс.
			unix.CloseOnExec(fd)
			c.fds = append(c.fds, fd)
		}
	}
}

func (c *FrameConn) popFD() int {
	if len(c.fds) == 0 {
		return -1
	}
	fd := c.fds[0]
	c.fds = c.fds[1:]
	return fd
}

// CloseUnclaimedFDs закрывает дескрипторы, не доставшиеся ни одному кадру.
// Зовётся при закрытии соединения.
func (c *FrameConn) CloseUnclaimedFDs() {
	for _, fd := range c.fds {
		_ = unix.Close(fd)
	}
	c.fds = nil
}

// VerifyTunFD проверяет, что дескриптор — действительно tun и действительно
// того интерфейса, который назван в attach-tun.
//
// Проверка обязательна, а не желательна. hello защищает от подключения к чужому
// сокету, но не от ошибки самого менеджера: имя в TUNSETIFF и поле iface в
// сообщении — разные переменные, и перепутать их при ренумерации
// OpkgTun17 → OpkgTun18 — одна ошибка индексации. Это единственное место
// контракта, где ошибка даёт НЕВИДИМЫЙ отказ: state честно ответит
// attached:true, реконсиляция увидит settled, а пакеты уйдут не в тот
// интерфейс, и диагностировать это можно только перехватом трафика.
func VerifyTunFD(fd int, iface string) error {
	ifr, err := unix.NewIfreq("")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.TUNGETIFF, ifr); err != nil {
		return fmt.Errorf("ожидали tun %s: дескриптор не отвечает на TUNGETIFF (%v)", iface, err)
	}
	return checkTunIfreq(ifr.Name(), ifr.Uint16(), iface)
}

// checkTunIfreq — чистая проверка ответа ядра. Отделена от ioctl ради
// тестируемости: так набор флагов проверяется таблицей, без прав и без
// устройства. Настоящий tun в тестах тоже создаётся — под `unshare -Urn`, где
// TUNSETIFF разрешён (см. TestVerifyTunFDOnRealTun); пропускается такой тест
// только там, где запрещены user namespace.
func checkTunIfreq(gotIface string, flags uint16, wantIface string) error {
	if flags&unix.IFF_TAP != 0 {
		return fmt.Errorf("ожидали tun %s, получили tap %s", wantIface, gotIface)
	}
	if flags&unix.IFF_TUN == 0 {
		return fmt.Errorf("ожидали tun %s, в флагах дескриптора нет IFF_TUN (0x%04x)", wantIface, flags)
	}
	if flags&unix.IFF_NO_PI == 0 {
		// Дескриптор с packet info не «работает чуть иначе»: он сдвигает
		// каждый пакет на четыре байта — тот же тихий блэкхол.
		return fmt.Errorf("ожидали tun %s без packet info, в флагах нет IFF_NO_PI (0x%04x)", wantIface, flags)
	}
	if flags&unix.IFF_VNET_HDR != 0 {
		// Контракт §5.3 требует дескриптор БЕЗ virtio-заголовка. Отказ тот же,
		// что у packet info, и такой же тихий: перед каждым пакетом едет
		// заголовок virtio_net_hdr, процесс читает его как начало IP-пакета.
		return fmt.Errorf("ожидали tun %s без virtio-заголовка, в флагах есть IFF_VNET_HDR (0x%04x)", wantIface, flags)
	}
	if gotIface != wantIface {
		return fmt.Errorf("ожидали tun %s, получили %s", wantIface, gotIface)
	}
	return nil
}
