package vk

import (
	"context"
	"fmt"
	"net"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha"
	manualcaptcha "github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha/manual"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/vkauth"
)

type Config struct {
	// Link - VK callroom join-код (нормализованный, без префикса URL). Обязателен.
	Link string

	Dialer net.Dialer

	// ManualOnly форсирует ручной путь captcha с первой попытки.
	ManualOnly bool

	// Browser - браузерный профиль control-plane: "chrome" | "firefox" | "safari".
	// Пустое -> chrome
	Browser string

	// Platform - класс устройства персоны: "desktop" | "mobile". Пустое -> desktop.
	Platform string

	// StreamsPerCache - делитель streamID -> cacheID. <=0 -> дефолт (10).
	StreamsPerCache int

	// StreamsAlive возвращает число подключённых потоков; vkauth использует
	// для решения, является ли исчерпанная captcha фатальной или throttle.
	StreamsAlive func() int32

	// Credentials - VK app_id/secret пары; nil -> vkauth.DefaultCredentials.
	Credentials []vkauth.VKCredentials

	// Log - уровневый логгер. nil -> no-op.
	Log logx.Logger

	// Debug включает debug-вывод в manual-captcha (HTTP-сервер).
	Debug bool
}

// ManualSolverFunc - кастомный решатель captcha. Если nil, vkauth не пытается
// решать ручную captcha (поток падает на ErrFatalNoStreams при auto-fail).
type ManualSolverFunc = vkauth.ManualSolveFunc

type Provider struct {
	link string
	auth *vkauth.Client
}

// New: solver nil -> ручной путь captcha отключён (поток падает на auto-fail).
func New(cfg Config, solver ManualSolverFunc) (*Provider, error) {
	if cfg.Link == "" {
		return nil, fmt.Errorf("vk: empty Link")
	}
	// captcha-пакеты - internal/ для provider/vk, поэтому подключаем
	// логгер здесь, а не в cmd/client.
	captcha.SetLogger(cfg.Log)
	manualcaptcha.SetLogger(cfg.Log)
	manualcaptcha.Debug = cfg.Debug
	auth := vkauth.New(vkauth.Config{
		Credentials:     cfg.Credentials,
		Dialer:          cfg.Dialer,
		ManualOnly:      cfg.ManualOnly,
		Browser:         browserprofile.KindFromString(cfg.Browser),
		Platform:        browserprofile.PlatformFromString(cfg.Platform),
		StreamsPerCache: cfg.StreamsPerCache,
		StreamsAlive:    cfg.StreamsAlive,
		ManualSolver:    solver,
		Log:             cfg.Log,
	})
	return &Provider{link: cfg.Link, auth: auth}, nil
}

func (p *Provider) GetCredentials(ctx context.Context, streamID int) (provider.Credentials, error) {
	user, pass, addrs, err := p.auth.GetCredentials(ctx, p.link, streamID)
	if err != nil {
		return provider.Credentials{}, err
	}
	return provider.Credentials{User: user, Pass: pass, ServerAddrs: addrs}, nil
}

func (p *Provider) IsAuthError(err error) bool { return p.auth.IsAuthError(err) }

func (p *Provider) HandleAuthError(streamID int) bool { return p.auth.HandleAuthError(streamID) }

func (p *Provider) ResetErrors(streamID int) { p.auth.ResetErrors(streamID) }

func (p *Provider) BackoffUntilUnix() int64 { return p.auth.BackoffUntilUnix() }

func (*Provider) Name() string { return "vk" }

func DefaultManualSolver(ctx context.Context, e *captcha.Error, d net.Dialer) (string, error) {
	if e.RedirectURI == "" {
		return "", fmt.Errorf("no redirect_uri")
	}
	return manualcaptcha.SolveViaProxy(ctx, e.RedirectURI, d)
}
