package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// maxHostRouteEntries — потолок повторов слепого снятия host-route. Роутер
// снимает по одной записи за вызов и отвечает ложной ошибкой «file exists»,
// пока по адресу остаётся ещё одна (стенд 5.01); потолок нужен, чтобы
// постоянный отказ не крутился вечно.
const maxHostRouteEntries = 4

type RouteCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

func NewRouteCommands(p Poster, s *SaveCoordinator, q *query.Queries) *RouteCommands {
	return &RouteCommands{poster: p, save: s, queries: q}
}

// StaticRouteSpec describes a static route mutation. Exactly one of
// Host (/32) or Network+Mask must be set. Интерфейс подтверждён свежим
// списком (F546).
type StaticRouteSpec struct {
	Interface query.Confirmed
	Host      string
	Network   string
	Mask      string
	Reject    bool
	Comment   string
	// V6 selects the IPv6 route form: the payload uses the "ipv6" outer key,
	// а подсеть уезжает ключом prefix — у v6 нет ни mask, ни host.
	// Host здесь значит то же, что у v4: хост-маршрут, только выражается он
	// как prefix с /128 (стенд 5.01: `ipv6 route 2001:db8::1/128 PPPoE0 auto`).
	// Comment роутер принимает и хранит (`… auto !awgm-test`).
	V6 bool
}

func (c *RouteCommands) SetDefaultRoute(ctx context.Context, iface query.Confirmed) error {
	name := iface.Name()
	payload := map[string]any{
		"ip": map[string]any{
			"route": map[string]any{"default": true, "interface": name},
		},
	}
	return c.mutate(ctx, payload, "set default route "+name)
}

func (c *RouteCommands) RemoveDefaultRoute(ctx context.Context, iface query.Confirmed) error {
	name := iface.Name()
	payload := map[string]any{
		"ip": map[string]any{
			"route": map[string]any{"default": true, "interface": name, "no": true},
		},
	}
	return c.mutateTolerant(ctx, payload, "remove default route "+name, isNetlinkFileExists)
}

func (c *RouteCommands) SetIPv6DefaultRoute(ctx context.Context, iface query.Confirmed) error {
	name := iface.Name()
	payload := map[string]any{
		"ipv6": map[string]any{
			"route": map[string]any{"default": true, "interface": name},
		},
	}
	return c.mutate(ctx, payload, "set ipv6 default route "+name)
}

func (c *RouteCommands) RemoveIPv6DefaultRoute(ctx context.Context, iface query.Confirmed) error {
	name := iface.Name()
	payload := map[string]any{
		"ipv6": map[string]any{
			"route": map[string]any{"default": true, "interface": name, "no": true},
		},
	}
	return c.mutateTolerant(ctx, payload, "remove ipv6 default route "+name, isNetlinkFileExists)
}

// v6Prefix — подсеть для формы ipv6: сеть как есть, хост как /128.
//
// Пустой результат — отказ, а не запрос: NDMS молча отбрасывает неизвестное
// поле, и снятие без подсети целится в ::/0, то есть в ДЕФОЛТНЫЙ маршрут
// интерфейса (проверено на стенде 5.01). Тип комбинацию `V6` + `Host` не
// запрещает, поэтому проверка стоит здесь, а не у вызывающих.
// При обоих заполненных полях побеждает Network: у v6 сеть и хост выражаются
// одним ключом, и «сеть плюс хост» — не запрос, а ошибка вызывающего.
func v6Prefix(route StaticRouteSpec) (string, error) {
	switch {
	case route.Network != "":
		return route.Network, nil
	case route.Host != "":
		return route.Host + "/128", nil
	default:
		return "", fmt.Errorf("ipv6 route without prefix: %+v", route)
	}
}

