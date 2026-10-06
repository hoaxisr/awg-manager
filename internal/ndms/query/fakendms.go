package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

// FakeNDMS — стенд в памяти: множество интерфейсов + счётчики того, что на
// живом роутере превращается в E журнала ndm и в фантомные интерфейсы.
// Реализует Getter и command.Poster. Для тестов этого и других пакетов (тот
// же приём, что FakeGetter). Поля-счётчики читать после вызовов, не во время.
// Хуки существования тест доставляет диспетчеру сам: DrainHooks — всю
// очередь по порядку, HooksFor(id) — только хуки данного id, остальные
// остаются в очереди в своём порядке (рецепт устаревшего хука, П8: см.
// DrainHooks).
type FakeNDMS struct {
	mu          sync.Mutex
	ifaces      map[string]ndms.Interface
	listCalls   int
	listErr     error
	hooks       []FakeHook
	expect      map[string]bool
	inList      func()
	netdev      map[string]bool            // kernel-устройства, видимые «прошивке» (SetNetdev)
	amneziawg   map[string]bool            // из них — amneziawg нашего kernel-бэкенда (SetAmneziaWG)
	detail      map[string]json.RawMessage // поля записи сверх toWire (SetDetail)
	rc          map[string]json.RawMessage // объект rc интерфейса (SetRC)
	rcListCalls int
	rcListErr   error
	// hideNext/hidden — сколько чтений списка ещё не показывают созданное (HideCreated).
	hideNext int
	hidden   map[string]int

	// D-N3 (dn3-round2-evidence.md §3, R60 в обе стороны: FULL2 6/8 против
	// NOSAVE 0/8; save вне окна 0/12): проход сохранения, открытый при нашем
	// `no interface`, или стартующий ≤4 с после снятия, ловит снимаемую
	// запись — E. Сохранение «летит» от POST до SaveSettled, только если тест
	// включил HoldSaves; иначе запись мгновенная. Часы — SetClock.
	holdSaves     bool
	saving        bool
	lastRemovedAt time.Time
	now           func() time.Time

	E        int      // точечное чтение отсутствующего; ссылка на отсутствующий из ip route/nat/static/name-server/policy/hotspot/dns-proxy
	Phantoms int      // `interface X …` по отсутствующему X, не объявленному ExpectCreate: X создан
	C        int      // строки C прошивки от наших действий: создание (F569) и снос (F598, только amneziawg) OpkgTunN при живом opkgtunN; исчезновение opkgtunN под up-записью (0ba1, SetNetdev)
	Posts    []string // все payload в JSON, по порядку (пакет — одной строкой)
	// Created — намеренно созданные: `interface X …` по объявленному через
	// ExpectCreate X и импорт. Намерение объявляет тест, а не форма payload:
	// по форме создание от правки не отличить. Инвариант сценариев — Phantoms == 0.
	Created []string
}

// FakeHook — хук NDMS, который тест доставляет в диспетчер сам (с задержкой,
// в другом порядке или теряет). events.Event сюда не импортировать: events → query.
// Layer/Level — только у "iflayerchanged".
type FakeHook struct{ Type, ID, Layer, Level string } // "ifcreated" | "ifdestroyed" | "iflayerchanged"

func NewFakeNDMS(ifaces ...ndms.Interface) *FakeNDMS {
	f := &FakeNDMS{ifaces: make(map[string]ndms.Interface, len(ifaces)), now: time.Now}
	for _, i := range ifaces {
		f.ifaces[i.ID] = i
	}
	return f
}

// Add — внешнее создание/правка; ifcreated в очередь, если имени не было.
func (f *FakeNDMS) Add(iface ndms.Interface) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.ifaces[iface.ID]; !ok {
		f.created(iface.ID)
	}
	f.ifaces[iface.ID] = iface
}

// SetClock — часы правила D-N3 («сохранение ≤4 с после снятия»).
func (f *FakeNDMS) SetClock(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}

// HoldSaves — сохранение длится от POST до SaveSettled (без вызова запись
// мгновенная): `no interface` в этом окне — E (D-N3).
func (f *FakeNDMS) HoldSaves() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holdSaves = true
}

