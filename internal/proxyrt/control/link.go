package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// Сроки из §7 спеки протокола.
const (
	defaultRetryEvery      = 200 * time.Millisecond
	defaultConnectDeadline = 20 * time.Second
	defaultCallTimeout     = 5 * time.Second
)

// ErrEvicted — инстансом владеет другой менеджер.
//
// Получив его, реконсиляция завершается фазой failed с этой причиной: это
// делает видимой настоящую проблему (два демона) вместо бесконечной пляски
// взаимных вытеснений.
var ErrEvicted = errors.New("инстансом владеет другой менеджер")

// ErrNoSocket — связь с управляющим сокетом не установилась за отведённое
// время.
//
// Шапка намеренно не говорит «процесс не открыл сокет»: из трёх случаев,
// которые сюда приходят, это лишь один. Другие два — сокет есть, но соединение
// не принимают, и соединение приняли, но hello не прислали. Причина последней
// неудачи едет в тексте отказа отдельно.
var ErrNoSocket = errors.New("связь с управляющим сокетом не установлена")

// ErrForeignProcess — на сокете инстанса отвечает не тот процесс.
var ErrForeignProcess = errors.New("на управляющем сокете чужой процесс")

// LinkOpts — что нужно долгоживущей связи.
type LinkOpts struct {
	Path string
	// Impl и Role — чем процесс обязан представиться в hello. Пустые означают
	// «не сверять». Это единственная настоящая проверка «мой ли это процесс»:
	// оба значения зашиты в бинарь. Instance сверяется тоже, но он эхо —
	// процесс выводит его из имени сокета, которое назвал сам менеджер.
	Impl     string
	Role     string
	Instance string
	// Binary — путь бинаря для сверки /proc/<pid>. Пустой означает «сверять
	// нечем», и тогда живость pid не проверяется.
	Binary string
	// Post ставит будильник воркеру инстанса — это Worker.Post. Будильник
	// несёт ВИД события и ничего больше: идентификатор инстанса в него не
	// входит, потому что воркер и есть инстанс.
	Post func(proxyrt.EventKind) bool
	// Log — куда уходит пояснение к будильнику («push address», «соединение
	// разорвано»). В событие оно не кладётся: движок его не читает, а в
	// журнале без него не разобрать, почему инстанс проснулся. nil — молча.
	Log func(string)
	// Alive — сверка живости pid с бинарём. Обычно childproc.MatchesBinary.
	// Сигналом 0 проверять нельзя: pid переиспользуются.
	Alive func(pid int, binary string) bool
	// Dial подменяется в тестах.
	Dial func(ctx context.Context, path string) (*Client, error)
	// Нули означают значения по умолчанию из §7.
	RetryEvery      time.Duration
	ConnectDeadline time.Duration
	CallTimeout     time.Duration
}

// Snapshot — последнее, что процесс о себе рассказал.
type Snapshot struct {
	State awgmproto.State
	At    time.Time
}

// Link — долгоживущая связь с одним инстансом: одна на инстанс, создаётся
// парой с воркером и закрывается при удалении инстанса и при остановке демона.
//
// Закрывает её instance.Instance.Stop — связь принадлежит инстансу, и других
// владельцев у неё нет. Обещание «закрывается при удалении инстанса» держится
// кодом там, а не договорённостью проводки: без него удаление инстанса
// оставляло бы горутину watch и открытый сокет.
//
// Соединение поднимается лениво, первым же обращением: так наблюдение ресурса
// process само лечит «сокета ещё нет», не требуя отдельного будильника.
//
// Контракт вызывающего: контексты, с которыми зовут State, AttachTun и
// DetachTun, обязаны иметь срок НЕ МЕНЬШЕ CallTimeout либо не иметь срока
// вовсе. Почему — в докстроке State.
type Link struct {
	opts LinkOpts

	mu   sync.Mutex
	cur  *Client
	last *Snapshot
	// evicted — защёлка. Снимается ТОЛЬКО извне, в обёртке onState: это
	// единственное место, где граница прогона видна снаружи движка. Запрет
	// не переживает реконсиляцию — иначе случайно забредший второй менеджер
	// оставил бы инстанс бесхозным навсегда при живом процессе.
	evicted bool
	closed  bool
	// lastPID — pid из последнего hello: единственный источник для проверки
	// живости усыновлённого процесса, pid-файл после перезапуска демона
	// доверия не заслуживает. Обнуляется, когда воплощение с этим номером
	// признано мёртвым (lost): дальше номер не опознаёт никого.
	lastPID int

	wg sync.WaitGroup
}