// RemoveHostRoute removes a host route.
//
// NDMS хранит ОТДЕЛЬНУЮ запись на каждый интерфейс, а форма без interface
// снимает ровно одну и на остатке отвечает `system failed [0xcffd0198]`.
// Стенд 5.01: две записи (PPPoE0 и Bridge0) → первая команда убирает одну и
// отдаёт ошибку, вторая убирает последнюю и отвечает успехом, третья говорит
// «no such route» (это тоже успех). Поэтому повторяем, пока не перестанет
// отказывать: иначе при смене WAN записи накапливались бы, а снятие вечно
// возвращало ошибку в журнал (F120).
//
// Интерфейс не указываем сознательно: вызывающие снимают маршрут по адресу и
// не знают, через какой WAN он был поставлен — в этом и смысл уборки.
//
// У v6 своя форма: ключ `ipv6` и `prefix` с /128. Проверено на стенде 5.01:
// v4-форма с v6-адресом отвергается («invalid destination host»), а
// `ipv6.route.host` НЕ адресует хост — запрос вырождается в удаление ::/0,
// то есть дефолтного маршрута (тот же капкан описан у AddStaticRoute).
//
// Неразобранный адрес уходит v4-формой: пусть отказывает NDMS и причина видна
// в журнале — молчаливый v6-путь превратил бы мусор в «/128».
//
// Кто свою запись подписывает комментарием, тому нужна RemoveOwnHostRoute:
// она снимает только свои записи и не трогает чужие на том же адресе.
func (c *RouteCommands) RemoveHostRoute(ctx context.Context, host string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"route": map[string]any{"no": true, "host": host},
		},
	}
	op := "remove host route " + host
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		payload = map[string]any{
			"ipv6": map[string]any{
				"route": map[string]any{"prefix": host + "/128", "no": true},
			},
		}
		op = "remove ipv6 host route " + host
	}

	// Повторяем ТОЛЬКО на «file exists»: этим роутер отвечает, когда запись
	// снята, а в таблице остался ещё один маршрут на тот же адрес (стенд 5.01,
	// см. isNetlinkFileExists). Любой другой отказ — настоящий, и долбить им
	// роутер незачем: каждая попытка стоит save.Request() и двух инвалидаций.
	var lastErr error
	for attempt := 0; attempt < maxHostRouteEntries; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		lastErr = c.mutate(ctx, payload, op)
		if lastErr == nil || !isNetlinkFileExists(lastErr.Error()) {
			return lastErr
		}
	}
	return lastErr
}

// RemoveOwnHostRoute снимает host-route по адресу, но только записи,
// подписанные ИМЕННО этим комментарием, — каждую своей парой (host, interface).
//
// Зачем отдельно от RemoveHostRoute: слепая форма уносит всё, что стоит на
// адресе, включая статический маршрут, заведённый пользователем руками. Кто
// свою запись подписывает (комментарий уезжает в AddStaticRoute.Comment и
// виден в конфигурации роутера как `!<comment>`), тот снимает ровно её. Кто не
// подписывает, тому нужна слепая форма: у kernel-туннелей OS5 своей записи в
// NDMS нет вовсе (маршрут живёт в ядре), и там снимается наследство прежних
// версий — по нему подписи нет и быть не может.
//
// Не прочиталась running-config — падаем на слепую форму: лучше снять
// лишнее, чем оставить собственный маршрут на роутере. Не прочитался список
// интерфейсов — ошибка, ни одной команды, слепой формы нет (R20).
//
// Запись на интерфейс, которого нет в свежем списке, не трогаем: снимать её
// нечем, команда по отсутствующему — E в журнале ndm (F546).
func (c *RouteCommands) RemoveOwnHostRoute(ctx context.Context, host, comment string) error {
	if comment == "" || c.queries == nil || c.queries.RunningConfig == nil {
		return c.RemoveHostRoute(ctx, host)
	}
	lines, err := c.queries.RunningConfig.Lines(ctx)
	if err != nil {
		return c.RemoveHostRoute(ctx, host)
	}
	v6 := false
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		v6 = true
	}
	// Пустой список — снимать нечего, и это успех: записи, за которую отвечает
	// вызывающий, на роутере нет.
	names := ownHostRouteIfaces(lines, host, comment, v6)
	if len(names) == 0 {
		return nil
	}
	present, err := c.queries.Interfaces.ConfirmEach(ctx, names)
	if err != nil {
		return fmt.Errorf("remove own host route %s: %w", host, err)
	}
	var firstErr error
	for _, name := range names {
		iface, ok := present[name]
		if !ok {
			continue
		}
		spec := StaticRouteSpec{Host: host, Interface: iface, V6: v6}
		if rmErr := c.RemoveStaticRoute(ctx, spec); rmErr != nil && firstErr == nil {
			firstErr = rmErr
		}
	}
	return firstErr
}

