package vkauth

import (
	"context"
	"fmt"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// fetchAnonToken - шаг 1 цепочки: обменивает app client_id/client_secret
// на анонимный access token из login.vk.ru.
func (c *Client) fetchAnonToken(ctx context.Context, httpClient tlsclient.HttpClient, profile browserprofile.Profile, creds VKCredentials) (string, error) {
	data := fmt.Sprintf("client_secret=%s&client_id=%s&scopes=audio_anonymous,video_anonymous,photos_anonymous,profile_anonymous&isApiOauthAnonymEnabled=false&version=1&app_id=%s",
		creds.ClientSecret, creds.ClientID, creds.ClientID)
	resp, err := c.doRequest(ctx, httpClient, profile, data, "https://login.vk.ru/?act=get_anonym_token")
	if err != nil {
		return "", err
	}
	dataMap, ok := resp["data"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("unexpected anon token response: %v", resp)
	}
	token, ok := dataMap["access_token"].(string)
	if !ok {
		return "", fmt.Errorf("missing access_token in response: %v", resp)
	}
	return token, nil
}
