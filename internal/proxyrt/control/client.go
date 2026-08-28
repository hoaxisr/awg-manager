// Package control — сторона менеджера в протоколе управляющего сокета.
//
// Client — одно соединение и ничего больше. Живучесть (переподключение,
// защёлка вытеснения, последний снимок) живёт в Link: разделение нужно, чтобы
// ресурс process имел дело с долгоживущим объектом, переживающим смерть
// соединения, а разбор кадров не знал про политику ретраев.
package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"

	"golang.org/x/sys/unix"
)

// eventQueue — глубина очереди push. Push для менеджера — будильник, решение
// принимается по последующему state, поэтому переполнение безопасно: лишние
// будильники отбрасываются.
const eventQueue = 16

// ErrClosed — соединение больше не живо.
var ErrClosed = errors.New("управляющее соединение закрыто")

// ErrProtocolVersion — процесс говорит на другой мажорной версии протокола.
//
// Отдельный класс, а не «кадр не разобран»: по §4 это отказ без деградации и
// БЕЗ ретраев. Смешавшись с «сокета ещё нет», он давал бы окно бессмысленных
// переподключений и, главное, приезжал бы наверх как «процесс не открыл
// управляющий сокет» — то есть как временная неготовность там, где инстанс не
// поднимется никогда.
var ErrProtocolVersion = errors.New("несовместимая мажорная версия протокола")

// Client — одно соединение к управляющему сокету процесса.
type Client struct {
	uc    *net.UnixConn
	fc    *awgmproto.FrameConn
	hello awgmproto.Event

	events chan awgmproto.Event
	done   chan struct{}

	mu      sync.Mutex
	nextID  uint64
	waiters map[uint64]chan awgmproto.Response
	err     error
	// evicted — процесс объявил, что инстансом завладел другой менеджер.
	evicted bool
	// wmu сериализует запись: sendmsg с дескриптором обязан быть цельным.
	wmu sync.Mutex
}

// Dial подключается и дожидается hello. Первым сообщением обязан прийти
// именно он: по нему менеджер убеждается, что попал в свой инстанс.
func Dial(ctx context.Context, path string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: ожидали unix-соединение", path)
	}
	c := &Client{
		uc:      uc,
		fc:      awgmproto.NewFrameConn(uc),
		events:  make(chan awgmproto.Event, eventQueue),
		done:    make(chan struct{}),
		waiters: make(map[uint64]chan awgmproto.Response),
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = uc.SetReadDeadline(dl)
	}
	line, fd, err := c.fc.ReadFrame()
	closeFD(fd)
	if err != nil {
		_ = uc.Close()
		return nil, fmt.Errorf("%s: hello не прочитан: %w", path, err)
	}
	_ = uc.SetReadDeadline(time.Time{})
	kind, msg, err := awgmproto.DecodeLine(line)
	if err != nil {
		_ = uc.Close()
		// Чужой мажор и мусор в кадре приезжают из разбора разными классами, и
		// наверх обязаны уехать так же: первый — терминальным отказом (§4),
		// второй — обычной неудачей соединения, которую Link ретраит.
		var major *awgmproto.ProtocolMajorError
		if errors.As(err, &major) {
			return nil, fmt.Errorf("%w: процесс говорит на версии %d, менеджер на %d",
				ErrProtocolVersion, major.Got, awgmproto.Version)
		}
		return nil, fmt.Errorf("%s: hello не разобран: %w", path, err)
	}
	ev, ok := msg.(awgmproto.Event)
	if kind != awgmproto.KindEvent || !ok || ev.Event != awgmproto.EventHello {
		_ = uc.Close()
		return nil, fmt.Errorf("%s: первым сообщением пришёл не hello", path)
	}
	c.hello = ev
	go c.read()
	return c, nil
}

// Hello — то, чем представился процесс.
func (c *Client) Hello() awgmproto.Event { return c.hello }

// Events — push-сообщения процесса.
func (c *Client) Events() <-chan awgmproto.Event { return c.events }

// Done закрывается, когда соединение умерло.
func (c *Client) Done() <-chan struct{} { return c.done }

