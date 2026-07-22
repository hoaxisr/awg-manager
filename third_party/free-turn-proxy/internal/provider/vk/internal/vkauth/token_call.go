package vkauth

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"strings"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// fetchCallToken - шаг 2 цепочки: вызывает calls.getAnonymousToken и ведёт
// цикл retry captcha до получения call-токена или исчерпания всех режимов решения.
func (c *Client) fetchCallToken(
	ctx context.Context,
	httpClient tlsclient.HttpClient,
	profile browserprofile.Profile,
	streamID int,
	link, escapedName, token1 string,
	creds VKCredentials,
) (string, error) {
	urlAddr := fmt.Sprintf("https://api.vk.ru/method/calls.getAnonymousToken?v=%s&client_id=%s", APIVersion, creds.ClientID)
	data := fmt.Sprintf("vk_join_link=https://vk.ru/call/join/%s&name=%s&access_token=%s",
		link, escapedName, token1)

	for attempt := 0; ; attempt++ {
		resp, err := c.doRequest(ctx, httpClient, profile, data, urlAddr)
		if err != nil {
			return "", err
		}

		if errObj, hasErr := resp["error"].(map[string]any); hasErr {
			captchaErr := captcha.ParseError(errObj)
			if captchaErr != nil && captchaErr.IsCaptcha() {
				retryData, err := c.solveCaptcha(ctx, httpClient, profile, streamID, attempt, link, escapedName, token1, captchaErr)
				if err != nil {
					return "", err
				}
				data = retryData
				continue
			}
			if termErr := classifyLinkError(errObj); termErr != nil {
				c.log.Errorf("[STREAM %d] [VK Auth] terminal link error: %v", streamID, termErr)
				return "", termErr
			}
			return "", fmt.Errorf("VK API error: %v", errObj)
		}

		respMap, ok := resp["response"].(map[string]any)
		if !ok {
			return "", fmt.Errorf("unexpected getAnonymousToken response: %v", resp)
		}
		token2, ok := respMap["token"].(string)
		if !ok {
			return "", fmt.Errorf("missing token in response: %v", resp)
		}
		return token2, nil
	}
}

// classifyLinkError распознаёт ТЕРМИНАЛЬНЫЕ ответы VK в join-флоу и возвращает
// соответствующий sentinel (или nil, если ошибку можно ретраить дальше по
// client_id). Терминал = ни client_id, ни captcha не помогут, поэтому fast-fail
// вместо бесконечного цикла "решить captcha -> ошибка -> следующий client_id".
//
// Порядок важен: сначала явные коды (терминальные и транзиентные), и только для
// неизвестного кода - матч по тексту error_msg. Так текстовый матч не может
// перекрыть транзиентный код (5 auth-failed, 6/9/29 rate/flood, 14 captcha) и
// убить рабочее подключение из-за подстроки в сообщении.
func classifyLinkError(errObj map[string]any) error {
	code := 0
	if f, ok := errObj["error_code"].(float64); ok {
		code = int(f)
	}

	// Явные терминальные коды: 9008 Join link is not valid, 9000 Call not found.
	if code == 9000 || code == 9008 {
		return ErrInvalidJoinLink
	}
	// Явные транзиентные коды: гасить нельзя, текст ниже к ним не применяем.
	switch code {
	case 5, 6, 9, 14, 29:
		return nil
	}

	// Код неизвестен/плавает - осторожный матч по тексту.
	msg := ""
	if s, ok := errObj["error_msg"].(string); ok {
		msg = strings.ToLower(s)
	}
	switch {
	case strings.Contains(msg, "not valid") || strings.Contains(msg, "not found"):
		return ErrInvalidJoinLink
	// Анонимный вход запрещён. Матчим по "anonym" (не "authoriz" - коллизия с auth-failed).
	case strings.Contains(msg, "anonym"):
		return ErrAnonymousBlocked
	case strings.Contains(msg, "full"):
		return ErrCallFull
	}
	return nil
}

