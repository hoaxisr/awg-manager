// Package instancestore — единый store прокси-инстансов обеих подсистем (§9):
// один файл, один писатель, намерение и только намерение (плюс продуктовые
// данные, у которых нет другого дома: абоненты сервера, link-метаданные).
// Кэшей факта нет по построению: RawClientIP/RawClientMTU старого мира не
// имеют поля, куда их можно положить.
package instancestore

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instance"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

type Kind string

const (
	KindWdttClient     Kind = "wdtt-client"
	KindWdttServer     Kind = "wdtt-server"
	KindFreeTurnClient Kind = "freeturn-client"
	KindFreeTurnServer Kind = "freeturn-server"
)

// AllKinds — канонический перечень ролей. Существует ради тестов полноты:
// диспатчи по Kind размазаны по дереву, у каждого есть ветка «прочее», и
// молчаливо неверный ответ там дороже отказа — от осиротевших данных до
// секретов, ушедших в выдачу API открытым текстом. Новая роль обязана попасть
// СЮДА (пин TestAllKinds_Inventory) и быть классифицированной в каждом
// TestKinds_*-тесте.
//
// Охват пинов НЕ полный и полным не заявляется: сегодня закрыты восемь
// диспатчей в `instancestore`, `internal/api` и проводке. Незапиненными
// остаются как минимум `install.Binary`/`PinnedSHA256`
// (`proxyapp/install/install.go`), `Manager.ensurePins`
// (`proxyrt/manager/manager.go`) и `proxyImplRole`
// (`cmd/awg-manager/wiring_proxyrt.go`) — заводя роль, пройтись по ним руками.
var AllKinds = []Kind{
	KindWdttClient,
	KindWdttServer,
	KindFreeTurnClient,
	KindFreeTurnServer,
}