// SaveSettled — запись во флеш окончена (строка `configuration saved`).
func (f *FakeNDMS) SaveSettled() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saving = false
}

// ExpectCreate объявляет имена, которые тест создаёт намеренно: первая
// команда `interface X …` по отсутствующему X создаёт его без фантома.
func (f *FakeNDMS) ExpectCreate(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.expect == nil {
		f.expect = make(map[string]bool)
	}
	for _, n := range names {
		f.expect[n] = true
	}
}

// Remove — внешнее `no interface`; ifdestroyed в очередь, если имя было.
func (f *FakeNDMS) Remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remove(name)
}

// SetNetdev — есть ли kernel-устройство name. Remove запись снимает, а
// устройство нет (прошивка чужое amneziawg снять не может): тест ведёт его сам.
//
// Устройство opkgtunN исчезло под записью OpkgTunN в State "up" — C (NDMS
// асинхронно пишет `C 0xcffd0ba1, no such device`: стенд K-0ba1 3/3, A7′
// F559; подмена под `running` без `down` — C3b 3/10, Task 59). Под записью
// в "down" — 0 C (C3a 20/20, T1 ц.1, уборка). Подмену бэкенд показывает
// оракулу как снятие и появление — так она и идёт в ядре (del, затем add).
func (f *FakeNDMS) SetNetdev(name string, present bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.netdev == nil {
		f.netdev = make(map[string]bool)
	}
	if !present && f.netdev[name] {
		for id, iface := range f.ifaces {
			if kernel, ok := ndms.KernelName(id); ok && kernel == name && strings.HasPrefix(id, "OpkgTun") && iface.State == "up" {
				f.C++
			}
		}
	}
	f.netdev[name] = present
}

// SetAmneziaWG — устройство name (если есть) — amneziawg нашего
// kernel-бэкенда, а не plain tun. Только у такого снос записи OpkgTunN при
// живом устройстве даёт C (ifaceCmd, R60): это доказано стендом 01.10
// (A7/B); при plain tun без держателя снос чистый и tun NDMS снимает сам
// (Task 59, П4).
func (f *FakeNDMS) SetAmneziaWG(name string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.amneziawg == nil {
		f.amneziawg = make(map[string]bool)
	}
	f.amneziawg[name] = on
}

// SetDetail — поля записи интерфейса сверх toWire (`wireguard`, `summary`…):
// список и точечный ответ отдают слияние toWire ∪ extra (ключи extra
// побеждают) — те же байты в обеих формах, как у NDMS.
func (f *FakeNDMS) SetDetail(name string, extra json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detail == nil {
		f.detail = make(map[string]json.RawMessage)
	}
	f.detail[name] = extra
}

// SetRC — объект rc интерфейса name (форма /show/rc/interface/<name>).
func (f *FakeNDMS) SetRC(name string, rc json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rc == nil {
		f.rc = make(map[string]json.RawMessage)
	}
	f.rc[name] = rc
}

// RCListCalls — сколько раз читали полное дерево rc (/show/rc/interface/),
// включая отказанные по FailRCList.
func (f *FakeNDMS) RCListCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rcListCalls
}

// FailRCList — следующие чтения полного дерева rc отвечают err; nil снимает.
func (f *FakeNDMS) FailRCList(err error) {
	f.mu.Lock()
	f.rcListErr = err
	f.mu.Unlock()
}

// HideCreated — записи, созданные после вызова (импорт, `interface X`), не
// видны в полном списке, хотя NDMS их уже знает (снос проходит): n > 0 — n
// чтений списка. 0 снимает для следующих. n < 0 — паника: запись в списке не
// зависит от доставки хуков (F595).
func (f *FakeNDMS) HideCreated(n int) {
	if n < 0 {
		panic("режим удалён: запись в списке не зависит от доставки хуков (F595)")
	}
	f.mu.Lock()
	f.hideNext = n
	f.mu.Unlock()
}

// ShowHidden — снимает сокрытие у уже скрытых записей (доставка хуков тут ни
// при чём: список показывает запись, когда оракул так решил).
func (f *FakeNDMS) ShowHidden() {
	f.mu.Lock()
	f.hidden = nil
	f.mu.Unlock()
}

