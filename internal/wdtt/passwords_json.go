package wdtt

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// passwordsJSON mirrors SpaceNeuroX monolith passwords.json (minimal headless subset).
type passwordsJSON struct {
	MainPassword string                       `json:"main_password"`
	AdminID      string                       `json:"admin_id,omitempty"`
	BotToken     string                       `json:"bot_token,omitempty"`
	Passwords    map[string]passwordsJSONUser `json:"passwords"`
	Devices      map[string]any               `json:"devices"`
}

// passwordsJSONUser повторяет PasswordEntry монолита (server.go форка).
// Наши — только label и vk_hash; остальное принадлежит серверу, мы его
// переносим как есть.
type passwordsJSONUser struct {
	Label         string   `json:"label,omitempty"`
	DeviceID      string   `json:"device_id"`
	DeviceIDs     []string `json:"device_ids"`
	MaxDevices    int      `json:"max_devices"`
	ExpiresAt     int64    `json:"expires_at"`
	DownBytes     int64    `json:"down_bytes"`
	UpBytes       int64    `json:"up_bytes"`
	VkHash        string   `json:"vk_hash,omitempty"`
	Ports         string   `json:"ports,omitempty"`
	IsDeactivated bool     `json:"is_deactivated,omitempty"`
}

type passwordsJSONDevice struct {
	IP    string `json:"ip"`
	RawIP string `json:"raw_ip,omitempty"`
}

func passwordsJSONPath(configDir string) string {
	return filepath.Join(strings.TrimSpace(configDir), "passwords.json")
}

func loadPasswordsJSON(configDir string) (passwordsJSON, error) {
	path := passwordsJSONPath(configDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return passwordsJSON{}, nil
		}
		return passwordsJSON{}, err
	}
	var doc passwordsJSON
	if err := json.Unmarshal(data, &doc); err != nil {
		return passwordsJSON{}, err
	}
	if doc.Passwords == nil {
		doc.Passwords = map[string]passwordsJSONUser{}
	}
	if doc.Devices == nil {
		doc.Devices = map[string]any{}
	}
	return doc, nil
}

// deviceAddrsFromPasswordsEntry достаёт оба адреса устройства: WG (ip) и raw
// (raw_ip). Форма записи в файле бывает трёх видов — карта из чужого JSON,
// наша структура и всё остальное через marshal/unmarshal.
func deviceAddrsFromPasswordsEntry(v any) passwordsJSONDevice {
	trim := func(d passwordsJSONDevice) passwordsJSONDevice {
		d.IP = strings.TrimSpace(d.IP)
		d.RawIP = strings.TrimSpace(d.RawIP)
		return d
	}
	switch d := v.(type) {
	case map[string]any:
		var out passwordsJSONDevice
		if ip, ok := d["ip"].(string); ok {
			out.IP = ip
		}
		if ip, ok := d["raw_ip"].(string); ok {
			out.RawIP = ip
		}
		return trim(out)
	case passwordsJSONDevice:
		return trim(d)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return passwordsJSONDevice{}
		}
		var dev passwordsJSONDevice
		if json.Unmarshal(b, &dev) == nil {
			return trim(dev)
		}
	}
	return passwordsJSONDevice{}
}

func deviceIPFromPasswordsEntry(v any) string {
	return deviceAddrsFromPasswordsEntry(v).IP
}

// sanitizePasswordsDevices drops devices bound to a server gateway IP:
// 10.66.0.1 на WG-половине и 10.70.0.1 на raw. Оба адреса локальны на самом
// интерфейсе — обратный трафик к ним не уходит в туннель, и абонент с таким
// адресом молча без связи. Запись выбрасывается целиком (вместе с ключами):
// устройство переподключится и получит свободный адрес.
//
// Условия «raw зарегистрирован в NDMS» здесь нет намеренно: 10.70.0.1 с этого
// релиза зарезервирован всегда — его же пропускает getNextRawIP форка.
func sanitizePasswordsDevices(devices map[string]any) (map[string]any, bool) {
	if len(devices) == 0 {
		return devices, false
	}
	out := make(map[string]any, len(devices))
	changed := false
	for id, entry := range devices {
		dev := deviceAddrsFromPasswordsEntry(entry)
		if dev.IP == DefaultWdttServerGatewayAddr || dev.RawIP == DefaultRawServerGatewayAddr {
			// Свой резерв снимаем молча: его кладёт reserveGatewayIPInDevices
			// на каждой записи файла, и без этого исключения признак был бы
			// истинен всегда — то есть не значил бы ничего.
			changed = changed || id != gatewayReserveDeviceID
			continue
		}
		out[id] = entry
	}
	return out, changed
}

