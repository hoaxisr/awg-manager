package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
)

type installerDownloaderAdapter struct {
	svc *downloader.Service
}

type dnsRouteDownloaderAdapter struct {
	svc *downloader.Service
}

func (a *dnsRouteDownloaderAdapter) ReadAll(
	ctx context.Context,
	req dnsroute.SubscriptionDownloadRequest,
) ([]byte, dnsroute.SubscriptionDownloadMeta, error) {
	if a == nil || a.svc == nil {
		return nil, dnsroute.SubscriptionDownloadMeta{}, fmt.Errorf("downloader service is not configured")
	}
	body, meta, err := a.svc.ReadAll(ctx, downloader.Request{
		Purpose:       "dnsroute-subscription",
		URL:           req.URL,
		Timeout:       req.Timeout,
		MaxBodyBytes:  req.MaxBodyBytes,
		AllowedStatus: req.AllowedStatus,
	})
	if err != nil {
		return nil, dnsroute.SubscriptionDownloadMeta{}, err
	}
	return body, dnsroute.SubscriptionDownloadMeta{ContentType: meta.ContentType}, nil
}

func (a *installerDownloaderAdapter) DownloadFile(ctx context.Context, req installer.DownloadFileRequest) (installer.DownloadFileResult, error) {
	if a == nil || a.svc == nil {
		return installer.DownloadFileResult{}, fmt.Errorf("downloader service is not configured")
	}

	res, err := a.svc.DownloadFile(ctx, downloader.FileRequest{
		Request: downloader.Request{
			Purpose: "singbox-binary",
			URL:     req.URL,
			Timeout: req.Timeout,
		},
		// Intentional: req.DestPath is already "<binary>.tmp" from installer.
		// We keep temp == dest here, and activation of live binary is done
		// separately in Installer.Activate().
		DestPath:     req.DestPath,
		TempPath:     req.DestPath,
		MaxFileBytes: req.MaxFileBytes,
		Mode:         req.Mode,
		// Intentional: do not atomically move to final binary here.
		// Installer.Activate() performs chmod + final atomic rename.
		Atomic:   false,
		Progress: req.Progress,
	})
	if err != nil {
		return installer.DownloadFileResult{}, err
	}
	return installer.DownloadFileResult{
		Path: res.Path,
		Size: res.Size,
	}, nil
}

// freeturnDownloaderAdapter bridges freeturn.Downloader onto the shared
// downloader.Service (то же, что installerDownloaderAdapter для sing-box:
// temp == dest, без atomic — активацию делает freeturn.installOne).
type freeturnDownloaderAdapter struct {
	svc *downloader.Service
}

func (a *freeturnDownloaderAdapter) DownloadFile(ctx context.Context, url, destPath string, maxBytes int64) error {
	if a == nil || a.svc == nil {
		return fmt.Errorf("downloader service is not configured")
	}
	_, err := a.svc.DownloadFile(ctx, downloader.FileRequest{
		Request: downloader.Request{
			Purpose: "freeturn-binary",
			URL:     url,
			Timeout: 5 * time.Minute,
		},
		DestPath:     destPath,
		TempPath:     destPath,
		MaxFileBytes: maxBytes,
		Mode:         0o644,
		Atomic:       false,
	})
	return err
}

type wdttDownloaderAdapter struct {
	svc *downloader.Service
}

func (a *wdttDownloaderAdapter) DownloadFile(ctx context.Context, url, destPath string, maxBytes int64) error {
	if a == nil || a.svc == nil {
		return fmt.Errorf("downloader service is not configured")
	}
	_, err := a.svc.DownloadFile(ctx, downloader.FileRequest{
		Request: downloader.Request{
			Purpose: "wdtt-binary",
			URL:     url,
			Timeout: 5 * time.Minute,
		},
		DestPath:     destPath,
		TempPath:     destPath,
		MaxFileBytes: maxBytes,
		Mode:         0o644,
		Atomic:       false,
	})
	return err
}