// ServerUser — абонент wdtt-сервера. Источник правды ЗДЕСЬ (посеян из
// ServerConfig.Clients старого wdtt.json — блокер B5 ревью); passwords.json
// в ConfigDir — производная, её собирает proxyapp/wdttusers перед стартом.
// ExpiresAt хранится, чтобы не воскресить отозванный доступ (янитор форка
// удаляет истёкших из passwords.json). Auto — абонента завёл инвариант, не
// человек; вычислить нечем, поэтому хранится (паритет старого ServerClient).
type ServerUser struct {
	Password  string `json:"password"`
	Comment   string `json:"comment,omitempty"`
	VkHash    string `json:"vkHash,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	Auto      bool   `json:"auto,omitempty"`
}

// Record — запись инстанса. Поля экспортированы (сериализация напрямую, Р2);
// целостность держит валидация store — на Load И на Replace, поэтому запись
// с чужим или отсутствующим конфигом за пределами загрузочного пути не живёт.
type Record struct {
	ID        string `json:"id"`
	Kind      Kind   `json:"kind"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"createdAt,omitempty"`

	// SeededFrom — имя старого конфига, из которого запись перенёс посев
	// (`wdtt.json` / `freeturn.json`). Пусто у всего, что завели через UI.
	// Признак принадлежит ЗАПИСИ, а не состоянию файла: `State.SeededFrom`
	// говорит лишь, что посев вообще был, и не отличает перенесённый инстанс
	// от заведённого руками после обновления. UI показывает по нему бейдж
	// «перенесено» — иначе происхождение «дефолтных» инстансов не объяснить.
	SeededFrom string `json:"seededFrom,omitempty"`

	// Sub — URL подписки wdtt-клиента (продуктовые метаданные, proxyapp).
	// У freeturn-клиента подписка — поле КОНФИГА роли (едет в -sub).
	Sub string `json:"sub,omitempty"`

	// PeerWg/PeerRaw — сохранённые адреса VPS обоих режимов wdtt-клиента
	// (Г-1 ревью: фронт восстанавливает адрес при переключении wg↔raw,
	// lib/components/proxy/wdttPeerMode.ts:18-31 — слот неактивного режима терять нельзя).
	// Инвариант «Peer конфига = слот активного режима» держит normalizeRecord
	// (паритет normalizePeers старого мира, wdtt/types.go:52-64).
	PeerWg  string `json:"peerWg,omitempty"`
	PeerRaw string `json:"peerRaw,omitempty"`

	// Продуктовые данные wdtt-сервера (замечание 4 ревью А: в ред. 1
	// терялись молча): абоненты, персист параметров ссылки, режим статистики.
	Users        []ServerUser `json:"users,omitempty"`
	LinkPeer     string       `json:"linkPeer,omitempty"`
	LinkVKHashes string       `json:"linkVkHashes,omitempty"`
	StatsLog     string       `json:"statsLog,omitempty"`

	WdttClient     *roles.WdttClientConfig     `json:"wdttClient,omitempty"`
	WdttServer     *roles.WdttServerConfig     `json:"wdttServer,omitempty"`
	FreeTurnClient *roles.FreeTurnClientConfig `json:"freeturnClient,omitempty"`
	FreeTurnServer *roles.FreeTurnServerConfig `json:"freeturnServer,omitempty"`
}

// Key — глобально-уникальный адрес записи. ID уникален только ВНУТРИ роли:
// старые подсистемы держали раздельные пространства («default» есть и у
// клиента, и у сервера wdtt, и у freeturn), а менять ID при посеве нельзя —
// на id wdtt-клиента завязан ExitID (wdttraw-<id>, правила пользователя), на
// id обоих клиентов — связи туннелей (WdttClientID/FreeTurnClientID).
// Key — для карты инстансов, владельца аллокатора и путей API; ID — для
// ExitID и связей.
func (r Record) Key() string { return string(r.Kind) + ":" + r.ID }

func (r Record) kindMismatch(want Kind) error {
	return fmt.Errorf("инстанс %s: роль %s, а спрошена %s (дефект вызывающего)", r.ID, r.Kind, want)
}

// WdttClientConfig — конфиг ЗНАЧЕНИЕМ (требование 19); имя впрыскивается из
// Record.Name (Р3: один писатель имени, у поля конфига json:"-").
func (r Record) WdttClientConfig() (roles.WdttClientConfig, error) {
	if r.Kind != KindWdttClient || r.WdttClient == nil {
		return roles.WdttClientConfig{}, r.kindMismatch(KindWdttClient)
	}
	c := *r.WdttClient
	c.Name = r.Name
	return c, nil
}

func (r Record) WdttServerConfig() (roles.WdttServerConfig, error) {
	if r.Kind != KindWdttServer || r.WdttServer == nil {
		return roles.WdttServerConfig{}, r.kindMismatch(KindWdttServer)
	}
	return *r.WdttServer, nil
}

func (r Record) FreeTurnClientConfig() (roles.FreeTurnClientConfig, error) {
	if r.Kind != KindFreeTurnClient || r.FreeTurnClient == nil {
		return roles.FreeTurnClientConfig{}, r.kindMismatch(KindFreeTurnClient)
	}
	return *r.FreeTurnClient, nil
}

func (r Record) FreeTurnServerConfig() (roles.FreeTurnServerConfig, error) {
	if r.Kind != KindFreeTurnServer || r.FreeTurnServer == nil {
		return roles.FreeTurnServerConfig{}, r.kindMismatch(KindFreeTurnServer)
	}
	return *r.FreeTurnServer, nil
}

// RawExiter — конфиг за интерфейсом ведомости выходов, ТИПИЗИРОВАННЫЙ switch
// по Kind (требование 16: никаких утверждений к any — шов ред. 1 через
// interface{ NDMSNames() } снят). После валидации store (Load и Replace)
// несоответствие роли и конфига невозможно; nil означает вызов мимо store.
func (r Record) RawExiter() roles.RawExiter {
	switch r.Kind {
	case KindWdttClient:
		c, _ := r.WdttClientConfig()
		return c
	case KindWdttServer:
		c, _ := r.WdttServerConfig()
		return c
	case KindFreeTurnClient:
		c, _ := r.FreeTurnClientConfig()
		return c
	case KindFreeTurnServer:
		c, _ := r.FreeTurnServerConfig()
		return c
	}
	return nil
}

// NDMSNamed — тот же конфиг за интерфейсом ведомости NDMS-имён уборщика
// (instance.DeclaredNDMSNames — план 1 написал её ИМЕННО под этот вызов).
func (r Record) NDMSNamed() instance.NDMSNamed {
	switch r.Kind {
	case KindWdttClient:
		c, _ := r.WdttClientConfig()
		return c
	case KindWdttServer:
		c, _ := r.WdttServerConfig()
		return c
	case KindFreeTurnClient:
		c, _ := r.FreeTurnClientConfig()
		return c
	case KindFreeTurnServer:
		c, _ := r.FreeTurnServerConfig()
		return c
	}
	return nil
}

// DataTarget — данные инстанса на диске и поддерево каталога данных, где им
// место. Поддерево едет рядом с путём потому, что путь без него опасен: поля
// configDir и clientsFile правятся через API как обычные строки, и уборка
// обязана сверить путь не с каталогом данных целиком (там же лежат туннели,
// настройки и данные СОСЕДНИХ инстансов), а со своим поддеревом.
type DataTarget struct {
	Path string
	Root string
}

// FreeTurnAllowlistPath — путь списка разрешённых по умолчанию. Один на две
// стороны: его пишет ftlink при включении списка, а читает уборка при удалении
// инстанса — разъехавшись, они оставили бы файл сиротой.
func FreeTurnAllowlistPath(dataDir, serverID string) string {
	safeID := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, serverID)
	return filepath.Join(dataDir, "freeturn", "allowlist-"+safeID+".json")
}

// DataTargets — данные, которые переживут удаление инстанса, если их не убрать:
// у wdtt-сервера каталог с абонентами и ключами, у freeturn-сервера файл
// списка разрешённых. Без инстанса они мертвы, а убрать их из UI нечем (стенд
// 2026-08-28).
//
// У freeturn-сервера путей ДВА: заданный в конфиге и путь по умолчанию.
// Выключение списка (ftlink.Disable) снимает поле с конфига, не трогая файл, —
// и по одному лишь конфигу файл переставал быть виден навсегда.
//
// Пусто у клиентских ролей: своих данных на диске у них нет.
func (r Record) DataTargets(dataDir string) []DataTarget {
	switch r.Kind {
	case KindWdttServer:
		if r.WdttServer == nil {
			return nil
		}
		return targetsOf("wdtt", r.WdttServer.ConfigDir)
	case KindFreeTurnServer:
		if r.FreeTurnServer == nil {
			return nil
		}
		return targetsOf("freeturn", r.FreeTurnServer.ClientsFile,
			FreeTurnAllowlistPath(dataDir, r.ID))
	}
	return nil
}

// targetsOf — непустые пути без повторов.
func targetsOf(root string, paths ...string) []DataTarget {
	var out []DataTarget
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Дедуп по каноничному виду: у включённого списка, лежащего по
		// умолчанию, оба пути указывают на один файл, и запись о сносе
		// печаталась бы дважды.
		key := filepath.Clean(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, DataTarget{Path: p, Root: root})
	}
	return out
}