// gatewayReserveDeviceID — идентификатор НАШЕЙ записи-резерва в devices. Не
// устройство абонента: владельца у неё нет по построению.
const gatewayReserveDeviceID = "__awgm_gateway_reserved__"

// reserveGatewayIPInDevices marks 10.66.0.1 as used so legacy wdtt-server getNextIP
// skips the OpkgTun gateway before the server binary is rebuilt.
func reserveGatewayIPInDevices(devices map[string]any) map[string]any {
	if devices == nil {
		devices = map[string]any{}
	}
	for _, entry := range devices {
		if deviceIPFromPasswordsEntry(entry) == DefaultWdttServerGatewayAddr {
			return devices
		}
	}
	devices[gatewayReserveDeviceID] = map[string]any{
		"ip":      DefaultWdttServerGatewayAddr,
		"comment": "awg-manager gateway reservation",
	}
	return devices
}

// UsableServerClients — абоненты, которых wdtt-server примет: непустой пароль,
// не равный главному, не просроченный. Экспортирована намеренно: тем же
// предикатом обязан пользоваться internal/api при выборе пароля для ссылки.
//
// Эта функция — единственное место, где такой отбор существует. Её результат
// уезжает в passwords.json, и ровно из него сервер соберёт wrap-ключи
// (refreshWrapKeysFromDBLocked, форк server.go:372-380, берёт только
// непросроченные записи). Инвариант «у сервера есть абонент» обязан спрашивать
// её же: «абонентов не ноль» и «ключей не ноль» — разные величины, и
// расхождение между ними даёт log.Fatalf на старте сервера (форк
// server.go:2711-2713).
//
// Пароль в результате уже подрезан: трим — здесь, и внутри конвейера больше
// нигде, иначе ключ файла и ключ сравнения однажды разойдутся.
//
// Своих условий у предиката НЕТ: отбор целиком делает
// ServerClientUnusableReason. Так причина отказа и сам отбор не могут разойтись
// — а именно расхождением получался ложный текст «просрочен» у абонента,
// непригодного по другой причине.
func UsableServerClients(clients []ServerClient, mainPassword string, now time.Time) []ServerClient {
	out := make([]ServerClient, 0, len(clients))
	for _, c := range clients {
		if ServerClientUnusableReason(c, mainPassword, now) != ServerClientUsable {
			continue
		}
		c.Password = strings.TrimSpace(c.Password)
		out = append(out, c)
	}
	return out
}

// ServerClientReason — почему wdtt-server не примет пароль абонента.
// Пустая строка (ServerClientUsable) означает «примет».
type ServerClientReason string

const (
	ServerClientUsable        ServerClientReason = ""
	ServerClientEmptyPassword ServerClientReason = "empty_password"
	ServerClientMainPassword  ServerClientReason = "main_password"
	ServerClientExpired       ServerClientReason = "expired"
)

// ServerClientUnusableReason называет ПРИЧИНУ непригодности — ту же, по которой
// абонент не попадает в passwords.json. Потребители причины (тексты отказов в
// internal/api) обязаны спрашивать её, а не выводить причину исключением:
// вычитание исчерпывающе ровно для сегодняшнего набора условий, и четвёртое
// условие сделало бы старый текст ложным, не сломав ни одного теста.
//
// Новое условие пригодности добавляется ЗДЕСЬ, и только здесь: предикат
// UsableServerClients построен на этой функции.
func ServerClientUnusableReason(c ServerClient, mainPassword string, now time.Time) ServerClientReason {
	pass := strings.TrimSpace(c.Password)
	switch {
	case pass == "":
		return ServerClientEmptyPassword
	case pass == strings.TrimSpace(mainPassword):
		return ServerClientMainPassword
	case c.ExpiresAt != 0 && c.ExpiresAt <= now.Unix():
		// Ноль — бессрочный; иначе сервер принимает запись, пока
		// ExpiresAt > now (isPasswordExpired, форк server.go:460-468).
		return ServerClientExpired
	}
	return ServerClientUsable
}

