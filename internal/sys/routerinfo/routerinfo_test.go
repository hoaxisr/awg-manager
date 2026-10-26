package routerinfo

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

func stubStatfs(t *testing.T, fn func(path string) (used, total int64, ok bool)) {
	t.Helper()
	orig := statfsUsageFunc
	t.Cleanup(func() { statfsUsageFunc = orig })
	statfsUsageFunc = fn
}

func TestFetchOPKGStorage_FallbackToOptStatfs(t *testing.T) {
	stubStatfs(t, func(path string) (used, total int64, ok bool) {
		if path != "/opt" {
			t.Fatalf("unexpected path: %s", path)
		}
		return 32 * 1024 * 1024, 55 * 1024 * 1024, true
	})

	got := fetchOPKGStorage(context.Background(), newRCIFake(nil))
	if got != "32 MB / 55 MB" {
		t.Fatalf("unexpected opkg storage: got %q, want %q", got, "32 MB / 55 MB")
	}
}

func TestFetchOPKGStorage_PrefersRCIValue(t *testing.T) {
	stubStatfs(t, func(path string) (used, total int64, ok bool) {
		return 999, 1000, true
	})
	fake := newRCIFake(map[string]string{
		"/show/sc/opkg/disk": `{"disk":"mydisk"}`,
		"/ls":                `{"mydisk:":{"free":1048576,"total":2097152}}`,
	})

	got := fetchOPKGStorage(context.Background(), fake)
	if got != "1 MB / 2 MB" {
		t.Fatalf("unexpected opkg storage: got %q, want %q", got, "1 MB / 2 MB")
	}
}

func TestFetchOPKGStorage_EmptyWhenNoRCIAndNoStatfs(t *testing.T) {
	stubStatfs(t, func(path string) (used, total int64, ok bool) {
		return 0, 0, false
	})

	got := fetchOPKGStorage(context.Background(), newRCIFake(nil))
	if got != "" {
		t.Fatalf("unexpected opkg storage: got %q, want empty", got)
	}
}

// rciFake — query.Getter из фиксированных ответов по пути; считает чтения.
// Путь без ответа — ошибка, как недоступный RCI.
type rciFake struct {
	mu    sync.Mutex
	resp  map[string]string
	calls map[string]int
}

func newRCIFake(resp map[string]string) *rciFake {
	return &rciFake{resp: resp, calls: map[string]int{}}
}

func (f *rciFake) GetRaw(_ context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[path]++
	body, ok := f.resp[path]
	if !ok {
		return nil, errors.New("rciFake: path not faked: " + path)
	}
	return []byte(body), nil
}

func (f *rciFake) Get(ctx context.Context, path string, dst any) error {
	body, err := f.GetRaw(ctx, path)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

func (f *rciFake) Post(context.Context, any) (json.RawMessage, error) {
	return nil, errors.New("rciFake: Post")
}

// F580: температура радио — из снимка общего InterfaceStore; полного списка
// Collect не читает (бюджет — 0 собственных списков).
func TestCollect_WiFiTempsFromSharedSnapshotNoOwnList(t *testing.T) {
	stubStatfs(t, func(string) (int64, int64, bool) { return 0, 0, false })
	t.Cleanup(ndmsinfo.Reset)

	fake := newRCIFake(map[string]string{
		"/show/version": `{"release":"5.01.C.6.0-1","model":"KN-1810"}`,
		"/show/interface/": `{
			"WifiMaster0":{"id":"WifiMaster0","type":"WifiMaster","temperature":61},
			"WifiMaster1":{"id":"WifiMaster1","type":"WifiMaster","temperature":57}
		}`,
	})
	if err := ndmsinfo.Init(context.Background(), query.NewSystemInfoStore(fake, nil), time.Second); err != nil {
		t.Fatalf("ndmsinfo.Init: %v", err)
	}
	snap, err := query.NewInterfaceStore(fake, nil).Snapshot(context.Background(), query.SnapshotRecent)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	listsBefore := fake.calls["/show/interface/"]

	got := Collect(context.Background(), fake, snap)
	if got == nil {
		t.Fatal("Collect вернул nil")
	}
	if got.WiFi24TempC != 61 || got.WiFi5TempC != 57 {
		t.Fatalf("температура из снимка: got %d/%d, want 61/57", got.WiFi24TempC, got.WiFi5TempC)
	}

	// Снимка нет (список не прочитан) — температуры нет, своего чтения тоже.
	if got := Collect(context.Background(), fake, nil); got == nil || got.WiFi24TempC != 0 || got.WiFi5TempC != 0 {
		t.Fatalf("Collect(nil): %+v", got)
	}
	if n := fake.calls["/show/interface/"] - listsBefore; n != 0 {
		t.Fatalf("Collect прочитал общий список %d раз, want 0", n)
	}
}

// Все чтения RCI идут через переданный клиент: своего HTTP-клиента у пакета
// нет, иначе отказы RCI мимо журнала и семафора (#1080).
func TestCollect_ReadsRCIThroughGivenGetter(t *testing.T) {
	stubStatfs(t, func(string) (int64, int64, bool) { return 0, 0, false })
	t.Cleanup(ndmsinfo.Reset)

	fake := newRCIFake(map[string]string{
		"/show/version":      `{"release":"5.02.B.1.0-3","model":"KN-1810","arch":"mips","sandbox":"preview"}`,
		"/show/sc/opkg/disk": `{"disk":"mydisk"}`,
		"/ls":                `{"mydisk:":{"free":1048576,"total":2097152}}`,
		"/show/mws/member":   `[]`,
	})
	if err := ndmsinfo.Init(context.Background(), query.NewSystemInfoStore(fake, nil), time.Second); err != nil {
		t.Fatalf("ndmsinfo.Init: %v", err)
	}

	got := Collect(context.Background(), fake, nil)
	if got == nil {
		t.Fatal("Collect вернул nil")
	}
	if got.FirmwareSandbox != "preview" || got.OpkgStorage != "1 MB / 2 MB" {
		t.Fatalf("sandbox/opkg: got %q/%q", got.FirmwareSandbox, got.OpkgStorage)
	}
	for _, p := range []string{"/show/sc/opkg/disk", "/ls", "/show/mws/member"} {
		if fake.calls[p] != 1 {
			t.Errorf("%s: %d чтений, want 1", p, fake.calls[p])
		}
	}
}