// ownHostRouteIfaces — интерфейсы записей host-route по адресу, подписанных
// заданным комментарием. Формат строки: `ip route <host> <iface> auto
// !<comment>`, у v6 — `ipv6 route <prefix>/128 <iface> auto !<comment>`
// (стенд 5.01).
//
// Комментарий сверяется целиком и только в своём токене: поиск подстрокой по
// всей строке принял бы за свою чужую запись, в комментарий которой наша
// подпись попала куском — у пользовательских маршрутов текст произвольный
// (internal/staticroute).
func ownHostRouteIfaces(lines []string, host, comment string, v6 bool) []string {
	keyword, dest := "ip", host
	if v6 {
		keyword, dest = "ipv6", host+"/128"
	}
	want := "!" + comment
	var out []string
	for _, line := range lines {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 4 || f[0] != keyword || f[1] != "route" || f[2] != dest {
			continue
		}
		// Интерфейс — четвёртое поле только у host-формы: у формы через шлюз
		// там IP, у network+mask — маска. Такая запись не наша, даже если
		// подпись совпала.
		if net.ParseIP(f[3]) != nil {
			continue
		}
		if commentOf(f) != want {
			continue
		}
		out = append(out, f[3])
	}
	return out
}

// commentOf — комментарий записи: токен, начинающийся с `!`, и всё за ним
// (в комментарии бывают пробелы: `!маршрут пользователя`).
func commentOf(fields []string) string {
	for i, f := range fields {
		if strings.HasPrefix(f, "!") {
			return strings.Join(fields[i:], " ")
		}
	}
	return ""
}

// AddStaticRoute adds a network or host route to the given interface. For v6
// (route.V6) it emits {prefix, interface, auto} plus reject/comment when set
// (NDMS reasserts on iface up); for v4 it keeps the full
// auto/reject/comment/mask/host form under "ip".
//
// Стенд 5.01 принял v6-reject: `ipv6 route 2001:db8:bb::/48 PPPoE0 auto
// reject`. Интерфейс обязателен для любого v6-маршрута — без него роутер
// отвечает «no input».
//
// Ключ подсети у v6 — ИМЕННО prefix, не network (как у v4). NDMS молча
// отбрасывает неизвестное поле, и запрос вырождается: add остаётся без
// обязательной подсети и получает ложный «no input», а remove без неё целится
// в ::/0 — то есть в ДЕФОЛТНЫЙ маршрут интерфейса. Форма стенд-проверена
// 2026-08-24: сам роутер хранит запись как {prefix, interface, auto, comment}.
func (c *RouteCommands) AddStaticRoute(ctx context.Context, route StaticRouteSpec) error {
	payload, op, err := addRoutePayload(route)
	if err != nil {
		return err
	}
	return c.mutate(ctx, payload, op)
}

