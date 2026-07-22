package captcha

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	neturl "net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/randx"
)

var Log logx.Logger = logx.Nop()

func SetLogger(l logx.Logger) { Log = logx.OrNop(l) }

const (
	captchaAPIVersion        = "5.131"
	captchaAPIOrigin         = "https://id.vk.ru"
	captchaDomain            = "vk.ru"
	captchaConnectionSamples = 4
)

var (
	reCaptchaPowInput   = regexp.MustCompile(`const\s+powInput\s*=\s*"([^"]+)"`)
	reCaptchaDifficulty = regexp.MustCompile(`const\s+difficulty\s*=\s*(\d+)`)
	reCaptchaScriptSrc  = regexp.MustCompile(`src="(https://[^"]+not_robot_captcha[^"]+)"`)
	reCaptchaDebugInfo  = regexp.MustCompile(`debug_info:(?:[^"]*\|\|)?"([a-fA-F0-9]{64})"`)

	errCaptchaRateLimit   = errors.New("captcha session rate limit reached")
	errCaptchaBot         = errors.New("captcha bot challenge")
	errCaptchaSessionDead = errors.New("captcha session expired")

	captchaMaxAttempts = 4

	// debugInfoCache кэширует 64-hex debug_info по URL JS-бандла (константа виджета,
	// одна на всех).
	debugInfoCache sync.Map

	captchaHeaderOrder = []string{
		"host",
		"content-length",
		"sec-ch-ua-platform",
		"accept-language",
		"sec-ch-ua",
		"content-type",
		"sec-ch-ua-mobile",
		"user-agent",
		"accept",
		"origin",
		"sec-fetch-site",
		"sec-fetch-mode",
		"sec-fetch-dest",
		"referer",
		"accept-encoding",
		"priority",
	}
	captchaPHeaderOrder = []string{":method", ":path", ":authority", ":scheme"}
)

type captchaInitSetting struct {
	Type        string `json:"type"`
	Settings    string `json:"settings"`
	SettingsKey string `json:"settings_key"`
}

type captchaPage struct {
	PowInput      string
	PowDifficulty int
	ScriptURL     string
}

type captchaCheck struct {
	Status       string
	SuccessToken string
	ShowType     string
}

type captchaShowTypeError struct {
	ShowType string
}

func (e *captchaShowTypeError) Error() string {
	return "captcha show type mismatch: " + e.ShowType
}

func isCheckboxRetryableStatus(status string) bool {
	return strings.EqualFold(status, "bot") || strings.EqualFold(status, "error")
}

type captchaSession struct {
	ctx     context.Context
	client  tlsclient.HttpClient
	profile browserprofile.Profile
	domain  string
	log     logx.Logger
}

func (s *captchaSession) logger() logx.Logger {
	if s.log != nil {
		return s.log
	}
	return Log
}