func (f *FakeNDMS) Has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.ifaces[name]
	return ok
}

// DrainHooks — вся очередь хуков по порядку; очередь опустошается целиком.
// Рецепт устаревшего хука (П8): если между Remove(X) и повторным Add(X) не
// дренировать очередь, HooksFor("X") отдаёт старый ifdestroyed первым, а
// затем свежие хуки пересоздания — так моделируется опоздавший хук чужого,
// уже пересозданного интерфейса.
func (f *FakeNDMS) DrainHooks() []FakeHook {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hooks
	f.hooks = nil
	return h
}

// HooksFor — забирает из очереди ТОЛЬКО хуки с ID == name, в порядке
// очереди; остальные хуки остаются в очереди в своём порядке.
func (f *FakeNDMS) HooksFor(name string) []FakeHook {
	f.mu.Lock()
	defer f.mu.Unlock()
	var take, keep []FakeHook
	for _, e := range f.hooks {
		if e.ID == name {
			take = append(take, e)
		} else {
			keep = append(keep, e)
		}
	}
	f.hooks = keep
	return take
}

// ListCalls — сколько раз читали полный список (/show/interface/ или
// {"show":{"interface":{}}}), включая отказанные по FailList.
func (f *FakeNDMS) ListCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}

// FailList — следующие чтения списка отвечают err; nil снимает.
func (f *FakeNDMS) FailList(err error) {
	f.mu.Lock()
	f.listErr = err
	f.mu.Unlock()
}

// InList — fn вызывается в каждом GET /show/interface/ после снимка списка и
// до ответа, вне замка фейка: так тест доставляет хуки, пока список в полёте.
// nil снимает.
func (f *FakeNDMS) InList(fn func()) {
	f.mu.Lock()
	f.inList = fn
	f.mu.Unlock()
}