func addRoutePayload(route StaticRouteSpec) (any, string, error) {
	if err := requireRouteIface(route); err != nil {
		return nil, "", err
	}
	// Общая часть у обеих форм одна и та же; различаются только ключ
	// назначения (prefix против host|network+mask) и внешний ключ.
	inner := map[string]any{
		"interface": route.Interface.Name(),
		"auto":      true,
	}
	if route.Reject {
		inner["reject"] = true
	}
	if route.Comment != "" {
		inner["comment"] = route.Comment
	}

	if route.V6 {
		prefix, err := v6Prefix(route)
		if err != nil {
			return nil, "", err
		}
		inner["prefix"] = prefix
		return map[string]any{"ipv6": map[string]any{"route": inner}}, "add ipv6 static route", nil
	}

	if route.Host != "" {
		inner["host"] = route.Host
	} else {
		inner["network"] = route.Network
		inner["mask"] = route.Mask
	}
	return map[string]any{"ip": map[string]any{"route": inner}}, "add static route", nil
}

// RemoveStaticRoute removes a previously-added static route. For v6 (route.V6)
// it emits {prefix, interface, no} under "ipv6" — ключ ИМЕННО prefix, см.
// v6Prefix; for v4 it emits {interface, no, host|network+mask} under "ip".
func (c *RouteCommands) RemoveStaticRoute(ctx context.Context, route StaticRouteSpec) error {
	payload, op, err := removeRoutePayload(route)
	if err != nil {
		return err
	}
	return c.mutateTolerant(ctx, payload, op, toleratesRouteRemoval)
}

func removeRoutePayload(route StaticRouteSpec) (any, string, error) {
	if err := requireRouteIface(route); err != nil {
		return nil, "", err
	}
	if route.V6 {
		prefix, err := v6Prefix(route)
		if err != nil {
			return nil, "", err
		}
		payload := map[string]any{
			"ipv6": map[string]any{
				"route": map[string]any{
					"prefix":    prefix,
					"interface": route.Interface.Name(),
					"no":        true,
				},
			},
		}
		return payload, "remove ipv6 static route", nil
	}
	inner := map[string]any{
		"interface": route.Interface.Name(),
		"no":        true,
	}
	if route.Host != "" {
		inner["host"] = route.Host
	} else {
		inner["network"] = route.Network
		inner["mask"] = route.Mask
	}
	payload := map[string]any{
		"ip": map[string]any{"route": inner},
	}
	return payload, "remove static route", nil
}

// routeBatchSize — маршрутов в одном POST. Стенд KN-1810 5.02.B.2: ~50 команд/с
// при размере пакета от 100 до 1000 против ~12 POST/с по одному. Размер на
// скорость не влиял, поэтому взят малый: ~2 с на запрос при таймауте 30 с, запас
// на роутеры медленнее стенда.
const routeBatchSize = 100

// Отказ всего пакета, а не маршрута. ErrRouteOutcomeUnknown — пакет ушёл, но
// ответа нет или он не разобран: часть команд могла примениться, вызывающему,
// откатывающему правку, снимать и эти маршруты. ErrRouteNotSent — следующие
// пакеты не отправлялись: ndm не отвечает, и новые команды только добавят ему
// очереди.
var (
	ErrRouteOutcomeUnknown = errors.New("static routes batch: outcome unknown")
	ErrRouteNotSent        = errors.New("static routes batch: not sent")
)

// AddStaticRoutes ставит маршруты пакетами. Результат — ошибка на каждый
// маршрут, по индексу: NDMS отвечает на каждый элемент отдельно, и отказ одного
// соседним не мешает (стенд 5.02.B.2).
func (c *RouteCommands) AddStaticRoutes(ctx context.Context, routes []StaticRouteSpec) []error {
	return c.routeBatches(ctx, routes, addRoutePayload, nil)
}

// RemoveStaticRoutes — снятие пакетами; те же отказы безобидны, что и у
// RemoveStaticRoute.
func (c *RouteCommands) RemoveStaticRoutes(ctx context.Context, routes []StaticRouteSpec) []error {
	return c.routeBatches(ctx, routes, removeRoutePayload, toleratesRouteRemoval)
}

