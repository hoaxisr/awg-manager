package wdttlink

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// LinkRequest — тело ручки ссылки, СУПЕРСЕТ полей обоих старых DTO. json-имена
// вербатим: `peer`/`vkHashes`/`name`/`password` — со старого
// api.WdttGenerateLinkRequest (wdtt_server.go:106-111);
// `provider`/`mtu`/`wg`/`clientId`/`n`/`streamsPerCred`/`transport`/`serverId` —
// со старого api.GenerateLinkRequest freeturn (freeturn.go:249-258; их читает
// реализация freeturn); `name` общее для обоих.
//
// Mode — единственное НОВОЕ поле (§11): режим ссылки wg|raw, независимый от
// RelayMode записи. Пусто — режим записи.
type LinkRequest struct {
	Peer     string   `json:"peer,omitempty"`
	VKHashes []string `json:"vkHashes,omitempty"`
	Name     string   `json:"name,omitempty"`
	Password string   `json:"password,omitempty"`
	Mode     string   `json:"mode,omitempty"`

	Provider       string `json:"provider,omitempty"`
	MTU            int    `json:"mtu,omitempty"`
	WG             string `json:"wg,omitempty"`
	ClientID       string `json:"clientId,omitempty"`
	N              int    `json:"n,omitempty"`
	StreamsPerCred int    `json:"streamsPerCred,omitempty"`
	Transport      string `json:"transport,omitempty"`
	ServerID       string `json:"serverId,omitempty"`
}

// LinkBuilder — сборщик ссылки для ОДНОЙ роли. Ручка ссылки одна на все роли
// (шов Г-8 п. 1): диспетчер по rec.Kind собирает проводка, реализация freeturn
// живёт в своём пакете. Возвращаемое тело — форма ответа своей подсистемы
// (у wdtt: {link, linkQwdtt, peer}), поэтому any, а не общий тип: сводить
// разные формы к одной значило бы менять контракт фронта.
type LinkBuilder interface {
	BuildLink(ctx context.Context, rec instancestore.Record, req LinkRequest) (any, error)
}

// LinkError — отказ сборки ссылки со СВОИМ кодом ответа. Коды — вербатим
// старые: фронт и пользователь видят прежние тексты и коды.
type LinkError struct {
	Code string
	Msg  string
}

func (e *LinkError) Error() string { return e.Msg }

// UnusableReason — почему wdtt-server не примет пароль абонента. Значения —
// вербатим строки старого wdtt.ServerClientReason (passwords_json.go:189-196),
// чтобы прод-реализация переводила их без карты соответствий.
type UnusableReason string

const (
	ReasonUsable        UnusableReason = ""
	ReasonEmptyPassword UnusableReason = "empty_password"
	ReasonMainPassword  UnusableReason = "main_password"
	ReasonExpired       UnusableReason = "expired"
)

// UserVetting — предикат пригодности абонента сервера. Прод-реализация живёт
// там же, где перенесённый passwords_json.go (proxyapp/wdttusers): предикат
// ОДИН на всех потребителей — по нему абоненты уезжают в passwords.json, по
// нему же выдаётся ссылка. Своей копии правила здесь быть не должно: проверка
// по всему списку была бы мягче, и ссылка на просроченного абонента
// собралась бы без единой жалобы и молча не подключилась.
type UserVetting interface {
	UsableUsers(users []instancestore.ServerUser, mainPassword string, now time.Time) []instancestore.ServerUser
	UnusableReason(u instancestore.ServerUser, mainPassword string, now time.Time) UnusableReason
}

// BuilderDeps — зависимости сборщика ссылок wdtt-сервера.
type BuilderDeps struct {
	// Vetting обязателен: без него пригодность абонента не проверить, и
	// ссылка выдавалась бы на любой пароль. Отсутствие — отказ, не пропуск.
	Vetting UserVetting
	// Mutator — персист адреса последней ссылки (Record.LinkPeer).
	Mutator Mutator
	// ExternalIP — внешний адрес роутера, когда peer не задан ни запросом,
	// ни записью.
	ExternalIP func(ctx context.Context) (string, error)
	Now        func() time.Time
}

