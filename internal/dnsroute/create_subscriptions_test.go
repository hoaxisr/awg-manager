package dnsroute

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Create со списком подписок проверяет их ДО записи (F696): загрузка —
// один раз на подписку, запись — одна, отвергнутый список не попадает ни в
// файл, ни в кэш, ни на роутер.

// urlDownloader отдаёт тело по адресу и считает загрузки; onRead зовётся на
// каждой загрузке — тест смотрит, что лежит в файле store в этот момент.
type urlDownloader struct {
	mu     sync.Mutex
	bodies map[string]string
	fail   map[string]error
	calls  map[string]int
	onRead func()
}

func (d *urlDownloader) ReadAll(_ context.Context, req SubscriptionDownloadRequest) ([]byte, SubscriptionDownloadMeta, error) {
	d.mu.Lock()
	d.calls[req.URL]++
	d.mu.Unlock()
	if d.onRead != nil {
		d.onRead()
	}
	if err := d.fail[req.URL]; err != nil {
		return nil, SubscriptionDownloadMeta{}, err
	}
	return []byte(d.bodies[req.URL]), SubscriptionDownloadMeta{ContentType: "text/plain"}, nil
}

// newCreateSubsService — сервис с одним списком и подтверждённой целью
// OpkgTun0: сверка нового списка шлёт POST. Возвращает и содержимое файла
// store до вызова Create.
func newCreateSubsService(t *testing.T, dl *urlDownloader) (*ServiceImpl, *fakePoster, []byte) {
	t.Helper()
	store := NewStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	q, c, poster, fg := newTestNDMS()
	svc := &ServiceImpl{store: store, queries: q, commands: c}
	if _, err := svc.Create(context.Background(), DomainList{
		Name:          "seed",
		ManualDomains: []string{"seed.example"},
		Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
	}); err != nil {
		t.Fatal(err)
	}
	fg.SetJSON("/show/interface/", `{"OpkgTun0":{"id":"OpkgTun0","type":"OpkgTun"}}`)
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	dl.calls = map[string]int{}
	// Пока идёт загрузка, файл не тронут: проверка — до записи.
	dl.onRead = func() {
		now, err := os.ReadFile(store.path)
		if err != nil {
			t.Error(err)
			return
		}
		if !bytes.Equal(now, before) {
			t.Error("store записан до загрузки подписок")
		}
	}
	svc.SetDownloader(dl)
	return svc, poster, before
}

const (
	subURL1 = "https://1.1.1.1/a.txt"
	subURL2 = "https://8.8.8.8/b.txt"
)