func NewLink(opts LinkOpts) *Link {
	if opts.RetryEvery <= 0 {
		opts.RetryEvery = defaultRetryEvery
	}
	if opts.ConnectDeadline <= 0 {
		opts.ConnectDeadline = defaultConnectDeadline
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = defaultCallTimeout
	}
	if opts.Dial == nil {
		opts.Dial = Dial
	}
	return &Link{opts: opts}
}

// Evicted сообщает, стоит ли защёлка.
func (l *Link) Evicted() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.evicted
}

// ClearEvicted снимает защёлку. Зовётся ровно из одного места — обёртки
// колбэка onState воркера, на границе прогона реконсиляции.
func (l *Link) ClearEvicted() {
	l.mu.Lock()
	l.evicted = false
	l.mu.Unlock()
}

// Snapshot — последний снимок состояния и его возраст. false, если процесс
// ещё ничего не рассказывал.
func (l *Link) Snapshot() (Snapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		return Snapshot{}, false
	}
	return *l.last, true
}

// Close рвёт связь навсегда.
func (l *Link) Close() {
	l.mu.Lock()
	l.closed = true
	c := l.cur
	l.cur = nil
	l.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	l.wg.Wait()
}

// State — наблюдение ресурса process: подключиться, если ещё не подключены, и
// спросить состояние.
//
// При таймауте ответа делается ровно один повторный запрос: attach мог пройти,
// и ретраить его вслепую нельзя, а вот переспросить состояние — можно.
//
// # Требование к вызывающему: срок ctx не меньше CallTimeout
//
// Наблюдение с дедлайном КОРОЧЕ CallTimeout ломает распознавание зависшего
// процесса: наш собственный срок не наступит никогда, оба таймаута окажутся
// чужими, и мёртвое соединение не будет сброшено НИ РАЗУ — сколько бы прогонов
// ни случилось. Внешне это выглядит как исправная связь: cur не пуст,
// переподключаться некуда, EventProcessDied не наступит. Требование
// невозможно проверить изнутри (короткий срок законен сам по себе), поэтому
// оно записано здесь и в §7 спеки, а не защищено кодом.
func (l *Link) State(ctx context.Context) (awgmproto.State, error) {
	c, err := l.connect(ctx)
	if err != nil {
		return awgmproto.State{}, err
	}
	st, err := l.callState(ctx, c)
	// ctx.Err() == nil — срок вышел НАШ, а не вызывающего. Переспрашивать при
	// истёкшем сроке вызывающего незачем: второй запрос ушёл бы на провод и
	// заведомо остался бы без ответа.
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		st, err = l.callState(ctx, c)
		// ctx.Err() == nil — срок вышел НАШ (CallTimeout), а не вызывающего.
		// Без этой оговорки истёкший ctx наблюдения неотличим от двойного
		// таймаута, и drop снимает с поста исправное соединение: связь
		// пересоздавалась бы на каждом нетерпеливом прогоне.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			// Второй таймаут подряд — соединение мёртвое (§5.1). Без этого
			// зависший процесс с открытым сокетом навсегда оставался бы
			// «подключённым»: cur жив, переподключаться некуда, died не
			// наступит никогда.
			l.drop(c)
		}
	}
	if err != nil {
		return awgmproto.State{}, err
	}
	now := time.Now()
	l.mu.Lock()
	l.last = &Snapshot{State: st, At: now}
	l.mu.Unlock()
	return st, nil
}

// drop объявляет соединение мёртвым: снимает его с поста и закрывает.
//
// Закрытие будит watch, и дальше всё идёт по строке «разорвано без evicted»
// из §7 — включая различение «умер» и «связь потеряна» по живости pid.
func (l *Link) drop(c *Client) {
	l.mu.Lock()
	if l.cur == c {
		l.cur = nil
	}
	l.mu.Unlock()
	_ = c.Close()
}

func (l *Link) callState(ctx context.Context, c *Client) (awgmproto.State, error) {
	cctx, cancel := context.WithTimeout(ctx, l.opts.CallTimeout)
	defer cancel()
	return c.State(cctx)
}

// AttachTun — применение шага tun_handoff.
func (l *Link) AttachTun(ctx context.Context, iface string, f *os.File) error {
	c, err := l.connect(ctx)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, l.opts.CallTimeout)
	defer cancel()
	return c.AttachTun(cctx, iface, f)
}

