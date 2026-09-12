package command

import (
	"context"
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
// Host (/32) or Network+Mask must be set.
type StaticRouteSpec struct {
	Interface string
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

func (c *RouteCommands) SetDefaultRoute(ctx context.Context, name string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"route": map[string]any{"default": true, "interface": name},
		},
	}
	return c.mutate(ctx, payload, "set default route "+name)
}

func (c *RouteCommands) RemoveDefaultRoute(ctx context.Context, name string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"route": map[string]any{"default": true, "interface": name, "no": true},
		},
	}
	return c.mutateTolerant(ctx, payload, "remove default route "+name, isNetlinkFileExists)
}

func (c *RouteCommands) SetIPv6DefaultRoute(ctx context.Context, name string) error {
	payload := map[string]any{
		"ipv6": map[string]any{
			"route": map[string]any{"default": true, "interface": name},
		},
	}
	return c.mutate(ctx, payload, "set ipv6 default route "+name)
}

func (c *RouteCommands) RemoveIPv6DefaultRoute(ctx context.Context, name string) error {
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
func (c *RouteCommands) RemoveHostRoute(ctx context.Context, host string) error {
	// Сначала точный путь: читаем записи по адресу из конфигурации роутера и
	// снимаем СВОИ (по метке владения) каждую своей парой (host, interface).
	// Слепая форма снимает всё, что стоит на адресе, включая статический
	// маршрут, заведённый пользователем руками, — а различить их можно только
	// по метке, которую ставим мы сами (AddStaticRoute.Comment).
	if done, err := c.removeOwnedHostRoutes(ctx, host); done {
		return err
	}

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

// ownedRouteMark — метка владения в комментарии записи. Её ставит
// AddStaticRoute всем нашим маршрутам (`… auto !awgm-obfuscator awg20`).
const ownedRouteMark = "!awgm-"

// removeOwnedHostRoutes снимает записи host-route по адресу, помеченные как
// наши, каждую парной формой (host, interface). done=false означает «точным
// путём не получилось» — конфигурацию не прочитать; вызывающий падает на
// слепую форму, как раньше.
//
// Запись без нашей метки не трогаем вовсе: это либо маршрут пользователя,
// либо запись, которую NDMS завёл сам, — снимать её за него мы не вправе.
func (c *RouteCommands) removeOwnedHostRoutes(ctx context.Context, host string) (bool, error) {
	if c.queries == nil || c.queries.RunningConfig == nil {
		return false, nil
	}
	lines, err := c.queries.RunningConfig.Lines(ctx)
	if err != nil {
		return false, nil
	}
	v6 := false
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		v6 = true
	}
	// Пустой список — снимать нечего, и это успех: маршрута, за который
	// отвечает вызывающий, на роутере нет. Слепой залп по адресу тут только
	// снёс бы чужую запись.
	var firstErr error
	for _, iface := range ownedHostRouteIfaces(lines, host, v6) {
		spec := StaticRouteSpec{Host: host, Interface: iface, V6: v6}
		if rmErr := c.RemoveStaticRoute(ctx, spec); rmErr != nil && firstErr == nil {
			firstErr = rmErr
		}
	}
	return true, firstErr
}

// ownedHostRouteIfaces — интерфейсы наших записей host-route по адресу.
// Формат строки конфигурации: `ip route <host> <iface> auto !awgm-…`,
// у v6 — `ipv6 route <prefix>/128 <iface> auto !awgm-…` (стенд 5.01).
func ownedHostRouteIfaces(lines []string, host string, v6 bool) []string {
	keyword, dest := "ip", host
	if v6 {
		keyword, dest = "ipv6", host+"/128"
	}
	var out []string
	for _, line := range lines {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 4 || f[0] != keyword || f[1] != "route" || f[2] != dest {
			continue
		}
		if !strings.Contains(line, ownedRouteMark) {
			continue
		}
		out = append(out, f[3])
	}
	return out
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
	// Общая часть у обеих форм одна и та же; различаются только ключ
	// назначения (prefix против host|network+mask) и внешний ключ.
	inner := map[string]any{
		"interface": route.Interface,
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
			return err
		}
		if route.Interface == "" {
			// Стенд 5.01: ЛЮБОЙ v6-маршрут без интерфейса роутер отвергает
			// («no input») — проверено и на host-, и на сетевой форме. Отказ
			// здесь даёт причину в журнале вместо загадочного отказа RCI, а
			// для reject это ещё и разница между kill-switch и утечкой.
			return fmt.Errorf("ipv6 route without interface: %+v", route)
		}
		inner["prefix"] = prefix
		return c.mutate(ctx, map[string]any{"ipv6": map[string]any{"route": inner}}, "add ipv6 static route")
	}

	if route.Host != "" {
		inner["host"] = route.Host
	} else {
		inner["network"] = route.Network
		inner["mask"] = route.Mask
	}
	return c.mutate(ctx, map[string]any{"ip": map[string]any{"route": inner}}, "add static route")
}

// RemoveStaticRoute removes a previously-added static route. For v6 (route.V6)
// it emits {prefix, interface, no} under "ipv6" — ключ ИМЕННО prefix, см.
// v6Prefix; for v4 it emits {interface, no, host|network+mask} under "ip".
func (c *RouteCommands) RemoveStaticRoute(ctx context.Context, route StaticRouteSpec) error {
	if route.V6 {
		prefix, err := v6Prefix(route)
		if err != nil {
			return err
		}
		payload := map[string]any{
			"ipv6": map[string]any{
				"route": map[string]any{
					"prefix":    prefix,
					"interface": route.Interface,
					"no":        true,
				},
			},
		}
		return c.mutateTolerant(ctx, payload, "remove ipv6 static route", toleratesRouteRemoval)
	}
	inner := map[string]any{
		"interface": route.Interface,
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
	return c.mutateTolerant(ctx, payload, "remove static route", toleratesRouteRemoval)
}

// mutate is a thin wrapper over postMutation with RouteCommands' fixed
// invalidation set (Routes + RunningConfig). Every route mutation touches
// both caches identically, so we pin them in one place.
func (c *RouteCommands) mutate(ctx context.Context, payload any, op string) error {
	return c.mutateTolerant(ctx, payload, op, nil)
}

// mutateTolerant — mutate, признающий часть отказов безобидными. Нужен снятию
// маршрутов: отложенный drain fakeip снимает их уже ПОСЛЕ удаления интерфейса,
// и NDMS отвечает «no such interface» на живую, ожидаемую ситуацию.
func (c *RouteCommands) mutateTolerant(ctx context.Context, payload any, op string, tolerate func(string) bool) error {
	return postMutationCheckedTolerant(ctx, c.poster, c.save, payload, op, tolerate,
		c.queries.Routes.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}
