package warpscout

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// API Cloudflare WARP. Домен api.devices.cloudflare.com (v0a4005) не режется
// ТСПУ на прямом WAN большинства провайдеров РФ в отличие от устаревшего
// api.cloudflareclient.com; при блокировке и его Service перебирает TLS-split,
// резервные выходы (локальный прокси / активные VPN-туннели) и запасной хост.
const (
	apiBase       = "https://api.devices.cloudflare.com/v0a4005"
	apiBaseAlt    = "https://api.cloudflareclient.com/v0a4005"
	apiClientVer  = "a-6.11-2223"
	apiUserAgent  = "okhttp/3.12.1"
	apiMaxBody    = 64 << 10
	apiReqTimeout = 15 * time.Second
)

// RegisterOptions — необязательные параметры ручной регистрации WARP через прокси или relay.
type RegisterOptions struct {
	ProxyURL string `json:"proxyUrl,omitempty"`
	RelayURL string `json:"relayUrl,omitempty"`
}

// ErrDisallowedAddress возвращается при попытке подключиться к запрещённому IP
// (link-local, multicast, unspecified), включая адреса после DNS-резолва.
var ErrDisallowedAddress = errors.New("подключение к указанному адресу не допускается")

// IsDisallowedIP запрещает link-local (включая metadata 169.254.169.254),
// multicast и unspecified (0.0.0.0, ::) адреса.
func IsDisallowedIP(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.WithZone("").Unmap()
	return addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified()
}

// IsDisallowedLiteralIP запрещает link-local (включая metadata 169.254.169.254),
// multicast и unspecified (0.0.0.0, ::) литеральные адреса.
func IsDisallowedLiteralIP(host string) bool {
	h := strings.TrimSpace(strings.Trim(host, "[]"))
	if h == "" {
		return false
	}
	addr, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	return IsDisallowedIP(addr)
}

// RedactURLCredentials маскирует пароль или учётные данные в URL для безопасных ошибок и логов.
func RedactURLCredentials(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if u, err := url.Parse(trimmed); err == nil && u.Scheme != "" {
		if u.User != nil {
			return u.Redacted()
		}
		return trimmed
	}
	if schemeEnd := strings.Index(trimmed, "://"); schemeEnd >= 0 {
		authStart := schemeEnd + 3
		rest := trimmed[authStart:]
		authEnd := len(rest)
		if idx := strings.IndexAny(rest, "/?#"); idx >= 0 {
			authEnd = idx
		}
		if at := strings.LastIndex(rest[:authEnd], "@"); at >= 0 {
			return trimmed[:authStart] + "***@" + rest[at+1:]
		}
	}
	return trimmed
}

type maskedSentinelError struct {
	msg       string
	sentinels []error
}

func (e *maskedSentinelError) Error() string { return e.msg }

func (e *maskedSentinelError) Is(target error) bool {
	for _, s := range e.sentinels {
		if errors.Is(s, target) {
			return true
		}
	}
	return false
}

// SanitizeProxyError удаляет пароль и логин прокси из текста ошибки.
func SanitizeProxyError(err error, rawProxyURL string) error {
	if err == nil {
		return nil
	}
	trimmed := strings.TrimSpace(rawProxyURL)
	redacted := RedactURLCredentials(trimmed)
	msg := err.Error()
	changed := false
	if trimmed != "" && redacted != "" && trimmed != redacted && strings.Contains(msg, trimmed) {
		msg = strings.ReplaceAll(msg, trimmed, redacted)
		changed = true
	}
	if u, parseErr := url.Parse(trimmed); parseErr == nil && u.User != nil {
		if pass, ok := u.User.Password(); ok && pass != "" && strings.Contains(msg, pass) {
			msg = strings.ReplaceAll(msg, pass, "xxxxx")
			changed = true
		}
		if user := u.User.Username(); user != "" && strings.Contains(msg, user+":") {
			msg = strings.ReplaceAll(msg, user+":", "***:")
			changed = true
		}
	}
	if !changed {
		return err
	}
	var sentinels []error
	if errors.Is(err, ErrInvalidInput) {
		sentinels = append(sentinels, ErrInvalidInput)
	}
	if errors.Is(err, ErrDisallowedAddress) {
		sentinels = append(sentinels, ErrDisallowedAddress)
	}
	if len(sentinels) > 0 {
		return &maskedSentinelError{msg: msg, sentinels: sentinels}
	}
	return errors.New(msg)
}