func (l *Link) DetachTun(ctx context.Context) error {
	c, err := l.connect(ctx)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, l.opts.CallTimeout)
	defer cancel()
	return c.DetachTun(cctx)
}

// connect возвращает живое соединение, поднимая его при необходимости.
func (l *Link) connect(ctx context.Context) (*Client, error) {
	l.mu.Lock()
	closed, evicted, cur := l.closed, l.evicted, l.cur
	l.mu.Unlock()
	switch {
	case closed:
		return nil, ErrClosed
	case evicted:
		return nil, ErrEvicted
	case cur != nil:
		return cur, nil
	}

	deadline := time.Now().Add(l.opts.ConnectDeadline)
	for {
		c, err := l.dial(ctx)
		if err == nil {
			return l.adopt(c)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Несовпадение мажора — терминальный отказ (§4), и ретраить его нельзя
		// (§5.4): процесс говорит на другом языке и заговорит на нашем только
		// после подмены бинаря. Без своей ветки он уезжал бы наверх как
		// ErrNoSocket после всего окна ретраев, то есть как «ещё не поднялся»
		// там, где инстанс не поднимется никогда.
		if errors.Is(err, ErrProtocolVersion) {
			return nil, err
		}
		// «Ещё не создал» против «умер»: мёртвый pid — сразу мёртв, 20 секунд
		// не ждать.
		//
		// Причина последней неудачи едет в тексте отказа: без неё «процесс не
		// открыл управляющий сокет» врал бы про процесс, который сокет открыл,
		// соединение принял и замолчал.
		if pid := l.knownPID(); pid > 0 && !l.alive(pid) {
			return nil, fmt.Errorf("%w: pid %d мёртв (%v)", ErrNoSocket, pid, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: %v", ErrNoSocket, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(l.opts.RetryEvery):
		}
	}
}

// dial поднимает соединение под СВОИМ сроком.
//
// Срок обязан стоять всегда, а не только когда вызывающий передал ctx с
// дедлайном: Dial ждёт hello чтением из сокета, и процесс, который соединение
// принял и замолчал, подвесил бы наблюдение навсегда. Это худший вид отказа для
// движка — воркер инстанса встаёт целиком, и симптома «ошибка» нет, есть
// тишина, неотличимая от долгой работы: ни stuck, ни StopAwaiting её не видят.
// Гарантию даёт связь, а не её пользователь.
func (l *Link) dial(ctx context.Context) (*Client, error) {
	dctx, cancel := context.WithTimeout(ctx, l.opts.CallTimeout)
	defer cancel()
	return l.opts.Dial(dctx, l.opts.Path)
}

// checkHello сверяет представление процесса с ожидаемым.
//
// Проверять обязательно: Dial убеждается лишь в том, что первым сообщением
// пришёл hello, а «чей это hello» — вопрос отдельный. Расхождение постоянно
// (impl и role зашиты в бинарь), поэтому ретраев здесь нет: отказ уходит
// наверх сразу, с обоими значениями в тексте.
func (l *Link) checkHello(ev awgmproto.Event) error {
	switch {
	case l.opts.Impl != "" && ev.Impl != l.opts.Impl:
		return fmt.Errorf("%w: ожидали impl %q, представился %q", ErrForeignProcess, l.opts.Impl, ev.Impl)
	case l.opts.Role != "" && ev.Role != l.opts.Role:
		return fmt.Errorf("%w: ожидали роль %q, представился %q", ErrForeignProcess, l.opts.Role, ev.Role)
	case l.opts.Instance != "" && ev.Instance != l.opts.Instance:
		return fmt.Errorf("%w: ожидали инстанс %q, представился %q", ErrForeignProcess, l.opts.Instance, ev.Instance)
	}
	return nil
}

// adopt принимает поднятое соединение и заводит наблюдателя за ним.
func (l *Link) adopt(c *Client) (*Client, error) {
	if err := l.checkHello(c.Hello()); err != nil {
		_ = c.Close()
		return nil, err
	}
	l.mu.Lock()
	if l.closed || l.evicted {
		evicted := l.evicted
		l.mu.Unlock()
		_ = c.Close()
		if evicted {
			return nil, ErrEvicted
		}
		return nil, ErrClosed
	}
	if l.cur != nil {
		// Пока мы дозванивались, кто-то уже поставил соединение на пост.
		// Защиты от вытеснения здесь НЕТ и быть не может: процесс вытеснил
		// прежнее соединение ещё в момент accept нашего, и закрытие «лишнего»
		// закроет как раз то, которое процесс считает активным. Возвращаем
		// старое потому, что за ним уже есть наблюдатель, а не потому, что это
		// что-то чинит. Путь недостижим, пока Link зовут из одного воркера
		// последовательно (сегодня так и есть) — он оставлен страховкой от
		// второго Dial, а не механизмом.
		old := l.cur
		l.mu.Unlock()
		_ = c.Close()
		return old, nil
	}
	l.cur = c
	l.lastPID = c.Hello().PID
	l.mu.Unlock()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.watch(c)
	}()
	return c, nil
}

// watch превращает жизнь соединения в будильники воркеру.
//
// Особо выделен ровно один push — evicted; остальные равнозначны, и ждать
// какой-то конкретный НЕЛЬЗЯ. Картина по ролям несимметрична: freeturn шлёт
// error и заполняет last_error, но никогда его не очищает; wt-client и
// wdtt-server error не шлют вовсе и оставляют поле пустым (§6.1 — решение
// владельца, классификатор строк журнала не заводится). Отсюда: ни отсутствие
// события, ни пустое поле не означают «здоров», а непустое поле не означает
// «сломан сейчас». Решение принимается по последующему state, причина отказа
// ищется в журнале процесса.
func (l *Link) watch(c *Client) {
	for {
		select {
		case ev := <-c.Events():
			if ev.Event == awgmproto.EventEvicted {
				l.mu.Lock()
				// evicted значим только на соединении, которое менеджер считает
				// активным: брошенное соединение может донести evicted,
				// вызванный переподключением этого же менеджера, и принять его
				// за чужого владельца значит объявить отказ на пустом месте.
				if l.cur == c {
					l.evicted = true
					l.cur = nil
				}
				l.mu.Unlock()
				_ = c.Close()
				l.post(proxyrt.EventProcessState, "инстанс вытеснен другим менеджером")
				return
			}
			// Содержимое push идёт в журнал, решение принимается по
			// последующему state.
			l.post(proxyrt.EventProcessState, ev.Event)
		case <-c.Done():
			l.lost(c)
			return
		}
	}
}

// lost решает, что означает разрыв: смерть процесса или временную потерю связи.
func (l *Link) lost(c *Client) {
	l.mu.Lock()
	if l.cur == c {
		l.cur = nil
	}
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return
	}
	if pid := l.knownPID(); pid > 0 && !l.alive(pid) {
		// Воплощение кончилось: этим номером больше некого опознавать, и
		// держать его — значит судить по нему СЛЕДУЮЩЕЕ воплощение. Короткое
		// замыкание connect отказало бы новому процессу за микросекунды,
		// отобрав окно ретраев §7 (на mips сокет поднимается 4-5 с).
		//
		// Забывается только доказанно мёртвый pid: при разрыве связи с ЖИВЫМ
		// процессом (ветка ниже) номер остаётся в силе — воплощение то же, и
		// если процесс умрёт в окне переподключения, вердикт будет вынесен
		// сразу, а не через 20 секунд ретраев.
		l.forgetPID(pid)
		l.post(proxyrt.EventProcessDied, "управляющее соединение закрыто, pid мёртв")
		return
	}
	// Разорвано без evicted: следующее наблюдение переподключится (200 мс до
	// 20 с) и, если не выйдет, объявит отказ с причиной.
	l.post(proxyrt.EventProcessState, "управляющее соединение разорвано")
}

// forgetPID забывает pid умершего воплощения. Сверка с текущим значением —
// защита от гонки с новым воплощением: пока мы выносили вердикт, adopt мог
// записать pid из свежего hello, и затирать его нельзя.
func (l *Link) forgetPID(pid int) {
	l.mu.Lock()
	if l.lastPID == pid {
		l.lastPID = 0
	}
	l.mu.Unlock()
}

func (l *Link) knownPID() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastPID
}

func (l *Link) alive(pid int) bool {
	if l.opts.Alive == nil || l.opts.Binary == "" {
		return true // сверять нечем — не объявляем смерть на пустом месте
	}
	return l.opts.Alive(pid, l.opts.Binary)
}

func (l *Link) post(kind proxyrt.EventKind, detail string) {
	if l.opts.Log != nil {
		l.opts.Log(l.opts.Instance + ": " + detail)
	}
	if l.opts.Post == nil {
		return
	}
	l.opts.Post(kind)
}