// Builder — ссылки wdtt-сервера (wdtt:// для роутера, qwdtt:// для телефона).
type Builder struct{ deps BuilderDeps }

func NewBuilder(d BuilderDeps) *Builder { return &Builder{deps: d} }

func (b *Builder) now() time.Time {
	if b.deps.Now != nil {
		return b.deps.Now()
	}
	return time.Now()
}

// BuildLink собирает пару ссылок абоненту. Порядок и тексты отказов —
// перенос generateLinkCore (api/wdtt_server.go:441-494).
func (b *Builder) BuildLink(ctx context.Context, rec instancestore.Record, req LinkRequest) (any, error) {
	cfg, err := rec.WdttServerConfig()
	if err != nil {
		return nil, &LinkError{Code: "WDTT_SERVER_NOT_FOUND", Msg: err.Error()}
	}
	if strings.TrimSpace(cfg.Password) == "" {
		return nil, &LinkError{Code: "WDTT_SERVER_NO_PASSWORD",
			Msg: "укажите пароль сервера перед генерацией ссылки"}
	}

	linkPassword, err := b.linkPasswordFor(req, rec, cfg.Password)
	if err != nil {
		return nil, &LinkError{Code: "WDTT_LINK_NO_CLIENT", Msg: err.Error()}
	}

	// §11: режим ссылки задаёт запрос; пусто — режим записи. От режима зависит
	// ТОЛЬКО порт в peer и пометка mode=raw в qwdtt://.
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = cfg.RelayMode
	}
	mode = normalizeConnMode(mode)
	linkPort := LinkListenPortForMode(cfg, mode)

	// Адрес: запрос → память записи (LinkPeer) → внешний IP роутера.
	peer := strings.TrimSpace(req.Peer)
	if peer == "" {
		peer = strings.TrimSpace(rec.LinkPeer)
	}
	if peer == "" {
		ip, ipErr := b.externalIP(ctx)
		if ipErr != nil {
			return nil, &LinkError{Code: "WDTT_EXTERNAL_IP_FAILED",
				Msg: "Не удалось определить внешний IP: " + ipErr.Error() + ". Укажите peer вручную."}
		}
		peer = ip
	}
	if !strings.Contains(peer, ":") {
		peer = peer + ":" + strconv.Itoa(linkPort)
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Router WDTT"
	}

	hashes := req.VKHashes
	if len(hashes) == 0 {
		hashes = splitHashes(rec.LinkVKHashes)
	}

	link, err := EncodeLink(peer, cfg.WgPort, linkPassword, hashes, name)
	if err != nil {
		return nil, &LinkError{Code: "WDTT_LINK_ENCODE_FAILED", Msg: err.Error()}
	}
	qLink, err := EncodeQwdttLink(peer, linkPassword, hashes, name, 0, 0, mode)
	if err != nil {
		return nil, &LinkError{Code: "WDTT_LINK_ENCODE_FAILED", Msg: err.Error()}
	}

	b.persistPeer(ctx, rec, peer)

	return map[string]string{
		"link":      link,
		"linkQwdtt": qLink,
		"peer":      peer,
	}, nil
}

func (b *Builder) externalIP(ctx context.Context) (string, error) {
	if b.deps.ExternalIP == nil {
		return "", errors.New("определение внешнего адреса не подключено")
	}
	return b.deps.ExternalIP(ctx)
}

