package routerinfo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

func TestFetchOPKGStorage_FallbackToOptStatfs(t *testing.T) {
	origGetJSON := rciGetJSONFunc
	origGetRaw := rciGetRawFunc
	origStatfs := statfsUsageFunc
	t.Cleanup(func() {
		rciGetJSONFunc = origGetJSON
		rciGetRawFunc = origGetRaw
		statfsUsageFunc = origStatfs
	})

	rciGetJSONFunc = func(path string, dst any) error {
		return errors.New("rci unavailable")
	}
	rciGetRawFunc = func(path string) ([]byte, error) {
		return nil, errors.New("rci unavailable")
	}
	statfsUsageFunc = func(path string) (used, total int64, ok bool) {
		if path != "/opt" {
			t.Fatalf("unexpected path: %s", path)
		}
		return 32 * 1024 * 1024, 55 * 1024 * 1024, true
	}

	got := fetchOPKGStorage()
	if got != "32 MB / 55 MB" {
		t.Fatalf("unexpected opkg storage: got %q, want %q", got, "32 MB / 55 MB")
	}
}

func TestFetchOPKGStorage_PrefersRCIValue(t *testing.T) {
	origGetJSON := rciGetJSONFunc
	origGetRaw := rciGetRawFunc
	origStatfs := statfsUsageFunc
	t.Cleanup(func() {
		rciGetJSONFunc = origGetJSON
		rciGetRawFunc = origGetRaw
		statfsUsageFunc = origStatfs
	})

	rciGetJSONFunc = func(path string, dst any) error {
		if path != "/show/sc/opkg/disk" {
			return errors.New("unexpected path")
		}
		d, ok := dst.(*rciOpkgDiskWire)
		if !ok {
			return errors.New("unexpected dst type")
		}
		d.Disk = "mydisk"
		return nil
	}
	rciGetRawFunc = func(path string) ([]byte, error) {
		if path != "/ls" {
			return nil, errors.New("unexpected path")
		}
		return []byte(`{"mydisk:":{"free":1048576,"total":2097152}}`), nil
	}
	statfsUsageFunc = func(path string) (used, total int64, ok bool) {
		return 999, 1000, true
	}

	got := fetchOPKGStorage()
	if got != "1 MB / 2 MB" {
		t.Fatalf("unexpected opkg storage: got %q, want %q", got, "1 MB / 2 MB")
	}
}

func TestFetchOPKGStorage_EmptyWhenNoRCIAndNoStatfs(t *testing.T) {
	origGetJSON := rciGetJSONFunc
	origGetRaw := rciGetRawFunc
	origStatfs := statfsUsageFunc
	t.Cleanup(func() {
		rciGetJSONFunc = origGetJSON
		rciGetRawFunc = origGetRaw
		statfsUsageFunc = origStatfs
	})

	rciGetJSONFunc = func(path string, dst any) error {
		return errors.New("rci unavailable")
	}
	rciGetRawFunc = func(path string) ([]byte, error) {
		return nil, errors.New("rci unavailable")
	}
	statfsUsageFunc = func(path string) (used, total int64, ok bool) {
		return 0, 0, false
	}

	got := fetchOPKGStorage()
	if got != "" {
		t.Fatalf("unexpected opkg storage: got %q, want empty", got)
	}
}

// rciFake — query.Getter из фиксированных ответов по пути; считает чтения.
type rciFake struct {
	mu    sync.Mutex
	resp  map[string]string
	calls map[string]int
}

func (f *rciFake) Get(_ context.Context, path string, dst any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[path]++
	body, ok := f.resp[path]
	if !ok {
		return errors.New("rciFake: path not faked: " + path)
	}
	return json.Unmarshal([]byte(body), dst)
}

func (f *rciFake) GetRaw(context.Context, string) ([]byte, error) {
	return nil, errors.New("rciFake: GetRaw")
}

func (f *rciFake) Post(context.Context, any) (json.RawMessage, error) {
	return nil, errors.New("rciFake: Post")
}

// F580: температура радио — из снимка общего InterfaceStore; полного списка
// своим клиентом Collect не читает (бюджет — 0 собственных списков).
func TestCollect_WiFiTempsFromSharedSnapshotNoOwnList(t *testing.T) {
	origGetJSON := rciGetJSONFunc
	origGetRaw := rciGetRawFunc
	origStatfs := statfsUsageFunc
	t.Cleanup(func() {
		rciGetJSONFunc = origGetJSON
		rciGetRawFunc = origGetRaw
		statfsUsageFunc = origStatfs
		ndmsinfo.Reset()
	})

	var mu sync.Mutex
	var own []string
	rciGetJSONFunc = func(path string, dst any) error {
		mu.Lock()
		own = append(own, path)
		mu.Unlock()
		return errors.New("rci unavailable")
	}
	rciGetRawFunc = func(string) ([]byte, error) { return nil, errors.New("rci unavailable") }
	statfsUsageFunc = func(string) (int64, int64, bool) { return 0, 0, false }

	fake := &rciFake{calls: map[string]int{}, resp: map[string]string{
		"/show/version": `{"release":"5.01.C.6.0-1","model":"KN-1810"}`,
		"/show/interface/": `{
			"WifiMaster0":{"id":"WifiMaster0","type":"WifiMaster","temperature":61},
			"WifiMaster1":{"id":"WifiMaster1","type":"WifiMaster","temperature":57}
		}`,
	}}
	if err := ndmsinfo.Init(context.Background(), query.NewSystemInfoStore(fake, nil), time.Second); err != nil {
		t.Fatalf("ndmsinfo.Init: %v", err)
	}
	snap, err := query.NewInterfaceStore(fake, nil).Snapshot(context.Background(), query.SnapshotRecent)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	listsBefore := fake.calls["/show/interface/"]

	got := Collect(snap)
	if got == nil {
		t.Fatal("Collect вернул nil")
	}
	if got.WiFi24TempC != 61 || got.WiFi5TempC != 57 {
		t.Fatalf("температура из снимка: got %d/%d, want 61/57", got.WiFi24TempC, got.WiFi5TempC)
	}
	if n := fake.calls["/show/interface/"] - listsBefore; n != 0 {
		t.Fatalf("Collect прочитал общий список %d раз, want 0", n)
	}
	for _, p := range own {
		if strings.HasPrefix(p, "/show/interface") {
			t.Fatalf("Collect читает список интерфейсов своим клиентом: %s (own=%v)", p, own)
		}
	}

	// Снимка нет (список не прочитан) — температуры нет, своего чтения тоже.
	own = nil
	if got := Collect(nil); got == nil || got.WiFi24TempC != 0 || got.WiFi5TempC != 0 {
		t.Fatalf("Collect(nil): %+v", got)
	}
	for _, p := range own {
		if strings.HasPrefix(p, "/show/interface") {
			t.Fatalf("Collect(nil) читает список своим клиентом: %s", p)
		}
	}
}
