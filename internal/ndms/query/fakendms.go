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

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

// FakeNDMS — стенд в памяти: множество интерфейсов + счётчики того, что на
// живом роутере превращается в E журнала ndm и в фантомные интерфейсы.
// Реализует Getter и command.Poster. Для тестов этого и других пакетов (тот
// же приём, что FakeGetter). Поля-счётчики читать после вызовов, не во время.
type FakeNDMS struct {
	mu        sync.Mutex
	ifaces    map[string]ndms.Interface
	listCalls int
	listErr   error
	hooks     []FakeHook
	expect    map[string]bool

	E        int      // точечное чтение отсутствующего; ссылка на отсутствующий из ip route/nat/static/name-server/policy/hotspot/dns-proxy
	Phantoms int      // `interface X …` по отсутствующему X, не объявленному ExpectCreate: X создан
	Posts    []string // все payload в JSON, по порядку (пакет — одной строкой)
	// Created — намеренно созданные: `interface X …` по объявленному через
	// ExpectCreate X и импорт. Намерение объявляет тест, а не форма payload:
	// по форме создание от правки не отличить. Инвариант сценариев — Phantoms == 0.
	Created []string
}

// FakeHook — хук NDMS, который тест доставляет в диспетчер сам (с задержкой,
// в другом порядке или теряет). events.Event сюда не импортировать: events → query.
type FakeHook struct{ Type, ID string } // "ifcreated" | "ifdestroyed"

func NewFakeNDMS(ifaces ...ndms.Interface) *FakeNDMS {
	f := &FakeNDMS{ifaces: make(map[string]ndms.Interface, len(ifaces))}
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
		f.hooks = append(f.hooks, FakeHook{Type: "ifcreated", ID: iface.ID})
	}
	f.ifaces[iface.ID] = iface
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

func (f *FakeNDMS) Has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.ifaces[name]
	return ok
}

func (f *FakeNDMS) DrainHooks() []FakeHook {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hooks
	f.hooks = nil
	return h
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

func (f *FakeNDMS) Get(ctx context.Context, path string, dst any) error {
	raw, err := f.GetRaw(ctx, path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// GetRaw: "/show/interface/" — список; "/show/interface/X" и
// "/show/rc/interface/X…" — точечные чтения: отсутствующий X → E++ и 404.
func (f *FakeNDMS) GetRaw(ctx context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == "/show/interface/" {
		return f.list()
	}
	rest, ok := strings.CutPrefix(path, "/show/rc/interface/")
	if !ok {
		rest, ok = strings.CutPrefix(path, "/show/interface/")
	}
	if !ok {
		return nil, errors.New("FakeNDMS: нет модели для пути " + path)
	}
	name, _, _ := strings.Cut(rest, "/")
	iface, present := f.ifaces[name]
	if !present {
		f.E++
		return nil, &transport.HTTPError{Method: "GET", Path: path, Status: 404}
	}
	if strings.HasPrefix(path, "/show/rc/") {
		return []byte(`{}`), nil
	}
	return json.Marshal(toWire(iface))
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
	out := make(map[string]ifaceWire, len(f.ifaces))
	for id, iface := range f.ifaces {
		out[id] = toWire(iface)
	}
	return json.Marshal(out)
}

func (f *FakeNDMS) showOne(name string) (json.RawMessage, error) {
	iface, ok := f.ifaces[name]
	if !ok {
		f.E++
		return json.RawMessage(`{"show":{"interface":{"status":[{"status":"error","code":"` + ndmsUnableToFindCode + `","message":"unable to find"}]}}}`), nil
	}
	w, err := json.Marshal(toWire(iface))
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
	f.hooks = append(f.hooks, FakeHook{Type: "ifcreated", ID: name})
	return json.RawMessage(`{"interface":{"wireguard":{"import":{"created":"` + name + `","intersects":"","status":[]}}}}`), nil
}

// ifaceCmd — `interface name …`. По отсутствующему: `no` — отказ без E (это
// ответ на снос, не чтение); иначе NDMS создаёт name — намеренно, если
// объявлен ExpectCreate, иначе фантом.
func (f *FakeNDMS) ifaceCmd(name string, body map[string]any) (json.RawMessage, error) {
	iface, ok := f.ifaces[name]
	if no, _ := body["no"].(bool); no {
		if !ok {
			return statusError(fmt.Sprintf("unable to find interface %q", name)), nil
		}
		f.remove(name)
		return json.RawMessage(`{}`), nil
	}
	if !ok {
		if f.expect[name] {
			delete(f.expect, name)
			f.Created = append(f.Created, name)
		} else {
			f.Phantoms++
		}
		iface = ndms.Interface{ID: name, Type: typeByPrefix(name), State: "down"}
		f.hooks = append(f.hooks, FakeHook{Type: "ifcreated", ID: name})
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
	return json.RawMessage(`{}`), nil
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

func (f *FakeNDMS) remove(name string) {
	if _, ok := f.ifaces[name]; !ok {
		return
	}
	delete(f.ifaces, name)
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
