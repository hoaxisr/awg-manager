package dnsroute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// Кэш store меняется только успешной записью (F695): мутатор, получивший
// ошибку записи, не оставляет правку в живом кэше — иначе её довозят до
// роутера следующая сверка и до диска следующий успешный Save.

// newFailingWriteService — сервис с двумя списками; возвращает функцию,
// ломающую запись: путь файла уводится под обычный файл, MkdirAll в
// AtomicWrite получает ENOTDIR (работает и под root, в отличие от chmod).
func newFailingWriteService(t *testing.T) (svc *ServiceImpl, poster *fakePoster, fg *query.FakeGetter, breakWrite, fixWrite func()) {
	t.Helper()
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	q, c, poster, fg := newTestNDMS()
	svc = &ServiceImpl{store: store, queries: q, commands: c}
	ctx := context.Background()
	for _, name := range []string{"alpha", "beta"} {
		_, err := svc.Create(ctx, DomainList{
			Name:          name,
			ManualDomains: []string{name + ".example"},
			Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
		})
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	good := store.path
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	breakWrite = func() { store.path = filepath.Join(blocker, "dns-routes.json") }
	fixWrite = func() { store.path = good }
	return svc, poster, fg, breakWrite, fixWrite
}

// snapshot — глубокий снимок кэша: правка на месте видна при сравнении.
func snapshot(t *testing.T, s *Store) string {
	t.Helper()
	raw, err := json.Marshal(s.GetCached())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestFailedWrite_LeavesCacheUntouched(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(svc *ServiceImpl) error
	}{
		{"Create", func(svc *ServiceImpl) error {
			_, err := svc.Create(ctx, DomainList{Name: "ghost", ManualDomains: []string{"ghost.example"}})
			return err
		}},
		{"Update", func(svc *ServiceImpl) error {
			_, err := svc.Update(ctx, DomainList{ID: "list_1", Name: "renamed", ManualDomains: []string{"other.example"}})
			return err
		}},
		{"UpdateRoutesOmitted", func(svc *ServiceImpl) error {
			// Маршруты не присланы: Update берёт срез Routes записи и
			// resolveRouteInterfaces пишет Interface в него на месте.
			svc.resolver = &stubResolver{kernelByTunnel: map[string]string{"t1": "OpkgTun7"}}
			_, err := svc.Update(ctx, DomainList{ID: "list_1", Name: "renamed"})
			return err
		}},
		{"Delete", func(svc *ServiceImpl) error { return svc.Delete(ctx, "list_1") }},
		{"DeleteBatch", func(svc *ServiceImpl) error {
			_, err := svc.DeleteBatch(ctx, []string{"list_1"})
			return err
		}},
		{"SetEnabled", func(svc *ServiceImpl) error { return svc.SetEnabled(ctx, "list_1", false) }},
		{"CreateBatch", func(svc *ServiceImpl) error {
			_, err := svc.CreateBatch(ctx, []DomainList{
				{Name: "ghost1", ManualDomains: []string{"ghost1.example"}},
				{Name: "ghost2", ManualDomains: []string{"ghost2.example"}},
			})
			return err
		}},
		{"OnTunnelDelete", func(svc *ServiceImpl) error { return svc.OnTunnelDelete(ctx, "t1") }},
		{"setHRIcon", func(svc *ServiceImpl) error {
			return svc.setHRIcon("Wireguard0", "group", "https://1.1.1.1/i.png")
		}},
		{"RefreshSubscriptions", func(svc *ServiceImpl) error {
			// Неудачная загрузка пишет LastError в Subscriptions[i] на месте.
			svc.SetDownloader(failingDownloader{err: errors.New("i/o timeout")})
			return svc.RefreshSubscriptions(ctx, "list_1")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, poster, _, breakWrite, _ := newFailingWriteService(t)
			if tc.name == "RefreshSubscriptions" {
				d := svc.store.Draft()
				d.Lists[0].Subscriptions = []Subscription{{URL: "https://1.1.1.1/l.txt"}}
				if err := svc.store.Save(d); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, svc.store)
			posts := len(poster.Payloads())
			breakWrite()

			if err := tc.call(svc); err == nil {
				t.Fatal("ожидалась ошибка записи")
			}
			if after := snapshot(t, svc.store); after != before {
				t.Fatalf("кэш изменён без записи:\nдо:    %s\nпосле: %s", before, after)
			}
			// Холостое для этого дефекта: мутаторы выходят по ошибке Save до сверки; утечку до роутера ловит TestFailedWrite_NotResurrectedByLaterSave.
			if got := len(poster.Payloads()); got != posts {
				t.Fatalf("после отказа записи ушли POST: %d → %d", posts, got)
			}
		})
	}
}

// TestFailedWrite_NotResurrectedByLaterSave — отвергнутый Create не доезжает
// ни до диска со следующим успешным Save чужой правки, ни до роутера со
// следующей сверкой.
func TestFailedWrite_NotResurrectedByLaterSave(t *testing.T) {
	ctx := context.Background()
	svc, poster, fg, breakWrite, fixWrite := newFailingWriteService(t)
	// Цель подтверждена: сверка ставит группы и строки маршрутов.
	fg.SetJSON("/show/interface/", `{"OpkgTun0":{"id":"OpkgTun0","type":"OpkgTun"}}`)
	breakWrite()
	if _, err := svc.Create(ctx, DomainList{
		Name:          "ghost",
		ManualDomains: []string{"ghost.example"},
		Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
	}); err == nil {
		t.Fatal("ожидалась ошибка записи")
	}
	fixWrite()

	posts := len(poster.Payloads())
	if err := svc.SetEnabled(ctx, "list_1", false); err != nil {
		t.Fatal(err)
	}

	for _, l := range svc.store.GetCached().Lists {
		if l.Name == "ghost" {
			t.Fatal("отвергнутый список в кэше")
		}
	}
	reloaded := NewStore(filepath.Dir(svc.store.path))
	data, err := reloaded.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range data.Lists {
		if l.Name == "ghost" {
			t.Fatal("отвергнутый список записан на диск чужим Save")
		}
	}
	for _, p := range poster.Payloads()[posts:] {
		if strings.Contains(fmt.Sprint(p), "ghost") {
			t.Fatalf("отвергнутый список ушёл на роутер: %v", p)
		}
	}
}
