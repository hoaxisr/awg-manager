package localdeps

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/geodata"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

type fakeGeo struct {
	files   []geodata.GeoFileEntry
	tags    []geodata.GeoTag
	allN    int
	allErr  error
	oneErr  error
	via     string
	updated string
}

func (f *fakeGeo) List() []geodata.GeoFileEntry { return f.files }
func (f *fakeGeo) GetTags(string) ([]geodata.GeoTag, error) {
	return f.tags, nil
}
func (f *fakeGeo) UpdateWithClientVia(_ context.Context, path string, _ *http.Client, label string) (*geodata.GeoFileEntry, error) {
	f.updated, f.via = path, label
	return &geodata.GeoFileEntry{Path: path}, f.oneErr
}
func (f *fakeGeo) UpdateAllWithClientVia(_ context.Context, _ *http.Client, label string) (int, error) {
	f.updated, f.via = "*", label
	return f.allN, f.allErr
}

type fakeDownloads struct{ err error }

func (f fakeDownloads) ResolveClient(context.Context, *downloader.Route) (*downloader.Lease, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &downloader.Lease{Client: http.DefaultClient, Route: downloader.RouteInfo{Tag: "proxy-1", Kind: "vless"}}, nil
}

// TestLocal_GeoData — список отдаёт только хост URL; теги
// страничные; обновление идёт через маршрут загрузок, частичный успех —
// не ошибка.
func TestLocal_GeoData(t *testing.T) {
	g := &fakeGeo{files: []geodata.GeoFileEntry{{Type: "geoip", Path: "/opt/geo/geoip.dat", URL: "https://user:secret@github.com/x/geoip.dat?token=abc", Updated: "2026-09-01T00:00:00Z", TagCount: 250}}}
	for i := range 250 {
		g.tags = append(g.tags, geodata.GeoTag{Name: "t" + strings.Repeat("x", i%3), Count: i})
	}
	l := New(Config{Geo: g, Downloads: fakeDownloads{}})
	files, err := l.ListGeoFiles(context.Background())
	if err != nil || len(files) != 1 || files[0].Host != "github.com" || files[0].Updated == "" {
		t.Fatalf("files = %+v, %v", files, err)
	}
	page, err := l.GeoTags(context.Background(), "/opt/geo/geoip.dat", 0)
	if err != nil || len(page.Tags) != mcpsrv.MaxGeoTagsInOutput || !page.Truncated || page.Total != 250 {
		t.Fatalf("page = %d tags, %v, %v", len(page.Tags), page.Truncated, err)
	}
	if page, _ = l.GeoTags(context.Background(), "/opt/geo/geoip.dat", 200); len(page.Tags) != 50 || page.Truncated {
		t.Fatalf("second page = %d, %v", len(page.Tags), page.Truncated)
	}
	if _, err := l.GeoTags(context.Background(), "/etc/passwd", 0); err == nil {
		t.Fatal("a path that is not a geo file must be refused")
	}

	if r, err := l.UpdateGeoFiles(context.Background(), "/opt/geo/geoip.dat"); err != nil || r.Updated != 1 || r.DownloadError != "" || g.updated != "/opt/geo/geoip.dat" || g.via != "proxy-1 (vless)" {
		t.Fatalf("one: %+v %v via %q", r, err, g.via)
	}
	g.allN, g.allErr = 2, errors.New("update errors: /opt/geo/geosite.dat: re-download https://user:secret@github.com/x/geosite.dat?token=abc: timeout")
	r, err := l.UpdateGeoFiles(context.Background(), "")
	if err != nil || r.Updated != 2 || g.updated != "*" || !strings.Contains(r.DownloadError, "github.com") || strings.Contains(r.DownloadError, "secret") {
		t.Fatalf("some files failing: the reason comes, its address cut to the host: %+v, %v", r, err)
	}
	g.allN = 0
	if _, err := l.UpdateGeoFiles(context.Background(), ""); err == nil {
		t.Fatal("nothing updated must be an error")
	}
	g.allErr = errors.New("update errors: /opt/geo/geoip.dat: re-download https://user:secret@github.com/x/geoip.dat?token=abc: timeout")
	if _, err := l.UpdateGeoFiles(context.Background(), ""); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") || !strings.Contains(err.Error(), "github.com") {
		t.Fatalf("the source URL is cut to its host: %v", err)
	}
	g.oneErr = g.allErr
	if _, err := l.UpdateGeoFiles(context.Background(), "/opt/geo/geoip.dat"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("one file: the source URL is cut to its host: %v", err)
	}
	g.oneErr = nil
	if _, err := New(Config{Geo: g, Downloads: fakeDownloads{err: errors.New("no route")}}).UpdateGeoFiles(context.Background(), ""); err == nil {
		t.Fatal("no download route must be an error")
	}
}
