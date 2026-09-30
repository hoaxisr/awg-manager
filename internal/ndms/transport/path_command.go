package transport

import (
	"fmt"
	"strings"
)

// pathToCommand парсит RCI-путь в JSON-дерево для batch-POST'а.
//
// Возвращает:
//   - cmd — JSON-дерево для упаковки в batch request
//   - unwrapKeys — список ключей для walk'а внутрь response item'а,
//     чтобы достать данные эквивалентные direct GET. NDMS оборачивает
//     batch response item в путь-tree (`{"show":{"version":{...content...}}}`),
//     callers ожидают direct content (как от direct GET).
//
// Поддерживает формы:
//
//	"/show/interface/"                            → cmd {"show":{"interface":{}}}, unwrap ["show","interface"]
//	"/show/interface/system-name?name=Wireguard0" → cmd {"show":{"interface":{"system-name":{"name":"Wireguard0"}}}}, unwrap ["show","interface","system-name"]
//	"/show/sc/dns-proxy/route"                    → cmd {"show":{"sc":{"dns-proxy":{"route":{}}}}}, unwrap ["show","sc","dns-proxy","route"]
//	"/show/running-config"                        → cmd {"show":{"running-config":{}}}, unwrap ["show","running-config"]
//
// Эвристика: последний "содержательный" сегмент это leaf. Если последний
// сегмент содержит `?k=v` — параметры применяются к leaf'у. Формы
// интерфейса по имени (`/show/interface/<name>`) нет: по имени демон не
// читает (F546). Leaf без
// параметров получает значение `{}` (empty object) — NDMS требует
// non-null для batch query, иначе вернёт пустой response.
func pathToCommand(path string) (any, []string, error) {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return nil, nil, fmt.Errorf("pathToCommand: empty path")
	}

	hasTrailingSlash := strings.HasSuffix(path, "/")
	path = strings.TrimSuffix(path, "/")

	segments := strings.Split(path, "/")
	if len(segments) == 0 {
		return nil, nil, fmt.Errorf("pathToCommand: no segments")
	}

	last := segments[len(segments)-1]
	var leafParams map[string]any

	if idx := strings.Index(last, "?"); idx >= 0 {
		paramStr := last[idx+1:]
		last = last[:idx]
		if last == "" || paramStr == "" {
			return nil, nil, fmt.Errorf("pathToCommand: malformed query in %q", path)
		}
		segments[len(segments)-1] = last

		leafParams = map[string]any{}
		for _, kv := range strings.Split(paramStr, "&") {
			eq := strings.Index(kv, "=")
			if eq <= 0 || eq == len(kv)-1 {
				return nil, nil, fmt.Errorf("pathToCommand: malformed param %q in %q", kv, path)
			}
			leafParams[kv[:eq]] = kv[eq+1:]
		}
	}

	var leafKey string
	var leafValue any

	if leafParams != nil {
		// "/show/interface/system-name?name=Wireguard0"
		// → segments [show, interface, system-name], leafParams {name: Wireguard0}
		leafKey = last
		leafValue = leafParams
		segments = segments[:len(segments)-1]
	} else if hasTrailingSlash {
		// "/show/interface/" → leaf is "interface", empty params {}
		leafKey = last
		leafValue = map[string]any{}
		segments = segments[:len(segments)-1]
	} else {
		// Общий случай: last — leaf endpoint без параметров.
		// Покрывает 2-seg ("/show/version", "/show/running-config"),
		// 3-seg не-interface ("/show/ip/hotspot"),
		// 4+-seg ("/show/rc/ip/host", "/show/sc/dns-proxy/route", …).
		leafKey = last
		leafValue = map[string]any{}
		segments = segments[:len(segments)-1]
	}

	// unwrapKeys = segments + leafKey (по этим ключам walk'аем response
	// чтобы достать content — параметр-значения вроде "Wireguard0" в
	// unwrap path НЕ входят, т.к. NDMS не оборачивает по name).
	unwrapKeys := make([]string, 0, len(segments)+1)
	unwrapKeys = append(unwrapKeys, segments...)
	unwrapKeys = append(unwrapKeys, leafKey)

	tree := map[string]any{leafKey: leafValue}
	for i := len(segments) - 1; i >= 0; i-- {
		tree = map[string]any{segments[i]: tree}
	}
	return tree, unwrapKeys, nil
}
