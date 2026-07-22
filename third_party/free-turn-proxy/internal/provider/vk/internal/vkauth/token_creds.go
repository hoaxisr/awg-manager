package vkauth

import (
	"context"
	"fmt"
	"strings"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// fetchTurnCreds - шаг 4 цепочки: вызывает vchat.joinConversationByLink
// и извлекает TURN username, credential и список адресов из ответа.
func (c *Client) fetchTurnCreds(
	ctx context.Context,
	httpClient tlsclient.HttpClient,
	profile browserprofile.Profile,
	streamID int,
	link, token2, sessionKey string,
) (user, pass string, addresses []string, err error) {
	data := fmt.Sprintf(
		"joinLink=%s&isVideo=false&protocolVersion=5&capabilities=2F7F&anonymToken=%s&method=vchat.joinConversationByLink&format=JSON&application_key=CGMMEJLGDIHBABABA&session_key=%s",
		link, token2, sessionKey,
	)
	resp, err := c.doRequest(ctx, httpClient, profile, data, "https://calls.okcdn.ru/fb.do")
	if err != nil {
		return "", "", nil, err
	}

	tsRaw, ok := resp["turn_server"].(map[string]any)
	if !ok {
		return "", "", nil, fmt.Errorf("missing turn_server in response: %v", resp)
	}
	user, ok = tsRaw["username"].(string)
	if !ok {
		return "", "", nil, fmt.Errorf("missing username in turn_server")
	}
	pass, ok = tsRaw["credential"].(string)
	if !ok {
		return "", "", nil, fmt.Errorf("missing credential in turn_server")
	}
	urlsRaw, ok := tsRaw["urls"].([]any)
	if !ok || len(urlsRaw) == 0 {
		return "", "", nil, fmt.Errorf("missing or empty urls in turn_server")
	}

	c.log.Infof("[STREAM %d] [VK Auth] TURN urls (%d total):", streamID, len(urlsRaw))
	for i, u := range urlsRaw {
		c.log.Infof("[STREAM %d] [VK Auth]   [%d] %v", streamID, i, u)
	}

	for _, u := range urlsRaw {
		urlStr, ok := u.(string)
		if !ok {
			continue
		}
		clean := strings.Split(urlStr, "?")[0]
		address := strings.TrimPrefix(strings.TrimPrefix(clean, "turn:"), "turns:")
		addresses = append(addresses, address)
	}
	if len(addresses) == 0 {
		return "", "", nil, fmt.Errorf("no valid TURN addresses found")
	}
	return user, pass, addresses, nil
}
