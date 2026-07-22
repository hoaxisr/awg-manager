package vkauth

import (
	"context"
	"fmt"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/captcha"

	tlsclient "github.com/bogdanfinn/tls-client"
)

func captchaFamiliesFor(base browserprofile.Kind) []browserprofile.Kind {
	all := []browserprofile.Kind{
		browserprofile.Firefox,
		browserprofile.Chrome,
		browserprofile.Safari,
	}
	if base == "" {
		base = browserprofile.Chrome
	}
	out := make([]browserprofile.Kind, 0, len(all))
	out = append(out, base)
	for _, k := range all {
		if k != base {
			out = append(out, k)
		}
	}
	return out
}

func (*Client) defaultAutoSolve(
	ctx context.Context,
	captchaErr *captcha.Error,
	streamID int,
	client tlsclient.HttpClient,
	profile browserprofile.Profile,
) (string, error) {
	return defaultAutoSolveRound(ctx, captchaErr, streamID, 0, client, profile)
}

func (c *Client) autoSolveRound(
	ctx context.Context,
	captchaErr *captcha.Error,
	streamID, round int,
	_ tlsclient.HttpClient,
	_ browserprofile.Profile,
) (string, error) {
	families := captchaFamiliesFor(c.browser)
	family := families[round%len(families)]
	profile := browserprofile.For(family, c.platform)
	jar := tlsclient.NewCookieJar()
	captchaClient, err := c.newTLSClient(profile, jar)
	if err != nil {
		return "", fmt.Errorf("captcha tls client: %w", err)
	}
	c.log.Infof("[STREAM %d] [Captcha] Auto round %d/%d (family=%s platform=%s)",
		streamID, round+1, captchaAutoRounds, profile.Family, profile.Platform)
	return defaultAutoSolveRound(ctx, captchaErr, streamID, round, captchaClient, profile)
}

func defaultAutoSolveRound(
	ctx context.Context,
	captchaErr *captcha.Error,
	streamID, round int,
	client tlsclient.HttpClient,
	profile browserprofile.Profile,
) (string, error) {
	log := captcha.Log
	if captchaErr.SessionToken == "" {
		return "", fmt.Errorf("no session_token in redirect_uri for auto-solve")
	}
	if captchaErr.RedirectURI == "" {
		return "", fmt.Errorf("no redirect_uri for auto-solve")
	}

	successToken, err := captcha.Solve(ctx, captchaErr, streamID, client, profile, log)
	if err != nil {
		return "", err
	}
	log.Infof("[STREAM %d] [Captcha] Auto round %d succeeded", streamID, round+1)
	return successToken, nil
}
