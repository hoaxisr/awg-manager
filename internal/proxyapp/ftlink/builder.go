package ftlink

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// BuilderDeps — зависимости сборщика ссылок freeturn-сервера.
type BuilderDeps struct {
	// ExternalIP — внешний адрес роутера, когда адреса нет ни в запросе, ни в
	// настройках сервера. Этот путь отдаёт ТОЛЬКО IP: DNS-имя роутера он не
	// вернёт никогда, даже при настроенном KeenDNS, — ради имени и заведена
	// настройка LinkPeer (#933).
	ExternalIP func(ctx context.Context) (string, error)
}

// Builder — ссылки freeturn:// одного сервера. Реализация
// wdttlink.LinkBuilder: ручка ссылки ОДНА на все роли (шов Г-8 п. 1),
// диспетчер по Kind собирает проводка.
type Builder struct{ deps BuilderDeps }

func NewBuilder(d BuilderDeps) *Builder { return &Builder{deps: d} }

// BuildLink собирает ссылку абоненту. Порядок, дефолты и тексты отказов —
// перенос generateLinkCore (api/freeturn.go:706-791).
func (b *Builder) BuildLink(ctx context.Context, rec instancestore.Record, req wdttlink.LinkRequest) (any, error) {
	cfg, err := rec.FreeTurnServerConfig()
	if err != nil {
		return nil, &wdttlink.LinkError{Code: "FREETURN_SERVER_NOT_FOUND", Msg: err.Error()}
	}

	// Адрес: запрос → настройка сервера → внешний IP роутера (#933). Средним
	// звеном была дыра: поле ввода адреса пропало при переезде UI на общую
	// поверхность прокси-рантайма (#814), и любая ссылка после этого получала
	// внешний IP — DNS-имя вписать стало негде, а ExternalIP имён не отдаёт.
	peer := strings.TrimSpace(req.Peer)
	if peer == "" {
		peer = strings.TrimSpace(cfg.LinkPeer)
	}
	if peer == "" {
		ip, ipErr := b.externalIP(ctx)
		if ipErr != nil {
			return nil, &wdttlink.LinkError{Code: "FREETURN_EXTERNAL_IP_FAILED",
				Msg: "Не удалось определить внешний IP: " + ipErr.Error() + ". Укажите адрес сервера в настройках раздачи."}
		}
		peer = ip
	}
	peer = withLinkPort(peer, listenPortOf(cfg.Listen))

	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = "vk"
	}
	mtu := req.MTU
	if mtu == 0 {
		mtu = 1280
	}
	n := req.N
	if n <= 0 {
		n = 10
	}
	spc := req.StreamsPerCred
	if spc <= 0 {
		spc = 10
	}
	transport := strings.TrimSpace(req.Transport)
	if transport == "" {
		transport = "tcp"
	}
	wg := strings.TrimSpace(req.WG)
	if wg != "" {
		wg = StripWGConfMTU(wg)
	}

	// Профиль обфускации none выключает и ключ: ключ без профиля увёз бы
	// абонента в несовпадающую обфускацию.
	obfProfile := cfg.ObfProfile
	obfKey := cfg.ObfKey
	if obfProfile == "" || obfProfile == "none" {
		obfProfile = ""
		obfKey = ""
	}

	link, err := EncodeLink(LinkPayload{
		V:              1,
		Provider:       provider,
		Peer:           peer,
		Transport:      transport,
		Mode:           cfg.Mode,
		Obf:            obfProfile,
		Key:            obfKey,
		N:              n,
		StreamsPerCred: spc,
		MTU:            mtu,
		WG:             wg,
		ClientID:       strings.TrimSpace(req.ClientID),
		Name:           strings.TrimSpace(req.Name),
	})
	if err != nil {
		return nil, &wdttlink.LinkError{Code: "FREETURN_LINK_ENCODE_FAILED", Msg: err.Error()}
	}

	// Ссылку эта ручка НЕ сохраняет: запоминает её внесение в список (#919,
	// F370). Иначе ссылка абонента, которого в список не внесли, оседала бы в
	// файле навсегда — показать её негде, а приватный ключ пира лежал бы там
	// до удаления инстанса.
	//
	// clientId отдаётся ТАКИМ, КАКИМ пришёл (без трима) — форма старого
	// ответа; фронт тримит его сам (ServerAllowlist.svelte:72).
	return map[string]string{"link": link, "peer": peer, "clientId": req.ClientID}, nil
}

func (b *Builder) externalIP(ctx context.Context) (string, error) {
	if b.deps.ExternalIP == nil {
		return "", errors.New("определение внешнего адреса не подключено")
	}
	return b.deps.ExternalIP(ctx)
}

// withLinkPort дописывает порт, если его нет. Признак «есть порт» — разбор
// net.SplitHostPort, а НЕ наличие двоеточия: у голого IPv6 двоеточий много, и
// проверка по символу оставляла такой адрес без порта (F390). Форма `[v6]`
// распознаётся отдельно — SplitHostPort её не разбирает, порта в ней нет.
func withLinkPort(peer, port string) string {
	if _, _, err := net.SplitHostPort(peer); err == nil {
		return peer
	}
	return peer + ":" + port
}

// listenPortOf — хвост после последнего двоеточия: ровно то, что делал старый
// generateLinkCore. Адрес без двоеточия отдаёт себя целиком (паритет).
func listenPortOf(listen string) string {
	if idx := strings.LastIndex(listen, ":"); idx != -1 {
		return listen[idx+1:]
	}
	return listen
}
