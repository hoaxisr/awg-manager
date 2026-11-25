package localdeps

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/geodata"
	"github.com/hoaxisr/awg-manager/internal/logging"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

type (
	// GeoData is the part of *geodata.GeoDataStore the geo tools use.
	GeoData interface {
		List() []geodata.GeoFileEntry
		GetTags(path string) ([]geodata.GeoTag, error)
		UpdateWithClientVia(ctx context.Context, path string, client *http.Client, routeLabel string) (*geodata.GeoFileEntry, error)
		UpdateAllWithClientVia(ctx context.Context, client *http.Client, routeLabel string) (int, error)
	}
	// Downloads picks the HTTP client service downloads go through.
	Downloads interface {
		ResolveClient(ctx context.Context, route *downloader.Route) (*downloader.Lease, error)
	}
)

func (l *Local) ListGeoFiles(context.Context) ([]mcpsrv.GeoFile, error) {
	if l.c.Geo == nil {
		return nil, errUnavailable("geo files")
	}
	var out []mcpsrv.GeoFile
	for _, f := range l.c.Geo.List() {
		g := mcpsrv.GeoFile{Type: token(f.Type), Path: f.Path, Host: hostOf(f.URL), Size: f.Size, TagCount: f.TagCount}
		if _, err := time.Parse(time.RFC3339, f.Updated); err == nil {
			g.Updated = f.Updated
		}
		out = append(out, g)
	}
	return out, nil
}

func (l *Local) GeoTags(_ context.Context, path string, offset int) (mcpsrv.GeoTags, error) {
	if l.c.Geo == nil {
		return mcpsrv.GeoTags{}, errUnavailable("geo files")
	}
	known := false
	for _, f := range l.c.Geo.List() {
		if f.Path == path {
			known = true
		}
	}
	if !known {
		return mcpsrv.GeoTags{}, fmt.Errorf("%q is not a geo file (use list_geo_files)", path)
	}
	tags, err := l.c.Geo.GetTags(path)
	if err != nil {
		return mcpsrv.GeoTags{}, err
	}
	out := mcpsrv.GeoTags{Path: path, Total: len(tags)}
	if offset > len(tags) {
		offset = len(tags)
	}
	end := min(offset+mcpsrv.MaxGeoTagsInOutput, len(tags))
	for _, t := range tags[offset:end] {
		out.Tags = append(out.Tags, mcpsrv.GeoTag{Name: sanitizeLabel(t.Name), Count: t.Count})
	}
	out.Truncated = end < len(tags)
	return out, nil
}

// UpdateGeoFiles mirrors api.GeoDataHandler.UpdateGeoFile:
// the download goes through the route service downloads use.
func (l *Local) UpdateGeoFiles(ctx context.Context, path string) (mcpsrv.GeoUpdate, error) {
	if l.c.Geo == nil || l.c.Downloads == nil {
		return mcpsrv.GeoUpdate{}, errUnavailable("geo files")
	}
	lease, err := l.c.Downloads.ResolveClient(ctx, nil)
	if err != nil {
		return mcpsrv.GeoUpdate{}, fmt.Errorf("no route for the download: %w", err)
	}
	defer lease.Close()
	label := lease.Route.DisplayName()
	var out mcpsrv.GeoUpdate
	if path == "" {
		n, err := l.c.Geo.UpdateAllWithClientVia(ctx, lease.Client, label)
		out.Updated = n
		if err != nil {
			if n == 0 {
				l.dnsLog.Warn("geo-files", "", "Geo update failed (MCP): "+err.Error())
				return mcpsrv.GeoUpdate{}, fmt.Errorf("the geo files could not be updated via %s: %s", label, geoError(err))
			}
			out.DownloadError = geoError(err)
		}
	} else {
		if _, err := l.c.Geo.UpdateWithClientVia(ctx, path, lease.Client, label); err != nil {
			return mcpsrv.GeoUpdate{}, fmt.Errorf("the geo file could not be updated via %s: %s", label, geoError(err))
		}
		out.Updated = 1
	}
	l.dnsLog.Info("geo-files", path, fmt.Sprintf("%d geo files updated (MCP)", out.Updated))
	return out, nil
}

// geoError is the store's error for the agent: it quotes the source URL
// whole («re-download <url>: …»), and a custom source may carry a token
// or a login — list_geo_files names the host only.
func geoError(err error) string {
	return sanitizeText(logging.RedactURLs(err.Error()), maxMessageRunes)
}
