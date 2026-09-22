package updater

import (
	"bytes"
	"context"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/appver"
)

const (
	packagesMaxBytes  int64 = 2 << 20
	changelogMaxBytes int64 = 512 << 10
	ipkMaxBytes       int64 = 64 << 20
)

type Downloader interface {
	ReadAll(ctx context.Context, req downloader.Request) ([]byte, downloader.ResponseMeta, error)
	DownloadFile(ctx context.Context, req downloader.FileRequest) (downloader.FileResult, error)
}

type loggingDownloader struct {
	next Downloader
	log  *logging.ScopedLogger
}

func newDefaultDownloader() Downloader {
	return downloader.NewService(downloader.Deps{})
}

func newLoggingDownloader(next Downloader, log *logging.ScopedLogger) Downloader {
	if next == nil {
		next = newDefaultDownloader()
	}
	return &loggingDownloader{next: next, log: log}
}

func (d *loggingDownloader) ReadAll(ctx context.Context, req downloader.Request) ([]byte, downloader.ResponseMeta, error) {
	body, meta, err := d.next.ReadAll(ctx, req)
	switch req.Purpose {
	case "awgm-update-check":
		if err != nil {
			d.logWarn("check-url", req.URL, "Ошибка проверки обновлений", meta.Route, err)
		} else {
			d.logInfo("check-url", req.URL, "Проверка обновлений", meta.Route)
		}
	case "awgm-changelog":
		if err != nil {
			d.logWarn("changelog-url", req.URL, "Ошибка загрузки changelog", meta.Route, err)
		} else {
			d.logInfo("changelog-url", req.URL, "Загрузка changelog", meta.Route)
		}
	}
	return body, meta, err
}

func (d *loggingDownloader) DownloadFile(ctx context.Context, req downloader.FileRequest) (downloader.FileResult, error) {
	result, err := d.next.DownloadFile(ctx, req)
	if req.Purpose == "awgm-update-ipk" {
		if err != nil {
			d.logWarn("upgrade-url", req.URL, "Ошибка обновления AWGM", result.Route, err)
		} else {
			d.logInfo("upgrade-url", req.URL, "Обновление AWGM", result.Route)
		}
	}
	return result, err
}

func (d *loggingDownloader) logInfo(action, target, prefix string, route downloader.RouteInfo) {
	if d == nil || d.log == nil {
		return
	}
	d.log.Info(action, target, prefix+" через "+route.DisplayName()+": "+target)
}

func (d *loggingDownloader) logWarn(action, target, prefix string, route downloader.RouteInfo, err error) {
	if d == nil || d.log == nil {
		return
	}
	routePart := ""
	if route.Tag != "" {
		routePart = " через " + route.DisplayName()
	}
	d.log.Warn(action, target, prefix+routePart+": "+target+": "+err.Error())
}

func fetchLatestPackageWithDownloader(ctx context.Context, dl Downloader, pkgsURL, packageName string, cmp func(a, b string) int) (PackageEntry, error) {
	if dl == nil {
		dl = newDefaultDownloader()
	}
	body, _, err := dl.ReadAll(ctx, downloader.Request{
		Purpose:      "awgm-update-check",
		UserAgent:    appver.UA(),
		URL:          pkgsURL,
		MaxBodyBytes: packagesMaxBytes,
		Timeout:      repoTimeout,
	})
	if err != nil {
		return PackageEntry{}, err
	}
	return parsePackagesGz(bytes.NewReader(body), packageName, cmp)
}
