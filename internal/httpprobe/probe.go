package httpprobe

import (
	"context"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

const (
	Timeout        = 7 * time.Second
	ConnectTimeout = 3 * time.Second
	MaxTime        = 5 * time.Second
)

var Client httpclient.HTTPDoer = httpclient.DefaultClient

type Result struct {
	HTTPCode  int
	LatencyMs int
}

func ByInterface(ctx context.Context, ifaceName, checkURL string) (Result, error) {
	checkCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	checkURL = strings.TrimSpace(checkURL)
	if checkURL == "" {
		checkURL = storage.DefaultConnectivityCheckURL
	}

	res, err := Client.Do(checkCtx, httpclient.CallConfig{
		URL:            checkURL,
		Interface:      ifaceName,
		ConnectTimeout: ConnectTimeout,
		MaxTime:        MaxTime,
		DiscardBody:    true,
	})
	if err != nil {
		return Result{}, err
	}

	httpCode := res.Metrics.HTTPCode
	latencyMs := LatencyMs(res.Metrics)
	if SuccessCode(httpCode) && latencyMs <= 0 {
		latencyMs = 1
	}

	return Result{
		HTTPCode:  httpCode,
		LatencyMs: latencyMs,
	}, nil
}

func LatencyMs(metrics httpclient.Metrics) int {
	if metrics.TimeConnect > 0 && metrics.TimeConnect >= metrics.TimeNameLookup {
		return httpclient.SecToMs(metrics.TimeConnect - metrics.TimeNameLookup)
	}
	return httpclient.SecToMs(metrics.TimeTotal)
}

func SuccessCode(code int) bool {
	return code >= 200 && code < 400
}
