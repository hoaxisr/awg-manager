package openfluxlink

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	p := LinkPayload{
		V:         1,
		Role:      "client",
		Transport: roles.OpenFluxTransportYandex,
		URL:       "https://docs.example.com/doc",
		MaxToken:  "tok",
		MaxUID:    "uid",
		Codec:     roles.OpenFluxCodecLegacy,
		Key:       "s3cret",
		Name:      "тест",
	}
	link, err := EncodeLink(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, LinkScheme) {
		t.Fatalf("ссылка без префикса: %q", link)
	}
	got, err := DecodeLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("round trip: %+v, ждали %+v", got, p)
	}
	// Стандартный алфавит с паддингом — тоже читается (ссылку копируют
	// руками, хвост теряется по-разному).
	got2, err := DecodeLink(strings.TrimSuffix(link, "=") + "=")
	if err != nil {
		t.Fatalf("алфавит с паддингом не разобран: %v", err)
	}
	if got2.URL != p.URL || got2.Key != p.Key {
		t.Fatalf("паддинг исказил поля: %+v", got2)
	}
}

func TestDecodeLinkRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "   ", "openflux://", "не base64 !!!"} {
		if _, err := DecodeLink(bad); err == nil {
			t.Fatalf("мусор %q обязана быть отказом", bad)
		}
	}
}

func TestBuildLinkFromServerRecord(t *testing.T) {
	rec := instancestore.Record{ID: "srv", Kind: instancestore.KindOpenFluxServer, Name: "выход",
		OpenFluxServer: &roles.OpenFluxServerConfig{
			Transport:     roles.OpenFluxTransportMailru,
			URL:           "https://cloud.mail.example/pub/AbC",
			EncryptionKey: "k",
			Mode:          roles.OpenFluxModeL3,
		}}
	res, err := NewBuilder().BuildLink(nil, rec, wdttlink.LinkRequest{})
	if err != nil {
		t.Fatal(err)
	}
	share, ok := res.(ShareLink)
	if !ok {
		t.Fatalf("форма ответа: %T", res)
	}
	if share.Transport != "mailru" || share.URL != rec.OpenFluxServer.URL {
		t.Fatalf("канал не из конфига ноды: %+v", share)
	}
	if !share.Encrypted {
		t.Fatal("ключ в конфиге есть, признака шифрования нет")
	}
	if share.Mode != "l3" {
		t.Fatalf("режим %q, ждали l3", share.Mode)
	}
	if !strings.Contains(share.ClientCommand, "--transport=mailru") ||
		!strings.Contains(share.ClientCommand, "--encryption-key=k") {
		t.Fatalf("команда клиента неполна: %q", share.ClientCommand)
	}
	if strings.Contains(share.ClientCommand, "--mode=") {
		t.Fatalf("режим выхода уехал в команду клиента (клиент бэкенд не выбирает): %q", share.ClientCommand)
	}
	// Ссылка декодируется обратно в конфиг клиента.
	payload, err := DecodeLink(share.Link)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ClientConfigFromPayload(payload)
	if cfg.Transport != "mailru" || cfg.EncryptionKey != "k" {
		t.Fatalf("конфиг из ссылки: %+v", cfg)
	}
}

func TestBuildLinkRejectsForeignKind(t *testing.T) {
	rec := instancestore.Record{ID: "s", Kind: instancestore.KindWdttServer,
		WdttServer: &roles.WdttServerConfig{}}
	_, err := NewBuilder().BuildLink(nil, rec, wdttlink.LinkRequest{})
	linkErr, ok := err.(*wdttlink.LinkError)
	if !ok || linkErr.Code != "OPENFLUX_SERVER_NOT_FOUND" {
		t.Fatalf("чужая роль обязана дать LinkError с кодом роли: %v", err)
	}
}