// persistPeer запоминает адрес последней ссылки В ЗАПИСИ, чтобы ссылка
// восстанавливалась без повторного ввода WAN-адреса. Правка ПО МЕСТУ:
// пересборка записи литералом потеряла бы абонентов и слоты адресов.
//
// Отказ записи НЕ роняет ответ (паритет старого фронта, LinkPanel.svelte:84-88:
// «не критично: ссылка уже показана»): ссылка собрана и годна, а память об
// адресе — удобство. Хеши абонента сюда НЕ пишутся сознательно (W-33 фронта):
// vkHash принадлежит абоненту, и попав в параметры сервера, он уехал бы в
// ссылку следующего абонента.
func (b *Builder) persistPeer(ctx context.Context, rec instancestore.Record, peer string) {
	if b.deps.Mutator == nil || peer == "" || peer == strings.TrimSpace(rec.LinkPeer) {
		return
	}
	_ = b.deps.Mutator.Update(ctx, rec.Key(), func(r *instancestore.Record) error {
		r.LinkPeer = peer
		return nil
	})
}

// linkPasswordFor выбирает пароль ссылки. Главный пароль сервера — ключ
// администрирования (X-Admin-Password у admin-API форка), в ссылку он не
// попадает ни при каких условиях, поэтому пароль обязан принадлежать списку
// абонентов сервера.
//
// Членство считается по UserVetting — ровно по тому предикату, по которому
// абоненты уезжают в passwords.json и по которому сервер собирает wrap-ключи.
// Проверка по всему списку записи была бы мягче: ссылка на просроченного
// абонента собралась бы без единой жалобы и молча не подключилась.
func (b *Builder) linkPasswordFor(req LinkRequest, rec instancestore.Record, mainPassword string) (string, error) {
	if b.deps.Vetting == nil {
		// Fail-closed: без предиката пригодность абонента не проверить, а
		// выдать ссылку «на всякий пароль» хуже отказа.
		return "", errors.New("проверка абонентов не подключена")
	}
	now := b.now()
	usable := b.deps.Vetting.UsableUsers(rec.Users, mainPassword, now)
	if len(usable) == 0 {
		return "", errors.New("у сервера нет ни одного рабочего абонента: заведите абонента и повторите")
	}

	pass := strings.TrimSpace(req.Password)
	if pass == "" {
		return "", errors.New("выберите абонента: ссылка выдаётся на пароль абонента, а не на главный пароль сервера")
	}
	for _, u := range usable {
		// Пароль из UsableUsers уже подрезан — трим тут не нужен.
		if u.Password == pass {
			return pass, nil
		}
	}

	// Пароль не рабочий. Причину спрашиваем у классификатора — того же, на
	// котором построен предикат. Выводить её исключением («не пустой, не
	// главный, значит просрочен») нельзя: вычитание исчерпывающе только для
	// сегодняшнего набора условий, а четвёртое дало бы уверенный ложный текст.
	known := false
	target := instancestore.ServerUser{Password: pass}
	for _, u := range rec.Users {
		if strings.TrimSpace(u.Password) == pass {
			target, known = u, true
			break
		}
	}
	return "", errors.New(linkRejectMessage(b.deps.Vetting.UnusableReason(target, mainPassword, now), known))
}

// linkRejectMessage переводит причину непригодности в текст отказа.
// «Пароля нет в списке» проверяется ПОСЛЕ главного пароля: главный в списке
// абонентов не лежит, а сказать про него надо именно про него.
func linkRejectMessage(reason UnusableReason, knownClient bool) string {
	switch {
	case reason == ReasonMainPassword:
		return "это главный пароль сервера: он остаётся ключом администрирования, ссылка выдаётся на пароль абонента"
	case !knownClient:
		return "пароль не принадлежит ни одному абоненту сервера"
	case reason == ReasonExpired:
		return "абонент просрочен, ссылка не будет работать: заведите нового абонента"
	default:
		// Причина, которой у текстов ещё нет: новое условие пригодности.
		// Общий отказ честнее уверенного «просрочен».
		return "абонент непригоден для ссылки: заведите нового абонента"
	}
}