func (c *RouteCommands) routeBatches(
	ctx context.Context,
	routes []StaticRouteSpec,
	build func(StaticRouteSpec) (any, string, error),
	tolerate func(string) bool,
) []error {
	errs := make([]error, len(routes))
	posted := false
	for start := 0; start < len(routes); start += routeBatchSize {
		end := min(start+routeBatchSize, len(routes))
		var payloads []any
		var index []int // позиция элемента пакета в routes
		for i := start; i < end; i++ {
			payload, _, err := build(routes[i])
			if err != nil {
				errs[i] = err
				continue
			}
			payloads = append(payloads, payload)
			index = append(index, i)
		}
		if len(payloads) == 0 {
			continue
		}
		posted = true
		batchErrs, err := c.postRouteBatch(ctx, payloads, tolerate)
		for i, e := range batchErrs {
			errs[index[i]] = e
		}
		if err != nil {
			for i := end; i < len(routes); i++ {
				errs[i] = fmt.Errorf("%w: %w", ErrRouteNotSent, err)
			}
			break
		}
	}
	// После всей пачки, а не на пакет: пакет дольше дебаунса сохранения, и
	// сохранения шли бы между пакетами. Отказ не отменяет: что-то применилось.
	if posted {
		c.save.Request()
		c.queries.Routes.InvalidateAll()
		c.queries.RunningConfig.InvalidateAll()
		c.queries.StaticRoutes.InvalidateAll()
	}
	return errs
}

// postRouteBatch — один пакет: ошибки по элементам и причина, если исход всего
// пакета неизвестен.
func (c *RouteCommands) postRouteBatch(ctx context.Context, payloads []any, tolerate func(string) bool) ([]error, error) {
	raw, err := c.poster.Post(ctx, payloads)
	var replies []json.RawMessage
	if err == nil {
		if jerr := json.Unmarshal(raw, &replies); jerr != nil {
			err = fmt.Errorf("decode reply: %w", jerr)
		} else if len(replies) != len(payloads) {
			err = fmt.Errorf("reply has %d elements, sent %d", len(replies), len(payloads))
		}
	}
	errs := make([]error, len(payloads))
	if err != nil {
		for i := range errs {
			errs[i] = fmt.Errorf("%w: %w", ErrRouteOutcomeUnknown, err)
		}
		return errs, err
	}
	for i, reply := range replies {
		if msgs := ndmsStatusErrors(reply); len(msgs) > 0 && !allTolerated(msgs, tolerate) {
			errs[i] = fmt.Errorf("router reported error: %s", strings.Join(msgs, "; "))
		}
	}
	return errs, nil
}

// requireRouteIface — спека без интерфейса (нулевой Confirmed: поле пропущено
// в литерале, сканер такого не видит) в NDMS не уходит. У v6 это ещё и факт
// стенда 5.01: ЛЮБОЙ v6-маршрут без интерфейса роутер отвергает («no input»)
// — и host-, и сетевую форму; для reject отказ здесь — разница между
// kill-switch и утечкой. У v4 форма без интерфейса адресовала бы маршрут
// мимо подтверждения (F546).
func requireRouteIface(route StaticRouteSpec) error {
	if route.Interface.Name() == "" {
		return fmt.Errorf("static route without interface: %+v", route)
	}
	return nil
}

// mutate is a thin wrapper over postMutation with RouteCommands' fixed
// invalidation set (Routes + RunningConfig + StaticRoutes). Every route
// mutation touches all three caches identically, so we pin them in one place.
func (c *RouteCommands) mutate(ctx context.Context, payload any, op string) error {
	return c.mutateTolerant(ctx, payload, op, nil)
}

// mutateTolerant — mutate, признающий часть отказов безобидными. Нужен снятию
// маршрутов: отложенный drain fakeip снимает их уже ПОСЛЕ удаления интерфейса,
// и NDMS отвечает «no such interface» на живую, ожидаемую ситуацию.
func (c *RouteCommands) mutateTolerant(ctx context.Context, payload any, op string, tolerate func(string) bool) error {
	return postMutationCheckedTolerant(ctx, c.poster, c.save, payload, op, tolerate,
		c.queries.Routes.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll,
		c.queries.StaticRoutes.InvalidateAll)
}
