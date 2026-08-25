package ftlink

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// fakeExternalIP считает вызовы и отдаёт заданный ответ: тест проверяет, что
// внешний адрес спрашивают РОВНО когда peer не задан.
type fakeExternalIP struct {
	ip    string
	err   error
	calls int
}

func (f *fakeExternalIP) get(context.Context) (string, error) {
	f.calls++
	return f.ip, f.err
}

func buildLink(t *testing.T, b *Builder, rec instancestore.Record, req wdttlink.LinkRequest) (map[string]string, LinkPayload) {
	t.Helper()
	data, err := b.BuildLink(context.Background(), rec, req)
	if err != nil {
		t.Fatalf("BuildLink: %v", err)
	}
	body, ok := data.(map[string]string)
	if !ok {
		t.Fatalf("форма ответа=%T", data)
	}
	p, err := DecodeLink(body["link"])
	if err != nil {
		t.Fatalf("разбор собранной ссылки: %v", err)
	}
	return body, p
}

// Сборщик — реализация wdttlink.LinkBuilder: ручка ссылки ОДНА на все роли,
// диспетчер по Kind собирает проводка.
var _ wdttlink.LinkBuilder = (*Builder)(nil)

func TestBuildLink_FullRequest(t *testing.T) {
	ext := &fakeExternalIP{ip: "не должен спрашиваться"}
	b := NewBuilder(BuilderDeps{ExternalIP: ext.get})
	rec := ftServerRecord("")

	body, p := buildLink(t, b, rec, wdttlink.LinkRequest{
		Peer: "5.6.7.8:56100", Provider: "vk2", MTU: 1400, ClientID: okClientID,
		Name: "Абонент", N: 3, StreamsPerCred: 4, Transport: "udp",
		WG: "[Interface]\nPrivateKey = x\nMTU = 1376\n",
	})

	want := LinkPayload{
		V: 1, Provider: "vk2", Peer: "5.6.7.8:56100", Transport: "udp", Mode: "udp",
		Obf: "rtpopus2", Key: "aabb", N: 3, StreamsPerCred: 4, MTU: 1400,
		WG: "[Interface]\nPrivateKey = x", ClientID: okClientID, Name: "Абонент",
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("полезная нагрузка ссылки:\n got %+v\nwant %+v", p, want)
	}
	if !reflect.DeepEqual(body, map[string]string{
		"link": body["link"], "peer": "5.6.7.8:56100", "clientId": okClientID,
	}) {
		t.Fatalf("тело ответа=%+v", body)
	}
	if ext.calls != 0 {
		t.Fatalf("peer задан, а внешний адрес спрошен %d раз", ext.calls)
	}
}

func TestBuildLink_Defaults(t *testing.T) {
	ext := &fakeExternalIP{ip: "203.0.113.7"}
	b := NewBuilder(BuilderDeps{ExternalIP: ext.get})
	rec := ftServerRecord("")
	rec.FreeTurnServer.ObfProfile = "none"
	rec.FreeTurnServer.ObfKey = "aabb"

	body, p := buildLink(t, b, rec, wdttlink.LinkRequest{})

	want := LinkPayload{
		V: 1, Provider: "vk", Peer: "203.0.113.7:56000", Transport: "tcp", Mode: "udp",
		N: 10, StreamsPerCred: 10, MTU: 1280,
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("дефолты:\n got %+v\nwant %+v", p, want)
	}
	if body["peer"] != "203.0.113.7:56000" {
		t.Fatalf("peer=%q", body["peer"])
	}
	if ext.calls != 1 {
		t.Fatalf("внешний адрес спрошен %d раз", ext.calls)
	}
}

// Профиль обфускации none выключает и ключ: иначе абонент получил бы ключ без
// профиля и не подключился бы.
func TestBuildLink_ObfNoneDropsKey(t *testing.T) {
	b := NewBuilder(BuilderDeps{ExternalIP: (&fakeExternalIP{ip: "1.1.1.1"}).get})
	rec := ftServerRecord("")
	rec.FreeTurnServer.ObfProfile = ""
	rec.FreeTurnServer.ObfKey = "aabb"
	_, p := buildLink(t, b, rec, wdttlink.LinkRequest{Peer: "1.1.1.1:1"})
	if p.Obf != "" || p.Key != "" {
		t.Fatalf("obf=%q key=%q", p.Obf, p.Key)
	}
}

// Адрес без порта добирает порт из listen сервера.
func TestBuildLink_PeerWithoutPort(t *testing.T) {
	ext := &fakeExternalIP{ip: "не должен спрашиваться"}
	b := NewBuilder(BuilderDeps{ExternalIP: ext.get})
	rec := ftServerRecord("")
	rec.FreeTurnServer.Listen = "0.0.0.0:56123"
	body, p := buildLink(t, b, rec, wdttlink.LinkRequest{Peer: "vpn.example.org"})
	if body["peer"] != "vpn.example.org:56123" || p.Peer != "vpn.example.org:56123" {
		t.Fatalf("peer=%q payload=%q", body["peer"], p.Peer)
	}
	if ext.calls != 0 {
		t.Fatalf("внешний адрес спрошен %d раз", ext.calls)
	}
}

func TestBuildLink_Rejections(t *testing.T) {
	t.Run("чужая роль", func(t *testing.T) {
		b := NewBuilder(BuilderDeps{ExternalIP: (&fakeExternalIP{ip: "1.1.1.1"}).get})
		rec := instancestore.Record{ID: "default", Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9000"}}
		_, err := b.BuildLink(context.Background(), rec, wdttlink.LinkRequest{})
		var le *wdttlink.LinkError
		if !errors.As(err, &le) || le.Code != "FREETURN_SERVER_NOT_FOUND" {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("внешний адрес не определился", func(t *testing.T) {
		b := NewBuilder(BuilderDeps{ExternalIP: (&fakeExternalIP{err: errors.New("нет WAN")}).get})
		_, err := b.BuildLink(context.Background(), ftServerRecord(""), wdttlink.LinkRequest{})
		var le *wdttlink.LinkError
		if !errors.As(err, &le) || le.Code != "FREETURN_EXTERNAL_IP_FAILED" {
			t.Fatalf("err=%v", err)
		}
		if le.Msg != "Не удалось определить внешний IP: нет WAN. Укажите peer вручную." {
			t.Fatalf("текст отказа=%q", le.Msg)
		}
	})

	t.Run("определение адреса не подключено", func(t *testing.T) {
		b := NewBuilder(BuilderDeps{})
		_, err := b.BuildLink(context.Background(), ftServerRecord(""), wdttlink.LinkRequest{})
		var le *wdttlink.LinkError
		if !errors.As(err, &le) || le.Code != "FREETURN_EXTERNAL_IP_FAILED" {
			t.Fatalf("err=%v", err)
		}
		if !strings.Contains(le.Msg, "не подключено") {
			t.Fatalf("текст отказа=%q", le.Msg)
		}
	})
}
