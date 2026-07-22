package vkauth

import (
	"context"
	"fmt"
	neturl "net/url"

	"github.com/google/uuid"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// fetchOkRuSession - шаг 3 цепочки: получает анонимный ok.ru session_key
// через auth.anonymLogin.
func (c *Client) fetchOkRuSession(ctx context.Context, httpClient tlsclient.HttpClient, profile browserprofile.Profile) (string, error) {
	sessionData := fmt.Sprintf(`{"version":2,"device_id":"%s","client_version":1.1,"client_type":"SDK_JS"}`, uuid.New())
	data := fmt.Sprintf("session_data=%s&method=auth.anonymLogin&format=JSON&application_key=CGMMEJLGDIHBABABA",
		neturl.QueryEscape(sessionData))
	resp, err := c.doRequest(ctx, httpClient, profile, data, "https://calls.okcdn.ru/fb.do")
	if err != nil {
		return "", err
	}
	sessionKey, ok := resp["session_key"].(string)
	if !ok {
		return "", fmt.Errorf("missing session_key in response: %v", resp)
	}
	return sessionKey, nil
}
