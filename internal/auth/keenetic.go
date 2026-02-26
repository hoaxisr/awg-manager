// Package auth provides authentication via Keenetic RCI API.
package auth

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	authEndpoint = "/auth"
	httpTimeout  = 10 * time.Second
)

// KeeneticClient handles authentication against Keenetic router.
type KeeneticClient struct {
	routerAddr string
	httpClient *http.Client
}

// NewKeeneticClient creates a new Keenetic auth client.
// It detects router IP from br0 interface and port from ndmc config.
func NewKeeneticClient() *KeeneticClient {
	// Get IP from br0 interface
	ip := getBr0IP()
	if ip == "" {
		ip = "192.168.1.1"
	}

	// Get HTTP port from router config
	port := getHTTPPort()

	// Format address with port (skip :80 as it's default)
	var addr string
	if port != 0 && port != 80 {
		addr = fmt.Sprintf("%s:%d", ip, port)
	} else {
		addr = ip
	}

	return &KeeneticClient{
		routerAddr: addr,
		httpClient: &http.Client{
			Timeout: httpTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// getHTTPPort returns router HTTP port from NDMS RCI API.
func getHTTPPort() int {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://localhost:79/rci/show/running-config")
	if err != nil {
		return 80
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 80
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 80
	}

	// RCI returns {"message": ["line1", "line2", ...]}
	var result struct {
		Message []string `json:"message"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 80
	}

	for _, line := range result.Message {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ip http port ") {
			var port int
			if _, err := fmt.Sscanf(line, "ip http port %d", &port); err == nil {
				return port
			}
		}
	}

	return 80
}

// getBr0IP returns the first IPv4 address of br0 interface.
func getBr0IP() string {
	iface, err := net.InterfaceByName("br0")
	if err != nil {
		return ""
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return ""
}

// Authenticate verifies credentials against Keenetic router.
// Returns nil on success, error on failure.
func (c *KeeneticClient) Authenticate(ctx context.Context, login, password string) error {
	authURL := fmt.Sprintf("http://%s%s", c.routerAddr, authEndpoint)

	// Step 1: GET /auth to get challenge, realm and cookies
	challenge, realm, cookies, err := c.getChallenge(ctx, authURL)
	if err != nil {
		// If we get 200 on GET, auth is disabled or already authenticated
		if err == errAuthDisabled {
			return nil
		}
		return fmt.Errorf("get challenge from %s: %w", authURL, err)
	}

	// Step 2: Calculate hashed password
	// Formula: sha256(challenge + md5(login + ":" + realm + ":" + password))
	hashedPassword := c.hashPassword(login, password, realm, challenge)

	// Debug: log auth attempt
	fmt.Printf("[AUTH DEBUG] URL: %s, login: %s, challenge: %s, realm: %s\n", authURL, login, challenge, realm)

	// Step 3: POST /auth with credentials (include cookies from GET)
	if err := c.postAuth(ctx, authURL, login, hashedPassword, cookies); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	return nil
}

var errAuthDisabled = fmt.Errorf("auth disabled")

// ErrInvalidCredentials indicates wrong login or password.
var ErrInvalidCredentials = fmt.Errorf("invalid credentials")

// getChallenge performs GET /auth and extracts challenge/realm from 401 response.
// Also returns cookies that must be sent with POST request.
func (c *KeeneticClient) getChallenge(ctx context.Context, authURL string) (challenge, realm string, cookies []*http.Cookie, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return "", "", nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// 200 means auth is disabled or user already authenticated
	if resp.StatusCode == http.StatusOK {
		return "", "", nil, errAuthDisabled
	}

	// 401 is expected - extract challenge and realm
	if resp.StatusCode != http.StatusUnauthorized {
		return "", "", nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	challenge = strings.TrimSpace(resp.Header.Get("X-NDM-Challenge"))
	realm = strings.TrimSpace(resp.Header.Get("X-NDM-Realm"))

	if challenge == "" || realm == "" {
		return "", "", nil, fmt.Errorf("missing challenge or realm headers")
	}

	// Get cookies from response (needed for POST)
	cookies = resp.Cookies()

	return challenge, realm, cookies, nil
}

// hashPassword calculates the Keenetic password hash.
// Formula: sha256(challenge + md5(login + ":" + realm + ":" + password))
func (c *KeeneticClient) hashPassword(login, password, realm, challenge string) string {
	// Step 1: MD5(login:realm:password)
	md5Input := login + ":" + realm + ":" + password
	md5Hash := md5.Sum([]byte(md5Input))
	md5Hex := hex.EncodeToString(md5Hash[:])

	// Step 2: SHA256(challenge + md5hex)
	sha256Input := challenge + md5Hex
	sha256Hash := sha256.Sum256([]byte(sha256Input))
	return hex.EncodeToString(sha256Hash[:])
}

// postAuth sends POST /auth with credentials.
func (c *KeeneticClient) postAuth(ctx context.Context, authURL, login, hashedPassword string, cookies []*http.Cookie) error {
	body := map[string]string{
		"login":    login,
		"password": hashedPassword,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Add cookies from GET response
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	fmt.Printf("[AUTH DEBUG] POST %s response status: %d\n", authURL, resp.StatusCode)

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrInvalidCredentials
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected response status: %d", resp.StatusCode)
	}

	return nil
}

// GetRouterAddr returns the configured router address.
func (c *KeeneticClient) GetRouterAddr() string {
	return c.routerAddr
}