// solveCaptcha выполняет одну попытку решения captcha и возвращает тело POST
// для следующего retry или ошибку при исчерпании всех режимов.
func (c *Client) solveCaptcha(
	ctx context.Context,
	httpClient tlsclient.HttpClient,
	profile browserprofile.Profile,
	streamID, attempt int,
	link, escapedName, token1 string,
	captchaErr *captcha.Error,
) (retryData string, err error) {
	manualFallback := c.manualSolve != nil && !c.manualOnly
	solveMode, hasSolveMode := CaptchaSolveModeForAttempt(attempt, c.manualOnly, manualFallback)
	if !hasSolveMode {
		c.log.Warnf("[STREAM %d] [Captcha] No more solve modes available (attempt %d)", streamID, attempt+1)
		c.engageLockout(60 * time.Second)
		if c.streamsFn() == 0 {
			c.log.Warnf("[STREAM %d] [Captcha] Auto captcha exhausted with 0 connected streams; throttling (retry after lockout)", streamID)
		}
		return "", ErrCaptchaWaitRequired
	}

	unlock, err := c.acquireCaptchaLock(ctx, streamID)
	if err != nil {
		return "", err
	}
	defer unlock()

	var successToken string
	var solveErr error

	switch solveMode {
	case CaptchaSolveModeAuto:
		if captchaErr.SessionToken != "" && captchaErr.RedirectURI != "" {
			if c.autoSolver != nil {
				successToken, solveErr = c.autoSolver(ctx, captchaErr, streamID, httpClient, profile)
			} else {
				successToken, solveErr = c.autoSolveRound(ctx, captchaErr, streamID, attempt, httpClient, profile)
			}
			if solveErr != nil {
				c.log.Warnf("[STREAM %d] [Captcha] Auto captcha failed: %v", streamID, solveErr)
			}
		} else {
			solveErr = fmt.Errorf("missing fields for auto solve")
		}

	case CaptchaSolveModeManual:
		if c.manualSolve == nil {
			solveErr = fmt.Errorf("manual captcha solver not configured")
			break
		}
		c.log.Infof("[STREAM %d] [Captcha] Triggering manual captcha fallback", streamID)
		// Ручной решалке выделяется свой 3-минутный бюджет - жёсткий parent-deadline
		// не обрезает время пользователя. Отмена parent (завершение приложения)
		// всё равно propagate, горутина не переживает процесс.
		manualCtx, manualCancel := context.WithTimeout(ctx, 3*time.Minute)

		type manualRes struct {
			token string
			err   error
		}
		resCh := make(chan manualRes, 1)
		go func() {
			t, e := c.manualSolve(manualCtx, captchaErr, c.dialer)
			resCh <- manualRes{t, e}
		}()

		select {
		case res := <-resCh:
			successToken = res.token
			solveErr = res.err
			if successToken != "" {
				if solveErr != nil {
					c.log.Debugf("[STREAM %d] [Captcha] Token received (ignoring cleanup error: %v)", streamID, solveErr)
					solveErr = nil
				}
				c.log.Infof("[STREAM %d] [Captcha] Got token from browser", streamID)
			} else if solveErr != nil {
				c.log.Warnf("[STREAM %d] [Captcha] Manual solver error: %v", streamID, solveErr)
			}
		case <-manualCtx.Done():
			if errors.Is(manualCtx.Err(), context.DeadlineExceeded) {
				solveErr = fmt.Errorf("manual captcha timed out after 3m")
			} else {
				solveErr = fmt.Errorf("manual captcha interrupted: %w", manualCtx.Err())
			}
		}
		manualCancel()
	}

	if solveErr != nil {
		c.log.Warnf("[STREAM %d] [Captcha] %s failed (attempt %d): %v",
			streamID, CaptchaSolveModeLabel(solveMode), attempt+1, solveErr)
		nextSolveMode, hasNextSolveMode := CaptchaSolveModeForAttempt(attempt+1, c.manualOnly, manualFallback)
		if hasNextSolveMode {
			c.log.Infof("[STREAM %d] [Captcha] Falling back to %s",
				streamID, CaptchaSolveModeLabel(nextSolveMode))
			return buildCaptchaRetryData(link, escapedName, token1, captchaErr, ""), nil
		}
		c.engageLockout(60 * time.Second)
		if c.streamsFn() == 0 {
			c.log.Warnf("[STREAM %d] [Captcha] Captcha solve failed with 0 connected streams; throttling (retry after lockout)", streamID)
		}
		return "", ErrCaptchaWaitRequired
	}

	if captchaErr.CaptchaAttempt == "0" || captchaErr.CaptchaAttempt == "" {
		captchaErr.CaptchaAttempt = "1"
	}
	return buildCaptchaRetryData(link, escapedName, token1, captchaErr, successToken), nil
}

func buildCaptchaRetryData(link, escapedName, token1 string, captchaErr *captcha.Error, successToken string) string {
	if captchaErr.CaptchaSid == "" {
		return fmt.Sprintf(
			"vk_join_link=https://vk.ru/call/join/%s&name=%s&success_token=%s&access_token=%s",
			link, escapedName, neturl.QueryEscape(successToken), token1,
		)
	}
	return fmt.Sprintf(
		"vk_join_link=https://vk.ru/call/join/%s&name=%s&captcha_key=&captcha_sid=%s&is_sound_captcha=0&success_token=%s&captcha_ts=%s&captcha_attempt=%s&access_token=%s",
		link, escapedName, captchaErr.CaptchaSid, neturl.QueryEscape(successToken),
		captchaErr.CaptchaTs, captchaErr.CaptchaAttempt, token1,
	)
}
