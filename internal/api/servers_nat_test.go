package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/managed"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// natPoster — записывающий RCI-poster для managed.Service.
type natPoster struct {
	mu    sync.Mutex
	posts []string // JSON каждого payload
	// failOn — отказ NDMS на выбранном payload'е. Зовётся ПОСЛЕ записи
	// попытки в posts (тест видит, что запрос ушёл) и вне мьютекса, чтобы
	// колбэк мог сам заглянуть в snapshot и в стор.
	failOn func(payload string) error
}

func (p *natPoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	b, _ := json.Marshal(payload)
	p.mu.Lock()
	p.posts = append(p.posts, string(b))
	fail := p.failOn
	p.mu.Unlock()
	if fail != nil {
		if err := fail(string(b)); err != nil {
			return nil, err
		}
	}
	return json.RawMessage(`{}`), nil
}

// setFailOn ставит отказ NDMS под тем же мьютексом, под которым Post его
// читает: иначе -race ловил бы гонку с таймером SaveCoordinator.
func (p *natPoster) setFailOn(f func(payload string) error) {
	p.mu.Lock()
	p.failOn = f
	p.mu.Unlock()
}

func (p *natPoster) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.posts...)
}

// newServersNATHarness — ServersHandler над фейковым NDMS: один встроенный WG-сервер
// Wireguard0 (description = built-in, чтобы listServers его показал) и два глобальных
// выхода в running-config (цели static NAT для internet-only).
func newServersNATHarness(t *testing.T) (*ServersHandler, *storage.SettingsStore, *natPoster, *busProbe, *managed.Service) {
	t.Helper()
	fg := query.NewFakeGetter()
	// БЕЗ поля "system-name"/"interface-name": при непустом SystemName ResolveSystemName
	// зовёт kernelIfaceExists (interfaces.go:424-426) — хост. Пустой → fetchSystemName
	// через FakeGetter.Post без скрипта → errNoFakeResponse → имя = ndmsID (терпимо).
	fg.SetJSON("/show/interface/", `{
		"Wireguard0":{"id":"Wireguard0","type":"Wireguard","description":"Wireguard VPN Server","state":"up","link":"up","address":"10.9.0.1","mask":"255.255.255.0"}
	}`)
	fg.SetJSON("/show/running-config", `{"message":["interface PPPoE0","    ip global 32767","!","interface Wireguard2","    ip global 32000","!","interface Wireguard1","    ip access-group AWGM_Wireguard1 in","    ip access-group GUEST_ACL in","    ip access-group _WEBADMIN_Wireguard1 in","!"]}`)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	poster := &natPoster{}

	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	// Commands/SaveCoordinator для SetNAT не нужны — RCI идёт напрямую через transport.
	svc := managed.New(poster, nil, queries, nil, store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	h := NewServersHandler(queries, store, nil, nil)
	h.SetManagedService(svc)
	// Commands нужны пути добавления пира (SetNAT идёт мимо них). Debounce и
	// maxWait в час держат save-POST за пределами теста: иначе таймер
	// SaveCoordinator дострелил бы в общий poster уже после проверок.
	h.SetCommands(ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  poster,
		Queries: queries,
		Save:    ndmscommand.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil),
	}))
	p := newBusProbe(t)
	h.SetEventBus(p.bus())
	return h, store, poster, p, svc
}

func postNAT(t *testing.T, h *ServersHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/servers/Wireguard0/nat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.SetNAT(rr, req, "Wireguard0")
	return rr
}

// internet-only пишет список выходов, на которых поставлен static NAT; full/none — очищают его.
func TestServersHandler_SetNAT_InternetOnlyStoresStaticWANs(t *testing.T) {
	h, store, poster, p, _ := newServersNATHarness(t)

	rr := postNAT(t, h, `{"mode":"internet-only"}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	meta, _ := store.GetServerInterfaceMeta("Wireguard0")
	if want := []string{"PPPoE0", "Wireguard2"}; !reflect.DeepEqual(meta.NATStaticWANs, want) {
		t.Fatalf("NATStaticWANs = %v, want %v", meta.NATStaticWANs, want)
	}
	if meta.NATStaticWAN != "" {
		t.Fatalf("legacy NATStaticWAN обязан быть пуст: %q", meta.NATStaticWAN)
	}
	if got, want := p.invalidated(), []string{"servers/server-nat-changed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("публикации = %v, want %v", got, want)
	}
	// RCI: static NAT на оба выхода + снятие обычного NAT — по составу payload'ов.
	joined := strings.Join(poster.snapshot(), "\n")
	for _, want := range []string{`"to-interface":"PPPoE0"`, `"to-interface":"Wireguard2"`, `"no":true`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("нет static NAT на выход %s:\n%s", want, joined)
		}
	}

	// Переключение на full очищает список.
	rr = postNAT(t, h, `{"mode":"full"}`)
	if rr.Code != 200 {
		t.Fatalf("full: code=%d body=%s", rr.Code, rr.Body.String())
	}
	meta, _ = store.GetServerInterfaceMeta("Wireguard0")
	if len(meta.NATStaticWANs) != 0 {
		t.Fatalf("full обязан очистить NATStaticWANs: %v", meta.NATStaticWANs)
	}
}

func TestServersHandler_SetNAT_RejectsBadModeWithoutRCI(t *testing.T) {
	h, _, poster, p, _ := newServersNATHarness(t)
	rr := postNAT(t, h, `{"mode":"sideways"}`)
	pub := p.invalidated()
	if rr.Code != 400 || len(poster.snapshot()) != 0 || len(pub) != 0 {
		t.Fatalf("плохой режим: code=%d posts=%d pub=%v", rr.Code, len(poster.snapshot()), pub)
	}
}
