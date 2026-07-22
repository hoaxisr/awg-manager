package vkauth

import (
	"context"
	"net"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha"

	tlsclient "github.com/bogdanfinn/tls-client"
)

type CaptchaSolveMode int

const (
	CaptchaSolveModeAuto CaptchaSolveMode = iota
	CaptchaSolveModeManual
)

// captchaAutoRounds — число auto-попыток до optional manual fallback (WDTT-style
// orchestrator без WebView: несколько Go-раундов с ротацией browser persona).
const captchaAutoRounds = 5

func CaptchaSolveModeForAttempt(attempt int, manualOnly, manualFallback bool) (CaptchaSolveMode, bool) {
	if manualOnly {
		return CaptchaSolveModeManual, attempt == 0
	}
	if attempt < captchaAutoRounds {
		return CaptchaSolveModeAuto, true
	}
	if manualFallback && attempt == captchaAutoRounds {
		return CaptchaSolveModeManual, true
	}
	return 0, false
}

func CaptchaSolveModeLabel(mode CaptchaSolveMode) string {
	switch mode {
	case CaptchaSolveModeAuto:
		return "auto captcha"
	case CaptchaSolveModeManual:
		return "manual captcha"
	default:
		return "captcha"
	}
}

type AutoSolveFunc func(
	ctx context.Context,
	captchaErr *captcha.Error,
	streamID int,
	http tlsclient.HttpClient,
	profile browserprofile.Profile,
) (token string, err error)

type ManualSolveFunc func(
	ctx context.Context,
	captchaErr *captcha.Error,
	dialer net.Dialer,
) (token string, err error)