// NormalizeRelayBaseURL проверяет и нормализует пользовательский URL HTTP-relay для регистрации WARP.
func NormalizeRelayBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: некорректный URL relay: %v", ErrInvalidInput, err)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("%w: укажите чистый URL relay без параметров и учётных данных", ErrInvalidInput)
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	host := strings.ToLower(u.Hostname())
	if IsDisallowedLiteralIP(host) {
		return "", fmt.Errorf("%w: указанный IP-адрес relay не допускается", ErrInvalidInput)
	}
	switch scheme {
	case "https":
	case "http":
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return "", fmt.Errorf("%w: для внешнего relay допустим только HTTPS", ErrInvalidInput)
		}
	default:
		return "", fmt.Errorf("%w: relay URL должен начинаться с https://", ErrInvalidInput)
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" || !strings.Contains(path, "/v0") {
		path += "/v0a4005"
	}
	u.Scheme = scheme
	u.Path = path
	u.RawPath = ""
	return u.String(), nil
}

// cfAPIBase переопределяется в модульных тестах.
var (
	cfAPIBase    = apiBase
	cfAPIBaseAlt = apiBaseAlt
)

// HTTPClientFactory отдаёт клиента с выходом через указанный WAN-интерфейс
// (пустая строка — маршрут по умолчанию).
type HTTPClientFactory func(wanIface string) (*http.Client, error)

type accountMeta struct {
	AccountType string `json:"account_type"`
	WarpPlus    bool   `json:"warp_plus"`
	License     string `json:"license"`
	Quota       int64  `json:"quota"`
	PremiumData int64  `json:"premium_data"`
}

// isWarpPlus учитывает особенность v0a4005: у бесплатных учёток поле warp_plus
// приходит равным true при account_type="free" и нулевых quota/premium_data.
func (m accountMeta) isWarpPlus() bool {
	if m.Quota > 0 || m.PremiumData > 0 {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(m.AccountType)) {
	case "":
		return m.WarpPlus
	case "free":
		return false
	default:
		return true
	}
}

type regResponse struct {
	ID          string      `json:"id"`
	Token       string      `json:"token"`
	WarpEnabled bool        `json:"warp_enabled"`
	Account     accountMeta `json:"account"`
	Config      struct {
		ClientID string `json:"client_id"`
		Peers    []struct {
			PublicKey string `json:"public_key"`
		} `json:"peers"`
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
	} `json:"config"`
}

// apiStatusError — HTTP-ошибка ответа самого сервера Cloudflare (не сбой сети).
type apiStatusError struct {
	Code int
	Body string
}

func (e *apiStatusError) Error() string {
	return fmt.Sprintf("Cloudflare API: HTTP %d: %s", e.Code, e.Body)
}

// shouldRetryFallback решает, имеет ли смысл повторять запрос через резервный
// транспорт (прокси/туннель). Сетевые таймауты, сбросы ТСПУ, 403/429 и 5xx — да;
// 400/401/404/422 (неверный ключ лицензии и т.п.) — нет.
func shouldRetryFallback(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var se *apiStatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusForbidden || se.Code == http.StatusTooManyRequests || se.Code >= 500
	}
	return true
}

// isAccountNotFound сообщает, ответил ли Cloudflare API HTTP 404 (регистрация удалена на стороне сервера).
func isAccountNotFound(err error) bool {
	var se *apiStatusError
	return errors.As(err, &se) && se.Code == http.StatusNotFound
}

// GenerateKeyPair — пара X25519 в base64 (формат WireGuard).
func GenerateKeyPair() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()),
		base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// PublicKeyOf выводит публичный ключ из приватного.
func PublicKeyOf(priv string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(priv))
	if err != nil || len(raw) != 32 {
		return "", errors.New("некорректный приватный ключ")
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

func apiDo(ctx context.Context, c *http.Client, method, url, token string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, apiReqTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("CF-Client-Version", apiClientVer)
	req.Header.Set("User-Agent", apiUserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if c == nil {
		return errors.New("HTTP-клиент не настроен")
	}
	client := *c
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Cloudflare API: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, apiMaxBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &apiStatusError{Code: resp.StatusCode, Body: msg}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("Cloudflare API: разбор ответа: %w", err)
		}
	}
	return nil
}

func accountFromRegResp(r regResponse, priv, pub string) *Account {
	peer := WarpPeerPublicKey
	if len(r.Config.Peers) > 0 && r.Config.Peers[0].PublicKey != "" {
		peer = r.Config.Peers[0].PublicKey
	}
	v4 := r.Config.Interface.Addresses.V4
	if v4 == "" {
		v4 = "172.16.0.2"
	}
	return &Account{
		ID: r.ID, Token: r.Token, PrivateKey: priv, PublicKey: pub,
		PeerPublicKey: peer, AddressV4: v4, AddressV6: r.Config.Interface.Addresses.V6,
		ClientID: r.Config.ClientID, AccountType: r.Account.AccountType,
		WarpPlus: r.Account.isWarpPlus(), License: r.Account.License,
		HasToken:  r.ID != "" && r.Token != "",
		CreatedAt: time.Now().UTC(),
	}
}