// Solve запускает авторешение captcha против VK captchaNotRobot API.
func Solve(
	ctx context.Context,
	captchaErr *Error,
	streamID int,
	client tlsclient.HttpClient,
	profile browserprofile.Profile,
	log logx.Logger,
) (string, error) {
	if captchaErr == nil || captchaErr.SessionToken == "" {
		return "", fmt.Errorf("no session_token in redirect_uri")
	}
	l := logx.OrNop(log)
	l.Infof("[STREAM %d] [Captcha] Solving VK Smart Captcha automatically...", streamID)

	s := &captchaSession{ctx: ctx, client: client, profile: profile, domain: captchaDomain, log: l}

	for attempt := 1; attempt <= captchaMaxAttempts; attempt++ {
		token, solveErr := s.solveOnce(captchaErr)
		if solveErr == nil {
			return token, nil
		}
		l.Warnf("[STREAM %d] [Captcha] solve attempt %d failed: %v", streamID, attempt, solveErr)
		if errors.Is(solveErr, errCaptchaRateLimit) {
			if attempt < captchaMaxAttempts {
				wait := time.Duration(2+randx.Intn(4)) * time.Second
				l.Infof("[STREAM %d] [Captcha] rate limit on attempt %d, backing off %v", streamID, attempt, wait)
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return "", ctx.Err()
				case <-timer.C:
				}
				continue
			}
			return "", solveErr
		}
		if errors.Is(solveErr, errCaptchaSessionDead) {
			l.Infof("[STREAM %d] [Captcha] session burned on attempt %d, need fresh VK challenge", streamID, attempt)
			return "", solveErr
		}
		if errors.Is(solveErr, errCaptchaBot) {
			l.Infof("[STREAM %d] [Captcha] retryable rejection on attempt %d, rotating browser_fp", streamID, attempt)
			continue
		}

		backoffSteps := min(attempt, 10)
		timer := time.NewTimer(time.Duration(backoffSteps) * 500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", fmt.Errorf("captcha attempts exhausted")
}

func (s *captchaSession) solveOnce(captchaErr *Error) (string, error) {
	s.domain = captchaDomainFromRedirectURI(captchaErr.RedirectURI)
	s.logger().Debugf("[Captcha] using domain=%s", s.domain)

	html, err := s.fetchCaptchaHTML(captchaErr.RedirectURI)
	if err != nil {
		return "", err
	}
	s.logger().Debugf("[Captcha] html fetched bytes=%d", len(html))

	page, err := parseCaptchaPage(html)
	if err != nil {
		s.dumpChunks("html", html)
		return "", err
	}

	s.logger().Debugf("[Captcha] solving pow difficulty=%d", page.PowDifficulty)
	hash := solveCaptchaPoW(s.ctx, page.PowInput, page.PowDifficulty)
	if hash == "" {
		return "", errors.New("captcha pow failed")
	}

	debugInfo := s.resolveDebugInfo(page.ScriptURL)

	browserFP, err := captchaBrowserFP()
	if err != nil {
		return "", err
	}

	base := s.captchaBaseValues(captchaErr.SessionToken)
	if _, settingsErr := s.captchaRequest("captchaNotRobot.settings", base); settingsErr != nil {
		return "", fmt.Errorf("captcha settings failed: %w", settingsErr)
	}

	initResp, err := s.captchaRequest("captchaNotRobot.initSession", [][2]string{
		{"session_token", captchaErr.SessionToken},
		{"domain", s.domain},
		{"lang", "0"},
	})
	if err != nil {
		return "", fmt.Errorf("captcha initSession failed: %w", err)
	}
	showType, sliderContent := parseCaptchaInitSession(initResp)
	s.logger().Debugf("[Captcha] initSession show_type=%q slider_len=%d", showType, len(sliderContent.Value))

	var token string
	switch showType {
	case "slider":
		token, err = s.solveSliderCaptcha(captchaErr.SessionToken, browserFP, hash, sliderContent, debugInfo)
	case "checkbox", "":
		token, err = s.solveCheckboxCaptcha(captchaErr.SessionToken, browserFP, hash, debugInfo)
		if err != nil && sliderContent.Value != "" {
			trySlider := errors.Is(err, errCaptchaBot)
			var showTypeErr *captchaShowTypeError
			if errors.As(err, &showTypeErr) && strings.EqualFold(showTypeErr.ShowType, "slider") {
				trySlider = true
			}
			if trySlider {
				s.logger().Infof("[Captcha] checkbox rejected (bot/error/slider), trying slider")
				token, err = s.solveSliderCaptcha(captchaErr.SessionToken, browserFP, hash, sliderContent, debugInfo)
			}
		}
	default:
		return "", fmt.Errorf("unsupported captcha type: %s", showType)
	}
	if err != nil {
		return "", err
	}

	if _, endErr := s.captchaRequest("captchaNotRobot.endSession", base); endErr != nil {
		s.logger().Warnf("[Captcha] endSession failed: %v", endErr)
	}
	return token, nil
}

func parseCaptchaInitSession(raw map[string]any) (string, captchaContentRef) {
	resp, ok := raw["response"].(map[string]any)
	if !ok {
		return "", captchaContentRef{}
	}
	showType := captchaStringifyAny(resp["show_captcha_type"])
	content := captchaContentRef{}
	if data, err := json.Marshal(resp["content_settings"]); err == nil {
		var settings []captchaInitSetting
		if json.Unmarshal(data, &settings) == nil {
			for _, setting := range settings {
				if setting.Type == "slider" {
					content = setting.contentRef()
				}
			}
		}
	}
	return showType, content
}

func newCaptchaUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type captchaContentRef struct {
	Source string
	Value  string
}

func (s captchaInitSetting) contentRef() captchaContentRef {
	if v := strings.TrimSpace(s.SettingsKey); v != "" {
		return captchaContentRef{Source: "settings_key", Value: v}
	}
	if v := strings.TrimSpace(s.Settings); v != "" {
		return captchaContentRef{Source: "captcha_settings", Value: v}
	}
	return captchaContentRef{}
}

func (s *captchaSession) captchaBaseValues(sessionToken string) [][2]string {
	return [][2]string{
		{"session_token", sessionToken},
		{"domain", s.domain},
		{"adFp", ""},
		{"access_token", ""},
	}
}

func captchaDomainFromRedirectURI(redirectURI string) string {
	u, err := neturl.Parse(redirectURI)
	if err != nil {
		return captchaDomain
	}
	domain := strings.TrimSpace(u.Query().Get("domain"))
	if domain == "" {
		return captchaDomain
	}
	return domain
}

func captchaBrowserFP() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("browser fp generate: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *captchaSession) fetchCaptchaHTML(redirectURI string) (string, error) {
	body, err := s.doRaw(fhttp.MethodGet, redirectURI, nil, map[string]string{
		"Accept":         "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Sec-Fetch-Dest": "document",
		"Sec-Fetch-Mode": "navigate",
		"Sec-Fetch-Site": "cross-site",
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (s *captchaSession) resolveDebugInfo(scriptURL string) string {
	if scriptURL != "" {
		v, err := s.fetchDebugInfoJS(scriptURL)
		if err == nil {
			s.logger().Debugf("[Captcha] debug_info from JS len=%d", len(v))
			return v
		}
		s.logger().Warnf("[Captcha] debug_info JS fetch failed: %v; using UUID fallback", err)
	} else {
		s.logger().Warnf("[Captcha] captcha script URL not in HTML; using UUID fallback")
	}
	return newCaptchaUUID()
}

func (s *captchaSession) fetchDebugInfoJS(scriptURL string) (string, error) {
	if cached, ok := debugInfoCache.Load(scriptURL); ok {
		if v, ok := cached.(string); ok {
			return v, nil
		}
	}
	body, err := s.doRaw(fhttp.MethodGet, scriptURL, nil, map[string]string{
		"Accept":  "text/javascript,*/*",
		"Referer": captchaAPIOrigin + "/",
	})
	if err != nil {
		return "", err
	}
	m := reCaptchaDebugInfo.FindSubmatch(body)
	if len(m) < 2 {
		return "", errors.New("debug_info constant not found in JS")
	}
	v := string(m[1])
	debugInfoCache.Store(scriptURL, v)
	return v, nil
}

func (s *captchaSession) dumpChunks(label, body string) {
	const chunk = 1800
	total := (len(body) + chunk - 1) / chunk
	l := s.logger()
	l.Debugf("[Captcha][DUMP] %s bytes=%d chunks=%d", label, len(body), total)
	for i := range total {
		start := i * chunk
		end := min(start+chunk, len(body))
		l.Debugf("[Captcha][DUMP %s %d/%d] %s", label, i+1, total, body[start:end])
	}
}

func parseCaptchaPage(html string) (*captchaPage, error) {
	page := &captchaPage{}

	if m := reCaptchaScriptSrc.FindStringSubmatch(html); len(m) >= 2 {
		page.ScriptURL = m[1]
	}

	m := reCaptchaPowInput.FindStringSubmatch(html)
	if len(m) < 2 {
		return nil, errors.New("captcha powInput not found")
	}
	page.PowInput = m[1]

	dm := reCaptchaDifficulty.FindStringSubmatch(html)
	if len(dm) < 2 {
		return nil, errors.New("captcha difficulty const not found")
	}
	difficulty, err := strconv.Atoi(dm[1])
	if err != nil || difficulty <= 0 {
		return nil, fmt.Errorf("invalid captcha difficulty %q", dm[1])
	}
	page.PowDifficulty = difficulty
	return page, nil
}

func (s *captchaSession) captchaRequest(method string, form [][2]string) (map[string]any, error) {
	endpoint := "https://api.vk.ru/method/" + method + "?v=" + captchaAPIVersion
	headers := map[string]string{
		"Origin":  captchaAPIOrigin,
		"Referer": captchaAPIOrigin + "/",
	}
	if browserprofile.Family(s.profile) != browserprofile.Firefox {
		headers["Priority"] = "u=1, i"
	}
	body, err := s.doRaw(fhttp.MethodPost, endpoint, form, headers)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("captcha api decode: %w", err)
	}
	s.logger().Debugf("[Captcha] api %s response=%s", method, captchaAPIResponseSummary(out))
	return out, nil
}

func (s *captchaSession) performCaptchaCheck(
	sessionToken string,
	browserFP string,
	hash string,
	answerJSON string,
	cursor string,
	debugInfo string,
) (*captchaCheck, error) {
	mobile := browserprofile.IsMobile(s.profile)
	accelerometer := "[]"
	if mobile {
		accelerometer = captchaMobileAccelerometer()
	}
	values := make([][2]string, 0, 15)
	values = append(values,
		[2]string{"session_token", sessionToken},
		[2]string{"domain", s.domain},
		[2]string{"adFp", ""},
		[2]string{"accelerometer", accelerometer},
		[2]string{"gyroscope", "[]"},
		[2]string{"motion", "[]"},
		[2]string{"cursor", cursor},
		[2]string{"taps", "[]"},
	)
	values = append(values, captchaConnectionFields(browserprofile.Family(s.profile), mobile)...)
	values = append(values,
		[2]string{"browser_fp", browserFP},
		[2]string{"hash", hash},
		[2]string{"answer", base64.StdEncoding.EncodeToString([]byte(answerJSON))},
		[2]string{"debug_info", debugInfo},
		[2]string{"access_token", ""},
	)
	resp, err := s.captchaRequest("captchaNotRobot.check", values)
	if err != nil {
		return nil, fmt.Errorf("captcha check failed: %w", err)
	}
	s.logger().Debugf("[Captcha] check payload answer_bytes=%d cursor_bytes=%d debug_info=%t", len(answerJSON), len(cursor), debugInfo != "")
	check, err := parseCaptchaCheck(resp)
	if err != nil {
		return nil, err
	}
	if check.ShowType != "" {
		s.logger().Debugf("[Captcha] check status=%s show_type=%s", check.Status, check.ShowType)
	} else {
		s.logger().Debugf("[Captcha] check status=%s", check.Status)
	}
	return check, nil
}

func parseCaptchaCheck(raw map[string]any) (*captchaCheck, error) {
	resp, ok := raw["response"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid captcha check response: %v", raw)
	}
	out := &captchaCheck{
		Status:       captchaStringifyAny(resp["status"]),
		SuccessToken: captchaStringifyAny(resp["success_token"]),
		ShowType:     captchaStringifyAny(resp["show_captcha_type"]),
	}
	if out.Status == "" {
		return nil, fmt.Errorf("captcha check status missing: %v", raw)
	}
	return out, nil
}

func (s *captchaSession) solveCheckboxCaptcha(
	sessionToken string,
	browserFP string,
	hash string,
	debugInfo string,
) (string, error) {
	deviceJSON := s.profile.DeviceJSON
	s.logger().Debugf("[Captcha] checkbox componentDone device_bytes=%d", len(deviceJSON))
	if _, err := s.captchaRequest("captchaNotRobot.componentDone", [][2]string{
		{"session_token", sessionToken},
		{"domain", s.domain},
		{"adFp", ""},
		{"browser_fp", browserFP},
		{"device", deviceJSON},
		{"access_token", ""},
	}); err != nil {
		return "", fmt.Errorf("captcha componentDone failed: %w", err)
	}

	select {
	case <-s.ctx.Done():
		return "", s.ctx.Err()
	case <-time.After(time.Duration(400+randx.Intn(250)) * time.Millisecond):
	}

	check, err := s.performCaptchaCheck(sessionToken, browserFP, hash, "{}", "[]", debugInfo)
	if err != nil {
		return "", err
	}
	if check.ShowType != "" && !strings.EqualFold(check.ShowType, "checkbox") {
		if strings.EqualFold(check.ShowType, "slider") {
			return "", fmt.Errorf("%w: checkbox redirected to slider (status=%s)", errCaptchaBot, check.Status)
		}
		return "", &captchaShowTypeError{ShowType: check.ShowType}
	}
	if strings.EqualFold(check.Status, "error_limit") {
		return "", errCaptchaRateLimit
	}
	if isCheckboxRetryableStatus(check.Status) {
		return "", fmt.Errorf("%w: checkbox captcha rejected: status=%s", errCaptchaBot, check.Status)
	}
	if !strings.EqualFold(check.Status, "ok") {
		return "", fmt.Errorf("checkbox captcha rejected: status=%s", check.Status)
	}
	if check.SuccessToken == "" {
		return "", errors.New("captcha success token not found")
	}
	return check.SuccessToken, nil
}

func solveCaptchaPoW(ctx context.Context, input string, difficulty int) string {
	if input == "" || difficulty <= 0 {
		return ""
	}
	target := strings.Repeat("0", difficulty)
	buf := make([]byte, 0, len(input)+20)
	buf = append(buf, input...)
	for nonce := 1; nonce <= 10_000_000; nonce++ {
		if nonce&1023 == 0 {
			select {
			case <-ctx.Done():
				return ""
			default:
			}
		}
		buf = strconv.AppendInt(buf[:len(input)], int64(nonce), 10)
		sum := sha256.Sum256(buf)
		hashHex := hex.EncodeToString(sum[:])
		if strings.HasPrefix(hashHex, target) {
			return hashHex
		}
	}
	return ""
}

func (s *captchaSession) doRaw(
	method string,
	endpoint string,
	form [][2]string,
	extraHeaders map[string]string,
) ([]byte, error) {
	var body []byte
	if form != nil {
		body = []byte(captchaEncodeForm(form))
	}
	req, err := fhttp.NewRequestWithContext(s.ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	browserprofile.ApplyFhttp(req, s.profile)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Origin", captchaAPIOrigin)
	req.Header.Set("Referer", captchaAPIOrigin+"/")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	req.Header[fhttp.HeaderOrderKey] = captchaHeaderOrder
	req.Header[fhttp.PHeaderOrderKey] = captchaPHeaderOrder

	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		s.logger().Debugf("[Captcha] http %s %s failed after=%s form=%s err=%v", method, captchaSafeURL(endpoint), time.Since(start).Truncate(time.Millisecond), captchaFormSummary(form), err)
		return nil, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			s.logger().Warnf("[Captcha] close body: %s", closeErr)
		}
	}()
	data, readErr := io.ReadAll(resp.Body)
	s.logger().Debugf("[Captcha] http %s %s status=%d bytes=%d after=%s form=%s", method, captchaSafeURL(endpoint), resp.StatusCode, len(data), time.Since(start).Truncate(time.Millisecond), captchaFormSummary(form))
	return data, readErr
}

func captchaAPIResponseSummary(raw map[string]any) string {
	if errData, ok := raw["error"].(map[string]any); ok {
		return fmt.Sprintf("error code=%s msg=%q keys=%s", captchaStringifyAny(errData["error_code"]), captchaStringifyAny(errData["error_msg"]), captchaMapKeys(errData))
	}
	if resp, ok := raw["response"].(map[string]any); ok {
		status := captchaStringifyAny(resp["status"])
		showType := captchaStringifyAny(resp["show_captcha_type"])
		tokenLen := len(captchaStringifyAny(resp["success_token"]))
		return fmt.Sprintf("ok status=%q show_type=%q success_token_len=%d keys=%s", status, showType, tokenLen, captchaMapKeys(resp))
	}
	return "unknown keys=" + captchaMapKeys(raw)
}

func captchaMapKeys(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func captchaSafeURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return "<invalid-url>"
	}
	if u.Host == "" {
		return u.Path
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return u.Host + path
}

func captchaFormSummary(values [][2]string) string {
	if len(values) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(values))
	for _, kv := range values {
		switch kv[0] {
		case "session_token", "browser_fp", "hash", "answer", "debug_info", "device", "settings_key", "captcha_settings":
			parts = append(parts, fmt.Sprintf("%s:%d", kv[0], len(kv[1])))
		default:
			parts = append(parts, kv[0])
		}
	}
	return strings.Join(parts, ",")
}

func captchaEncodeForm(values [][2]string) string {
	if len(values) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, kv := range values {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(captchaQueryEscape(kv[0]))
		sb.WriteByte('=')
		sb.WriteString(captchaQueryEscape(kv[1]))
	}
	return sb.String()
}

func captchaQueryEscape(s string) string {
	const upper = "0123456789ABCDEF"
	hexDigits := func(b byte) [3]byte {
		return [3]byte{'%', upper[b>>4], upper[b&0xF]}
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			out = append(out, '+')
		case ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~':
			out = append(out, c)
		default:
			h := hexDigits(c)
			out = append(out, h[:]...)
		}
	}
	return string(out)
}