func TestCreate_Subscriptions_FetchOnceWriteOnce(t *testing.T) {
	dl := &urlDownloader{bodies: map[string]string{
		subURL1: "a.example\nb.example\n",
		subURL2: "c.example\n9.9.9.0/24\n",
	}}
	svc, poster, _ := newCreateSubsService(t, dl)
	posts := len(poster.Payloads())

	got, err := svc.Create(context.Background(), DomainList{
		Name:          "subs",
		ManualDomains: []string{"m.example"},
		Subscriptions: []Subscription{{URL: subURL1}, {URL: subURL2}},
		Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{subURL1, subURL2} {
		if n := dl.calls[u]; n != 1 {
			t.Errorf("%s загружена %d раз, ждали 1", u, n)
		}
	}

	var stored *DomainList
	for i := range svc.store.GetCached().Lists {
		if l := &svc.store.GetCached().Lists[i]; l.ID == got.ID {
			stored = l
		}
	}
	if stored == nil {
		t.Fatal("список не в кэше")
	}
	if want := "m.example,a.example,b.example,c.example"; strings.Join(stored.Domains, ",") != want {
		t.Errorf("Domains = %v, want %s", stored.Domains, want)
	}
	if want := "9.9.9.0/24"; strings.Join(stored.Subnets, ",") != want {
		t.Errorf("Subnets = %v, want %s", stored.Subnets, want)
	}
	for i, want := range []int{2, 2} {
		sub := stored.Subscriptions[i]
		if sub.LastCount != want || sub.LastError != "" || sub.LastFetched == "" {
			t.Errorf("Subscriptions[%d] = %+v, want LastCount=%d, LastFetched set", i, sub, want)
		}
	}
	if fmt.Sprint(got.Domains) != fmt.Sprint(stored.Domains) {
		t.Errorf("ответ %v расходится с записью %v", got.Domains, stored.Domains)
	}

	// Запись на диске — та же, что в кэше.
	reloaded := NewStore(filepath.Dir(svc.store.path))
	data, err := reloaded.Load()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(data.Lists); n != 2 || strings.Join(data.Lists[1].Domains, ",") != strings.Join(stored.Domains, ",") {
		t.Errorf("на диске %+v, ждали seed и %v", data.Lists, stored.Domains)
	}

	sent := false
	for _, p := range poster.Payloads()[posts:] {
		if strings.Contains(fmt.Sprint(p), "c.example") {
			sent = true
		}
	}
	if !sent {
		t.Error("сверка не довезла домены подписки до роутера")
	}
}

func TestCreate_Subscriptions_RejectedLeavesNothing(t *testing.T) {
	var many strings.Builder
	for i := 0; i <= MaxSubnetsPerList; i++ {
		fmt.Fprintf(&many, "1.%d.%d.0/24\n", i/256, i%256)
	}
	cases := []struct {
		name    string
		dl      *urlDownloader
		wantErr string
	}{
		{"FetchFails", &urlDownloader{
			bodies: map[string]string{subURL1: "a.example\n"},
			fail:   map[string]error{subURL2: errors.New("i/o timeout")},
		}, "i/o timeout"},
		{"TooManySubnets", &urlDownloader{
			bodies: map[string]string{subURL1: "a.example\n", subURL2: many.String()},
		}, "слишком много подсетей"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, poster, before := newCreateSubsService(t, tc.dl)
			cached := snapshot(t, svc.store)
			posts := len(poster.Payloads())

			_, err := svc.Create(context.Background(), DomainList{
				Name:          "subs",
				Subscriptions: []Subscription{{URL: subURL1}, {URL: subURL2}},
				Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			now, rerr := os.ReadFile(svc.store.path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if !bytes.Equal(now, before) {
				t.Error("файл store изменён отвергнутым Create")
			}
			if after := snapshot(t, svc.store); after != cached {
				t.Errorf("кэш изменён отвергнутым Create:\nдо:    %s\nпосле: %s", cached, after)
			}
			if got := len(poster.Payloads()); got != posts {
				t.Errorf("отвергнутый Create отправил POST: %d → %d", posts, got)
			}
		})
	}
}

// Update применяет новую подписку сразу, теми доменами, что скачала
// проверка: одна загрузка на новый адрес, старые не перекачиваются.

// newUpdateSubsService — сервис со списком «subs» из ручных manual и
// подписки subURL1; счётчик загрузок обнулён, страж «файл не тронут» снят.
func newUpdateSubsService(t *testing.T, dl *urlDownloader, manual []string) (*ServiceImpl, *fakePoster, string) {
	t.Helper()
	svc, poster, _ := newCreateSubsService(t, dl)
	created, err := svc.Create(context.Background(), DomainList{
		Name:          "subs",
		ManualDomains: manual,
		Subscriptions: []Subscription{{URL: subURL1}},
		Routes:        []RouteTarget{{Interface: "OpkgTun0", TunnelID: "t1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dl.onRead = nil
	dl.calls = map[string]int{}
	return svc, poster, created.ID
}

func TestUpdate_NewSubscription_AppliedOnce(t *testing.T) {
	dl := &urlDownloader{bodies: map[string]string{
		subURL1: "a.example\n",
		subURL2: "c.example\nd.example\n9.9.9.0/24\n",
	}}
	svc, poster, id := newUpdateSubsService(t, dl, []string{"m.example"})
	posts := len(poster.Payloads())

	got, err := svc.Update(context.Background(), DomainList{
		ID:            id,
		Subscriptions: []Subscription{{URL: subURL1}, {URL: subURL2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := dl.calls[subURL1]; n != 0 {
		t.Errorf("старая подписка перекачана %d раз", n)
	}
	if n := dl.calls[subURL2]; n != 1 {
		t.Errorf("новая подписка загружена %d раз, ждали 1", n)
	}

	var stored *DomainList
	for i := range svc.store.GetCached().Lists {
		if l := &svc.store.GetCached().Lists[i]; l.ID == id {
			stored = l
		}
	}
	if stored == nil {
		t.Fatal("список не в кэше")
	}
	if want := "m.example,a.example,c.example,d.example"; strings.Join(stored.Domains, ",") != want {
		t.Errorf("Domains = %v, want %s", stored.Domains, want)
	}
	if want := "9.9.9.0/24"; strings.Join(stored.Subnets, ",") != want {
		t.Errorf("Subnets = %v, want %s", stored.Subnets, want)
	}
	if sub := stored.Subscriptions[1]; sub.LastCount != 3 || sub.LastFetched == "" || sub.LastError != "" {
		t.Errorf("новая подписка = %+v, want LastCount=3, LastFetched set", sub)
	}
	if fmt.Sprint(got.Domains) != fmt.Sprint(stored.Domains) {
		t.Errorf("ответ %v расходится с записью %v", got.Domains, stored.Domains)
	}

	reloaded := NewStore(filepath.Dir(svc.store.path))
	data, err := reloaded.Load()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(data.Lists); n != 2 || strings.Join(data.Lists[1].Domains, ",") != strings.Join(stored.Domains, ",") {
		t.Errorf("на диске %+v, ждали seed и %v", data.Lists, stored.Domains)
	}

	sent := false
	for _, p := range poster.Payloads()[posts:] {
		if strings.Contains(fmt.Sprint(p), "d.example") {
			sent = true
		}
	}
	if !sent {
		t.Error("сверка не довезла домены новой подписки до роутера")
	}
}

func TestUpdate_NewSubscription_MergedLimitRejected(t *testing.T) {
	// Подсети стоящей подписки и новой порознь в лимите, вместе — за ним.
	half := func(first int) string {
		var b strings.Builder
		for i := 0; i < MaxSubnetsPerList/2+1; i++ {
			fmt.Fprintf(&b, "%d.%d.%d.0/24\n", first, i/256, i%256)
		}
		return b.String()
	}
	dl := &urlDownloader{bodies: map[string]string{subURL1: half(1), subURL2: half(2)}}
	svc, poster, id := newUpdateSubsService(t, dl, []string{"m.example"})
	before, err := os.ReadFile(svc.store.path)
	if err != nil {
		t.Fatal(err)
	}
	cached := snapshot(t, svc.store)
	posts := len(poster.Payloads())

	_, err = svc.Update(context.Background(), DomainList{
		ID:            id,
		Subscriptions: []Subscription{{URL: subURL1}, {URL: subURL2}},
	})
	if err == nil || !strings.Contains(err.Error(), "слишком много подсетей") {
		t.Fatalf("err = %v, want лимит подсетей", err)
	}
	now, err := os.ReadFile(svc.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(now, before) {
		t.Error("файл store изменён отвергнутым Update")
	}
	if after := snapshot(t, svc.store); after != cached {
		t.Error("кэш изменён отвергнутым Update")
	}
	if got := len(poster.Payloads()); got != posts {
		t.Errorf("отвергнутый Update отправил POST: %d → %d", posts, got)
	}
}

// F697: Update переносит содержимое стоящих подписок из Domains и Subnets
// за вычетом ручных строк — подсети подписок не теряются до обновления.
func TestUpdate_KeepsSubscriptionSubnets(t *testing.T) {
	cases := []struct {
		name        string
		upd         DomainList
		wantSubnets string
	}{
		{"RenameOnly", DomainList{Name: "renamed"}, "5.5.5.0/24,9.9.9.0/24"},
		{"ManualCIDRRemoved", DomainList{ManualDomains: []string{"m.example"}}, "9.9.9.0/24"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dl := &urlDownloader{bodies: map[string]string{subURL1: "a.example\n9.9.9.0/24\n"}}
			svc, poster, id := newUpdateSubsService(t, dl, []string{"m.example", "5.5.5.0/24"})
			posts := len(poster.Payloads())

			upd := tc.upd
			upd.ID = id
			if _, err := svc.Update(context.Background(), upd); err != nil {
				t.Fatal(err)
			}
			if n := dl.calls[subURL1]; n != 0 {
				t.Errorf("подписка перекачана %d раз", n)
			}
			var stored *DomainList
			for i := range svc.store.GetCached().Lists {
				if l := &svc.store.GetCached().Lists[i]; l.ID == id {
					stored = l
				}
			}
			if got := strings.Join(stored.Subnets, ","); got != tc.wantSubnets {
				t.Errorf("Subnets = %s, want %s", got, tc.wantSubnets)
			}
			if want := "m.example,a.example"; strings.Join(stored.Domains, ",") != want {
				t.Errorf("Domains = %v, want %s", stored.Domains, want)
			}
			sent := false
			for _, p := range poster.Payloads()[posts:] {
				if strings.Contains(fmt.Sprint(p), "9.9.9.0/24") {
					sent = true
				}
			}
			if !sent {
				t.Error("подсеть подписки не дошла до роутера после Update")
			}
		})
	}
}