// Evicted сообщает, объявил ли процесс это соединение вытесненным.
func (c *Client) Evicted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evicted
}

// Err — причина смерти соединения.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) Close() error { return c.uc.Close() }

// State спрашивает состояние. Без побочных эффектов.
func (c *Client) State(ctx context.Context) (awgmproto.State, error) {
	resp, err := c.do(ctx, awgmproto.Request{Cmd: awgmproto.CmdState}, -1)
	if err != nil {
		return awgmproto.State{}, err
	}
	if resp.State == nil {
		return awgmproto.State{}, fmt.Errorf("ответ на state без состояния")
	}
	return *resp.State, nil
}

// AttachTun передаёт дескриптор. Файл остаётся у вызывающего: закрыть свою
// копию — его обязанность, дескриптор у процесса от этого не пострадает.
func (c *Client) AttachTun(ctx context.Context, iface string, f *os.File) error {
	_, err := c.do(ctx, awgmproto.Request{Cmd: awgmproto.CmdAttachTun, Iface: iface}, int(f.Fd()))
	return err
}

func (c *Client) DetachTun(ctx context.Context) error {
	_, err := c.do(ctx, awgmproto.Request{Cmd: awgmproto.CmdDetachTun}, -1)
	return err
}

// do шлёт запрос и ждёт ответа с тем же id. Ответ с чужим id игнорируется
// читателем, поэтому здесь достаточно своего канала.
func (c *Client) do(ctx context.Context, req awgmproto.Request, fd int) (awgmproto.Response, error) {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return awgmproto.Response{}, err
	}
	c.nextID++
	req.ID = c.nextID
	req.V = awgmproto.Version
	ch := make(chan awgmproto.Response, 1)
	c.waiters[req.ID] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.waiters, req.ID)
		c.mu.Unlock()
	}()

	if err := c.send(req, fd); err != nil {
		return awgmproto.Response{}, err
	}

	select {
	case <-ctx.Done():
		return awgmproto.Response{}, ctx.Err()
	case <-c.done:
		if err := c.Err(); err != nil {
			return awgmproto.Response{}, err
		}
		return awgmproto.Response{}, ErrClosed
	case resp := <-ch:
		if !resp.OK {
			return resp, &awgmproto.Error{Code: resp.Code, Msg: resp.Error}
		}
		return resp, nil
	}
}

// send пишет одну строку за один sendmsg: батчить нельзя, иначе привязка
// дескриптора к строке размывается.
func (c *Client) send(req awgmproto.Request, fd int) error {
	line, err := awgmproto.EncodeLine(req)
	if err != nil {
		return err
	}
	var oob []byte
	if fd >= 0 {
		oob = unix.UnixRights(fd)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _, err = c.uc.WriteMsgUnix(line, oob, nil)
	return err
}

// read разбирает входящий поток до смерти соединения.
func (c *Client) read() {
	defer close(c.done)
	for {
		line, fd, err := c.fc.ReadFrame()
		closeFD(fd) // процесс дескрипторов не шлёт; пришедший — закрыть
		if err != nil {
			c.fail(err)
			return
		}
		kind, msg, err := awgmproto.DecodeLine(line)
		if err != nil {
			// Несовпадение мажора и мусор в кадре — отказ, а не деградация.
			c.fail(err)
			_ = c.uc.Close()
			return
		}
		switch kind {
		case awgmproto.KindResponse:
			resp := msg.(awgmproto.Response)
			c.mu.Lock()
			ch := c.waiters[resp.ID]
			c.mu.Unlock()
			if ch != nil {
				ch <- resp // канал буферизован, ждать некому
			}
		case awgmproto.KindEvent:
			ev := msg.(awgmproto.Event)
			if ev.Event == awgmproto.EventEvicted {
				c.mu.Lock()
				c.evicted = true
				c.mu.Unlock()
			}
			select {
			case c.events <- ev:
			default: // очередь полна: будильник и так предстоит
			}
		default:
			// Запрос от процесса протоколом не предусмотрен.
		}
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
}

func closeFD(fd int) {
	if fd >= 0 {
		_ = unix.Close(fd)
	}
}
