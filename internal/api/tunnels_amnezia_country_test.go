package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// Фикстуры различаются между собой и с нулём: одинаковые «до» и «после»
// сделали бы проверки слепыми.
const (
	countryInRecord  = "pt"   // страна, уже записанная в туннеле
	countryInBody    = " DE " // что присылает мастер (нормализует служба)
	tunnelNameBefore = "Португалия"
	tunnelNameAfter  = "Германия"
)

// Страна — поле подписки, а не карточки: PATCH её не правит. Иначе метка
// развязалась бы с конфигурацией, которую описывает, и осталась бы на
// туннеле после замены конфига чужим файлом.
func TestTunnelUpdate_IgnoresAmneziaCountryFromBody(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	if err := store.Create(&storage.AWGTunnel{
		ID: "awg10", Name: tunnelNameBefore, AmneziaCountry: countryInRecord,
		Interface: storage.AWGInterface{Address: "10.0.0.2/32"},
		Peer:      storage.AWGPeer{Endpoint: "1.2.3.4:51820"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h.Update(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost,
		"/tunnels/update?id=awg10",
		strings.NewReader(`{"name":"`+tunnelNameAfter+`","amneziaCountry":"de"}`)))

	saved, err := store.Get("awg10")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if saved.AmneziaCountry != countryInRecord {
		t.Errorf("amneziaCountry = %q, want %q: карточка страну не правит", saved.AmneziaCountry, countryInRecord)
	}
	// Полнота: «не правит страну» не должно означать «не применяет тело».
	if saved.Name != tunnelNameAfter {
		t.Errorf("имя = %q, want %q: остальные присланные поля обязаны примениться", saved.Name, tunnelNameAfter)
	}
}

// Замена конфигурации несёт страну В СЛУЖБУ (там она ляжет тем же мутатором,
// что и конфиг), а не сохраняется хендлером отдельно. Тело без поля значит
// «замена не из мастера» — метку снять, поэтому опция непустая и несёт
// пустую строку, а не nil.
func TestTunnelReplaceConf_PassesCountryToService(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"замена файлом: метку снять", `{"content":"[Interface]\nAddress = 10.0.0.2/32\n"}`, ""},
		{"замена из мастера", `{"content":"[Interface]\nAddress = 10.0.0.2/32\n","amneziaCountry":"` + countryInBody + `"}`, countryInBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubTunnelSvc{getFn: func(context.Context, string) (*service.TunnelWithStatus, error) {
				return &service.TunnelWithStatus{ID: "awg10", Name: tunnelNameBefore}, nil
			}}
			h, store := newTunnelsUpdateHarness(t, stub)
			if err := store.Create(&storage.AWGTunnel{
				ID: "awg10", Name: tunnelNameBefore, AmneziaCountry: countryInRecord,
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}

			rec := httptest.NewRecorder()
			h.ReplaceConf(rec, httptest.NewRequest(http.MethodPost,
				"/tunnels/replace?id=awg10", strings.NewReader(tc.body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("код = %d, тело: %s", rec.Code, rec.Body.String())
			}

			if stub.replaceOpts.AmneziaCountry == nil {
				t.Fatal("опция страны nil: замена конфигурации обязана сказать службе, что делать с меткой")
			}
			if got := *stub.replaceOpts.AmneziaCountry; got != tc.want {
				t.Errorf("опция страны = %q, want %q", got, tc.want)
			}
			// Хендлер сам запись не трогает: служба-заглушка ничего не
			// сохраняла, поэтому прежнее значение обязано уцелеть. Мутация
			// «дописать страну вторым store.Update после ReplaceConfig»
			// краснеет здесь — это лишняя запись на флеш.
			saved, err := store.Get("awg10")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if saved.AmneziaCountry != countryInRecord {
				t.Errorf("запись правил сам хендлер: amneziaCountry = %q, want %q (служба-заглушка не пишет)",
					saved.AmneziaCountry, countryInRecord)
			}
		})
	}
}

// Импорт: страна из тела обязана уехать в СОЗДАНИЕ записи (ImportLink), а
// тело без неё — оставить туннель без метки.
func TestImportConf_PassesAmneziaCountryToImport(t *testing.T) {
	t.Run("страна из мастера", func(t *testing.T) {
		h, svc, _ := importObfHarness(t)
		postImport(t, h, ImportConfRequest{
			Content: "[Interface]\nAddress = 10.0.0.2/32\n", Name: "de",
			AmneziaCountry: countryInBody,
		})
		if svc.link.AmneziaCountry != countryInBody {
			t.Fatalf("ImportLink.AmneziaCountry = %q, want %q", svc.link.AmneziaCountry, countryInBody)
		}
	})
	t.Run("импорт файла метки не ставит", func(t *testing.T) {
		h, svc, _ := importObfHarness(t)
		postImport(t, h, ImportConfRequest{
			Content: "[Interface]\nAddress = 10.0.0.2/32\n", Name: tunnelNameBefore,
		})
		if svc.link.AmneziaCountry != "" {
			t.Fatalf("ImportLink.AmneziaCountry = %q, want пусто", svc.link.AmneziaCountry)
		}
	})
}

// Детальный ответ карточки.
func TestBuildTunnelResponse_CarriesAmneziaCountry(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID: "awg10", Name: tunnelNameBefore, AmneziaCountry: countryInRecord,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := &viewStubSvc{got: &service.TunnelWithStatus{ID: "awg10", Name: tunnelNameBefore}}

	resp, err := BuildTunnelResponse(httptest.NewRequest(http.MethodGet, "/api/tunnels/awg10", nil),
		svc, store, "awg10", time.Time{})
	if err != nil {
		t.Fatalf("BuildTunnelResponse: %v", err)
	}
	if got, _ := resp["amneziaCountry"].(string); got != countryInRecord {
		t.Fatalf("amneziaCountry = %v, want %q", resp["amneziaCountry"], countryInRecord)
	}
}

// Список. Отдельный тест от детального ответа намеренно: мастер читает
// ИМЕННО список (главная страница), и метка страны, доехавшая только до
// карточки, не появится в мастере никогда.
func TestList_CarriesAmneziaCountry(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID: "awg10", Name: tunnelNameBefore, AmneziaCountry: countryInRecord,
		Interface: storage.AWGInterface{Address: "10.0.0.2/32"},
		Peer:      storage.AWGPeer{Endpoint: "1.2.3.4:51820"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := &TunnelsHandler{
		store: store,
		svc:   &listSvcStub{tunnels: []service.TunnelWithStatus{{ID: "awg10", Name: tunnelNameBefore}}},
	}

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/api/tunnels/list", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Data []struct {
			AmneziaCountry string `json:"amneziaCountry"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].AmneziaCountry != countryInRecord {
		t.Fatalf("amneziaCountry в списке = %q, want %q", resp.Data[0].AmneziaCountry, countryInRecord)
	}
}
