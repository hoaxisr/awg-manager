package vkauth

import (
	"errors"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/provider"
)

type VKCredentials struct {
	ClientID     string
	ClientSecret string
}

type TurnCredentials struct {
	Username    string
	Password    string
	ServerAddrs []string
	ExpiresAt   time.Time
	Link        string
}

// Публичные app_id/secret штатных VK-клиентов (зашиты в сами приложения), не наши приватные креды.
//
//nolint:gosec // G101: публичные VK app secrets
var DefaultCredentials = []VKCredentials{
	{ClientID: "6287487", ClientSecret: "QbYic1K3lEV5kTGiqlq2"},  // VK_WEB_APP_ID
	{ClientID: "7879029", ClientSecret: "aR5NKGmm03GYrCiNKsaw"},  // VK_MVK_APP_ID
	{ClientID: "2274003", ClientSecret: "hHbZxrka2uZ6jB1inYsH"},  // VK_ANDROID_APP
	{ClientID: "51453752", ClientSecret: "4UyuCUsdK8pVCNoeQuGi"}, // VK_MESSENGER_DESKTOP
	{ClientID: "3140623", ClientSecret: "VeWdmVclDCtn6ihuP1nt"},  // VK_IPHONE_APP
}

const APIVersion = "5.282"

const (
	CredentialLifetime = 10 * time.Minute
	CacheSafetyMargin  = 60 * time.Second
	MaxCacheErrors     = 3
	ErrorWindow        = 10 * time.Second

	DefaultStreamsPerCache = 10
)

var (
	ErrCaptchaWaitRequired   = errors.Join(provider.ErrBackoffActive, errors.New("CAPTCHA_WAIT_REQUIRED"))
	ErrFatalCaptchaNoStreams = errors.Join(provider.ErrFatalNoStreams, errors.New("FATAL_CAPTCHA_FAILED_NO_STREAMS"))
	ErrLockoutActive         = errors.New("global lockout active")
	ErrInvalidJoinLink       = errors.Join(provider.ErrFatalNoStreams, errors.New("INVALID_JOIN_LINK"))
	ErrAnonymousBlocked      = errors.Join(provider.ErrFatalNoStreams, errors.New("ANON_BLOCKED"))
	ErrCallFull              = errors.Join(provider.ErrFatalNoStreams, errors.New("CALL_FULL"))
)
