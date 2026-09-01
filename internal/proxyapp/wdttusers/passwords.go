// Package wdttusers — абоненты wdtt-сервера: материализация passwords.json,
// усыновление записей, заведённых мимо менеджера, ручки users и увод журнала
// статистики форка с флеш-памяти.
//
// Перенос из internal/wdtt (passwords_json.go, server_clients.go,
// server_stats_log.go). Источник правды по составу абонентов —
// instancestore.Record.Users; passwords.json в ConfigDir — производная, но
// производная СЛИЯНИЕМ: серверные поля записи (устройства, счётчики, лимиты,
// деактивация) принадлежат форку и переносятся как есть.
package wdttusers

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// Адреса шлюзов половин сервера — константы старого мира
// (internal/wdtt/types.go:182,193). 10.66.0.1 — NDMS на WG-OpkgTun, 10.70.0.1 —
// на raw-OpkgTun; оба локальны на самом интерфейсе, и абонент, получивший такой
// адрес, молча остаётся без связи.
const (
	wdttServerGatewayAddr = "10.66.0.1"
	rawServerGatewayAddr  = "10.70.0.1"
)

// passwordsJSON mirrors SpaceNeuroX monolith passwords.json (minimal headless subset).
type passwordsJSON struct {
	MainPassword string                       `json:"main_password"`
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
// Условия «raw зарегистрирован в NDMS» здесь нет намеренно: 10.70.0.1
// зарезервирован всегда — его же пропускает getNextRawIP форка.
func sanitizePasswordsDevices(devices map[string]any) (map[string]any, bool) {
	if len(devices) == 0 {
		return devices, false
	}
	out := make(map[string]any, len(devices))
	changed := false
	for id, entry := range devices {
		dev := deviceAddrsFromPasswordsEntry(entry)
		if dev.IP == wdttServerGatewayAddr || dev.RawIP == rawServerGatewayAddr {
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
		if deviceIPFromPasswordsEntry(entry) == wdttServerGatewayAddr {
			return devices
		}
	}
	devices[gatewayReserveDeviceID] = map[string]any{
		"ip":      wdttServerGatewayAddr,
		"comment": "awg-manager gateway reservation",
	}
	return devices
}

// UsableUsers — абоненты, которых wdtt-server примет. Условие спрашивается у
// UnusableReason и здесь НЕ перечисляется: прежняя редакция называла три
// («непустой, не равный главному, не просроченный»), из которых сегодня живо
// одно, и перечень в двух местах разошёлся ровно так, как и должен был.
// Экспортирована намеренно: тем же предикатом обязан пользоваться сборщик
// ссылок (wdttlink.UserVetting).
//
// Эта функция — единственное место, где такой отбор существует. Её результат
// уезжает в passwords.json, и ровно из него сервер соберёт wrap-ключи
// (refreshWrapKeysFromDBLocked, форк server.go:372-380; отбор там СВОЙ — у
// форка есть срок действия записи, у нашей ServerUser его нет). Инвариант
// «у сервера есть абонент» обязан спрашивать
// её же: «абонентов не ноль» и «ключей не ноль» — разные величины, и
// расхождение между ними даёт log.Fatalf на старте сервера (форк
// server.go:2711-2713).
//
// Пароль в результате уже подрезан: трим — здесь, и внутри конвейера больше
// нигде, иначе ключ файла и ключ сравнения однажды разойдутся.
//
// Своих условий у предиката НЕТ: отбор целиком делает UnusableReason. Так
// причина отказа и сам отбор не могут разойтись — историческое расхождение
// давало пользователю уверенный, но неверный текст причины у абонента,
// непригодного по другой.
func UsableUsers(users []instancestore.ServerUser) []instancestore.ServerUser {
	out := make([]instancestore.ServerUser, 0, len(users))
	for _, u := range users {
		if UnusableReason(u) != wdttlink.ReasonUsable {
			continue
		}
		u.Password = strings.TrimSpace(u.Password)
		out = append(out, u)
	}
	return out
}

// UnusableReason называет ПРИЧИНУ непригодности — ту же, по которой абонент не
// попадает в passwords.json. Потребители причины (тексты отказов сборщика
// ссылок) обязаны спрашивать её, а не выводить причину исключением.
//
// Условие сегодня ровно одно — пустой пароль. Срок действия и деактивацию
// назначал только админ-путь форка, которого у нас нет; главный пароль
// сервера снят как ненужный (он не участвовал ни в WRAP-ключах, ни в
// аутентификации абонента — только в admin-API форка). Новое условие
// пригодности добавляется ЗДЕСЬ, и только здесь.
func UnusableReason(u instancestore.ServerUser) wdttlink.UnusableReason {
	if strings.TrimSpace(u.Password) == "" {
		return wdttlink.ReasonEmptyPassword
	}
	return wdttlink.ReasonUsable
}

// Vetting — реализация wdttlink.UserVetting: предикат ОДИН на всех
// потребителей. Состояния у неё нет, поэтому проводке достаточно значения.
type Vetting struct{}

func (Vetting) UsableUsers(users []instancestore.ServerUser) []instancestore.ServerUser {
	return UsableUsers(users)
}

func (Vetting) UnusableReason(u instancestore.ServerUser) wdttlink.UnusableReason {
	return UnusableReason(u)
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

// preparePasswordsJSONForServer merges записи абонентов с существующим файлом и
// снимает протухшие привязки к IP шлюза перед стартом wdtt-server.
//
// Записи абонентов МЕРЖАТСЯ поверх лежащих в файле: is_deactivated, device_ids,
// max_devices, expires_at, счётчики трафика и ports принадлежат серверу, наши —
// только label и vk_hash.
func preparePasswordsJSONForServer(configDir string, users []instancestore.ServerUser) (passwordsJSON, bool, error) {
	existing, err := loadPasswordsJSON(configDir)
	if err != nil {
		return passwordsJSON{}, false, err
	}
	devices, sanitized := sanitizePasswordsDevices(existing.Devices)
	doc := passwordsJSON{
		Passwords: map[string]passwordsJSONUser{},
		Devices:   devices,
	}
	for _, u := range UsableUsers(users) {
		entry := existing.Passwords[u.Password] // нулевая, если абонента ещё нет
		if label := strings.TrimSpace(u.Comment); label != "" {
			entry.Label = label
		}
		if vk := strings.TrimSpace(u.VkHash); vk != "" {
			entry.VkHash = vk
		}
		doc.Passwords[u.Password] = entry
	}
	// Порядок обязателен: прополка сирот — до резерва шлюза, иначе она снимет
	// сам резерв (владельца у него нет по построению).
	doc.Devices = reserveGatewayIPInDevices(dropOrphanPasswordsDevices(doc.Devices, doc.Passwords))
	return doc, sanitized, nil
}

// syncPasswordsJSON writes passwords.json — the auth source of wdtt-server.
// Второе значение — «вычищены устройства с IP шлюза», для журнала.
func syncPasswordsJSON(configDir string, users []instancestore.ServerUser) (bool, error) {
	dir := strings.TrimSpace(configDir)
	if dir == "" {
		return false, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false, err
	}
	doc, sanitized, err := preparePasswordsJSONForServer(dir, users)
	if err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return sanitized, err
	}
	path := passwordsJSONPath(dir)
	// Запись ТОЛЬКО при отличии. Хранилище роутера — флеш, а материализацию
	// зовут в цикле: путь старта повторяется каждые 30 с, пока инстанс
	// заблокирован (RecheckAfter гейта усыновления), и байт-в-байт та же
	// запись стачивала бы флеш вечно. Тот же принцип, что у ErrNoChange в
	// хранилище записей: «менять нечего — файл не трогаем».
	if cur, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(cur, data) {
		return sanitized, nil
	}
	return sanitized, os.WriteFile(path, data, 0600)
}