func (f *FakeNDMS) Get(ctx context.Context, path string, dst any) error {
	raw, err := f.GetRaw(ctx, path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// GetRaw: "/show/interface/" — список; "/show/rc/interface/" — полное
// дерево rc (карта id → rc по присутствующим, E не растёт); "/show/interface/X"
// и "/show/rc/interface/X…" — точечные чтения: отсутствующий X → E++ и 404.
func (f *FakeNDMS) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if path == "/show/rc/interface/" {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.rcListCalls++
		if f.rcListErr != nil {
			return nil, f.rcListErr
		}
		out := make(map[string]json.RawMessage, len(f.ifaces))
		for id := range f.ifaces {
			out[id] = f.rcOf(id)
		}
		return json.Marshal(out)
	}
	if path == "/show/interface/" {
		f.mu.Lock()
		raw, err := f.list()
		fn := f.inList
		f.mu.Unlock()
		if fn != nil {
			fn()
		}
		return raw, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rest, ok := strings.CutPrefix(path, "/show/rc/interface/")
	if !ok {
		rest, ok = strings.CutPrefix(path, "/show/interface/")
	}
	if !ok {
		return nil, errors.New("FakeNDMS: нет модели для пути " + path)
	}
	name, suffix, _ := strings.Cut(rest, "/")
	iface, present := f.ifaces[name]
	if !present {
		f.E++
		return nil, &transport.HTTPError{Method: "GET", Path: path, Status: 404}
	}
	if strings.HasPrefix(path, "/show/rc/") {
		if suffix == "wireguard/asc" {
			var rc struct {
				Wireguard struct {
					ASC json.RawMessage `json:"asc"`
				} `json:"wireguard"`
			}
			if json.Unmarshal(f.rcOf(name), &rc) == nil && rc.Wireguard.ASC != nil {
				return rc.Wireguard.ASC, nil
			}
		}
		return f.rcOf(name), nil
	}
	return f.wire(iface)
}

// ndmsUnableToFindCode — код NDMS-конверта "unable to find" (стенд KN-1810,
// 5.02.A.11): ответ на точечное чтение отсутствующей записи.
const ndmsUnableToFindCode = "6553619"

// rcOf — rc интерфейса name; без SetRC — пустой объект.
func (f *FakeNDMS) rcOf(name string) json.RawMessage {
	if rc, ok := f.rc[name]; ok {
		return rc
	}
	return json.RawMessage(`{}`)
}

// wire — запись интерфейса как у NDMS: toWire, поверх — поля SetDetail.
func (f *FakeNDMS) wire(iface ndms.Interface) (json.RawMessage, error) {
	b, err := json.Marshal(toWire(iface))
	if err != nil {
		return nil, err
	}
	extra, ok := f.detail[iface.ID]
	if !ok {
		return b, nil
	}
	var fields, over map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(extra, &over); err != nil {
		return nil, err
	}
	for k, v := range over {
		fields[k] = v
	}
	return json.Marshal(fields)
}

func (f *FakeNDMS) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Posts = append(f.Posts, string(b))
	arr, ok := v.([]any)
	if !ok {
		return f.post(v)
	}
	// Пакет, как у NDMS: каждый элемент отвечается отдельно, ответ — массив.
	out := make([]json.RawMessage, len(arr))
	for i, item := range arr {
		r, err := f.post(item)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return json.Marshal(out)
}

func (f *FakeNDMS) post(v any) (json.RawMessage, error) {
	m, _ := v.(map[string]any)
	if show, ok := sub(m, "show", "interface"); ok {
		if sn, ok := sub(show, "system-name"); ok {
			name, _ := sn["name"].(string)
			return f.systemName(name)
		}
		if name, ok := show["name"].(string); ok {
			return f.showOne(name)
		}
		raw, err := f.list()
		if err != nil {
			return nil, err
		}
		return json.RawMessage(`{"show":{"interface":` + string(raw) + `}}`), nil
	}
	if wg, ok := sub(m, "interface", "wireguard"); ok && wg["import"] != nil {
		return f.importWG()
	}
	if iface, ok := sub(m, "interface"); ok {
		if name, ok := iface["name"].(string); ok { // payloads-форма
			body := make(map[string]any, len(iface))
			for k, val := range iface {
				if k != "name" {
					body[k] = val
				}
			}
			return f.ifaceCmd(name, body)
		}
		// command-форма {"interface":{X:{…}}}
		names := make([]string, 0, len(iface))
		for name := range iface {
			names = append(names, name)
		}
		sort.Strings(names)
		resp := json.RawMessage(`{}`)
		for _, name := range names {
			body, _ := iface[name].(map[string]any)
			r, err := f.ifaceCmd(name, body)
			if err != nil {
				return nil, err
			}
			if string(r) != "{}" {
				resp = r
			}
		}
		return resp, nil
	}
	if line, ok := m["parse"].(string); ok {
		return f.parseCmd(line)
	}
	if _, ok := sub(m, "system", "configuration", "save"); ok {
		if !f.lastRemovedAt.IsZero() && f.now().Sub(f.lastRemovedAt) < 4*time.Second {
			f.E++ // проход стартует ≤4 с после снятия (D-N3)
		}
		f.saving = f.holdSaves
		return json.RawMessage(`{}`), nil
	}
	var names []string
	for _, section := range []string{"ip", "ipv6", "dns-proxy"} {
		if s, ok := m[section]; ok {
			names = collectIfaceRefs(s, names)
		}
	}
	var errs []string
	for _, name := range names {
		if _, ok := f.ifaces[name]; !ok {
			f.E++
			errs = append(errs, "no such interface: "+name+".")
		}
	}
	if len(errs) > 0 {
		return statusError(errs...), nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *FakeNDMS) list() ([]byte, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make(map[string]json.RawMessage, len(f.ifaces))
	for id, iface := range f.ifaces {
		if n := f.hidden[id]; n > 0 {
			f.hidden[id]--
			continue
		}
		w, err := f.wire(iface)
		if err != nil {
			return nil, err
		}
		out[id] = w
	}
	return json.Marshal(out)
}

func (f *FakeNDMS) showOne(name string) (json.RawMessage, error) {
	iface, ok := f.ifaces[name]
	if !ok {
		f.E++
		return json.RawMessage(`{"show":{"interface":{"status":[{"status":"error","code":"` + ndmsUnableToFindCode + `","message":"unable to find"}]}}}`), nil
	}
	w, err := f.wire(iface)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(`{"show":{"interface":` + string(w) + `}}`), nil
}

func (f *FakeNDMS) systemName(name string) (json.RawMessage, error) {
	iface, ok := f.ifaces[name]
	if !ok {
		f.E++
		return json.RawMessage(`{"show":{"interface":{"system-name":{"status":[{"status":"error","code":"` + ndmsUnableToFindCode + `","message":"unable to find"}]}}}}`), nil
	}
	sn, err := json.Marshal(iface.SystemName)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(`{"show":{"interface":{"system-name":` + string(sn) + `}}}`), nil
}

// importWG создаёт WireguardN с наименьшим свободным N.
func (f *FakeNDMS) importWG() (json.RawMessage, error) {
	name := ""
	for n := 0; ; n++ {
		name = "Wireguard" + strconv.Itoa(n)
		if _, ok := f.ifaces[name]; !ok {
			break
		}
	}
	f.ifaces[name] = ndms.Interface{ID: name, Type: "Wireguard", State: "down"}
	f.Created = append(f.Created, name)
	f.created(name)
	return json.RawMessage(`{"interface":{"wireguard":{"import":{"created":"` + name + `","intersects":"","status":[]}}}}`), nil
}

// ifaceCmd — `interface name …`. По отсутствующему: `no` — отказ и E (стенд
// 5.01.C.6: `Network::Interface::Repository: unable to find interface "X"` в
// журнале ndm, хотя ответ терпим); иначе NDMS создаёт name — намеренно, если
// объявлен ExpectCreate, иначе фантом.
func (f *FakeNDMS) ifaceCmd(name string, body map[string]any) (json.RawMessage, error) {
	resp := json.RawMessage(`{}`)
	iface, ok := f.ifaces[name]
	if no, _ := body["no"].(bool); no {
		if !ok {
			f.E++
			return statusError(fmt.Sprintf("unable to find interface %q", name)), nil
		}
		// Снос записи OpkgTunN при живом amneziawg opkgtunN: запись
		// снимается, ответ успешный, но прошивка пишет `C Tun: system failed
		// [0xcffd003b]` (стенд 01.10, A7/K7/B). Пара имён та же, что у
		// проверки создания ниже; только amneziawg — R60, см. SetAmneziaWG.
		// При живом plain tun (не amneziawg) — 0 C, и NDMS снимает tun сам,
		// даже созданный не им: persistent tun без держателя снят 30/30
		// (стенд Task 59, П4 C3a/C3b). С держателем (sing-box, X3) — C busy и
		// tun остаётся; держателей оракул не знает и это не моделирует.
		if f.saving {
			f.E++ // снятие при сохранении в полёте (D-N3)
		}
		f.lastRemovedAt = f.now()
		if kernel, isKernel := ndms.KernelName(name); isKernel && strings.HasPrefix(name, "OpkgTun") && f.netdev[kernel] {
			if f.amneziawg[kernel] {
				f.C++
			} else {
				f.netdev[kernel] = false
			}
		}
		f.remove(name)
		return json.RawMessage(`{}`), nil
	}
	if !ok {
		// Создание OpkgTunN при живом opkgtunN: NDMS отказывает (C), запись
		// не появляется, а каждый прочий ключ того же тела адресован
		// несозданной записи — E (стенд 5.01.C.6, F569).
		if kernel, isKernel := ndms.KernelName(name); isKernel && strings.HasPrefix(name, "OpkgTun") && f.netdev[kernel] {
			f.C++
			msgs := []string{"Network::Interface::Tun: system failed [0xcffd00a9]"}
			for k := range body {
				f.E++
				msgs = append(msgs, "Base: unable to find "+name+" ("+k+")")
			}
			return statusError(msgs...), nil
		}
		if f.expect[name] {
			delete(f.expect, name)
			f.Created = append(f.Created, name)
		} else {
			f.Phantoms++
		}
		iface = ndms.Interface{ID: name, Type: typeByPrefix(name), State: "down"}
		f.created(name)
		resp = createdMessage(name)
	}
	if up, ok := body["up"].(bool); ok {
		iface.State = "down"
		if up {
			iface.State = "up"
		}
	}
	if d, ok := body["description"].(string); ok {
		iface.Description = d
	}
	f.ifaces[name] = iface
	return resp, nil
}

// createdMessage — ответ NDMS на создание записи (стенд 5.01: message
// `"X" interface created.`, code 6553601); по существующей его нет.
func createdMessage(name string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"status": []map[string]any{{
		"status": "message", "code": "6553601", "ident": "Network::Interface::Repository",
		"message": fmt.Sprintf("%q interface created.", name),
	}}})
	return b
}

// parseCmd — `{"parse":"…"}`: разбираются только `interface X …` и
// `no interface X …`; прочее (access-list и т.п.) — `{}`. Снос — только
// ровно `no interface X`; `no interface X <настройка>` снимает настройку у X
// (отсутствующий X она создаёт, как `interface X …`).
func (f *FakeNDMS) parseCmd(line string) (json.RawMessage, error) {
	w := strings.Fields(line)
	switch {
	case len(w) == 3 && w[0] == "no" && w[1] == "interface":
		return f.ifaceCmd(w[2], map[string]any{"no": true})
	case len(w) > 3 && w[0] == "no" && w[1] == "interface":
		return f.ifaceCmd(w[2], map[string]any{})
	case len(w) >= 2 && w[0] == "interface":
		body := map[string]any{}
		if len(w) == 3 && (w[2] == "up" || w[2] == "down") {
			body["up"] = w[2] == "up"
		}
		return f.ifaceCmd(w[1], body)
	}
	return json.RawMessage(`{}`), nil
}

// created ставит в очередь хуки создания в порядке живого NDMS (стенд
// 5.01.C.6): два iflayerchanged ctrl, затем ifcreated ~1 с спустя. Level
// стенд не записал — пустой.
func (f *FakeNDMS) created(name string) {
	if f.hideNext != 0 {
		if f.hidden == nil {
			f.hidden = make(map[string]int)
		}
		f.hidden[name] = f.hideNext
	}
	f.hooks = append(f.hooks,
		FakeHook{Type: "iflayerchanged", ID: name, Layer: "ctrl"},
		FakeHook{Type: "iflayerchanged", ID: name, Layer: "ctrl"},
		FakeHook{Type: "ifcreated", ID: name})
}

func (f *FakeNDMS) remove(name string) {
	if _, ok := f.ifaces[name]; !ok {
		return
	}
	delete(f.ifaces, name)
	delete(f.hidden, name)
	f.hooks = append(f.hooks, FakeHook{Type: "ifdestroyed", ID: name})
}

func typeByPrefix(name string) string {
	for _, p := range []string{"Wireguard", "OpkgTun", "Proxy"} {
		if strings.HasPrefix(name, p) {
			return p
		}
	}
	return ""
}

// collectIfaceRefs собирает строковые значения ключей interface/to-interface
// на любой глубине (объекты и массивы).
func collectIfaceRefs(v any, out []string) []string {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok && (k == "interface" || k == "to-interface") {
				out = append(out, s)
				continue
			}
			out = collectIfaceRefs(val, out)
		}
	case []any:
		for _, val := range t {
			out = collectIfaceRefs(val, out)
		}
	}
	return out
}

// sub спускается по вложенным объектам m[k0][k1]…
func sub(m map[string]any, keys ...string) (map[string]any, bool) {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, cur != nil
}

// statusError — вложенный конверт отказа, который ловит command.ndmsStatusErrors.
func statusError(msgs ...string) json.RawMessage {
	st := make([]map[string]string, len(msgs))
	for i, m := range msgs {
		st[i] = map[string]string{"status": "error", "code": "1", "message": m}
	}
	b, _ := json.Marshal(map[string]any{"status": st})
	return b
}

func toWire(i ndms.Interface) ifaceWire {
	w := ifaceWire{
		ID:            i.ID,
		InterfaceName: i.SystemName,
		Type:          i.Type,
		Description:   i.Description,
		State:         i.State,
		Link:          i.Link,
		Connected:     i.Connected,
		SecurityLevel: i.SecurityLevel,
		Address:       i.Address,
		Mask:          i.Mask,
		MTU:           i.MTU,
		Uptime:        i.Uptime,
		ConfLayer:     i.ConfLayer,
		Priority:      i.Priority,
	}
	w.Summary.Layer.IPv4 = i.IPv4
	return w
}
