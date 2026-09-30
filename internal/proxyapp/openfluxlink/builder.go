package openfluxlink

import (
	"context"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// Builder — ссылки openflux:// выходной ноды. Реализация wdttlink.LinkBuilder:
// ручка ссылки ОДНА на все роли (шов Г-8 п. 1), диспетчер по Kind собирает
// проводка.
//
// Внешний адрес роутера сборщику НЕ нужен: у OpenFlux нет входящего порта,
// абонент выходит на тот же релей, что и нода. Совместимость сторон —
// транспорт + канал + кодек + секрет, и всё это в конфиге ноды.
type Builder struct{}

func NewBuilder() *Builder { return &Builder{} }

// ShareLink — форма ответа ручки ссылки: ссылка для мобильных клиентов и
// готовые командные строки для десктопных (README upstream).
type ShareLink struct {
	Link          string `json:"link"`
	Transport     string `json:"transport"`
	URL           string `json:"url,omitempty"`
	Mode          string `json:"mode,omitempty"`
	Codec         string `json:"codec,omitempty"`
	Encrypted     bool   `json:"encrypted"`
	ClientCommand string `json:"clientCommand"`
}

// BuildLink собирает ссылку абоненту-клиенту. Поля заполнителя из запроса не
// берёт: менять канал/секрет абоненту — значит увести его с канала ноды.
func (b *Builder) BuildLink(_ context.Context, rec instancestore.Record, _ wdttlink.LinkRequest) (any, error) {
	cfg, err := rec.OpenFluxServerConfig()
	if err != nil {
		return nil, &wdttlink.LinkError{Code: "OPENFLUX_SERVER_NOT_FOUND", Msg: err.Error()}
	}
	link, err := EncodeLink(LinkPayload{
		V:         1,
		Role:      "client",
		Transport: cfg.Transport,
		URL:       cfg.URL,
		MaxToken:  cfg.MaxToken,
		MaxUID:    cfg.MaxUID,
		Codec:     cfg.Codec,
		Key:       cfg.EncryptionKey,
	})
	if err != nil {
		return nil, &wdttlink.LinkError{Code: "OPENFLUX_LINK_ENCODE_FAILED", Msg: err.Error()}
	}
	return ShareLink{
		Link:          link,
		Transport:     cfg.Transport,
		URL:           cfg.URL,
		Mode:          cfg.NormalizedMode(),
		Codec:         cfg.Codec,
		Encrypted:     strings.TrimSpace(cfg.EncryptionKey) != "",
		ClientCommand: clientCommand(cfg),
	}, nil
}

// clientCommand — командная строка клиента для копирования: у клиента OpenFlux
// на телефоне/ПК нет импорта ссылок upstream, и паритет с «показать повторно»
// держит не ссылка, а готовая команда.
func clientCommand(c roles.OpenFluxServerConfig) string {
	var b strings.Builder
	b.WriteString("openflux --role=client --inbound=socks5")
	if c.Transport != "" {
		b.WriteString(" --transport=" + c.Transport)
	}
	if strings.TrimSpace(c.URL) != "" {
		b.WriteString(" --url=" + c.URL)
	}
	if strings.TrimSpace(c.MaxToken) != "" {
		b.WriteString(" --maxToken=" + c.MaxToken)
	}
	if strings.TrimSpace(c.MaxUID) != "" {
		b.WriteString(" --maxUid=" + c.MaxUID)
	}
	if c.Codec == roles.OpenFluxCodecLegacy {
		b.WriteString(" --codec=legacy")
	}
	if strings.TrimSpace(c.EncryptionKey) != "" {
		b.WriteString(" --encryption-key=" + c.EncryptionKey)
	}
	return b.String()
}