func captchaStringifyAny(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(data)
	}
}

// captchaConnectionFields возвращает Chrome-only поля NetworkInformation API.
func captchaConnectionFields(family browserprofile.Kind, mobile bool) [][2]string {
	if family != browserprofile.Chrome {
		return nil
	}
	n := captchaConnectionSamples
	if mobile {
		rtt := 50 * (1 + randx.Intn(2)) // 50 | 100
		dl := 1.4 + randx.Float64()*0.7 // [1.4, 2.1)
		return [][2]string{
			{"connectionRtt", captchaRepeatNumberJSON(rtt, n)},
			{"connectionDownlink", captchaRepeatFloatJSON(round2(dl), n)},
		}
	}
	rtt := 50 * (1 + randx.Intn(3)) // 50 | 100 | 150
	dl := 5.0 + randx.Float64()*5.0 // [5, 10)
	return [][2]string{
		{"connectionRtt", captchaRepeatNumberJSON(rtt, n)},
		{"connectionDownlink", captchaRepeatFloatJSON(round1(dl), n)},
	}
}

// captchaMobileAccelerometer генерирует сэмплы покоящегося телефона.
func captchaMobileAccelerometer() string {
	type sample struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		Z float64 `json:"z"`
	}
	const g = 9.81
	bx := -2.5 + randx.Float64()*5.0 // [-2.5, 2.5)
	by := 4.0 + randx.Float64()*4.0  // [4, 8)
	bz := g
	if rem := g*g - bx*bx - by*by; rem > 0 {
		bz = math.Sqrt(rem)
	}
	jitter := func() float64 { return (randx.Float64() - 0.5) * 0.2 } // ±0.1
	pts := make([]sample, 3)
	for i := range pts {
		pts[i] = sample{X: round1(bx + jitter()), Y: round1(by + jitter()), Z: round1(bz + jitter())}
	}
	data, err := json.Marshal(pts)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func captchaRepeatFloatJSON(value float64, count int) string {
	if count <= 0 {
		return "[]"
	}
	s := strconv.FormatFloat(value, 'g', -1, 64)
	parts := make([]string, count)
	for i := range parts {
		parts[i] = s
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func captchaCursorPointCount(cursor string) int {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" || cursor == "[]" {
		return 0
	}
	var points []struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	if err := json.Unmarshal([]byte(cursor), &points); err != nil {
		return 0
	}
	return len(points)
}

func captchaRepeatNumberJSON(value int, count int) string {
	if count <= 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.Grow(count*4 + 2)
	sb.WriteByte('[')
	for i := 0; i < count; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.Itoa(value))
	}
	sb.WriteByte(']')
	return sb.String()
}