func resolveAPIBase(baseURL string) string {
	b := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if b == "" {
		return cfAPIBase
	}
	return b
}

// RegisterAccount регистрирует новую учётную запись WARP (POST /reg +
// PATCH /reg/{id} {"warp_enabled": true}).
func RegisterAccount(ctx context.Context, c *http.Client) (*Account, error) {
	return registerAccountWithBase(ctx, c, cfAPIBase)
}

func registerAccountWithBase(ctx context.Context, c *http.Client, baseURL string) (*Account, error) {
	base := resolveAPIBase(baseURL)
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	var r regResponse
	if err := apiDo(ctx, c, http.MethodPost, base+"/reg", "", map[string]string{"key": pub}, &r); err != nil {
		return nil, err
	}
	if r.ID == "" || r.Token == "" {
		return nil, errors.New("Cloudflare API: в ответе нет id/token")
	}
	if !r.WarpEnabled {
		var patched regResponse
		if err := apiDo(ctx, c, http.MethodPatch, base+"/reg/"+r.ID, r.Token, map[string]bool{"warp_enabled": true}, &patched); err != nil {
			return nil, err
		}
		if len(patched.Config.Peers) > 0 {
			patched.ID, patched.Token = r.ID, r.Token
			r = patched
		}
	}
	return accountFromRegResp(r, priv, pub), nil
}

// RotateAccountKey меняет ключевую пару существующей регистрации WARP на месте
// (PATCH /reg/{id} {"key": pub}), сохраняя привязанную лицензию WARP+ и слот.
func RotateAccountKey(ctx context.Context, c *http.Client, existing *Account) (*Account, error) {
	return rotateAccountKeyWithBase(ctx, c, existing, cfAPIBase)
}

func rotateAccountKeyWithBase(ctx context.Context, c *http.Client, existing *Account, baseURL string) (*Account, error) {
	if existing == nil || existing.ID == "" || existing.Token == "" {
		return nil, errors.New("нет существующей учётной записи для ротации ключа")
	}
	base := resolveAPIBase(baseURL)
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	var r regResponse
	if err := apiDo(ctx, c, http.MethodPatch, base+"/reg/"+existing.ID, existing.Token, map[string]string{"key": pub}, &r); err != nil {
		return nil, err
	}
	acct := accountFromRegResp(r, priv, pub)
	acct.ID = existing.ID
	acct.Token = existing.Token
	acct.HasToken = existing.ID != "" && existing.Token != ""
	if acct.License == "" {
		acct.License = existing.License
	}
	if r.Account.AccountType == "" {
		acct.AccountType = existing.AccountType
		acct.WarpPlus = existing.WarpPlus
	}
	if acct.ClientID == "" {
		acct.ClientID = existing.ClientID
	}
	return acct, nil
}

// ApplyLicense привязывает ключ WARP+ и возвращает обновлённые флаги.
func ApplyLicense(ctx context.Context, c *http.Client, a *Account, license string) error {
	return applyLicenseWithBase(ctx, c, a, license, cfAPIBase)
}

func applyLicenseWithBase(ctx context.Context, c *http.Client, a *Account, license, baseURL string) error {
	license = strings.TrimSpace(license)
	if license == "" {
		return errors.New("пустой ключ лицензии")
	}
	url := resolveAPIBase(baseURL) + "/reg/" + a.ID + "/account"
	if err := apiDo(ctx, c, http.MethodPut, url, a.Token, map[string]string{"license": license}, nil); err != nil {
		return err
	}
	var acc accountMeta
	if err := apiDo(ctx, c, http.MethodGet, url, a.Token, nil, &acc); err != nil {
		return err
	}
	a.License = license
	a.AccountType = acc.AccountType
	a.WarpPlus = acc.isWarpPlus()
	return nil
}

// DeleteAccount снимает регистрацию (лучшее усилие).
func DeleteAccount(ctx context.Context, c *http.Client, a *Account) error {
	return deleteAccountWithBase(ctx, c, a, cfAPIBase)
}

func deleteAccountWithBase(ctx context.Context, c *http.Client, a *Account, baseURL string) error {
	return apiDo(ctx, c, http.MethodDelete, resolveAPIBase(baseURL)+"/reg/"+a.ID, a.Token, nil, nil)
}
