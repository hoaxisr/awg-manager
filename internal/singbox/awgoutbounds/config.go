// internal/singbox/awgoutbounds/config.go
package awgoutbounds

import (
	"encoding/json"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fallbackResolver — DNS-сервер outbound'а, когда у туннеля своего нет
// (системные NDMS-туннели DNS не несут). Настройка bootstrap-DNS сюда
// НЕ прокидывается: её смена обязывала бы пересобирать 15-awg.json.
const fallbackResolver = "1.1.1.1"

// fileShape — содержимое 15-awg.json. Кроме outbounds слот объявляет
// dns.servers: по одному серверу на outbound, чтобы домен резолвился
// через сам туннель (#846). sing-box сливает config.d/*.json по ключам,
// поэтому inbounds/route здесь по-прежнему не объявляются.
type fileShape struct {
	Outbounds []map[string]any `json:"outbounds"`
	DNS       dnsShape         `json:"dns"`
}

type dnsShape struct {
	Servers []map[string]any `json:"servers"`
}

// dnsTag — тег DNS-сервера, обслуживающего outbound с тегом tag.
func dnsTag(tag string) string { return "dns-" + tag }

// buildOutbounds проецирует записи в форму outbound'ов sing-box: по одному
// direct на запись, привязанному к её kernel-интерфейсу. domain_resolver
// уводит резолв домена из запроса на личный DNS-сервер outbound'а — иначе
// он уходит в route.default_domain_resolver (dns-bootstrap) мимо туннеля.
func buildOutbounds(entries []AWGEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"type":            "direct",
			"tag":             e.Tag,
			"bind_interface":  e.Iface,
			"domain_resolver": map[string]any{"server": dnsTag(e.Tag)},
		})
	}
	return out
}

// buildDNSServers — по DNS-серверу на каждый outbound. detour гонит сам
// запрос через туннель: лежит туннель — резолв падает, а не утекает.
func buildDNSServers(entries []AWGEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		server := e.Resolver
		if server == "" {
			server = fallbackResolver
		}
		out = append(out, map[string]any{
			"type":   "udp",
			"tag":    dnsTag(e.Tag),
			"server": server,
			"detour": e.Tag,
		})
	}
	return out
}

// saveFile пишет 15-awg.json атомарно (tmp + rename). Файл всегда
// остаётся валидным JSON-объектом: даже без записей это
// `{"outbounds":[],"dns":{"servers":[]}}`, чтобы sing-box чисто слил config.d.
func saveFile(path string, entries []AWGEntry) error {
	raw, err := marshalEntries(entries)
	if err != nil {
		return err
	}
	return storage.AtomicWrite(path, raw)
}

// marshalEntries renders entries as the indented JSON payload that
// 15-awg.json holds. Shared by saveFile (legacy direct-write) and the
// orchestrator-Save path in writeFile.
func marshalEntries(entries []AWGEntry) ([]byte, error) {
	f := fileShape{
		Outbounds: buildOutbounds(entries),
		DNS:       dnsShape{Servers: buildDNSServers(entries)},
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return raw, nil
}