// dropOrphanPasswordsDevices удаляет устройства, на которые не ссылается ни один
// абонент: сервер таких сирот не чистит, а они мешают удалению абонента снять
// его WG-пир — reloadDB снимает пир только у устройства, ИСЧЕЗНУВШЕГО из devices
// (форк server.go:410-415). Резерв шлюза сюда не попадает: его ставят после
// прополки.
func dropOrphanPasswordsDevices(devices map[string]any, passwords map[string]passwordsJSONUser) map[string]any {
	if len(devices) == 0 {
		return devices
	}
	owned := make(map[string]struct{}, len(passwords))
	for _, user := range passwords {
		if user.DeviceID != "" {
			owned[user.DeviceID] = struct{}{}
		}
		for _, id := range user.DeviceIDs {
			if id != "" {
				owned[id] = struct{}{}
			}
		}
	}
	out := make(map[string]any, len(devices))
	for id, entry := range devices {
		if _, ok := owned[id]; ok {
			out[id] = entry
		}
	}
	return out
}

// preparePasswordsJSONForServer merges wdtt.json clients with existing devices and
// removes stale gateway-IP bindings before wdtt-server starts.
//
// Записи абонентов МЕРЖАТСЯ поверх лежащих в файле: is_deactivated, device_ids,
// max_devices, expires_at, счётчики трафика и ports принадлежат серверу, наши —
// только label и vk_hash.
func preparePasswordsJSONForServer(configDir, mainPassword, adminID, botToken string, clients []ServerClient) (passwordsJSON, bool, error) {
	existing, err := loadPasswordsJSON(configDir)
	if err != nil {
		return passwordsJSON{}, false, err
	}
	devices, sanitized := sanitizePasswordsDevices(existing.Devices)
	doc := passwordsJSON{
		MainPassword: strings.TrimSpace(mainPassword),
		AdminID:      strings.TrimSpace(adminID),
		BotToken:     strings.TrimSpace(botToken),
		Passwords:    map[string]passwordsJSONUser{},
		Devices:      devices,
	}
	for _, c := range UsableServerClients(clients, doc.MainPassword, time.Now()) {
		entry := existing.Passwords[c.Password] // нулевая, если абонента ещё нет
		if label := strings.TrimSpace(c.Comment); label != "" {
			entry.Label = label
		}
		if vk := strings.TrimSpace(c.VkHash); vk != "" {
			entry.VkHash = vk
		}
		if c.ExpiresAt != 0 {
			// Наша память сильнее пустого файла: янитор форка удаляет истёкшую
			// запись, и без этого отозванный доступ стал бы бессрочным. Ветки
			// else здесь быть не должно.
			entry.ExpiresAt = c.ExpiresAt
		}
		doc.Passwords[c.Password] = entry
	}
	// Порядок обязателен: прополка сирот — до резерва шлюза, иначе она снимет
	// сам резерв (владельца у него нет по построению).
	doc.Devices = reserveGatewayIPInDevices(dropOrphanPasswordsDevices(doc.Devices, doc.Passwords))
	return doc, sanitized, nil
}

// syncPasswordsJSON writes passwords.json — the auth source of wdtt-server.
// Второе значение — «вычищены устройства с IP шлюза», для журнала.
func syncPasswordsJSON(configDir, mainPassword, adminID, botToken string, clients []ServerClient) (bool, error) {
	dir := strings.TrimSpace(configDir)
	if dir == "" {
		return false, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false, err
	}
	doc, sanitized, err := preparePasswordsJSONForServer(dir, mainPassword, adminID, botToken, clients)
	if err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return sanitized, err
	}
	return sanitized, os.WriteFile(passwordsJSONPath(dir), data, 0600)
}
