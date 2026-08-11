package nwg

import (
	"net"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ndmsEndpointPlaceholder — endpoint-заглушка для NDMS: RCI не принимает
// IPv6-endpoint ни в импорте .conf, ни в peer-командах, а endpoint в конфиге
// NDMS для v6-туннеля в любом случае фиктивен — реальный живёт в ядре
// (wg set, см. wg_tool.go/endpoint_guard.go).
//
// Порт 1 выбран сознательно: kmod-слоты awg_proxy слушают на 127.0.0.1 с
// kernel-ephemeral портами (32768+), и заглушка с реальным remote-портом
// (например 51820, попадает в ephemeral-диапазон) могла бы указать в ЧУЖОЙ
// живой слот — kernel WG слал бы хендшейки в прокси другого туннеля. На
// порт 1 ядро эфемерные сокеты не вешает.
const ndmsEndpointPlaceholder = "127.0.0.1:1"

// EndpointHostIsIPv6 — endpoint несёт IPv6-литерал хоста. Понимает все
// реальные формы: "[v6]:port", "[v6]" без порта, голый "v6",
// небракетированный "v6:port" (некоторые провайдеры так выгружают конфиги)
// и IPv4-mapped "::ffff:1.2.3.4" (форма с двоеточиями — NDMS отвергает и
// её). Hostname и IPv4 → false.
func EndpointHostIsIPv6(endpoint string) bool {
	host, ok := splitEndpointHost(strings.TrimSpace(endpoint))
	return ok && strings.Contains(host, ":") && net.ParseIP(host) != nil
}

// EndpointMayResolveIPv6 — endpoint МОЖЕТ дать IPv6-адрес при старте:
// v6-литерал или hostname (во что резолвится — заранее неизвестно, например
// DDNS только с AAAA). Если последний Start ушёл по v6-пути, конфиг NDMS
// несёт заглушку — после ребута роутера такому туннелю нужен полный Start
// (orchestrator/decideBoot). IPv4-литерал → false: у него в NDMS реальный
// endpoint и boot ничего делать не должен (историческое поведение).
//
// Критерий v6-литерала — форма с двоеточиями (как в EndpointHostIsIPv6),
// НЕ ip.To4(): IPv4-mapped "::ffff:1.2.3.4" даёт To4()!=nil, но NDMS эту
// форму отвергает и SyncPeer/Start кладут для неё заглушку — boot обязан
// классифицировать её так же, иначе после ребута туннель не самолечится.
func EndpointMayResolveIPv6(endpoint string) bool {
	host, ok := splitEndpointHost(strings.TrimSpace(endpoint))
	if !ok {
		return false
	}
	return net.ParseIP(host) == nil || strings.Contains(host, ":")
}

// canonicalV6Endpoint нормализует v6-endpoint к "[addr]:port" — форме,
// которую принимает wg set. ok=false, если это не v6-литерал с валидным
// портом (в т.ч. для форм без порта — их нечего ставить в ядро).
func canonicalV6Endpoint(endpoint string) (string, bool) {
	addr := strings.TrimSpace(endpoint)
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if net.ParseIP(host) != nil && strings.Contains(host, ":") && validPortString(port) {
			return net.JoinHostPort(host, port), true
		}
		return "", false
	}
	// Небракетированный v6:port — сплит по последнему двоеточию.
	if i := strings.LastIndex(addr, ":"); i > 0 {
		host, port := strings.Trim(addr[:i], "[]"), addr[i+1:]
		if net.ParseIP(host) != nil && strings.Contains(host, ":") && validPortString(port) {
			return net.JoinHostPort(host, port), true
		}
	}
	return "", false
}

func validPortString(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

// splitEndpointHost достаёт хост из endpoint'а в любой из принимаемых форм.
func splitEndpointHost(addr string) (string, bool) {
	if addr == "" || strings.Contains(addr, "/") {
		return "", false
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host, true
	}
	// "[v6]" без порта / голый IP-литерал.
	if trimmed := strings.Trim(addr, "[]"); net.ParseIP(trimmed) != nil {
		return trimmed, true
	}
	// Небракетированный v6:port.
	if i := strings.LastIndex(addr, ":"); i > 0 {
		host := strings.Trim(addr[:i], "[]")
		if net.ParseIP(host) != nil && strings.Contains(host, ":") && validPortString(addr[i+1:]) {
			return host, true
		}
	}
	return "", false
}

// importConfEndpoint возвращает адрес, которым надо подменить строку
// `Endpoint` в .conf'е перед RCI-импортом; "" — оставить как есть.
//
// Доменное имя в конфиг NDMS не отдаём (#702): туннель, созданный и ни разу
// не запущенный, демон после ребута не поднимает (decideBoot пропускает
// !Enabled) — интерфейс поднимет сам NDMS, а при неудаче СВОЕГО резолва он
// молча не поднимает его вовсе, без единой строки в журнале роутера.
// IPv6 (литерал или результат резолва) NDMS в импорте отвергает целиком
// («invalid endpoint format») — для него заглушка, реальный адрес выставит
// Start. Резолв не удался — оставляем имя: туннель ещё ни разу не работал,
// прежнего адреса не существует.
func (o *OperatorNativeWG) importConfEndpoint(stored *storage.AWGTunnel) string {
	if EndpointHostIsIPv6(stored.Peer.Endpoint) {
		return ndmsEndpointPlaceholder
	}
	host, ok := splitEndpointHost(stored.Peer.Endpoint)
	if !ok || net.ParseIP(host) != nil {
		return "" // пустой/мусорный endpoint или v4-литерал — резолвить нечего
	}
	ip, port, err := o.resolveEndpointWithFallback(stored)
	if err != nil {
		o.appLog.Warn("create", stored.Name,
			"резолв "+stored.Peer.Endpoint+" не удался, в .conf уходит имя: "+err.Error())
		return ""
	}
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		return ndmsEndpointPlaceholder
	}
	return net.JoinHostPort(ip, strconv.Itoa(port))
}

// replaceConfEndpointLine переписывает строку `Endpoint = ...` в секции
// [Peer] .conf'а, сгенерированного config.GenerateForExport. Скоуп по секции
// принципиален: свободнотекстовые I-параметры в [Interface] могут содержать
// строку с префиксом "Endpoint" — трогать её нельзя.
func replaceConfEndpointLine(conf, endpoint string) string {
	lines := strings.Split(conf, "\n")
	inPeer := false
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "[") {
			inPeer = strings.EqualFold(trimmed, "[peer]")
			continue
		}
		if inPeer && strings.HasPrefix(trimmed, "Endpoint") && strings.Contains(trimmed, "=") {
			lines[i] = "Endpoint = " + endpoint
		}
	}
	return strings.Join(lines, "\n")
}
